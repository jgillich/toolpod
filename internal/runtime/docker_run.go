package runtime

import (
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
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	"github.com/jgillich/toolpod/internal/mise"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func (d *DockerRuntime) Run(ctx context.Context, spec Spec) (int, error) {
	runtimeHome := spec.RuntimeHome

	// Build the container entrypoint:
	// 1. Write profile tools to mise global config + activate (sets up shims on PATH)
	// 2. Install project tools (mise auto-detects mise.toml/.tool-versions in CWD)
	// 3. Evaluate project environment (adds project tools to PATH immediately)
	// 4. If shell command, inject activate hooks into .bashrc for interactive cd-switching
	// 5. exec the profile command
	configDir := filepath.Join(runtimeHome, ".config", "mise")
	activateCmd := mise.ActivateCommand(configDir, spec.Tools)
	installCmd := "mise install 2>/dev/null || true"
	hookEnvCmd := `eval "$(mise hook-env 2>/dev/null)" || true`

	var parts []string
	for _, p := range []string{activateCmd, installCmd, hookEnvCmd} {
		if p != "" {
			parts = append(parts, p)
		}
	}

	if isShellCommand(spec.Command) {
		miseBin := "/usr/local/bin/mise"
		parts = append(parts, fmt.Sprintf("echo 'eval \"$(%s activate sh)\"' >> /root/.bashrc", miseBin))
	}

	parts = append(parts, "exec "+shellQuote(spec.Command))
	shellCmd := strings.Join(parts, " && ")
	cmd := []string{"sh", "-c", shellCmd}

	mounts := buildMounts(spec, runtimeHome)
	envList := buildEnv(spec, runtimeHome)
	containerName := "toolpod-" + spec.ProfileName + "-" + randomID(8)

	exposedPorts, portBindings := buildPortBindings(spec)
	devices := buildDevices(spec)
	cgroupRules := buildDeviceCgroupRules(spec)

	tty := spec.TTY == "true" || ((spec.TTY == "auto" || spec.TTY == "") && term.IsTerminal(int(os.Stdout.Fd())))

	var oldState *term.State
	var err error
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
		Tty:          tty,
		OpenStdin:    true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   spec.Workspace.Target,
		Labels:       spec.Labels,
		Hostname:     "toolpod",
		Entrypoint:   []string{},
		ExposedPorts: exposedPorts,
	}, &container.HostConfig{
		Mounts:       mounts,
		NetworkMode:  container.NetworkMode(spec.Network),
		AutoRemove:   false,
		PortBindings: portBindings,
		Resources: container.Resources{
			Devices:           devices,
			DeviceCgroupRules: cgroupRules,
		},
	}, &network.NetworkingConfig{}, nil, containerName)
	if err != nil {
		return 3, fmt.Errorf("create container: %w", err)
	}

	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = d.cli.ContainerRemove(cleanupCtx, resp.ID, container.RemoveOptions{Force: true})
	}()

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

	// Signal forwarding with cleanup
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

	if err := d.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return 3, fmt.Errorf("start container: %w", err)
	}

	for _, p := range spec.PortSpecs {
		ip := p.HostIP
		if ip == "" || ip == "0.0.0.0" {
			ip = "127.0.0.1"
		}
		fmt.Fprintf(os.Stderr, "listening on %s://%s:%s\n", p.Protocol, ip, p.HostPort)
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
	var connMu sync.Mutex
	connClosed := false
	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		buf := make([]byte, 32*1024)
		for {
			n, readErr := os.Stdin.Read(buf)
			if n > 0 {
				connMu.Lock()
				if connClosed {
					connMu.Unlock()
					return
				}
				_, writeErr := hijacked.Conn.Write(buf[:n])
				connMu.Unlock()
				if writeErr != nil {
					return
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

func buildMounts(spec Spec, runtimeHome string) []mount.Mount {
	m := []mount.Mount{
		{Type: mount.TypeBind, Source: spec.Workspace.HostPath, Target: spec.Workspace.Target},
	}
	for _, mt := range spec.Mounts {
		if mt.Optional {
			if _, err := os.Stat(mt.Source); err != nil {
				fmt.Fprintf(os.Stderr, "warning: skipping optional mount %s: %s not found\n", mt.Target, mt.Source)
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
	miseVol := mise.MiseVolume(runtimeHome)
	m = append(m, mount.Mount{
		Type:   mount.TypeVolume,
		Source: miseVol.Name,
		Target: miseVol.Target,
	})
	for _, c := range spec.Caches {
		m = append(m, mount.Mount{
			Type:   mount.TypeVolume,
			Source: c.Name,
			Target: c.Target,
		})
	}
	return m
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
		major, minor, ok := deviceMajorMinor(d.Host)
		if !ok {
			fmt.Fprintf(os.Stderr, "warning: device %s: cannot stat %s, no cgroup rule emitted\n", d.Container, d.Host)
			continue
		}
		prefix := deviceRulePrefix(major, minor)
		rule := fmt.Sprintf("%s %d:%d rwm", prefix, major, minor)
		if prefix == "" {
			rule = fmt.Sprintf("c %d:* rwm", major)
			fmt.Fprintf(os.Stderr, "warning: device %s: no sysfs entry for %d:%d, using broad rule %s\n", d.Container, major, minor, rule)
		}
		out = append(out, rule)
	}
	return out
}

func deviceRulePrefix(major, minor int) string {
	if _, err := os.Lstat(fmt.Sprintf("/sys/dev/char/%d:%d", major, minor)); err == nil {
		return "c"
	}
	if _, err := os.Lstat(fmt.Sprintf("/sys/dev/block/%d:%d", major, minor)); err == nil {
		return "b"
	}
	return ""
}

func deviceMajorMinor(path string) (int, int, bool) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, 0, false
	}
	return int(unix.Major(uint64(st.Rdev))), int(unix.Minor(uint64(st.Rdev))), true
}

func buildEnv(spec Spec, runtimeHome string) []string {
	env := []string{
		"HOME=" + runtimeHome,
		"MISE_CONFIG_DIR=" + filepath.Join(runtimeHome, ".config", "mise"),
	}
	for k, v := range spec.Env {
		if v == "" {
			if hostVal, ok := os.LookupEnv(k); ok {
				env = append(env, k+"="+hostVal)
			}
		} else {
			env = append(env, k+"="+v)
		}
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

func isShellCommand(cmd []string) bool {
	if len(cmd) == 0 {
		return false
	}
	base := filepath.Base(cmd[0])
	return base == "sh" || base == "bash" || base == "zsh" || base == "fish"
}
