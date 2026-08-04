package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	"github.com/jgillich/tpd/internal/mise"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func (d *DockerRuntime) Run(ctx context.Context, spec Spec) (int, error) {
	runtimeHome := spec.RuntimeHome

	// Run an init process (tini) as PID 1 so SIGINT/SIGTERM forwarded to the
	// container reach the wrapped command; the kernel ignores signals sent to
	// a bare PID 1 that has no handler for them.
	initEnabled := true

	// Run as root so the wrapper can create/chown $HOME and the volumes, then
	// drop to the host user via setpriv (see containerIdentity).
	userns, rootUser, hostUID, hostGID := containerIdentity(d.podman)

	configDir := filepath.Join(runtimeHome, ".config", "mise")
	activateCmd := mise.ActivateCommand(configDir, spec.Tools)
	// The container's WorkingDir is the runtime home (see launch.go) so
	// Podman keep-id derives the passwd home from it; the command itself
	// cd's into the workspace so the actual cwd matches the user's.
	parts := []string{"cd " + shq(spec.Workspace.Target)}
	if activateCmd != "" {
		parts = append(parts, activateCmd)
	}
	if cmd := mise.BackendRuntimesCommand(configDir, spec.Tools); cmd != "" {
		parts = append(parts, cmd)
	}
	if mise.NeedsEmbeddedPlugin(spec.Tools) {
		parts = append(parts, mise.PluginInstallCommand())
	}
	parts = append(parts, "mise install")
	parts = append(parts, `eval "$(mise hook-env 2>/dev/null)" || true`)

	parts = append(parts, "exec "+shellQuote(spec.Command))
	userCmd := strings.Join(parts, " && ")

	writable := []string{runtimeHome}
	for _, c := range spec.Caches {
		writable = append(writable, c.Target)
	}
	writable = append(writable, homeParents(runtimeHome, mountTargets(spec))...)
	writable = append(writable, homeParents(runtimeHome, fileTargets(spec))...)
	bootstrap := fmt.Sprintf("mkdir -p %s && chown %d:%d %s", shq(runtimeHome), hostUID, hostGID, quoteJoin(writable))
	cmd := wrapAsUser(bootstrap, hostUID, hostGID, []string{"sh", "-c", userCmd})

	mounts, err := buildMounts(spec, runtimeHome, d.subpathSupported(ctx))
	if err != nil {
		return 3, fmt.Errorf("build mounts: %w", err)
	}
	envList := buildEnv(spec, runtimeHome)
	containerName := "tpd-" + spec.ProfileName + "-" + randomID(8)

	exposedPorts, portBindings := buildPortBindings(spec)
	devices := buildDevices(spec)
	cgroupRules := buildDeviceCgroupRules(spec)

	tty := spec.TTY == "true" || ((spec.TTY == "auto" || spec.TTY == "") && term.IsTerminal(int(os.Stdout.Fd())))

	var oldState *term.State
	if tty && term.IsTerminal(int(os.Stdin.Fd())) {
		oldState, err = term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			return 3, fmt.Errorf("set raw mode: %w", err)
		}
		defer term.Restore(int(os.Stdin.Fd()), oldState)
	}

	resp, err := d.cli.ContainerCreate(ctx, &container.Config{
		Image:        spec.Image,
		Cmd:          cmd,
		Env:          envList,
		User:         rootUser,
		Tty:          tty,
		OpenStdin:    true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   spec.RuntimeHome,
		Labels:       spec.Labels,
		Hostname:     spec.ProfileName,
		Entrypoint:   []string{},
		ExposedPorts: exposedPorts,
	}, &container.HostConfig{
		Mounts:       mounts,
		NetworkMode:  container.NetworkMode(spec.Network),
		UsernsMode:   userns,
		SecurityOpt:  d.securityOpts(),
		AutoRemove:   false,
		PortBindings: portBindings,
		Init:         &initEnabled,
		Resources: container.Resources{
			Devices:           devices,
			DeviceCgroupRules: cgroupRules,
			Memory:            spec.Resources.MemoryBytes,
			NanoCPUs:          spec.Resources.NanoCPUs,
		},
	}, &network.NetworkingConfig{}, nil, containerName)
	if err != nil {
		return 3, fmt.Errorf("create container: %w", err)
	}

	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := d.cli.ContainerRemove(cleanupCtx, resp.ID, container.RemoveOptions{Force: true}); err != nil {
			fmt.Fprintf(os.Stderr, "tpd: warning: remove container %s: %v\n", resp.ID, err)
		}
	}()

	if len(spec.Files) > 0 {
		if err := writeContainerFiles(ctx, d.cli, resp.ID, spec.Files, hostUID, hostGID); err != nil {
			return 3, fmt.Errorf("write profile files: %w", err)
		}
	}

	// Attach BEFORE start so we don't miss early output (spec §3.3).
	// attachAndPump would block until the stream closes, but the container
	// hasn't started yet — that's a deadlock. So we split: attach here,
	// start the container, then pump in a goroutine.
	hijacked, err := d.cli.ContainerAttach(ctx, resp.ID, container.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return 3, fmt.Errorf("attach: %w", err)
	}
	defer hijacked.Close()

	if tty {
		rows, cols := terminalSize()
		if rows > 0 && cols > 0 {
			_ = d.cli.ContainerResize(ctx, resp.ID, container.ResizeOptions{
				Height: rows,
				Width:  cols,
			})
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer func() {
		signal.Stop(sigCh)
		close(sigCh)
	}()

	if tty {
		winCh := make(chan os.Signal, 1)
		signal.Notify(winCh, syscall.SIGWINCH)
		defer func() {
			signal.Stop(winCh)
			close(winCh)
		}()
		go d.handleResize(ctx, resp.ID, winCh)
	}

	go func() {
		for sig := range sigCh {
			if s, ok := sig.(syscall.Signal); ok {
				_ = d.cli.ContainerKill(ctx, resp.ID, strconv.Itoa(int(s)))
			}
		}
	}()

	// Raw mode disables ISIG, so Ctrl+Z reaches the pump as a byte (see the
	// stdin pump); catch SIGTSTP too so suspend works however the signal
	// arrives. Without it, a TUI in the container that stops itself on
	// Ctrl+Z (opencode's suspend keybind) leaves this process holding a raw
	// terminal the shell can never regain.
	if oldState != nil {
		stopCh := make(chan os.Signal, 1)
		signal.Notify(stopCh, syscall.SIGTSTP)
		defer func() {
			signal.Stop(stopCh)
			close(stopCh)
		}()
		go func() {
			for range stopCh {
				d.suspendSession(ctx, resp.ID, oldState)
			}
		}()
	}

	if err := d.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return 3, fmt.Errorf("start container: %w", err)
	}

	for _, p := range spec.PortSpecs {
		ip := p.HostIP
		if ip == "" || ip == "0.0.0.0" {
			ip = "127.0.0.1"
		}
		fmt.Fprintf(os.Stderr, "listening on %s://%s:%s\r\n", p.Protocol, ip, p.HostPort)
	}

	// Pump streams AFTER start. This blocks until the container exits and
	// the output stream closes.
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		if tty {
			if _, err := io.Copy(os.Stdout, hijacked.Reader); err != nil {
				fmt.Fprintf(os.Stderr, "stdout pump: %v\n", err)
			}
		} else {
			if _, err := stdcopy.StdCopy(os.Stdout, os.Stderr, hijacked.Reader); err != nil {
				fmt.Fprintf(os.Stderr, "stdout pump: %v\n", err)
			}
		}
	}()

	// stdinPump copies os.Stdin to the hijacked connection. Unlike io.Copy,
	// it guards writes with a mutex so that shutdown can close the
	// connection without the goroutine writing to a closed socket (which
	// produces "broken pipe" / "use of closed network connection" errors).
	// In raw mode Ctrl+Z is a plain 0x1A byte, so the pump intercepts it and
	// suspends the whole session; forwarding it would let a TUI that stops
	// itself on Ctrl+Z hang the terminal.
	var connMu sync.Mutex
	connClosed := false
	writeConn := func(b []byte) bool {
		connMu.Lock()
		defer connMu.Unlock()
		if connClosed {
			return false
		}
		_, err := hijacked.Conn.Write(b)
		return err == nil
	}
	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		buf := make([]byte, 32*1024)
		for {
			n, readErr := os.Stdin.Read(buf)
			if n > 0 {
				rest := buf[:n]
				for {
					i := bytes.IndexByte(rest, ctrlZ)
					if i < 0 {
						if !writeConn(rest) {
							return
						}
						break
					}
					if i > 0 && !writeConn(rest[:i]) {
						return
					}
					if oldState != nil {
						d.suspendSession(ctx, resp.ID, oldState)
					}
					rest = rest[i+1:]
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	// closeConn safely closes the hijacked connection, ensuring the stdin
	// pump goroutine stops writing before the connection is torn down.
	closeConn := func() {
		connMu.Lock()
		connClosed = true
		hijacked.Close()
		connMu.Unlock()
	}

	statusCh, errCh := d.cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		<-pumpDone
		closeConn()
		return 3, fmt.Errorf("container wait: %w", err)
	case status := <-statusCh:
		<-pumpDone
		closeConn()
		return int(status.StatusCode), nil
	}
}

// ctrlZ is the raw-mode byte produced by Ctrl+Z; with ISIG off the kernel
// does not turn it into SIGTSTP.
const ctrlZ = 0x1A

// suspendSession hands the terminal back to the shell and stops this process
// (and the container command) so the session can be backgrounded with Ctrl+Z
// and resumed with fg. Without it, a TUI in the container that stops itself
// on Ctrl+Z (opencode's suspend keybind) leaves tpd holding a raw terminal
// with nothing to wake it. The terminal is restored before stopping so the
// shell can repaint; on resume we re-enter raw mode and continue the
// container.
func (d *DockerRuntime) suspendSession(ctx context.Context, containerID string, oldState *term.State) {
	if oldState == nil {
		return
	}
	_ = term.Restore(int(os.Stdin.Fd()), oldState)
	// Pause the container command too (tini forwards SIGTSTP); best effort,
	// the container may already have exited.
	_ = d.cli.ContainerKill(ctx, containerID, strconv.Itoa(int(syscall.SIGTSTP)))
	// SIGSTOP cannot be caught, so fg resumes us right after this call.
	_ = unix.Kill(unix.Getpid(), unix.SIGSTOP)
	_, _ = term.MakeRaw(int(os.Stdin.Fd()))
	_ = d.cli.ContainerKill(ctx, containerID, strconv.Itoa(int(syscall.SIGCONT)))
	rows, cols := terminalSize()
	if rows > 0 && cols > 0 {
		_ = d.cli.ContainerResize(ctx, containerID, container.ResizeOptions{Height: rows, Width: cols})
	}
}

// tarFiles renders the container-file tar stream: one regular file entry per
// target with a relative path (CopyToContainer untars at "/"), the file's
// mode, and the execution user's uid/gid. The header requests FormatPAX
// (Go's writer falls back to USTAR for short headers and uses PAX only when
// needed, e.g. long paths); explicit TypeReg/mode/uid/gid are the real
// guarantees.
func tarFiles(files []FileSpec, uid, gid int) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		rel := strings.TrimPrefix(f.Target, "/")
		if err := tw.WriteHeader(&tar.Header{
			Name:     rel,
			Typeflag: tar.TypeReg,
			Mode:     int64(f.Mode),
			Uid:      uid,
			Gid:      gid,
			Size:     int64(len(f.Content)),
			Format:   tar.FormatPAX,
		}); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(f.Content)); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeContainerFiles untars the profile's files into the created-but-not-
