package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/jgillich/tpod/internal/workspace"
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
