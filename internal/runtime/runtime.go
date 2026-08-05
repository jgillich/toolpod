package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/jgillich/tpd/internal/mise"
	"github.com/jgillich/tpd/internal/workspace"
)

// CacheSubpath derives the stable per-target cache key: the first 8 hex chars
// of the sha256 of the target path. Hashing keeps the key order-independent
// and collision-safe as profiles add or remove cache paths.
func CacheSubpath(target string) string {
	sum := sha256.Sum256([]byte(target))
	return hex.EncodeToString(sum[:])[:8]
}

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
	Tools       map[string]mise.Tool
	Caches      []CacheSpec
	Network     string
	Labels      map[string]string
	Services    []ServiceSpec
	SocketPaths []string
	Workspace   WorkspaceSpec
	TTY         string
	RuntimeHome string
	Resources   ResourceSpec
}

type ResourceSpec struct {
	MemoryBytes int64
	NanoCPUs    int64
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
	Service  string
	Socket   string
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
	// Name is the shared volume for the whole cache entry. On engines that
	// honor volume subpaths each target mounts Name with VolumeOptions.Subpath
	// set to Subpath; otherwise a dedicated volume Name-<Subpath> backs each
	// target.
	Name   string
	Target string
	// Subpath is the stable per-target key (sha256 of the target path,
	// truncated). It keeps the subdirectory/fallback-volume stable and
	// order-independent as profiles add or remove paths.
	Subpath string
}

// ServiceSpec is a service container launched alongside the main container:
// it shares the profile's packages/repos/files/caches and publishes its
// service sockets back into the main container.
type ServiceSpec struct {
	Name       string
	Hash       string
	Image      string
	Packages   []string
	Repos      map[string]Repo
	Files      []FileSpec
	Command    []string
	Caches     []CacheSpec
	Mounts     []MountSpec
	Env        map[string]string
	Labels     map[string]string
	Exposes    map[string]string
	Privileged bool
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
	Prepare(ctx context.Context, spec Spec, w ProgressWriter, pull bool) (string, error)
	CreateContainer(ctx context.Context, spec Spec) (CreateResult, error)
	RunContainer(ctx context.Context, spec Spec, created CreateResult) (int, error)
	StartServices(ctx context.Context, spec Spec, w ProgressWriter, pull bool) (ServiceBindings, error)
	StopServices(ctx context.Context, spec Spec) error
}

// ServiceBindings carries the container-side socket paths for each running
// service (Sockets maps service name to container path) and Release tears the
// services down when the launch finishes.
type ServiceBindings struct {
	Sockets map[string]string
	Release func()
}

type CreateResult struct {
	ContainerID string
}
