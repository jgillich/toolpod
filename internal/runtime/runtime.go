package runtime

import "context"

type Spec struct {
	ProfileName string
	Image       string
	Build       *BuildSpec
	Command     []string
	Mounts      []MountSpec
	PortSpecs   []PortSpec
	DeviceSpecs []DeviceSpec
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
	Optional bool
}

type PortSpec struct {
	HostIP    string
	HostPort  string
	Container string
	Protocol  string
}

type DeviceSpec struct {
	Container string
	Host      string
	Perms     string
	Cgroup    bool
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
