package tpd

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"

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

		code, err := rt.Run(ctx, runSpec)
		if err != nil {
			return Result{ExitCode: 3, Err: fmt.Errorf("run: %w", err)}
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
