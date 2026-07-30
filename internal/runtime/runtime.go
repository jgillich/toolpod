package runtime

import "context"

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

type BuildSpec struct {
	Dockerfile string
	Context    string
	DependsOn  []string
}

type MountSpec struct {
	Target   string
	Source   string
	ReadOnly bool
}

type CacheSpec struct {
	Name   string
	Target string
}

type WorkspaceSpec struct {
	HostPath string
	Target   string
	Mode     string
}

type ProgressWriter interface {
	WriteProgress(line string)
}

type NoopProgressWriter struct{}

func (NoopProgressWriter) WriteProgress(string) {}

type Runtime interface {
	Prepare(ctx context.Context, spec Spec, w ProgressWriter) (string, error)
	Run(ctx context.Context, spec Spec) (int, error)
}

// ContainerRunner runs a command in a throwaway container (auto-removed)
// with named volumes mounted. Implemented by DockerRuntime; accepted by
// mise.EnsureTools to avoid an import cycle between runtime and mise.
type ContainerRunner interface {
	RunInContainer(ctx context.Context, image string, volumes []VolumeMount, env []string, cmd []string) (int, error)
}

// VolumeMount is a named volume to mount in a ContainerRunner execution.
type VolumeMount struct {
	Name   string
	Target string
}