// yet-started container, so they exist before the command runs and are owned
// by the execution user.
func writeContainerFiles(ctx context.Context, cli *client.Client, containerID string, files []FileSpec, uid, gid int) error {
	data, err := tarFiles(files, uid, gid)
	if err != nil {
		return err
	}
	return cli.CopyToContainer(ctx, containerID, "/", bytes.NewReader(data), container.CopyToContainerOptions{})
}

func fileTargets(spec Spec) []string {
	targets := make([]string, 0, len(spec.Files))
	for _, f := range spec.Files {
		targets = append(targets, f.Target)
	}
	return targets
}

// homeParents returns the engine-created (root-owned) parent dirs under home
// of the mount targets; chowning them lets the user create non-mounted paths
// like $HOME/.config/mise. Mount targets themselves are excluded so a chown
// never propagates into a bind-mounted host directory.
func homeParents(home string, targets []string) []string {
	leaf := make(map[string]bool, len(targets))
	for _, t := range targets {
		leaf[t] = true
	}
	var out []string
	seen := map[string]bool{home: true}
	for _, t := range targets {
		if !strings.HasPrefix(t, home+"/") {
			continue
		}
		for dir := filepath.Dir(t); dir != home && dir != "/" && dir != "."; dir = filepath.Dir(dir) {
			if seen[dir] || leaf[dir] {
				continue
			}
			seen[dir] = true
			out = append(out, dir)
		}
	}
	return out
}

