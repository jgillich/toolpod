package runtime

import (
	"context"

	"github.com/jgillich/tpod/internal/workspace"
)

type Spec struct {
	ProfileName string
	Image       string
	Packages    []string
	Repos       map[string]Repo
	Files       []FileSpec
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

// Repo mirrors profile.Repo: a single extra apt source, either an extrepo
// catalog name (ExtRepo) or a fully inline custom repo (URL/KeyURL/...).
// Fields are duplicated (not the profile type) so the runtime package stays
// independent of the profile package.
type Repo struct {
	ExtRepo    string
	URL        string
	KeyURL     string
	Suites     string
	Components string
}

type FileSpec struct {
	Target  string
	Content string
	Mode    uint32
}

type MountSpec struct {
	Target   string
	Source   string
	ReadOnly bool
	Optional bool
	Create   bool
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
	Mode     workspace.Mode
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
