package toolpod

import "github.com/jgillich/toolpod/internal/runtime"

// Spec types are defined in internal/runtime and re-exported here via type
// aliases so callers in pkg/toolpod and above can use them directly.
type (
	Spec          = runtime.Spec
	BuildSpec     = runtime.BuildSpec
	MountSpec     = runtime.MountSpec
	CacheSpec     = runtime.CacheSpec
	WorkspaceSpec = runtime.WorkspaceSpec
)

// ProgressWriter reports progress lines during Prepare/Run.
type ProgressWriter = runtime.ProgressWriter

// Runtime is the contract for container runtimes (podman, docker, etc.).
type Runtime = runtime.Runtime

// LaunchOpts holds all inputs to Launch.
type LaunchOpts struct {
	ProfileName string   // e.g. "opencode"
	Args        []string // passthrough args after the profile name
	Workspace   string   // workspace path (default $PWD)
	ConfigDir   string   // override user config dir (also TOOLPOD_CONFIG_DIR)
	ExtraTools  []string // from --tool name=version, merged with config tools
	Rebuild     bool     // --rebuild
	DryRun      bool     // --dry-run
	Verbose     bool     // --verbose / -v
	Runtime     Runtime  // container runtime; nil in dry-run
}

// Result is the outcome of a Launch.
type Result struct {
	ExitCode int
	Err      error
}
