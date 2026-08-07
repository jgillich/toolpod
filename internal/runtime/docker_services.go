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
	"syscall"
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
	// Rootful sockets must live where the host tpd (a non-root user) can
	// create, unlink, and probe them; /tmp is user-writable and transient, and
	// the uid suffix keeps this tree distinct from a concurrent rootless run.
	return fmt.Sprintf("/tmp/tpd-svc-%s-%d/", name, os.Getuid())
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

// validateServiceExposePath rejects an expose socket path that would escape the
// host run-dir: it must be absolute, free of ".." segments, and sit below a
// non-root parent. The socket's parent dir is bind-mounted from the run-dir and
// the socket name is joined onto it, so a root parent or traversal would place
// the socket over the service container root or outside the run-dir. This is
// the runtime boundary check (mirrors internal/profile.checkExposePath) so a
// hand-built Spec is safe even without the profile validator.
func validateServiceExposePath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("expose path %q must be absolute", path)
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." {
			return fmt.Errorf("expose path %q must not contain '..' segments", path)
		}
	}
	if filepath.Dir(path) == "/" {
		return fmt.Errorf("expose path %q must be inside a non-root directory", path)
	}
	return nil
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

// ensureServiceRunDir creates the host run-dir and rejects a pre-existing
// symlink or a directory owned by another user: /tmp is world-writable, so a
// malicious local user could otherwise redirect sockets or drop files. Expose
// parent dirs sit inside the verified dir (0700, ours), so they need no check.
func ensureServiceRunDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("service run dir %s is not a directory", path)
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("service run dir %s is not owned by the current user", path)
	}
	return nil
}

