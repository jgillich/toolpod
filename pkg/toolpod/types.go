package toolpod

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
}

// Result is the outcome of a Launch.
type Result struct {
	ExitCode int
	Err      error
}

// Spec is the resolved container spec passed to the runtime.
// In Plan 1, --dry-run prints this; Plan 2's Runtime consumes it.
type Spec struct {
	ProfileName string
	Image       string
	Build       *BuildSpec
	Command     []string
	Mounts      []MountSpec
	Env         map[string]string
	Tools       map[string]string
	Caches      []CacheSpec
	Network     string
	Labels      map[string]string
	Workspace   WorkspaceSpec
	TTY         string
	RuntimeHome string
}

// BuildSpec is the build source for a profile using build:.
type BuildSpec struct {
	Dockerfile string
	Context    string
	DependsOn  []string
}

// MountSpec is a resolved mount (absolute paths, no tildes).
type MountSpec struct {
	Target   string
	Source   string
	ReadOnly bool
}

// CacheSpec is a resolved cache volume.
type CacheSpec struct {
	Name   string // toolpod-cache-<name>
	Target string
}

// WorkspaceSpec is the resolved workspace mount.
type WorkspaceSpec struct {
	HostPath string
	Target   string
	Mode     string // "A" (rootless podman) or "B" (fallback)
}
