package tpd

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jgillich/tpd/internal/approval"
	"github.com/jgillich/tpd/internal/profile"
	"github.com/jgillich/tpd/internal/runtime"
	"github.com/jgillich/tpd/internal/ui"
	"github.com/jgillich/tpd/internal/workspace"
)

// PortAllocator reserves an unused host port for a published binding.
// protocol is "tcp", "udp", or "sctp"; hostIP is the requested bind address
// ("" = all interfaces). Returns the allocated port as a string.
//
// Allocation is necessarily best-effort: tpd binds an ephemeral socket to find
// a free port and closes it before the engine binds the real port at container
// start, so another process can claim the port in that window. The engine
// surfaces a collision only as a generic create/start failure, not a distinct
// tpd error, so no retry is attempted. This does not eliminate the race.
type PortAllocator func(protocol, hostIP string) (string, error)

// defaultPortAllocator finds a free host port by binding an ephemeral socket
// and immediately closing it; the bind-then-close window is the best-effort
// gap documented on PortAllocator.
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
	diag := opts.Stderr
	if diag == nil {
		diag = os.Stderr
	}
	progress := opts.Progress
	if progress == nil {
		progress = &stderrProgress{out: diag}
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
	resolved, err := profile.ResolveProfileWithProv(cat, opts.ProfileName)
	if err != nil {
		return Result{ExitCode: 2, Err: err}
	}

	// Approval gate.
	store := opts.ApprovalStore
	if store == nil {
		store = approval.NewFSStore(defaultApprovalDir())
	}
	// In dry-run the store is read-only: the initial Filter must read
	// stored approvals (so an already-approved profile doesn't prompt)
	// but its reconciliation write-back must never touch disk.
	gateStore := store
	if opts.DryRun {
		gateStore = approval.NewReadOnlyStore(store)
	}
	isTTY := opts.IsTTY
	if isTTY == nil {
		isTTY = ui.IsTTYReader
	}
	in := opts.In
	if in == nil {
		in = os.Stdin
	}

	// Approval gate. The Load → Filter → merge → Save transaction runs
	// under an advisory lock on the profile's state file so concurrent tpd
	// processes cannot lose each other's approvals. Dry-run never writes
	// (the ReadOnly/Ephemeral stores below) and a single read of the
	// rename-replaced state file is always a consistent snapshot, so it
	// skips locking.
	var cfg profile.Profile
	var promptReq approval.PromptRequest
	gate := func() error {
		var err error
		cfg, promptReq, err = approval.Filter(resolved, gateStore)
		if err != nil {
			return err
		}
		if len(promptReq.Items) > 0 {
			if opts.AssumeYes || opts.AssumeNo {
				choices := buildChoices(promptReq, opts.AssumeYes)
				prior, err := gateStore.Load(resolved.FullName)
				if err != nil {
					return err
				}
				merged := mergeChoicesIntoState(prior, promptReq, choices)
				effectiveStore := gateStore
				if opts.DryRun {
					effectiveStore = approval.NewEphemeralStore(gateStore, merged)
				} else {
					if err := store.Save(resolved.FullName, merged); err != nil {
						return err
					}
				}
				cfg, _, err = approval.Filter(resolved, effectiveStore)
				return err
			} else if opts.DryRun || !isTTY(in) {
				return fmt.Errorf("unapproved sensitive fields require --yes or --no: %s", summarizeItems(promptReq.Items))
			} else {
				prompt := opts.ApprovalPrompt
				if prompt == nil {
					prompt = approval.DefaultPrompt
				}
				choices, err := prompt(promptReq, in, w)
				if err != nil {
					return fmt.Errorf("approval: %w", err)
				}
				if incomplete(promptReq, choices) {
					return fmt.Errorf("approval incomplete: %s", summarizeUndecided(promptReq, choices))
				}
				prior, err := gateStore.Load(resolved.FullName)
				if err != nil {
					return err
				}
				merged := mergeChoicesIntoState(prior, promptReq, choices)
				if err := store.Save(resolved.FullName, merged); err != nil {
					return err
				}
				cfg, _, err = approval.Filter(resolved, store)
				return err
			}
		}
		return nil
	}
	if opts.DryRun {
		err = gate()
	} else {
		err = approval.WithLock(store, resolved.FullName, gate)
	}
	if err != nil {
		return Result{ExitCode: 2, Err: err}
	}

	if len(opts.ExtraTools) > 0 {
		if cfg.Tools == nil {
			cfg.Tools = map[string]profile.Tool{}
		}
		for _, t := range opts.ExtraTools {
			name, ver, err := parseToolFlag(t)
			if err != nil {
				return Result{ExitCode: 2, Err: err}
			}
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

		if md, ok := rt.(modeDetector); ok {
			detected, err := md.DetectMode(ctx)
			if err != nil {
				return Result{ExitCode: 3, Err: fmt.Errorf("detect engine mode: %w", err)}
			}
			mode = detected
		}
	} else {
		// A dry-run never queries a daemon, so no launch mode can be claimed.
		mode = workspace.ModeUnknown
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
			renderSpec(w, spec)
		}

		sp := newSpinnerProgress(diag, progress, ui.IsTTY(diag))
		sp.Start()
		defer sp.Stop()

		imageRef, err := rt.Prepare(ctx, spec, sp, opts.Pull)
		if err != nil {
			return Result{ExitCode: 3, Err: fmt.Errorf("prepare: %w", err)}
		}
		cleanupProxy, busAddr, err := startBusProxy(cfg, diag)
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
					fmt.Fprintf(diag, "tpd: warning: stop services: %v\n", err)
				}
			}()
		}

		var serviceBindings runtime.ServiceBindings
		// Initialize Release to a no-op so the missing-socket-key path can
		// call it unconditionally before StartServices succeeds.
		serviceBindings.Release = func() {}
		if len(spec.Services) > 0 {
			bindings, err := rt.StartServices(ctx, spec, sp, opts.Pull)
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

		sp.Stop()
		created, err := rt.CreateContainer(ctx, runSpec)
		if err != nil {
			serviceBindings.Release()
			return Result{ExitCode: 3, Err: fmt.Errorf("create container: %w", err)}
		}

		if serviceBindings.Network != "" {
			if err := rt.ConnectContainerToNetwork(ctx, created.ContainerID, serviceBindings.Network, nil); err != nil {
				if rerr := rt.RemoveContainer(ctx, created.ContainerID); rerr != nil {
					fmt.Fprintf(os.Stderr, "tpd: warning: remove container: %v\n", rerr)
				}
				serviceBindings.Release()
				return Result{ExitCode: 3, Err: fmt.Errorf("connect service network: %w", err)}
			}
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
	if err := renderSpec(w, spec); err != nil {
		return Result{ExitCode: 3, Err: err}
	}
	return Result{ExitCode: 0}
}

type stderrProgress struct {
	out io.Writer
}

func (p stderrProgress) WriteProgress(line string) {
	fmt.Fprintln(p.out, line)
}

func parseToolFlag(s string) (string, string, error) {
	for i, c := range s {
		if c == '=' {
			name, ver := s[:i], s[i+1:]
			if name == "" || ver == "" {
				return "", "", fmt.Errorf("malformed tool flag %q: want NAME or NAME=VERSION", s)
			}
			return name, ver, nil
		}
	}
	if s == "" {
		return "", "", fmt.Errorf("malformed tool flag %q: want NAME or NAME=VERSION", s)
	}
	return s, "latest", nil
}

// defaultApprovalDir returns the directory for stored approval decisions,
// always absolute: $XDG_DATA_HOME/tpd, else $HOME/.local/share/tpd, else a
// fixed fallback when no absolute home can be resolved.
func defaultApprovalDir() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" && filepath.IsAbs(v) {
		return filepath.Join(v, "tpd")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !filepath.IsAbs(home) {
		home = os.Getenv("HOME")
	}
	if home != "" && filepath.IsAbs(home) {
		return filepath.Join(home, ".local", "share", "tpd")
	}
	return "/tmp/tpd-approvals"
}

