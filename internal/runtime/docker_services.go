package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/jgillich/tpd/internal/workspace"
	"golang.org/x/sys/unix"
)

// The lockfile and run-dir path functions are package-level vars so tests can
// redirect them to temp dirs; production computes them from the host user.
var serviceLockfilePath = func(name string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "tpd", "svc-"+name+".lock")
}

var serviceRunDir = func(name string, mode workspace.Mode) string {
	if mode == workspace.ModeRootless {
		return fmt.Sprintf("/run/user/%d/tpd-svc-%s/", os.Getuid(), name)
	}
	return fmt.Sprintf("/run/tpd-svc-%s-%d/", name, os.Getuid())
}

var serviceProbeTimeout = 30 * time.Second

func serviceContainerName(name string, mode workspace.Mode) string {
	if mode == workspace.ModeRootless {
		return "tpd-svc-" + name
	}
	return fmt.Sprintf("tpd-svc-%s-%d", name, os.Getuid())
}

func serviceSocketPath(name string, mode workspace.Mode, exposePath string) string {
	return filepath.Join(serviceRunDir(name, mode), exposePath)
}

// acquireServiceLock takes an exclusive flock on the service's lockfile,
// creating the ~/.local/share/tpd parent (mode 0700) on first use. The kernel
// releases the lock when the owning process dies, so a SIGKILL'd tpd never
// leaves a stale sentinel.
func acquireServiceLock(name string) (*os.File, error) {
	path := serviceLockfilePath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// StartServices finds-or-starts every service in spec.Services, holding the
// per-service lockfiles (acquired in sorted name order to prevent deadlock)
// until the caller invokes the returned Release. Locks are only released on
// error or via Release; Run's container-create must stay under the lock so a
// concurrent stop step can't see "zero consumers" mid-launch.
func (d *DockerRuntime) StartServices(ctx context.Context, spec Spec, w ProgressWriter, pull bool) (ServiceBindings, error) {
	if spec.Workspace.Mode == workspace.ModeRootful {
		return ServiceBindings{Sockets: map[string]string{}, Release: func() {}}, fmt.Errorf("services are not supported in rootful mode; use rootless podman")
	}
	if len(spec.Services) == 0 {
		return ServiceBindings{Sockets: map[string]string{}, Release: func() {}}, nil
	}

	services := make(map[string]ServiceSpec, len(spec.Services))
	names := make([]string, 0, len(spec.Services))
	for _, svc := range spec.Services {
		services[svc.Name] = svc
		names = append(names, svc.Name)
	}
	sort.Strings(names)

	var lockFiles []*os.File
	var once sync.Once
	releaseLocks := func() {
		once.Do(func() {
			for _, f := range lockFiles {
				unix.Flock(int(f.Fd()), unix.LOCK_UN)
				f.Close()
			}
		})
	}
	// Every error path releases the locks acquired so far; a successful return
	// hands ownership to the caller via ServiceBindings.Release.
	release := releaseLocks
	defer func() { release() }()

	bindings := map[string]string{}
	for _, name := range names {
		lock, err := acquireServiceLock(name)
		if err != nil {
			return ServiceBindings{Sockets: map[string]string{}, Release: func() {}}, fmt.Errorf("lock service %s: %w", name, err)
		}
		lockFiles = append(lockFiles, lock)

		if err := d.startService(ctx, spec, services[name], w, pull, bindings); err != nil {
			return ServiceBindings{Sockets: map[string]string{}, Release: func() {}}, err
		}
	}

	release = func() {}
	return ServiceBindings{Sockets: bindings, Release: releaseLocks}, nil
}

func (d *DockerRuntime) startService(ctx context.Context, spec Spec, svc ServiceSpec, w ProgressWriter, pull bool, bindings map[string]string) error {
	name := svc.Name
	containerName := serviceContainerName(name, spec.Workspace.Mode)

	existing, err := findServiceContainer(ctx, d.cli, containerName)
	if err != nil {
		return fmt.Errorf("find service container %s: %w", containerName, err)
	}
	switch {
	case existing == nil:
	case existing.State != "running":
		// A stopped same-named straggler from a SIGKILL'd tpd must not be
		// reused; remove it and start fresh.
		if err := d.cli.ContainerRemove(ctx, existing.ID, container.RemoveOptions{Force: true}); err != nil {
			return fmt.Errorf("remove stale service container %s: %w", existing.ID, err)
		}
	case existing.Labels[ServiceHashLabel] == svc.Hash:
		// Reuse never probes: the running daemon is already healthy by
		// definition, and probing it would add latency for no signal.
		// --pull still refreshes a mutable base tag.
		if err := ensureImagePulled(ctx, d.cli, svc.Image, w, pull); err != nil {
			return fmt.Errorf("ensure service image: %w", err)
		}
		fillBindings(bindings, name, spec.Workspace.Mode, svc)
		return nil
	default:
		// A shared daemon must never be killed under a live consumer. Only
		// recreate when nobody is attached.
		consumers, err := serviceConsumers(ctx, d.cli, name)
		if err != nil {
			return err
		}
		if len(consumers) > 0 {
			return fmt.Errorf("service %s is running with a different config and is in use by container(s) %s; stop them or rename the service", name, strings.Join(consumers, ", "))
		}
		if err := d.cli.ContainerStop(ctx, existing.ID, container.StopOptions{}); err != nil {
			return fmt.Errorf("stop service container %s: %w", existing.ID, err)
		}
		if err := d.cli.ContainerRemove(ctx, existing.ID, container.RemoveOptions{Force: true}); err != nil {
			return fmt.Errorf("remove service container %s: %w", existing.ID, err)
		}
	}

	return d.createService(ctx, spec, svc, w, pull, bindings)
}

func (d *DockerRuntime) createService(ctx context.Context, spec Spec, svc ServiceSpec, w ProgressWriter, pull bool, bindings map[string]string) error {
	name := svc.Name
	mode := spec.Workspace.Mode
	runDir := serviceRunDir(name, mode)

	if err := ensureImagePulled(ctx, d.cli, svc.Image, w, pull); err != nil {
		return fmt.Errorf("ensure service image: %w", err)
	}
	baseID, err := ResolveImageID(ctx, d.cli, svc.Image)
	if err != nil {
		return fmt.Errorf("resolve service image: %w", err)
	}

	imageRef := svc.Image
	if len(svc.Packages) > 0 || len(svc.Repos) > 0 {
		if err := checkExtrepoOnly(svc.Repos); err != nil {
			return err
		}
		derivedRef := DerivedTag(baseID, svc.Packages, svc.Repos)
		exists, err := imageExists(ctx, d.cli, derivedRef)
		if err != nil {
			return err
		}
		if !exists {
			if err := buildDerivedImage(ctx, d.cli, derivedRef, svc.Image, svc.Repos, svc.Packages, w); err != nil {
				return fmt.Errorf("build service image: %w", err)
			}
		}
		imageRef = derivedRef
	}

	subpath := d.subpathSupported(ctx)
	volumes := map[string][]string{}
	for _, c := range svc.Caches {
		if subpath {
			volumes[c.Name] = append(volumes[c.Name], c.Subpath)
		} else {
			volumes[c.Name+"-"+c.Subpath] = nil
		}
	}
	for name := range volumes {
		if err := EnsureVolume(ctx, d.cli, name); err != nil {
			return fmt.Errorf("cache volume %s: %w", name, err)
		}
	}
	if err := d.ensureCacheSubpaths(ctx, svc.Image, volumes); err != nil {
		return fmt.Errorf("prepare service caches: %w", err)
	}

	// The host run-dir holds each exposed socket; the service writes the
	// socket into a bind-mounted parent dir, which appears on the host at
	// runDir+exposePath where the probe and the main container reach it.
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return fmt.Errorf("create service run dir: %w", err)
	}
	// Two exposes sharing a parent dir produce one bind mount, not two.
	exposeParents := map[string]bool{}
	for _, exposePath := range svc.Exposes {
		parent := filepath.Dir(exposePath)
		if err := os.MkdirAll(serviceSocketPath(name, mode, parent), 0o700); err != nil {
			return fmt.Errorf("create service expose dir: %w", err)
		}
		exposeParents[parent] = true
		// A force-removed service leaves dead sockets behind; the next
		// launch's probe would otherwise succeed on a socket nobody serves.
		if err := os.Remove(serviceSocketPath(name, mode, exposePath)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale socket: %w", err)
		}
	}
	parents := make([]string, 0, len(exposeParents))
	for parent := range exposeParents {
		parents = append(parents, parent)
	}
	sort.Strings(parents)

	mounts, err := buildServiceMounts(svc, subpath, runDir, parents)
	if err != nil {
		return err
	}

	labels := make(map[string]string, len(svc.Labels)+1)
	for k, v := range svc.Labels {
		labels[k] = v
	}
	labels[ServiceRoleLabel] = ServiceRoleSidecar

	initEnabled := true
	env := []string{"HOME=/root"}
	for k, v := range svc.Env {
		if v != "" {
			env = append(env, k+"="+v)
		}
	}

	resp, err := d.cli.ContainerCreate(ctx, &container.Config{
		Image:      imageRef,
		Cmd:        svc.Command,
		Env:        env,
		User:       containerUser,
		WorkingDir: "/",
		Labels:     labels,
	}, &container.HostConfig{
		Mounts:      mounts,
		NetworkMode: "",
		SecurityOpt: d.securityOpts(),
		Init:        &initEnabled,
	}, nil, nil, serviceContainerName(name, mode))
	if err != nil {
		return fmt.Errorf("create service container: %w", err)
	}
	containerID := resp.ID

	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := d.cli.ContainerRemove(cleanupCtx, containerID, container.RemoveOptions{Force: true}); err != nil {
			fmt.Fprintf(os.Stderr, "tpd: warning: remove service container %s: %v\n", containerID, err)
		}
	}

	if len(svc.Files) > 0 {
		if err := writeContainerFiles(ctx, d.cli, containerID, svc.Files, 0, 0); err != nil {
			cleanup()
			return fmt.Errorf("write service files: %w", err)
		}
	}

	if err := d.cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		cleanup()
		return fmt.Errorf("start service container: %w", err)
	}

	for socketName, exposePath := range svc.Exposes {
		if err := waitForServiceSocket(serviceSocketPath(name, mode, exposePath)); err != nil {
			cleanup()
			return fmt.Errorf("service %s did not expose socket %s within %s", name, socketName, serviceProbeTimeout)
		}
		bindings[name+"/"+socketName] = serviceSocketPath(name, mode, exposePath)
	}
	return nil
}