func mountTargets(spec Spec) []string {
	targets := []string{spec.Workspace.Target}
	for _, mt := range spec.Mounts {
		targets = append(targets, mt.Target)
	}
	for _, c := range spec.Caches {
		targets = append(targets, c.Target)
	}
	return targets
}

// containerUser runs the container as root so the bootstrap can create/chown
// $HOME before setpriv drops to the host user.
const containerUser = "0:0"

// securityOpts disables SELinux label separation when SELinux is enforcing so
// bind-mounted host paths (workspace, home, dbus socket) keep their host
// labels and stay readable to the container. Relabeling with :Z would relabel
// the user's own files, breaking host access to the shared workspace.
func (d *DockerRuntime) securityOpts() []string {
	if d.selinux {
		return []string{"label=disable"}
	}
	return nil
}

// containerIdentity returns the userns mode, the container user, and the host
// uid/gid to drop to via setpriv — getuid() must match SO_PEERCRED for
// dbus-broker EXTERNAL auth. Rootless Podman needs keep-id so the dropped uid
// equals the host uid.
func containerIdentity(podman bool) (container.UsernsMode, string, int, int) {
	if podman {
		return "keep-id", containerUser, os.Getuid(), os.Getgid()
	}
	return "", containerUser, os.Getuid(), os.Getgid()
}

