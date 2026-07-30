package toolpod

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/jgillich/toolpod/internal/config"
	"github.com/jgillich/toolpod/internal/runtime"
)

func Launch(ctx context.Context, opts LaunchOpts) Result {
	return LaunchWithWriter(ctx, opts, os.Stdout)
}

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

	if len(opts.ExtraTools) > 0 {
		if cfg.Tools == nil {
			cfg.Tools = map[string]string{}
		}
		for _, t := range opts.ExtraTools {
			name, ver := parseToolFlag(t)
			cfg.Tools[name] = ver
		}
	}

	hostHome, _ := os.UserHomeDir()
	runtimeHome := "/root"
	mode := "B"

	if !opts.DryRun {
		rt := opts.Runtime
		if rt == nil {
			constructed, err := runtime.NewDockerRuntime()
			if err != nil {
				return Result{ExitCode: 3, Err: fmt.Errorf("runtime unavailable: %w (is Docker running?)", err)}
			}
			constructed.Rebuild = opts.Rebuild
			rt = constructed
		}

		if dr, ok := rt.(*runtime.DockerRuntime); ok {
			detected, err := dr.DetectMode(ctx)
			if err == nil {
				mode = detected
			}
			if mode == "A" {
				runtimeHome = hostHome
			}
		}

		spec := buildSpec(opts, cfg, mode, hostHome, runtimeHome)

		if opts.Verbose {
			RenderSpec(w, spec)
		}

		progress := &stdoutProgress{w: w}
		imageRef, err := rt.Prepare(ctx, spec, progress)
		if err != nil {
			return Result{ExitCode: 3, Err: fmt.Errorf("prepare: %w", err)}
		}
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

	spec := buildSpec(opts, cfg, mode, hostHome, runtimeHome)
	if err := RenderSpec(w, spec); err != nil {
		return Result{ExitCode: 3, Err: err}
	}
	return Result{ExitCode: 0}
}

type stdoutProgress struct {
	w io.Writer
}

func (s *stdoutProgress) WriteProgress(line string) {
	fmt.Fprintln(s.w, line)
}

func parseToolFlag(s string) (string, string) {
	for i, c := range s {
		if c == '=' {
			return s[:i], s[i+1:]
		}
	}
	return s, "latest"
}