// buildServiceMounts assembles a service container's mounts: cache volumes
// (same subpath/fallback logic as buildMounts), one bind per unique expose
// parent dir from the host run-dir, then the service's own host-bind mounts
// with their Optional/Create semantics. Never called for the main container's
// workspace bind: services get no access to the user's project.
func buildServiceMounts(svc ServiceSpec, subpath bool, runDir string, exposeParents []string) ([]mount.Mount, error) {
	var m []mount.Mount
	for _, c := range svc.Caches {
		mnt := mount.Mount{Type: mount.TypeVolume, Source: c.Name, Target: c.Target}
		if subpath {
			mnt.VolumeOptions = &mount.VolumeOptions{Subpath: c.Subpath}
		} else {
			mnt.Source = c.Name + "-" + c.Subpath
		}
		m = append(m, mnt)
	}
	for _, parent := range exposeParents {
		m = append(m, mount.Mount{
			Type:   mount.TypeBind,
			Source: filepath.Join(runDir, parent),
			Target: parent,
		})
	}
	for _, mt := range svc.Mounts {
		if mt.Create {
			if _, err := os.Stat(mt.Source); os.IsNotExist(err) {
				if err := os.MkdirAll(mt.Source, 0o755); err != nil {
					if mt.Optional {
						fmt.Fprintf(os.Stderr, "warning: creating mount source %s: %v\n", mt.Source, err)
						continue
					}
					return nil, fmt.Errorf("create mount source %s: %w", mt.Source, err)
				}
				fmt.Fprintf(os.Stderr, "creating mount source %s\n", mt.Source)
			}
		}
		if mt.Optional {
			if _, err := os.Stat(mt.Source); err != nil {
				continue
			}
		}
		m = append(m, mount.Mount{
			Type:     mount.TypeBind,
			Source:   mt.Source,
			Target:   mt.Target,
			ReadOnly: mt.ReadOnly,
		})
	}
	return m, nil
}

