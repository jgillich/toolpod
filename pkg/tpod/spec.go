package tpod

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/jgillich/tpod/internal/profile"
)

// buildSpec assembles a container Spec from a resolved profile and launch opts.
// mode is "A" (rootless podman) or "B" (fallback). hostHome is the host user's
// $HOME; runtimeHome is the in-container user's home (/home/<user> in Mode A,
// /root in Mode B).
func buildSpec(opts LaunchOpts, cfg profile.Profile, mode, hostHome, runtimeHome string) (Spec, error) {
	alloc := opts.PortAllocator
	if alloc == nil {
		alloc = defaultPortAllocator
	}
	portSpecs, portValues, err := buildPortSpecs(cfg.Ports, alloc)
	if err != nil {
		return Spec{}, fmt.Errorf("allocate ports: %w", err)
	}
	deviceSpecs := buildDeviceSpecs(cfg.Devices)

	cfg, err = profile.ResolveTildes(cfg, mode, hostHome, runtimeHome, portValues)
	if err != nil {
		return Spec{}, fmt.Errorf("resolve paths: %w", err)
	}

	mounts := make([]MountSpec, 0, len(cfg.Mounts))
	for target, m := range cfg.Mounts {
		mounts = append(mounts, MountSpec{
			Target:   target,
			Source:   m.Source,
			ReadOnly: m.ReadOnly,
			Optional: m.Optional,
			Create:   m.Create,
		})
	}

	caches := make([]CacheSpec, 0, len(cfg.Caches))
	for name, target := range cfg.Caches {
		caches = append(caches, CacheSpec{
			Name:   "tpod-cache-" + name,
			Target: target,
		})
	}

	tools := cfg.Tools
	if tools == nil {
		tools = map[string]string{}
	}

	env := cfg.Env
	if env == nil {
		env = map[string]string{}
	}

	labels := cfg.Labels
	if labels == nil {
		labels = map[string]string{}
	}

	// Always set the profile label to the actual profile being launched,
	// overriding any value inherited from a parent profile (e.g. a user
	// profile extending "opencode" should show its own name, not "opencode").
	labels["profile"] = opts.ProfileName

	// Workspace mount (CLI, not profile) per spec §4.2
	wsTarget := opts.Workspace
	if mode == "B" {
		wsTarget = "/workspace"
	}

	// Command = binary + passthrough args; user args replace the profile's
	// default args (command[1:]), which only apply when no args are given.
	cmd := append([]string{}, cfg.Command...)
	if opts.Command != "" {
		if isShellCommand(cmd) {
			cmd = append(cmd, "-c", opts.Command)
		} else {
			cmd = []string{"sh", "-c", opts.Command}
		}
	} else if len(opts.Args) > 0 {
		cmd = []string{}
		if len(cfg.Command) > 0 {
			cmd = append(cmd, cfg.Command[0])
		}
		cmd = append(cmd, opts.Args...)
	}

	return Spec{
		ProfileName: opts.ProfileName,
		Image:       cfg.Image,
		Packages:    cfg.Packages,
		Command:     cmd,
		Mounts:      mounts,
		PortSpecs:   portSpecs,
		DeviceSpecs: deviceSpecs,
		Env:         env,
		Tools:       tools,
		Caches:      caches,
		Network:     cfg.Network,
		Labels:      labels,
		Workspace:   WorkspaceSpec{HostPath: opts.Workspace, Target: wsTarget, Mode: mode},
		TTY:         cfg.TTY,
		RuntimeHome: runtimeHome,
	}, nil
}

func isShellCommand(cmd []string) bool {
	if len(cmd) == 0 {
		return false
	}
	base := filepath.Base(cmd[0])
	return base == "sh" || base == "bash" || base == "zsh" || base == "fish"
}

func buildPortSpecs(ports map[string]profile.PortBind, alloc PortAllocator) ([]PortSpec, map[string]string, error) {
	specs := make([]PortSpec, 0, len(ports))
	values := make(map[string]string, len(ports))
	for container, bind := range ports {
		proto := bind.Protocol
		if proto == "" {
			proto = "tcp"
		}
		hostPort := bind.Host
		if hostPort == "" || hostPort == "0" {
			allocated, err := alloc(proto, bind.HostIP)
			if err != nil {
				return nil, nil, fmt.Errorf("port %s: %w", container, err)
			}
			hostPort = allocated
		}
		values[container] = hostPort
		specs = append(specs, PortSpec{
			HostIP:    bind.HostIP,
			HostPort:  hostPort,
			Container: container,
			Protocol:  proto,
		})
	}
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].Container != specs[j].Container {
			return specs[i].Container < specs[j].Container
		}
		if specs[i].Protocol != specs[j].Protocol {
			return specs[i].Protocol < specs[j].Protocol
		}
		return specs[i].HostPort < specs[j].HostPort
	})
	return specs, values, nil
}

func buildDeviceSpecs(devices map[string]profile.DeviceBind) []DeviceSpec {
	specs := make([]DeviceSpec, 0, len(devices))
	for container, bind := range devices {
		source := bind.Source
		if source == "" {
			source = container
		}
		perms := bind.Permissions
		if perms == "" {
			perms = "rwm"
		}
		specs = append(specs, DeviceSpec{
			Container: container,
			Host:      source,
			Perms:     perms,
			Cgroup:    bind.Cgroup,
		})
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Container < specs[j].Container })
	return specs
}
