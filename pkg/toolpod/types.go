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
	ProfileDir  string
	ExtraTools  []string
	Rebuild     bool
	DryRun      bool
	Verbose     bool
	Runtime     runtime.Runtime
	// Progress receives status lines during Prepare (image pull, mise
	// install). If nil, progress goes to stderr. Set to a no-op writer
	// to silence progress entirely.
	Progress runtime.ProgressWriter
}

type Result struct {
	ExitCode int
	Err      error
}