// fillBindings populates the socket bindings for a reused service from the
// deterministic run-dir path, without probing.
func fillBindings(bindings map[string]string, name string, mode workspace.Mode, svc ServiceSpec) {
	for socketName, exposePath := range svc.Exposes {
		bindings[name+"/"+socketName] = serviceSocketPath(name, mode, exposePath)
	}
}

// findServiceContainer locates the container with the exact deterministic
// service name (names may arrive slash-prefixed), including stopped ones.
func findServiceContainer(ctx context.Context, cli *client.Client, containerName string) (*types.Container, error) {
	f := filters.NewArgs(filters.Arg("name", containerName))
	list, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}
	for i := range list {
		for _, n := range list[i].Names {
			if strings.TrimPrefix(n, "/") == containerName {
				return &list[i], nil
			}
		}
	}
	return nil, nil
}

// serviceConsumers returns the display names of all non-exited containers
// whose tpd.uses-service label lists name. The lookup is a label-presence
// filter plus an in-Go membership match: a value filter can't substring-match
// safely (a service named "a" would match "ab,cd"). All: true so a
// created-but-not-started main container from a concurrent launch still counts
// as a live consumer; only exited/dead containers release a service.
func serviceConsumers(ctx context.Context, cli *client.Client, name string) ([]string, error) {
	f := filters.NewArgs(filters.Arg("label", UsesServiceLabel))
	list, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}
	var consumers []string
	for _, c := range list {
		if c.State == "exited" || c.State == "dead" {
			continue
		}
		for _, n := range strings.Split(c.Labels[UsesServiceLabel], ",") {
			if strings.TrimSpace(n) == name {
				consumers = append(consumers, containerDisplayName(c))
				break
			}
		}
	}
	return consumers, nil
}

