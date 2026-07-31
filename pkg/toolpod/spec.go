package toolpod

import (
	"fmt"
	"path/filepath"

	"github.com/jgillich/toolpod/internal/profile"
)

// buildSpec assembles a container Spec from a resolved profile and launch opts.
// mode is "A" (rootless podman) or "B" (fallback). hostHome is the host user's
// $HOME; runtimeHome is the in-container user's home (/home/<user> in Mode A,
// /root in Mode B).
func buildSpec(opts LaunchOpts, cfg profile.Profile, mode, hostHome, runtimeHome string) (Spec, error) {
	cfg, err := profile.ResolveTildes(cfg, mode, hostHome, runtimeHome)
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

	// Always set the profile label to the actual profile being launched,
	// overriding any value inherited from a parent profile (e.g. a user
	// profile extending "opencode" should show its own name, not "opencode").
	labels["profile"] = opts.ProfileName

	// Workspace mount (CLI, not profile) per spec §4.2
	wsTarget := opts.Workspace
	if mode == "B" {
		wsTarget = "/workspace"
	}

	// Command = profile.Command + passthrough args (or args_if_none if no args)
	cmd := append([]string{}, cfg.Command...)
	if opts.Command != "" {
		if isShellCommand(cmd) {
			cmd = append(cmd, "-c", opts.Command)
		} else {
			cmd = []string{"sh", "-c", opts.Command}
		}
	} else if len(opts.Args) > 0 {
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
	}, nil
}

func isShellCommand(cmd []string) bool {
	if len(cmd) == 0 {
		return false
	}
	base := filepath.Base(cmd[0])
	return base == "sh" || base == "bash" || base == "zsh" || base == "fish"
}
