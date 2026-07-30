package toolpod

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/jgillich/toolpod/internal/config"
)

// Launch orchestrates: resolve config → (Plan 2: Prepare + Run) → result.
// In Plan 1, this resolves the config and renders the Spec for --dry-run.
// Non-dry-run returns an error (runtime added in Plan 2).
func Launch(ctx context.Context, opts LaunchOpts) Result {
	return LaunchWithWriter(ctx, opts, os.Stdout)
}

// LaunchWithWriter is like Launch but takes an explicit writer for testability.
func LaunchWithWriter(ctx context.Context, opts LaunchOpts, w io.Writer) Result {
	userDir := opts.ConfigDir
	if userDir == "" {
		userDir = config.DefaultUserConfigDir()
	}
	cat, err := config.LoadCatalog(userDir)
	if err != nil {
		return Result{ExitCode: 2, Err: err}
	}
	cfg, err := config.Resolve(cat, opts.ProfileName)
	if err != nil {
		return Result{ExitCode: 2, Err: err}
	}

	// Merge --tool flags into cfg.Tools
	if len(opts.ExtraTools) > 0 {
		if cfg.Tools == nil {
			cfg.Tools = map[string]string{}
		}
		for _, t := range opts.ExtraTools {
			name, ver := parseToolFlag(t)
			cfg.Tools[name] = ver
		}
	}

	// Determine mode + homes. Plan 1 defaults to Mode B (no runtime detection yet).
	// Plan 2 replaces this with real rootless-Podman detection.
	mode := "B"
	hostHome := os.Getenv("HOME")
	if hostHome == "" {
		hostHome = "/root"
	}
	runtimeHome := "/root"
	if mode == "A" {
		runtimeHome = hostHome
	}

	spec := buildSpec(opts, cfg, mode, hostHome, runtimeHome)

	if opts.DryRun {
		if err := RenderSpec(w, spec); err != nil {
			return Result{ExitCode: 3, Err: err}
		}
		return Result{ExitCode: 0}
	}

	// Plan 2: invoke Runtime.Prepare + Runtime.Run here.
	return Result{ExitCode: 3, Err: fmt.Errorf("runtime not yet implemented (Plan 2)")}
}

func parseToolFlag(s string) (string, string) {
	for i, c := range s {
		if c == '=' {
			return s[:i], s[i+1:]
		}
	}
	return s, "latest"
}