// StartServices finds-or-starts every service in spec.Services, holding the
// per-service lockfiles (acquired in sorted name order to prevent deadlock)
// until the caller invokes the returned Release. Locks are only released on
// error or via Release; Run's container-create must stay under the lock so a
// concurrent stop step can't see "zero consumers" mid-launch.
func (d *DockerRuntime) StartServices(ctx context.Context, spec Spec, w ProgressWriter, pull bool) (ServiceBindings, error) {
	if len(spec.Services) == 0 {
		return ServiceBindings{Sockets: map[string]string{}, Release: func() {}}, nil
	}

	// The shared network must exist before any container attaches; the helper
	// tolerates a concurrent launch racing the same create.
	if _, err := d.ensureServiceNetwork(ctx); err != nil {
		return ServiceBindings{Sockets: map[string]string{}, Release: func() {}}, err
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
	return ServiceBindings{Sockets: bindings, Network: ServiceNetworkName, Release: releaseLocks}, nil
}

func (d *DockerRuntime) startService(ctx context.Context, spec Spec, svc ServiceSpec, w ProgressWriter, pull bool, bindings map[string]string) error {
	name := svc.Name
	containerName := serviceContainerName(name, spec.Workspace.Mode)

	for _, exposePath := range svc.Exposes {
		if err := validateServiceExposePath(exposePath); err != nil {
			return fmt.Errorf("service %s: %w", name, err)
		}
	}

	existing, err := findServiceContainer(ctx, d.cli, name, containerName)
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
	case existing.Labels[ServiceHashLabel] == svc.Hash && serviceSocketsReusable(name, spec.Workspace.Mode, svc):
		// Reuse never probes: the running daemon is already healthy by
		// definition, and probing it would add latency for no signal.
		// --pull still refreshes a mutable base tag.
		if err := ensureImagePulled(ctx, d.cli, svc.Image, w, pull); err != nil {
			return fmt.Errorf("ensure service image: %w", err)
		}
		if err := d.ensureServiceAttached(ctx, existing.ID, name); err != nil {
			return err
		}
		fillBindings(bindings, name, spec.Workspace.Mode, svc)
		return nil
	default:
		// A stale hash or a missing expose socket forces recreation, even
		// under a live consumer: the old daemon is replaced, accepting a
		// brief outage rather than blocking the launch.
		consumers, err := serviceConsumers(ctx, d.cli, name)
		if err != nil {
			return err
		}
		if len(consumers) > 0 {
			fmt.Fprintf(os.Stderr, "tpd: warning: recreating service %s with a new config while in use by %s (brief downtime)\n", name, strings.Join(consumers, ", "))
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
		if err := ensureDerivedImage(ctx, d.cli, derivedRef, baseID, svc.Repos, svc.Packages, w); err != nil {
			return fmt.Errorf("service derived image: %w", err)
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
	if err := ensureServiceRunDir(runDir); err != nil {
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
		// A permission error is tolerated: the socket dir may be owned by the
		// previous instance's host subuid (a service that chowned it), in
		// which case the new instance's daemon replaces the socket on bind and
		// the probe's connect() already fails on a dead socket.
		path := serviceSocketPath(name, mode, exposePath)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			if os.IsPermission(err) {
				fmt.Fprintf(os.Stderr, "tpd: warning: cannot remove stale service socket %s: %v\n", path, err)
			} else {
				return fmt.Errorf("remove stale socket: %w", err)
			}
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
		Privileged:  svc.Privileged,
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
		if err := d.waitForServiceSocket(ctx, containerID, serviceSocketPath(name, mode, exposePath), exposePath, mode == workspace.ModeRootful); err != nil {
			cleanup()
			return fmt.Errorf("service %s did not expose socket %s within %s: %w", name, socketName, serviceProbeTimeout, err)
		}
		bindings[name+"/"+socketName] = serviceSocketPath(name, mode, exposePath)
	}
	if err := d.ConnectContainerToNetwork(ctx, containerID, ServiceNetworkName, []string{ServiceNetworkAlias(name)}); err != nil {
		cleanup()
		return err
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

// serviceSocketsReusable reports whether every exposed socket of a running
// service still exists on the host as a real socket. A live container whose
// sockets were unlinked (e.g. by the force-removal of a predecessor) cannot
// serve, so a hash match alone does not justify reuse.
func serviceSocketsReusable(name string, mode workspace.Mode, svc ServiceSpec) bool {
	for _, exposePath := range svc.Exposes {
		st, err := os.Lstat(serviceSocketPath(name, mode, exposePath))
		if err != nil || st.Mode()&os.ModeSocket == 0 {
			return false
		}
	}
	return true
}

// ForeignServiceContainerError reports that a container not created by tpd
// occupies a service's deterministic name. tpd never stops, removes, or creates
// over such a container: the owner must rename or remove it.
type ForeignServiceContainerError struct {
	ServiceName    string
	ContainerName string
}

func (e *ForeignServiceContainerError) Error() string {
	return fmt.Sprintf("container %q is not tpd-owned (a tpd service container needs %s=true and %s=%s); rename or remove it before using service %s", e.ContainerName, OwnershipLabel, ServiceLabel, e.ServiceName, e.ServiceName)
}

// findServiceContainer locates the container with the exact deterministic
// service name (names may arrive slash-prefixed), including stopped ones. A
// name match that lacks tpd's ownership labels is a foreign container: it is
// reported as a ForeignServiceContainerError so callers never reuse, stop,
// remove, or create over it.
func findServiceContainer(ctx context.Context, cli *client.Client, serviceName, containerName string) (*types.Container, error) {
	f := filters.NewArgs(filters.Arg("name", containerName))
	list, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}
	for i := range list {
		for _, n := range list[i].Names {
			if strings.TrimPrefix(n, "/") != containerName {
				continue
			}
			if list[i].Labels[OwnershipLabel] != "true" || list[i].Labels[ServiceLabel] != serviceName {
				return nil, &ForeignServiceContainerError{ServiceName: serviceName, ContainerName: containerName}
			}
			return &list[i], nil
		}
	}
	return nil, nil
}

// ensureServiceAttached repairs a reused service container's missing network
// membership, e.g. after an external network prune. Only a missing attachment
// is repaired; tpd does not inspect every reused container to verify its alias
// (the alias is set at create), so a legacy or externally modified container
// with the wrong alias is flagged by doctor, not rewritten here.
func (d *DockerRuntime) ensureServiceAttached(ctx context.Context, containerID, name string) error {
	inspected, err := d.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return fmt.Errorf("inspect service container %s: %w", containerID, err)
	}
	if inspected.NetworkSettings != nil && inspected.NetworkSettings.Networks[ServiceNetworkName] != nil {
		return nil
	}
	return d.ConnectContainerToNetwork(ctx, containerID, ServiceNetworkName, []string{ServiceNetworkAlias(name)})
}

// serviceConsumers returns the display names of all non-exited tpd-owned
// containers whose tpd.uses-service label lists name. The lookup is a
// label-presence filter plus an in-Go membership match: a value filter can't
// substring-match safely (a service named "a" would match "ab,cd"). All: true
// so a created-but-not-started main container from a concurrent launch still
// counts as a live consumer; only exited/dead containers release a service.
// Containers without tpd.managed=true are foreign and never count as
// consumers, so they cannot pin a service against replacement or removal.
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
		if c.Labels[OwnershipLabel] != "true" {
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
// touched-but-not-accepting socket both fail the dial) until it accepts, the
// deadline passes, or the context is canceled. In rootful mode the socket is
// created root-owned and is unconnectable by the host user, so the first time
// the host path appears as a socket we exec chown/chmod inside the service
// (running as root) to make it host-user-connectable before dialing.
func (d *DockerRuntime) waitForServiceSocket(ctx context.Context, containerID, hostPath, containerPath string, rootful bool) error {
	deadline := time.Now().Add(serviceProbeTimeout)
	dialer := net.Dialer{Timeout: time.Second}
	chowned := false
	for {
		if rootful && !chowned {
			if st, err := os.Lstat(hostPath); err == nil && st.Mode()&os.ModeSocket != 0 {
				if err := d.chownServiceSocket(ctx, containerID, containerPath); err != nil {
					return err
				}
				chowned = true
			}
		}
		conn, err := dialer.DialContext(ctx, "unix", hostPath)
		if err == nil {
			conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("socket %s did not appear", hostPath)
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// chownServiceSocket makes the socket file host-user-connectable. Rootful
// services create the socket root-owned; exec runs as root inside the service
// against the bind-mounted file. Safe: root keeps the bound socket, and peer
// credentials come from the connecting process, not the inode owner. Runs once
// per socket (see the caller); chmod 0770 is stricter than the daemon's 0755
// and still lets the owning host user connect.
func (d *DockerRuntime) chownServiceSocket(ctx context.Context, containerID, containerPath string) error {
	uidGid := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	for _, cmd := range [][]string{
		{"chown", uidGid, containerPath},
		{"chmod", "0770", containerPath},
	} {
		if err := d.runServiceExec(ctx, containerID, cmd); err != nil {
			return fmt.Errorf("exec %v: %w", cmd, err)
		}
	}
	return nil
}

// runServiceExec runs a command in a running container and waits for it to
// finish, surfacing a non-zero exit (e.g. a chown binary missing from an exotic
// service image) instead of silently proceeding. ContainerExecAttach can't be
// used for the wait: it doesn't report the exit code and needs a hijacked
// stream.
func (d *DockerRuntime) runServiceExec(ctx context.Context, containerID string, cmd []string) error {
	// Pin the exec to root so the socket chown keeps working even if a future
	// user: field on services changes the container's own user.
	execResp, err := d.cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{Cmd: cmd, User: "0:0"})
	if err != nil {
		return err
	}
	if err := d.cli.ContainerExecStart(ctx, execResp.ID, container.ExecStartOptions{}); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		inspect, err := d.cli.ContainerExecInspect(ctx, execResp.ID)
		if err != nil {
			return err
		}
		if !inspect.Running {
			if inspect.ExitCode != 0 {
				return fmt.Errorf("exit code %d", inspect.ExitCode)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("exec %v: did not finish within 10s", cmd)
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// StopServices stops and removes each service container once no container
// consumes it. Safe to run concurrently with another launch's StartServices:
// the per-service lock serializes the stop decision, and All: true consumer
// lookup counts a created-but-not-started main container as a live consumer.
func (d *DockerRuntime) StopServices(ctx context.Context, spec Spec) error {
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
	c, err := findServiceContainer(ctx, d.cli, name, containerName)
	if err != nil {
		return fmt.Errorf("find service container %s: %w", containerName, err)
	}
	if c != nil {
		if err := d.cli.ContainerStop(ctx, c.ID, container.StopOptions{}); err != nil {
			return fmt.Errorf("stop service container %s: %w", c.ID, err)
		}
		if err := d.cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
			return fmt.Errorf("remove service container %s: %w", c.ID, err)
		}
	}
	// The run-dir is host-side state (sockets, expose parents); remove it once
	// nobody consumes the service so dirs and stale exposes don't accumulate.
	return os.RemoveAll(serviceRunDir(name, mode))
}
