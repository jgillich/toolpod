package toolpod

import "github.com/jgillich/toolpod/internal/runtime"

type (
	Spec          = runtime.Spec
	BuildSpec     = runtime.BuildSpec
	MountSpec     = runtime.MountSpec
	CacheSpec     = runtime.CacheSpec
	WorkspaceSpec = runtime.WorkspaceSpec
)

type LaunchOpts struct {
	ProfileName string
	Args        []string
	Command     string
	Workspace   string
	ProfileDir   string
	ExtraTools  []string
	Rebuild     bool
	DryRun      bool
	Verbose     bool
	Runtime     runtime.Runtime
}

type Result struct {
	ExitCode int
	Err      error
}