func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func quoteJoin(paths []string) string {
	quoted := make([]string, len(paths))
	for i, p := range paths {
		quoted[i] = shq(p)
	}
	return strings.Join(quoted, " ")
}

// wrapAsUser runs a root bootstrap that creates/chowns the user-owned
// locations, then drops to the host user via setpriv (all caps dropped).
// Images without setpriv fall back to running un-dropped.
func wrapAsUser(bootstrap string, uid, gid int, shellCmd []string) []string {
	inner := strings.Join(shellCmd, " ")
	if len(shellCmd) == 3 && shellCmd[0] == "sh" && shellCmd[1] == "-c" {
		inner = shellCmd[2]
	}
	drop := fmt.Sprintf("exec setpriv --reuid=%d --regid=%d --clear-groups --inh-caps=-all --bounding-set=-all sh -c %s", uid, gid, shq(inner))
	fallback := fmt.Sprintf(`echo "tpd: setpriv not found, running as root" >&2; exec sh -c %s`, shq(inner))
	run := fmt.Sprintf("if command -v setpriv >/dev/null 2>&1; then %s; else %s; fi", drop, fallback)
	return []string{"sh", "-c", bootstrap + " && " + run}
}

func buildMounts(spec Spec, runtimeHome string, subpath bool) ([]mount.Mount, error) {
	m := []mount.Mount{
		{Type: mount.TypeBind, Source: spec.Workspace.HostPath, Target: spec.Workspace.Target},
	}
	for _, mt := range spec.Mounts {
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
	if rtDir := spec.Env["XDG_RUNTIME_DIR"]; rtDir != "" {
		busPath := filepath.Join(rtDir, "bus")
		overlaid := false
		for _, existing := range m {
			if existing.Target == busPath {
				overlaid = true
				break
			}
		}
		if !overlaid {
			m = append(m, mount.Mount{
				Type:   mount.TypeBind,
				Source: "/dev/null",
				Target: busPath,
			})
		}
	}
	for _, c := range spec.Caches {
		mnt := mount.Mount{Type: mount.TypeVolume, Source: c.Name, Target: c.Target}
		if subpath {
			mnt.VolumeOptions = &mount.VolumeOptions{Subpath: c.Subpath}
		} else {
			// Engines that ignore VolumeOptions.Subpath get a dedicated
			// volume per target so each path stays separate.
			mnt.Source = c.Name + "-" + c.Subpath
		}
		m = append(m, mnt)
	}
	return m, nil
}

func buildPortBindings(spec Spec) (nat.PortSet, nat.PortMap) {
	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, p := range spec.PortSpecs {
		port := nat.Port(p.Container + "/" + p.Protocol)
		exposed[port] = struct{}{}
		bindings[port] = []nat.PortBinding{{HostIP: p.HostIP, HostPort: p.HostPort}}
	}
	return exposed, bindings
}

func buildDevices(spec Spec) []container.DeviceMapping {
	var out []container.DeviceMapping
	for _, d := range spec.DeviceSpecs {
		if _, err := os.Stat(d.Host); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping device %s: %v\n", d.Container, err)
			continue
		}
		out = append(out, container.DeviceMapping{
			PathOnHost:        d.Host,
			PathInContainer:   d.Container,
			CgroupPermissions: d.Perms,
		})
	}
	return out
}

