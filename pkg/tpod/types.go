package tpod

import "github.com/jgillich/tpod/internal/runtime"

type (
	Spec          = runtime.Spec
	MountSpec     = runtime.MountSpec
	PortSpec      = runtime.PortSpec
	DeviceSpec    = runtime.DeviceSpec
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
	DryRun      bool
	Verbose     bool
	Runtime     runtime.Runtime
	// Progress receives status lines during Prepare (image pull, mise
	// install). If nil, progress goes to stderr. Set to a no-op writer
	// to silence progress entirely.
	Progress runtime.ProgressWriter
	// PortAllocator reserves host ports for auto-allocated bindings. If
	// nil, an ephemeral socket bind is used. Injectable for deterministic
	// tests.
	PortAllocator PortAllocator
}

type Result struct {
	ExitCode int
	Err      error
}
