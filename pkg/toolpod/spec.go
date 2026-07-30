package toolpod

import (
	"github.com/jgillich/toolpod/internal/config"
)

// buildSpec assembles a container Spec from a resolved config and launch opts.
// mode is "A" (rootless podman) or "B" (fallback). hostHome is the host user's
// $HOME; runtimeHome is the in-container user's home (/home/<user> in Mode A,
// /root in Mode B).
func buildSpec(opts LaunchOpts, cfg config.Config, mode, hostHome, runtimeHome string) Spec {
	cfg = config.ResolveTildes(cfg, mode, hostHome, runtimeHome)

	mounts := make([]MountSpec, 0, len(cfg.Mounts))
	for target, m := range cfg.Mounts {
		mounts = append(mounts, MountSpec{
			Target:   target,
			Source:   m.Source,
			ReadOnly: m.ReadOnly,
		})
	}

	caches := make([]CacheSpec, 0, len(cfg.Caches))
	for name, target := range cfg.Caches {
		caches = append(caches, CacheSpec{
			Name:   "toolpod-cache-" + name,
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

	// Workspace mount (CLI, not config) per spec §4.2
	wsTarget := opts.Workspace
	if mode == "B" {
		wsTarget = "/workspace"
	}

	// Command = config.Command + passthrough args (or args_if_none if no args)
	cmd := append([]string{}, cfg.Command...)
	if len(opts.Args) > 0 {
		cmd = append(cmd, opts.Args...)
	} else if len(cfg.ArgsIfNone) > 0 {
		cmd = append(cmd, cfg.ArgsIfNone...)
	}

	var buildCfg *BuildSpec
	if cfg.Build != nil {
		buildCfg = &BuildSpec{
			Dockerfile: cfg.Build.Dockerfile,
			Context:    cfg.Build.Context,
			DependsOn:  cfg.Build.DependsOn,
		}
	}

	return Spec{
		ProfileName: opts.ProfileName,
		Image:       cfg.Image,
		Build:       buildCfg,
		Command:     cmd,
		Mounts:      mounts,
		Env:         env,
		Tools:       tools,
		Caches:      caches,
		Network:     cfg.Network,
		Labels:      labels,
		Workspace:   WorkspaceSpec{HostPath: opts.Workspace, Target: wsTarget, Mode: mode},
		TTY:         cfg.TTY,
		RuntimeHome: runtimeHome,
	}
}