func buildDeviceCgroupRules(spec Spec) []string {
	var out []string
	for _, d := range spec.DeviceSpecs {
		if !d.Cgroup {
			continue
		}
		major, minor, prefix, ok := deviceMajorMinor(d.Host)
		if !ok {
			fmt.Fprintf(os.Stderr, "warning: device %s: cannot stat %s, no cgroup rule emitted\n", d.Container, d.Host)
			continue
		}
		rule := fmt.Sprintf("%s %d:%d rwm", prefix, major, minor)
		if prefix == "" {
			rule = fmt.Sprintf("c %d:* rwm", major)
			fmt.Fprintf(os.Stderr, "warning: device %s: no device type for %d:%d, using broad rule %s\n", d.Container, major, minor, rule)
		}
		out = append(out, rule)
	}
	return out
}

// deviceTypePrefix maps a device node's stat mode to a cgroup device rule
// prefix. The type comes from st_mode rather than /sys/dev lookups because
// major numbers are shared across device classes (major 7 is both the loop
// block family and the vcs char family).
func deviceTypePrefix(mode uint32) string {
	switch mode & unix.S_IFMT {
	case unix.S_IFCHR:
		return "c"
	case unix.S_IFBLK:
		return "b"
	}
	return ""
}

func deviceMajorMinor(path string) (int, int, string, bool) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, 0, "", false
	}
	return int(unix.Major(uint64(st.Rdev))), int(unix.Minor(uint64(st.Rdev))), deviceTypePrefix(st.Mode), true
}

func buildEnv(spec Spec, runtimeHome string) []string {
	env := []string{
		"HOME=" + runtimeHome,
		"MISE_CONFIG_DIR=" + filepath.Join(runtimeHome, ".config", "mise"),
		// aube (mise's npm backend) defaults its cache and store to $HOME, which
		// is ephemeral inside the container; the mise profile declares an `aube`
		// cache volume at ~/.aube, so point aube there to survive container exit.
		"AUBE_CACHE_DIR=" + filepath.Join(runtimeHome, ".aube", "cache"),
		"AUBE_STORE_DIR=" + filepath.Join(runtimeHome, ".aube", "store"),
	}
	for k, v := range spec.Env {
		if v == "" {
			// Empty values are not set; forward a host variable via {{ .Env.FOO }}.
			continue
		}
		env = append(env, k+"="+v)
	}
	return env
}

func shellQuote(cmd []string) string {
	var parts []string
	for _, s := range cmd {
		escaped := strings.ReplaceAll(s, "'", `'\''`)
		parts = append(parts, "'"+escaped+"'")
	}
	return strings.Join(parts, " ")
}

func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.Itoa(os.Getpid())
	}
	return hex.EncodeToString(b)
}
