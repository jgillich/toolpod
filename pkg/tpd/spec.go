package tpd

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jgillich/tpd/internal/mise"
	"github.com/jgillich/tpd/internal/profile"
	"github.com/jgillich/tpd/internal/runtime"
	"github.com/jgillich/tpd/internal/workspace"
)

// buildSpec assembles a container Spec from a resolved profile and launch opts.
// mode is ModeA (rootless podman) or ModeB (fallback). hostHome is the host
// user's $HOME; runtimeHome is the in-container user's home (/home/<user> in
// Mode A, /root in Mode B).
func buildSpec(opts LaunchOpts, cfg profile.Profile, mode workspace.Mode, hostHome, runtimeHome string) (Spec, error) {
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
	usedServices := map[string]bool{}
	for target, m := range cfg.Mounts {
		mounts = append(mounts, MountSpec{
			Target:   target,
			Source:   m.Source,
			Service:  m.Service,
			Socket:   m.Socket,
			ReadOnly: m.ReadOnly,
			Optional: m.Optional,
			Create:   m.Create,
		})
		if m.Service != "" {
			usedServices[m.Service] = true
		}
	}

	caches := make([]CacheSpec, 0)
	for name, paths := range cfg.Caches {
		for _, target := range paths {
			caches = append(caches, CacheSpec{
				Name:    "tpd-cache-" + name,
				Target:  target,
				Subpath: runtime.CacheSubpath(target),
			})
		}
	}

	services := make([]runtime.ServiceSpec, 0, len(cfg.Services))
	for name, svc := range cfg.Services {
		svcCaches := make([]runtime.CacheSpec, 0)
		for cacheName, paths := range svc.Caches {
			for _, target := range paths {
				svcCaches = append(svcCaches, runtime.CacheSpec{
					Name:    "tpd-cache-" + cacheName,
					Target:  target,
					Subpath: runtime.CacheSubpath(target),
				})
			}
		}
		svcMounts := make([]runtime.MountSpec, 0, len(svc.Mounts))
		for target, m := range svc.Mounts {
			svcMounts = append(svcMounts, runtime.MountSpec{
				Target:   target,
				Source:   m.Source,
				ReadOnly: m.ReadOnly,
				Optional: m.Optional,
				Create:   m.Create,
			})
		}
		svcRepos := make(map[string]Repo, len(svc.Repos))
		for rname, r := range svc.Repos {
			svcRepos[rname] = Repo{ExtRepo: r.ExtRepo, URL: r.URL, KeyURL: r.KeyURL, Suites: r.Suites, Components: r.Components}
		}
		svcFiles := make([]runtime.FileSpec, 0, len(svc.Files))
		for target, f := range svc.Files {
			mode := f.Mode
			if mode == 0 {
				mode = 0o644
			}
			svcFiles = append(svcFiles, runtime.FileSpec{Target: target, Content: f.Content, Mode: mode})
		}
		sort.Slice(svcFiles, func(i, j int) bool { return svcFiles[i].Target < svcFiles[j].Target })

		svcLabels := make(map[string]string, len(svc.Labels)+3)
		for k, v := range svc.Labels {
			svcLabels[k] = v
		}
		svcLabels[runtime.OwnershipLabel] = "true"
		svcLabels[runtime.ServiceLabel] = name
		svcLabels[runtime.ServiceHashLabel] = svc.Hash

		services = append(services, runtime.ServiceSpec{
			Name:       name,
			Hash:       svc.Hash,
			Image:      svc.Image,
			Packages:   svc.Packages,
			Repos:      svcRepos,
			Files:      svcFiles,
			Command:    svc.Command,
			Caches:     svcCaches,
			Mounts:     svcMounts,
			Env:        svc.Env,
			Labels:     svcLabels,
			Exposes:    svc.Exposes,
			Privileged: svc.Privileged,
		})
	}

	repos := make(map[string]Repo, len(cfg.Repos))
	for name, r := range cfg.Repos {
		repos[name] = Repo{ExtRepo: r.ExtRepo, URL: r.URL, KeyURL: r.KeyURL, Suites: r.Suites, Components: r.Components}
	}

	files := make([]FileSpec, 0, len(cfg.Files))
	for target, f := range cfg.Files {
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		files = append(files, FileSpec{Target: target, Content: f.Content, Mode: mode})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Target < files[j].Target })

	tools := make(map[string]mise.Tool, len(cfg.Tools))
	for name, t := range cfg.Tools {
		tools[name] = mise.Tool{Version: t.Version, SHA256: t.SHA256, SHA256ByArch: t.SHA256ByArch}
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
	labels[runtime.OwnershipLabel] = "true"
	if len(usedServices) > 0 {
		names := make([]string, 0, len(usedServices))
		for name := range usedServices {
			names = append(names, name)
		}
		sort.Strings(names)
		labels[runtime.UsesServiceLabel] = strings.Join(names, ",")
	}

	// Workspace mount (CLI, not profile) per spec §4.2
	wsTarget := opts.Workspace
	if mode == workspace.ModeRootful {
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

	// Validation guarantees the strings parse; parse errors here are ignored
	// rather than propagated back to a profile that already passed validate().
	resources := runtime.ResourceSpec{}
	if cfg.Resources != nil {
		if cfg.Resources.Memory != "" {
			resources.MemoryBytes, _ = profile.ParseMemoryBytes(cfg.Resources.Memory)
		}
		if cfg.Resources.CPUs != "" {
			resources.NanoCPUs, _ = profile.ParseNanoCPUs(cfg.Resources.CPUs)
		}
	}

	return Spec{
		ProfileName: opts.ProfileName,
		Image:       cfg.Image,
		Packages:    cfg.Packages,
		Repos:       repos,
		Files:       files,
		Command:     cmd,
		Mounts:      mounts,
		PortSpecs:   portSpecs,
		DeviceSpecs: deviceSpecs,
		Env:         env,
		Tools:       tools,
		Caches:      caches,
		Services:    services,
		Network:     cfg.Network,
		Labels:      labels,
		Workspace:   WorkspaceSpec{HostPath: opts.Workspace, Target: wsTarget, Mode: mode},
		TTY:         cfg.TTY,
		RuntimeHome: runtimeHome,
		Resources:   resources,
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