func buildChoices(req approval.PromptRequest, yes bool) map[string]map[string]bool {
	choices := map[string]map[string]bool{}
	for _, it := range req.Items {
		set, ok := choices[it.Field]
		if !ok {
			set = map[string]bool{}
			choices[it.Field] = set
		}
		set[it.Key] = yes
	}
	return choices
}

// mergeChoicesIntoState folds the dialog's per-field choices into the prior
// stored state, merging per-field: a decided field REPLACES its stored
// ApprovedField (the dialog returned the complete allowed-set for it);
// fields not present in choices keep their stored choices untouched. For
// map fields (mounts, devices, env, ports, dbus.talk, dbus.own, services),
// Keys is the approved set (denied = absent). For the scalar "network"
// field, the choice is stored in ApprovedField.Network (*bool): true → &true,
// false → &false. Hash is refreshed to the current request.
func mergeChoicesIntoState(prior approval.State, req approval.PromptRequest, choices map[string]map[string]bool) approval.State {
	st := prior
	st.Hash = req.Hash
	if st.Approved == nil {
		st.Approved = map[string]approval.ApprovedField{}
	}
	for field, set := range choices {
		if field == "network" {
			b := false
			for _, v := range set {
				if v {
					b = true
					break
				}
			}
			af := st.Approved[field]
			af.Network = &b
			st.Approved[field] = af
			continue
		}
		var keys []string
		for k, v := range set {
			if v {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		st.Approved[field] = approval.ApprovedField{Keys: keys}
	}
	return st
}

func incomplete(req approval.PromptRequest, choices map[string]map[string]bool) bool {
	for _, it := range req.Items {
		set, ok := choices[it.Field]
		if !ok {
			return true
		}
		if _, decided := set[it.Key]; !decided {
			return true
		}
	}
	return false
}

func summarizeItems(items []approval.SensitiveItem) string {
	var parts []string
	for _, it := range items {
		parts = append(parts, it.Field+"."+it.Key)
	}
	return strings.Join(parts, ", ")
}

func summarizeUndecided(req approval.PromptRequest, choices map[string]map[string]bool) string {
	var parts []string
	for _, it := range req.Items {
		set, ok := choices[it.Field]
		if !ok {
			parts = append(parts, it.Field+"."+it.Key)
			continue
		}
		if _, decided := set[it.Key]; !decided {
			parts = append(parts, it.Field+"."+it.Key)
		}
	}
	return strings.Join(parts, ", ")
}