func containerDisplayName(c types.Container) string {
	for _, n := range c.Names {
		if n = strings.TrimPrefix(n, "/"); n != "" {
			return n
		}
	}
	if len(c.ID) > 12 {
		return c.ID[:12]
	}
	return c.ID
}

// waitForServiceSocket probes a unix socket with connect() (a stale file or a
// touched-but-not-accepting socket both fail the dial) until it accepts or the
// deadline passes.
func waitForServiceSocket(path string) error {
	deadline := time.Now().Add(serviceProbeTimeout)
	for {
		conn, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("socket %s did not appear", path)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// StopServices stops and removes each service container once no container
// consumes it. Safe to run concurrently with another launch's StartServices:
// the per-service lock serializes the stop decision, and All: true consumer
// lookup counts a created-but-not-started main container as a live consumer.
func (d *DockerRuntime) StopServices(ctx context.Context, spec Spec) error {
	if spec.Workspace.Mode == workspace.ModeRootful {
		return nil
	}
	names := make([]string, 0, len(spec.Services))
	for _, svc := range spec.Services {
		names = append(names, svc.Name)
	}
	sort.Strings(names)
	var errs []error
	for _, name := range names {
		if err := d.stopService(ctx, name, spec.Workspace.Mode); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (d *DockerRuntime) stopService(ctx context.Context, name string, mode workspace.Mode) error {
	lock, err := acquireServiceLock(name)
	if err != nil {
		return fmt.Errorf("lock service %s: %w", name, err)
	}
	defer func() {
		unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		lock.Close()
	}()

	consumers, err := serviceConsumers(ctx, d.cli, name)
	if err != nil {
		return err
	}
	if len(consumers) > 0 {
		return nil
	}

	containerName := serviceContainerName(name, mode)
	c, err := findServiceContainer(ctx, d.cli, containerName)
	if err != nil {
		return fmt.Errorf("find service container %s: %w", containerName, err)
	}
	if c == nil {
		return nil
	}
	if err := d.cli.ContainerStop(ctx, c.ID, container.StopOptions{}); err != nil {
		return fmt.Errorf("stop service container %s: %w", c.ID, err)
	}
	if err := d.cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("remove service container %s: %w", c.ID, err)
	}
	return nil
}
