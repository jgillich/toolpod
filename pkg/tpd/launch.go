package tpd

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jgillich/tpd/internal/profile"
	"github.com/jgillich/tpd/internal/runtime"
	"github.com/jgillich/tpd/internal/workspace"
)

// PortAllocator reserves an unused host port for a published binding.
// protocol is "tcp", "udp", or "sctp"; hostIP is the requested bind address
// ("" = all interfaces). Returns the allocated port as a string.
type PortAllocator func(protocol, hostIP string) (string, error)

func defaultPortAllocator(protocol, hostIP string) (string, error) {
	addr := net.JoinHostPort(hostIP, "0")
	switch protocol {
	case "udp":
		pc, err := net.ListenPacket("udp", addr)
		if err != nil {
			return "", err
		}
		defer pc.Close()
		return strconv.Itoa(pc.LocalAddr().(*net.UDPAddr).Port), nil
	default: // tcp (sctp auto-allocation is rejected at validation)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return "", err
		}
		defer ln.Close()
		return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port), nil
	}
}

func Launch(ctx context.Context, opts LaunchOpts) Result {
	return LaunchWithWriter(ctx, opts, os.Stdout)
}

func LaunchWithWriter(ctx context.Context, opts LaunchOpts, w io.Writer) Result {
	progress := opts.Progress
	if progress == nil {
		progress = &stderrProgress{}
	}
	userDir := opts.ProfileDir
	if userDir == "" {
		userDir = profile.DefaultProfileDir()
	}
	cat, err := profile.LoadProfiles(userDir)
	if err != nil {
		return Result{ExitCode: 2, Err: err}
	}
	// Fragments are composition-only: they carry no image or command, so
	// launching one is impossible. ResolveProfile would fail with a confusing
	// "missing required field: command" pointing at the fragment file; reject
	// here with the composition path instead.
	if ref, err := cat.ParseRefForCatalog(opts.ProfileName); err == nil {
		if key, ok := cat.ResolveRef(ref); ok && cat.IsFragment(key) {
			name := strings.TrimPrefix(key, "core/")
			return Result{ExitCode: 2, Err: fmt.Errorf("fragment %q cannot be launched: fragments carry no image or command. Create a profile that extends it: tpd init myprofile --extends %s", name, name)}
		}
	}
	cfg, err := profile.ResolveProfile(cat, opts.ProfileName)
	if err != nil {
		return Result{ExitCode: 2, Err: err}
	}

	if len(opts.ExtraTools) > 0 {
		if cfg.Tools == nil {
			cfg.Tools = map[string]profile.Tool{}
		}
		for _, t := range opts.ExtraTools {
			name, ver := parseToolFlag(t)
			cfg.Tools[name] = profile.Tool{Version: ver}
		}
	}

	hostHome, _ := os.UserHomeDir()
	if hostHome == "" {
		hostHome = "/root"
	}
	mode := workspace.ModeRootful
	rt := opts.Runtime

	if !opts.DryRun {
		if rt == nil {
			constructed, err := runtime.NewDockerRuntime()
			if err != nil {
				return Result{ExitCode: 3, Err: fmt.Errorf("runtime unavailable: %w (is Docker running?)", err)}
			}
			rt = constructed
		}

		if dr, ok := rt.(*runtime.DockerRuntime); ok {
			detected, err := dr.DetectMode(ctx)
			if err == nil {
				mode = detected
			}
		}
	}

	// The in-container user's home: the host home in Mode A (rootless keep-id
	// maps the host user in with their home), /root in Mode B (the container
	// runs as root and drops to the host user via setpriv). Podman keep-id
	// writes the passwd entry's home equal to the container WorkingDir, so the
	// WorkingDir must match this or tools using getpwuid (ssh, git) resolve a
	// different home than $HOME and the ~-expanded mount targets.
	runtimeHome := hostHome
	if mode == workspace.ModeRootful {
		runtimeHome = "/root"
	}

	if !opts.DryRun {
		spec, err := buildSpec(opts, cfg, mode, hostHome, runtimeHome)
		if err != nil {
			return Result{ExitCode: 2, Err: err}
		}

		if opts.Verbose {
			RenderSpec(w, spec)
		}

		progress := progress
		imageRef, err := rt.Prepare(ctx, spec, progress, opts.Pull)
		if err != nil {
			return Result{ExitCode: 3, Err: fmt.Errorf("prepare: %w", err)}
		}
		cleanupProxy, busAddr, err := startBusProxy(cfg)
		if err != nil {
			return Result{ExitCode: 3, Err: fmt.Errorf("dbus: %w", err)}
		}
		if cleanupProxy != nil {
			defer cleanupProxy()
		}
		spec.Env["DBUS_SESSION_BUS_ADDRESS"] = busAddr
		runSpec := spec
		if imageRef != "" {
			runSpec.Image = imageRef
		}

		// Register stop BEFORE StartServices so it covers the StartServices error path.
		if len(spec.Services) > 0 {
			defer func() {
				stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := rt.StopServices(stopCtx, spec); err != nil {
					fmt.Fprintf(os.Stderr, "tpd: warning: stop services: %v\n", err)
				}
			}()
		}

		var serviceBindings runtime.ServiceBindings
		// Initialize Release to a no-op so the missing-socket-key path can
		// call it unconditionally before StartServices succeeds.
		serviceBindings.Release = func() {}
		if len(spec.Services) > 0 {
			bindings, err := rt.StartServices(ctx, spec, progress, opts.Pull)
			if err != nil {
				return Result{ExitCode: 3, Err: fmt.Errorf("start services: %w", err)}
			}
			serviceBindings = bindings
			for i := range runSpec.Mounts {
				m := &runSpec.Mounts[i]
				if m.Service == "" {
					continue
				}
				key := m.Service + "/" + m.Socket
				hostPath, ok := bindings.Sockets[key]
				if !ok {
					serviceBindings.Release()
					return Result{ExitCode: 3, Err: fmt.Errorf("service socket %s not found in bindings", key)}
				}
				runSpec.SocketPaths = append(runSpec.SocketPaths, m.Target)
				m.Source = hostPath
				m.Service = ""
				m.Socket = ""
			}
		}

		created, err := rt.CreateContainer(ctx, runSpec)
		if err != nil {
			serviceBindings.Release()
			return Result{ExitCode: 3, Err: fmt.Errorf("create container: %w", err)}
		}

		// Release service locks now that the main container is created and
		// labeled with tpd.uses-service — a concurrent stop step can see it.
		serviceBindings.Release()

		code, err := rt.RunContainer(ctx, runSpec, created)
		if err != nil {
			return Result{ExitCode: 3, Err: fmt.Errorf("run container: %w", err)}
		}
		return Result{ExitCode: code}
	}

	spec, err := buildSpec(opts, cfg, mode, hostHome, runtimeHome)
	if err != nil {
		return Result{ExitCode: 2, Err: err}
	}
	if err := RenderSpec(w, spec); err != nil {
		return Result{ExitCode: 3, Err: err}
	}
	return Result{ExitCode: 0}
}

type stderrProgress struct{}

func (stderrProgress) WriteProgress(line string) {
	fmt.Fprintln(os.Stderr, line)
}

func parseToolFlag(s string) (string, string) {
	for i, c := range s {
		if c == '=' {
			return s[:i], s[i+1:]
		}
	}
	return s, "latest"
}
