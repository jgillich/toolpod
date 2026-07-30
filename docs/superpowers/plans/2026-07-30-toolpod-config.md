# toolpod Config System + CLI Skeleton — Implementation Plan 1 of 3

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the config system (load, merge, validate, extends, catalog) and a thin CLI skeleton so that `toolpod config show/list/check` and `toolpod --dry-run` work end-to-end, with the three built-in profiles embedded.

**Architecture:** Go module `github.com/jgillich/toolpod`. Config is plain YAML, loaded from embedded built-ins (`go:embed`) and a user config dir, deep-merged with `extends:` + null-to-delete semantics, then validated. The CLI (`cmd/toolpod`) wires `os.Args` to `pkg/toolpod` library calls. No Docker/runtime code in this plan — `--dry-run` prints the resolved container *spec* (image, mounts, env, command) without launching anything. Plans 2 and 3 add the runtime, launch, doctor, and prune.

**Tech Stack:** Go 1.22+, `gopkg.in/yaml.v3` (preserves line numbers for errors), `github.com/spf13/pflag` (POSIX-style flag parsing with passthrough), `go:embed` for built-in configs.

## Global Constraints

- **Go version:** 1.22+ (uses `go:embed`, generics where helpful).
- **YAML library:** `gopkg.in/yaml.v3` — required because it preserves node positions for file:line error reporting (§10 of spec). Do NOT use `yaml.v2` or `sigs.k8s.io/yaml`.
- **Flag library:** `github.com/spf13/pflag` — required because it supports interspersed flags + passthrough (flags before profile name, everything after is passthrough args). Stdlib `flag` does not support this.
- **Module path:** `github.com/jgillich/toolpod`.
- **Exit codes (spec §10):** 0 = success, 2 = config error (parse/merge/validation), 3 = runtime error (reserved for Plan 2), N = profile exit code (propagated, reserved for Plan 2).
- **Config schema version:** `version: 1` (current). The `version` field is required on every config.
- **Reserved profile names (spec §4.4 #6):** `config`, `doctor`, `help`, `version`, `completion`, `prune` — rejected at load time.
- **No comments in code** unless the code itself doesn't make something apparent.
- **TDD:** Every task writes the failing test first, runs it to confirm failure, implements minimal code, runs to confirm pass, commits.

---

## File Structure

```
toolpod/
  go.mod
  go.sum
  cmd/toolpod/main.go              # CLI entrypoint: arg parsing, dispatch, exit codes
  internal/
    config/
      types.go        # Config, Mount, Build, Resources structs + YAML tags
      catalog.go      # Load embedded built-ins + user dir → name→raw-config map
      catalog_test.go
      merge.go        # Deep-merge with extends, null-to-delete, image/build slot
      merge_test.go
      validate.go     # Validate resolved config: required fields, reserved names, image/build exclusivity
      validate_test.go
      paths.go        # Tilde resolution: ~/source → host $HOME, ~/target → runtime home (Mode A/B)
      paths_test.go
      errors.go       # ConfigError type with file:line, ExitCode() helpers
    catalog/
      embed.go        # go:embed declarations for configs/*.yaml
      configs/
        opencode.yaml
        codex.yaml
        shell.yaml
    ui/
      output.go       # TTY detection, color helpers, ProgressWriter interface (stub for Plan 2)
  pkg/toolpod/
    launch.go        # Launch(opts) → Result (stub: resolves config + prints spec for --dry-run; Plan 2 adds runtime)
    types.go         # LaunchOpts, Result, Spec (container spec passed to runtime)
```

**Responsibilities:**
- `internal/config` owns all config logic: types, loading, merging, validation, path resolution. No I/O except reading the user config dir.
- `internal/catalog` owns the embedded built-in profiles. Pure data + `go:embed`.
- `internal/ui` owns output rendering. `ProgressWriter` is defined here (used by runtime in Plan 2).
- `pkg/toolpod` is the public library. `Launch` orchestrates; in this plan it resolves config and (for `--dry-run`) prints the spec. Plan 2 plugs in the `Runtime`.
- `cmd/toolpod` is the thin CLI. Parses args, calls `pkg/toolpod`, maps errors to exit codes.

---

## Task 1: Go module + directory scaffold

**Files:**
- Create: `go.mod`
- Create: `cmd/toolpod/main.go`
- Create: `internal/config/types.go`
- Create: `pkg/toolpod/types.go`
- Create: `pkg/toolpod/launch.go`

**Interfaces:**
- Produces: module `github.com/jgillich/toolpod`; `pkg/toolpod.LaunchOpts`, `pkg/toolpod.Result`, `pkg/toolpod.Spec` types; `pkg/toolpod.Launch` function (stub).

- [ ] **Step 1: Initialize the Go module**

Run:
```bash
go mod init github.com/jgillich/toolpod
go get gopkg.in/yaml.v3
go get github.com/spf13/pflag
```

- [ ] **Step 2: Write the config types**

Create `internal/config/types.go`:

```go
package config

// Config is a resolved toolpod profile config (after extends-merge and validation).
// YAML tags match the schema in the design doc §4.1.
type Config struct {
	Version    int               `yaml:"version"`
	Extends    string            `yaml:"extends,omitempty"`
	Image      string            `yaml:"image,omitempty"`
	Build      *Build            `yaml:"build,omitempty"`
	Command    []string          `yaml:"command"`
	ArgsIfNone []string          `yaml:"args_if_none,omitempty"`
	Mounts     map[string]Mount  `yaml:"mounts,omitempty"`
	Env        map[string]string `yaml:"environment,omitempty"`
	Caches     map[string]string `yaml:"caches,omitempty"`
	Labels     map[string]string `yaml:"labels,omitempty"`
	Network    string            `yaml:"network,omitempty"`
	Resources  *Resources        `yaml:"resources,omitempty"`
	TTY        string            `yaml:"tty,omitempty"`
	Tools      map[string]string `yaml:"tools,omitempty"`
}

// Build is the escape-hatch image source: a Dockerfile + optional depends_on.
type Build struct {
	Dockerfile string   `yaml:"dockerfile"`
	Context    string   `yaml:"context,omitempty"`
	DependsOn  []string `yaml:"depends_on,omitempty"`
}

// Mount is a single bind mount, keyed by container target path.
type Mount struct {
	Source   string `yaml:"source"`
	ReadOnly bool   `yaml:"read_only"`
}

// Resources are optional resource hints (best-effort; runtime may ignore).
type Resources struct {
	Memory string `yaml:"memory,omitempty"`
	CPUs   string `yaml:"cpus,omitempty"`
}

// RawConfig is a config as loaded from disk, before extends-merge.
// It carries the source file path for error reporting.
type RawConfig struct {
	Config
	Path string `yaml:"-"` // file path for error reporting
}
```

- [ ] **Step 3: Write the public library types**

Create `pkg/toolpod/types.go`:

```go
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
	HostPath   string
	Target     string
	Mode       string // "A" (rootless podman) or "B" (fallback)
}
```

Create `pkg/toolpod/launch.go`:

```go
package toolpod

import "context"

// Launch orchestrates: resolve config → (Plan 2: Prepare + Run) → result.
// In Plan 1, this resolves the config and returns a Spec for --dry-run;
// the Runtime integration is added in Plan 2.
func Launch(ctx context.Context, opts LaunchOpts) Result {
	// Stub: Plan 1 implements config resolution + dry-run spec rendering.
	_ = opts
	return Result{ExitCode: 0}
}
```

- [ ] **Step 4: Write a minimal main.go that compiles**

Create `cmd/toolpod/main.go`:

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jgillich/toolpod/pkg/toolpod"
)

func main() {
	result := toolpod.Launch(context.Background(), toolpod.LaunchOpts{
		ProfileName: os.Args[1],
	})
	if result.Err != nil {
		fmt.Fprintln(os.Stderr, result.Err)
	}
	os.Exit(result.ExitCode)
}
```

- [ ] **Step 5: Verify it compiles**

Run: `go build ./...`
Expected: compiles with no errors.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum cmd/ internal/ pkg/
git commit -m "feat: scaffold Go module, config types, and CLI entrypoint"
```

---

## Task 2: ConfigError type with file:line

**Files:**
- Create: `internal/config/errors.go`
- Create: `internal/config/errors_test.go`

**Interfaces:**
- Produces: `config.ConfigError` (carries file path + line + message), `config.ExitCoder` interface. Used by validate, merge, and catalog tasks.

- [ ] **Step 1: Write the failing test**

Create `internal/config/errors_test.go`:

```go
package config

import "testing"

func TestConfigErrorFormat(t *testing.T) {
	err := ConfigError{Path: "/home/me/.config/toolpod/opencode.yaml", Line: 5, Message: "extends: cycle detected"}
	want := "/home/me/.config/toolpod/opencode.yaml:5: extends: cycle detected"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestConfigErrorNoLine(t *testing.T) {
	err := ConfigError{Path: "/home/me/.config/toolpod/opencode.yaml", Message: "missing required field: command"}
	want := "/home/me/.config/toolpod/opencode.yaml: missing required field: command"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestConfigErrorExitCode(t *testing.T) {
	err := ConfigError{Message: "boom"}
	if err.ExitCode() != 2 {
		t.Errorf("ExitCode() = %d, want 2", err.ExitCode())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestConfigError -v`
Expected: FAIL — `ConfigError` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/config/errors.go`:

```go
package config

import "strconv"

// ConfigError is a config-layer error (parse, merge, validation)
// carrying the source file path and line for reporting (spec §10: exit code 2).
type ConfigError struct {
	Path    string
	Line    int
	Message string
}

func (e ConfigError) Error() string {
	if e.Line > 0 {
		return e.Path + ":" + strconv.Itoa(e.Line) + ": " + e.Message
	}
	return e.Path + ": " + e.Message
}

// ExitCode returns the exit code for this error type (spec §10: config errors = 2).
func (e ConfigError) ExitCode() int { return 2 }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestConfigError -v`
Expected: PASS (all 3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/config/errors.go internal/config/errors_test.go
git commit -m "feat: ConfigError type with file:line reporting and exit code 2"
```

---

## Task 3: Catalog — load embedded built-in profiles

**Files:**
- Create: `internal/catalog/embed.go`
- Create: `internal/catalog/configs/shell.yaml`
- Create: `internal/catalog/configs/opencode.yaml`
- Create: `internal/catalog/configs/codex.yaml`
- Create: `internal/config/catalog.go`
- Create: `internal/config/catalog_test.go`

**Interfaces:**
- Produces: `config.Catalog` (name→`RawConfig` map), `config.LoadCatalog(userDir string) (Catalog, error)`. Built-ins loaded via `go:embed`; user configs loaded + shadow built-ins.

- [ ] **Step 1: Write the built-in profile YAML files**

Create `internal/catalog/configs/shell.yaml`:

```yaml
version: 1
image: ghcr.io/jdx/mise:latest
command: ["sh"]
caches:
  npm: ~/.npm
  cargo: ~/.cargo
  pip: ~/.cache/pip
  go: ~/go
```

Create `internal/catalog/configs/opencode.yaml`:

```yaml
version: 1
image: ghcr.io/jdx/mise:latest
command: ["opencode"]
tools:
  opencode: latest
mounts:
  ~/.config/opencode:
    source: ~/.config/opencode
    read_only: true
  ~/.cache/opencode:
    source: ~/.cache/opencode
    read_only: false
  ~/.gitconfig:
    source: ~/.gitconfig
    read_only: true
caches:
  npm: ~/.npm
  cargo: ~/.cargo
  pip: ~/.cache/pip
  go: ~/go
labels:
  profile: opencode
network: bridge
tty: auto
```

Create `internal/catalog/configs/codex.yaml`:

```yaml
version: 1
image: ghcr.io/jdx/mise:latest
command: ["codex"]
tools:
  codex: latest
mounts:
  ~/.config/codex:
    source: ~/.config/codex
    read_only: true
  ~/.gitconfig:
    source: ~/.gitconfig
    read_only: true
caches:
  npm: ~/.npm
  cargo: ~/.cargo
  pip: ~/.cache/pip
  go: ~/go
labels:
  profile: codex
network: bridge
tty: auto
```

- [ ] **Step 2: Write the embed declaration**

Create `internal/catalog/embed.go`:

```go
package catalog

import "embed"

//go:embed configs/*.yaml
var Configs embed.FS
```

- [ ] **Step 3: Write the failing test**

Create `internal/config/catalog_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCatalogBuiltinsOnly(t *testing.T) {
	cat, err := LoadCatalog("")
	if err != nil {
		t.Fatalf("LoadCatalog(\"\"): %v", err)
	}
	for _, name := range []string{"opencode", "codex", "shell"} {
		if _, ok := cat.Get(name); !ok {
			t.Errorf("built-in %q missing from catalog", name)
		}
	}
}

func TestLoadCatalogUserShadowsBuiltin(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "shell.yaml"), []byte("version: 1\nimage: my/custom:latest\ncommand: [\"bash\"]\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := LoadCatalog(dir)
	if err != nil {
		t.Fatalf("LoadCatalog(%q): %v", dir, err)
	}
	rc, ok := cat.Get("shell")
	if !ok {
		t.Fatal("user shadow for shell not found")
	}
	if rc.Image != "my/custom:latest" {
		t.Errorf("shadow image = %q, want my/custom:latest", rc.Image)
	}
	if rc.Path == "" {
		t.Error("shadow RawConfig has empty Path (should point to user file)")
	}
}

func TestLoadCatalogUserAddsProfile(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "rustdev.yaml"), []byte("version: 1\nextends: shell\ntools:\n  rust: \"1.74\"\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := LoadCatalog(dir)
	if err != nil {
		t.Fatalf("LoadCatalog(%q): %v", dir, err)
	}
	if _, ok := cat.Get("rustdev"); !ok {
		t.Error("user profile rustdev not in catalog")
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestLoadCatalog -v`
Expected: FAIL — `LoadCatalog` undefined.

- [ ] **Step 5: Write minimal implementation**

Create `internal/config/catalog.go`:

```go
package config

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jgillich/toolpod/internal/catalog"
	"gopkg.in/yaml.v3"
)

// Catalog is the merged set of built-in + user raw configs, keyed by profile name.
type Catalog struct {
	entries map[string]RawConfig
}

// Get returns the raw config for a profile name, plus whether it was found.
func (c Catalog) Get(name string) (RawConfig, bool) {
	rc, ok := c.entries[name]
	return rc, ok
}

// Names returns all profile names in the catalog, sorted.
func (c Catalog) Names() []string {
	names := make([]string, 0, len(c.entries))
	for n := range c.entries {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// LoadCatalog loads embedded built-ins, then user configs from userDir (if non-empty),
// with user entries shadowing built-ins of the same name.
func LoadCatalog(userDir string) (Catalog, error) {
	entries := map[string]RawConfig{}

	if err := loadBuiltins(entries); err != nil {
		return Catalog{}, err
	}

	if userDir != "" {
		if err := loadUserDir(userDir, entries); err != nil {
			return Catalog{}, err
		}
	}

	return Catalog{entries: entries}, nil
}

func loadBuiltins(entries map[string]RawConfig) error {
	root := "configs"
	return fs.WalkDir(catalog.Configs, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		data, err := catalog.Configs.ReadFile(path)
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		rc, err := parseRaw(data, "built-in:"+path)
		if err != nil {
			return err
		}
		entries[name] = rc
		return nil
	})
}

func loadUserDir(dir string, entries map[string]RawConfig) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		rc, err := parseRaw(data, path)
		if err != nil {
			return err
		}
		entries[name] = rc // shadow
		return nil
	})
}

// parseRaw parses YAML bytes into a RawConfig with the given source path.
func parseRaw(data []byte, path string) (RawConfig, error) {
	var rc RawConfig
	rc.Path = path
	if err := yaml.Unmarshal(data, &rc.Config); err != nil {
		return RawConfig{}, ConfigError{
			Path:    path,
			Message: fmt.Sprintf("YAML parse error: %v", err),
		}
	}
	return rc, nil
}

// userConfigDir returns the default user config dir for the current OS.
// Used by the CLI when --config-dir is not set.
func DefaultUserConfigDir() string {
	if dir := os.Getenv("TOOLPOD_CONFIG_DIR"); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "toolpod")
	}
	return ""
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestLoadCatalog -v`
Expected: PASS (all 3 tests).

- [ ] **Step 7: Commit**

```bash
git add internal/catalog/ internal/config/catalog.go internal/config/catalog_test.go
git commit -m "feat: catalog loading from embedded built-ins + user config dir"
```

---

## Task 4: Merge — extends + deep-merge + null-to-delete

**Files:**
- Create: `internal/config/merge.go`
- Create: `internal/config/merge_test.go`

**Interfaces:**
- Consumes: `config.Catalog`, `config.RawConfig` (from Task 3).
- Produces: `config.Resolve(cat Catalog, name string) (Config, error)` — fully merged + validated config.

- [ ] **Step 1: Write the failing test for scalar + map merge**

Create `internal/config/merge_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func mustWriteConfig(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveScalarOverride(t *testing.T) {
	dir := t.TempDir()
	mustWriteConfig(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\nnetwork: bridge\n")
	mustWriteConfig(t, dir, "child.yaml", "version: 1\nextends: base\nnetwork: host\n")
	cat, err := LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Resolve(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Network != "host" {
		t.Errorf("Network = %q, want host", cfg.Network)
	}
	if cfg.Image != "base:1" {
		t.Errorf("Image = %q, want base:1 (inherited)", cfg.Image)
	}
}

func TestResolveMapMergeAndNullDelete(t *testing.T) {
	dir := t.TempDir()
	mustWriteConfig(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\ntools:\n  node: \"20\"\n  rust: \"1.74\"\n")
	mustWriteConfig(t, dir, "child.yaml", "version: 1\nextends: base\ntools:\n  node: \"22\"\n  rust: null\n")
	cat, err := LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Resolve(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Tools["node"] != "22" {
		t.Errorf("node = %q, want 22 (overridden)", cfg.Tools["node"])
	}
	if _, exists := cfg.Tools["rust"]; exists {
		t.Error("rust should be deleted by null-to-delete rule")
	}
}

func TestResolveListReplaced(t *testing.T) {
	dir := t.TempDir()
	mustWriteConfig(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"a\"]\nargs_if_none: [\"--x\"]\n")
	mustWriteConfig(t, dir, "child.yaml", "version: 1\nextends: base\nargs_if_none: [\"--y\"]\n")
	cat, err := LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Resolve(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.ArgsIfNone) != 1 || cfg.ArgsIfNone[0] != "--y" {
		t.Errorf("args_if_none = %v, want [--y] (replaced not concatenated)", cfg.ArgsIfNone)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestResolve -v`
Expected: FAIL — `Resolve` undefined.

- [ ] **Step 3: Write minimal implementation (scalar + map + list)**

Create `internal/config/merge.go`:

```go
package config

// Resolve walks the extends chain for name and produces a fully merged Config.
// Cycles are detected and rejected. Validation runs on the result.
func Resolve(cat Catalog, name string) (Config, error) {
	rc, ok := cat.Get(name)
	if !ok {
		return Config{}, ConfigError{Message: "profile not found: " + name}
	}
	merged, err := resolveChain(cat, name, map[string]bool{})
	if err != nil {
		return Config{}, err
	}
	merged.Path = rc.Path
	if err := validate(merged); err != nil {
		return Config{}, err
	}
	return merged.Config, nil
}

func resolveChain(cat Catalog, name string, seen map[string]bool) (RawConfig, error) {
	if seen[name] {
		return RawConfig{}, ConfigError{Message: "extends cycle detected at: " + name}
	}
	rc, ok := cat.Get(name)
	if !ok {
		return RawConfig{}, ConfigError{Message: "extends references unknown profile: " + name}
	}
	if rc.Extends == "" {
		return rc, nil
	}
	seen[name] = true
	defer delete(seen, name)

	parent, err := resolveChain(cat, rc.Extends, seen)
	if err != nil {
		return RawConfig{}, err
	}
	return mergeConfigs(parent, rc), nil
}

// mergeConfigs merges child on top of parent per spec §4.3:
// scalars replace, maps merge key-by-key with null-to-delete, lists replace,
// image/build treated as a single slot.
func mergeConfigs(parent, child RawConfig) RawConfig {
	out := parent

	// Scalars
	if child.Version != 0 {
		out.Version = child.Version
	}
	if child.Extends != "" {
		out.Extends = child.Extends
	}
	if child.Network != "" {
		out.Network = child.Network
	}
	if child.TTY != "" {
		out.TTY = child.TTY
	}

	// image/build single slot (replace semantics)
	if child.Image != "" || child.Build != nil {
		out.Image = child.Image
		out.Build = child.Build
	}

	// Lists (replaced)
	if child.Command != nil {
		out.Command = child.Command
	}
	if child.ArgsIfNone != nil {
		out.ArgsIfNone = child.ArgsIfNone
	}

	// Maps (merged, null-to-delete handled in mergeMap)
	out.Mounts = mergeMounts(parent.Mounts, child.Mounts)
	out.Env = mergeStringMap(parent.Env, child.Env)
	out.Tools = mergeStringMap(parent.Tools, child.Tools)
	out.Caches = mergeStringMap(parent.Caches, child.Caches)
	out.Labels = mergeStringMap(parent.Labels, child.Labels)

	if child.Resources != nil {
		out.Resources = child.Resources
	}

	// Clear extends on the merged result (resolved config doesn't carry it).
	out.Extends = ""

	return out
}

// mergeMounts merges mount maps. Null value deletes the key.
// NOTE: yaml.v3 unmarshals a null YAML value into a zero-value Mount{},
// so we must track null-deletes via raw yaml.Node parsing (Task 5 handles
// this precisely). For now this merges non-null entries; null-delete is
// tested and implemented in Task 5.
func mergeMounts(parent, child map[string]Mount) map[string]Mount {
	out := make(map[string]Mount, len(parent)+len(child))
	for k, v := range parent {
		out[k] = v
	}
	for k, v := range child {
		out[k] = v
	}
	return out
}

func mergeStringMap(parent, child map[string]string) map[string]string {
	out := make(map[string]string, len(parent)+len(child))
	for k, v := range parent {
		out[k] = v
	}
	for k, v := range child {
		out[k] = v
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it fails on null-delete**

Run: `go test ./internal/config/ -run TestResolve -v`
Expected: `TestResolveMapMergeAndNullDelete` FAILS — `yaml.v3` unmarshals `rust: null` into a zero-value string `""`, not a delete. The null-to-delete rule requires raw YAML node parsing. (The other two tests should pass.)

- [ ] **Step 5: Implement null-to-delete via yaml.Node**

The issue: `yaml.Unmarshal` into a struct loses the distinction between "key absent" and "key present with null value". We need to parse into `yaml.Node` to detect explicit nulls. Replace `parseRaw` and the merge logic to track null-deletes.

Edit `internal/config/catalog.go` — replace `parseRaw`:

```go
// parseRaw parses YAML bytes into a RawConfig with the given source path.
// It also captures explicit-null keys (for null-to-delete in merge) via
// a parallel yaml.Node parse of the map fields.
func parseRaw(data []byte, path string) (RawConfig, error) {
	var rc RawConfig
	rc.Path = path
	if err := yaml.Unmarshal(data, &rc.Config); err != nil {
		return RawConfig{}, ConfigError{
			Path:    path,
			Message: fmt.Sprintf("YAML parse error: %v", err),
		}
	}
	// Parse the top-level map to capture null-delete keys.
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return RawConfig{}, ConfigError{Path: path, Message: fmt.Sprintf("YAML parse error: %v", err)}
	}
	rc.NullKeys = collectNullKeys(&root)
	return rc, nil
}

// collectNullKeys returns the set of map keys whose value is explicitly null
// in the top-level or nested-map fields that support null-to-delete.
// Returns a map of field-name → null-key info. A nil map value means the
// entire field is null (delete the whole field). A non-nil map value lists
// specific keys to delete within that field's nested map.
func collectNullKeys(root *yaml.Node) map[string]map[string]bool {
	nulls := map[string]map[string]bool{
		"mounts":     {},
		"environment": {},
		"tools":      {},
		"caches":     {},
		"labels":     {},
	}
	if root == nil || root.Kind != yaml.DocumentNode {
		return nulls
	}
	body := root.Content[0]
	if body == nil || body.Kind != yaml.MappingNode {
		return nulls
	}
	for i := 0; i+1 < len(body.Content); i += 2 {
		keyNode := body.Content[i]
		valNode := body.Content[i+1]
		if _, tracked := nulls[keyNode.Value]; !tracked {
			continue
		}
		// Case 1: entire field is null (e.g. "tools: null") → delete the whole field.
		// Use a sentinel: set the map to a special key that means "delete all".
		if valNode.Tag == "!!null" {
			nulls[keyNode.Value] = map[string]bool{"*": true} // "*" = delete all
			continue
		}
		// Case 2: field is a map with some null-valued keys (e.g. "tools: { rust: null }").
		if valNode.Kind == yaml.MappingNode {
			keys := map[string]bool{}
			for j := 0; j+1 < len(valNode.Content); j += 2 {
				if valNode.Content[j+1].Tag == "!!null" {
					keys[valNode.Content[j].Value] = true
				}
			}
			if len(keys) > 0 {
				nulls[keyNode.Value] = keys
			}
		}
	}
	return nulls
}
```

Update `internal/config/types.go` — add `NullKeys` to `RawConfig`:

```go
// RawConfig is a config as loaded from disk, before extends-merge.
// It carries the source file path for error reporting.
type RawConfig struct {
	Config
	Path     string                          `yaml:"-"` // file path for error reporting
	NullKeys map[string]map[string]bool       `yaml:"-"` // field → set of keys that are explicitly null (delete-on-inherit)
}
```

Update `internal/config/merge.go` — replace `mergeMounts` and `mergeStringMap` with null-aware versions:

```go
func mergeConfigs(parent, child RawConfig) RawConfig {
	out := parent

	// Scalars
	if child.Version != 0 {
		out.Version = child.Version
	}
	if child.Extends != "" {
		out.Extends = child.Extends
	}
	if child.Network != "" {
		out.Network = child.Network
	}
	if child.TTY != "" {
		out.TTY = child.TTY
	}

	// image/build single slot
	if child.Image != "" || child.Build != nil {
		out.Image = child.Image
		out.Build = child.Build
	}

	// Lists
	if child.Command != nil {
		out.Command = child.Command
	}
	if child.ArgsIfNone != nil {
		out.ArgsIfNone = child.ArgsIfNone
	}

	// Maps with null-to-delete
	out.Mounts = mergeMounts(parent.Mounts, child.Mounts, child.NullKeys["mounts"])
	out.Env = mergeStringMap(parent.Env, child.Env, child.NullKeys["environment"])
	out.Tools = mergeStringMap(parent.Tools, child.Tools, child.NullKeys["tools"])
	out.Caches = mergeStringMap(parent.Caches, child.Caches, child.NullKeys["caches"])
	out.Labels = mergeStringMap(parent.Labels, child.Labels, child.NullKeys["labels"])

	if child.Resources != nil {
		out.Resources = child.Resources
	}

	out.Extends = ""
	out.NullKeys = nil
	return out
}

func mergeMounts(parent, child map[string]Mount, nullKeys map[string]bool) map[string]Mount {
	// "*" sentinel means the entire field was null → delete everything
	if nullKeys != nil && nullKeys["*"] {
		return map[string]Mount{}
	}
	out := make(map[string]Mount, len(parent)+len(child))
	for k, v := range parent {
		out[k] = v
	}
	for k, v := range child {
		out[k] = v
	}
	for k := range nullKeys {
		delete(out, k)
	}
	return out
}

func mergeStringMap(parent, child map[string]string, nullKeys map[string]bool) map[string]string {
	if nullKeys != nil && nullKeys["*"] {
		return map[string]string{}
	}
	out := make(map[string]string, len(parent)+len(child))
	for k, v := range parent {
		out[k] = v
	}
	for k, v := range child {
		out[k] = v
	}
	for k := range nullKeys {
		delete(out, k)
	}
	return out
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestResolve -v`
Expected: PASS (all 3 tests).

- [ ] **Step 7: Add cycle detection test**

Append to `internal/config/merge_test.go`:

```go
func TestResolveCycle(t *testing.T) {
	dir := t.TempDir()
	mustWriteConfig(t, dir, "a.yaml", "version: 1\nimage: x\ncommand: [\"x\"]\nextends: b\n")
	mustWriteConfig(t, dir, "b.yaml", "version: 1\nimage: y\ncommand: [\"y\"]\nextends: a\n")
	cat, err := LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Resolve(cat, "a")
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	ce, ok := err.(ConfigError)
	if !ok {
		t.Fatalf("expected ConfigError, got %T", err)
	}
	if ce.Message == "" || !strings.Contains(ce.Message, "cycle") {
		t.Errorf("error message %q should mention cycle", ce.Message)
	}
}
```

Add the import `"strings"` to the test file if not present.

- [ ] **Step 8: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestResolveCycle -v`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/config/merge.go internal/config/merge_test.go internal/config/catalog.go internal/config/types.go
git commit -m "feat: extends deep-merge with null-to-delete and cycle detection"
```

---

## Task 5: Validation — required fields, reserved names, image/build exclusivity

**Files:**
- Create: `internal/config/validate.go`
- Create: `internal/config/validate_test.go`
- Modify: `internal/config/merge.go` (already calls `validate` from Task 4)

**Interfaces:**
- Produces: `config.validate(RawConfig) error` — checks: version present, command present, image/build exclusivity, reserved name check.

- [ ] **Step 1: Write the failing test**

Create `internal/config/validate_test.go`:

```go
package config

import "testing"

func TestValidateMissingVersion(t *testing.T) {
	rc := RawConfig{Config: Config{Image: "x", Command: []string{"sh"}}}
	err := validate(rc)
	if err == nil {
		t.Fatal("expected error for missing version")
	}
	if _, ok := err.(ConfigError); !ok {
		t.Fatalf("expected ConfigError, got %T", err)
	}
}

func TestValidateMissingCommand(t *testing.T) {
	rc := RawConfig{Config: Config{Version: 1, Image: "x"}}
	err := validate(rc)
	if err == nil {
		t.Fatal("expected error for missing command")
	}
}

func TestValidateBothImageAndBuild(t *testing.T) {
	rc := RawConfig{Config: Config{Version: 1, Image: "x", Build: &Build{Dockerfile: "Dockerfile"}, Command: []string{"sh"}}}
	err := validate(rc)
	if err == nil {
		t.Fatal("expected error for both image and build")
	}
}

func TestValidateNeitherImageNorBuild(t *testing.T) {
	rc := RawConfig{Config: Config{Version: 1, Command: []string{"sh"}}}
	err := validate(rc)
	if err == nil {
		t.Fatal("expected error for neither image nor build")
	}
}

func TestValidateReservedName(t *testing.T) {
	for _, name := range []string{"config", "doctor", "help", "version", "completion", "prune"} {
		rc := RawConfig{Config: Config{Version: 1, Image: "x", Command: []string{"sh"}}}
		rc.Path = "/home/me/.config/toolpod/" + name + ".yaml"
		err := validateReservedName(rc, name)
		if err == nil {
			t.Errorf("expected reserved-name error for %q", name)
		}
	}
}

func TestValidateValid(t *testing.T) {
	rc := RawConfig{Config: Config{Version: 1, Image: "x", Command: []string{"sh"}}}
	if err := validate(rc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestValidate -v`
Expected: FAIL — `validate` and `validateReservedName` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/config/validate.go`:

```go
package config

import "strings"

var reservedNames = map[string]bool{
	"config":      true,
	"doctor":      true,
	"help":        true,
	"version":     true,
	"completion":  true,
	"prune":       true,
}

// validate checks a resolved config for required fields and invariants.
// It runs on the merged result (after extends resolution).
func validate(rc RawConfig) error {
	if rc.Version == 0 {
		return ConfigError{Path: rc.Path, Message: "missing required field: version"}
	}
	if len(rc.Command) == 0 {
		return ConfigError{Path: rc.Path, Message: "missing required field: command"}
	}
	hasImage := rc.Image != ""
	hasBuild := rc.Build != nil
	if hasImage && hasBuild {
		return ConfigError{Path: rc.Path, Message: "exactly one of image or build is required (both set)"}
	}
	if !hasImage && !hasBuild {
		return ConfigError{Path: rc.Path, Message: "exactly one of image or build is required (neither set)"}
	}
	return nil
}

// validateReservedName rejects profile names that collide with subcommands.
// Called during catalog load, not on the merged config.
func validateReservedName(rc RawConfig, name string) error {
	if reservedNames[name] {
		return ConfigError{Path: rc.Path, Message: "profile name " + name + " is reserved (collides with a subcommand)"}
	}
	return nil
}

// ProfileNameFromPath extracts the profile name from a config file path.
func ProfileNameFromPath(path string) string {
	base := path
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	if idx := strings.LastIndex(base, "."); idx >= 0 {
		base = base[:idx]
	}
	return base
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestValidate -v`
Expected: PASS (all 6 tests).

- [ ] **Step 5: Wire reserved-name check into catalog load**

Edit `internal/config/catalog.go` — in `loadBuiltins` and `loadUserDir`, after `entries[name] = rc`, add:

In `loadBuiltins`, replace `entries[name] = rc` with:
```go
		if err := validateReservedName(rc, name); err != nil {
			return err
		}
		entries[name] = rc
```

In `loadUserDir`, replace `entries[name] = rc` with:
```go
		if err := validateReservedName(rc, name); err != nil {
			return err
		}
		entries[name] = rc
```

- [ ] **Step 6: Add reserved-name load test**

Append to `internal/config/catalog_test.go`:

```go
func TestLoadCatalogRejectsReservedName(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "doctor.yaml"), []byte("version: 1\nimage: x\ncommand: [\"sh\"]\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LoadCatalog(dir)
	if err == nil {
		t.Fatal("expected reserved-name rejection, got nil")
	}
}
```

- [ ] **Step 7: Run all config tests**

Run: `go test ./internal/config/ -v`
Expected: all PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/config/validate.go internal/config/validate_test.go internal/config/catalog.go internal/config/catalog_test.go
git commit -m "feat: config validation (required fields, image/build exclusivity, reserved names)"
```

---

## Task 6: Tilde resolution for mount/cache targets and sources

**Files:**
- Create: `internal/config/paths.go`
- Create: `internal/config/paths_test.go`

**Interfaces:**
- Produces: `config.ResolveTildes(cfg Config, mode string, hostHome string, runtimeHome string) Config` — expands `~/` on mount sources (→ hostHome) and targets (→ runtimeHome). Used by the CLI/Launch to build the Spec for --dry-run and (Plan 2) the Runtime.

- [ ] **Step 1: Write the failing test**

Create `internal/config/paths_test.go`:

```go
package config

import "testing"

func TestResolveTildesMountSourceAndTarget(t *testing.T) {
	cfg := Config{
		Mounts: map[string]Mount{
			"~/.config/opencode": {Source: "~/.config/opencode", ReadOnly: true},
			"/etc/hosts":          {Source: "/etc/hosts", ReadOnly: true},
		},
		Caches: map[string]string{
			"npm": "~/.npm",
		},
	}
	out := ResolveTildes(cfg, "A", "/home/me", "/home/me")
	m := out.Mounts["/home/me/.config/opencode"]
	if m.Source != "/home/me/.config/opencode" {
		t.Errorf("target-expanded mount source = %q, want /home/me/.config/opencode", m.Source)
	}
	if _, exists := out.Mounts["~/.config/opencode"]; exists {
		t.Error("tilde target key should be replaced with absolute path")
	}
	if out.Caches["npm"] != "/home/me/.npm" {
		t.Errorf("cache target = %q, want /home/me/.npm", out.Caches["npm"])
	}
	if _, exists := out.Mounts["/etc/hosts"]; !exists {
		t.Error("absolute-path mount should be left as-is")
	}
}

func TestResolveTildesModeB(t *testing.T) {
	cfg := Config{
		Mounts: map[string]Mount{
			"~/.config/opencode": {Source: "~/.config/opencode", ReadOnly: true},
		},
	}
	out := ResolveTildes(cfg, "B", "/home/me", "/root")
	if _, exists := out.Mounts["/root/.config/opencode"]; !exists {
		t.Error("target should expand to /root/.config/opencode in Mode B")
	}
	m := out.Mounts["/root/.config/opencode"]
	if m.Source != "/home/me/.config/opencode" {
		t.Errorf("source should expand to host home /home/me/.config/opencode, got %q", m.Source)
	}
}

func TestResolveTildesNoHomeSubstitution(t *testing.T) {
	cfg := Config{
		Mounts: map[string]Mount{
			"/data": {Source: "/data", ReadOnly: false},
		},
	}
	out := ResolveTildes(cfg, "B", "/home/me", "/root")
	if _, exists := out.Mounts["/data"]; !exists {
		t.Error("absolute /data should be unchanged")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestResolveTildes -v`
Expected: FAIL — `ResolveTildes` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/config/paths.go`:

```go
package config

import "strings"

// ResolveTildes expands leading ~/ on mount sources (→ hostHome) and
// mount/cache targets (→ runtimeHome) per spec §5.6. Absolute paths are
// left as-is. The mode ("A" or "B") is informational only here; the caller
// has already determined runtimeHome based on the mode.
func ResolveTildes(cfg Config, mode, hostHome, runtimeHome string) Config {
	out := cfg

	if out.Mounts != nil {
		expanded := make(map[string]Mount, len(out.Mounts))
		for target, m := range out.Mounts {
			newTarget := expandTarget(target, runtimeHome)
			m.Source = expandSource(m.Source, hostHome)
			expanded[newTarget] = m
		}
		out.Mounts = expanded
	}

	if out.Caches != nil {
		expanded := make(map[string]string, len(out.Caches))
		for name, target := range out.Caches {
			expanded[name] = expandTarget(target, runtimeHome)
		}
		out.Caches = expanded
	}

	return out
}

func expandTarget(path, runtimeHome string) string {
	if path == "~" {
		return runtimeHome
	}
	if strings.HasPrefix(path, "~/") {
		return runtimeHome + path[1:]
	}
	return path
}

func expandSource(path, hostHome string) string {
	if path == "~" {
		return hostHome
	}
	if strings.HasPrefix(path, "~/") {
		return hostHome + path[1:]
	}
	return path
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestResolveTildes -v`
Expected: PASS (all 3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/config/paths.go internal/config/paths_test.go
git commit -m "feat: tilde resolution for mount/cache targets and sources"
```

---

## Task 7: Spec builder — resolved Config → toolpod.Spec

**Files:**
- Modify: `pkg/toolpod/launch.go`
- Create: `pkg/toolpod/spec.go`
- Create: `pkg/toolpod/spec_test.go`

**Interfaces:**
- Consumes: `config.Config`, `config.ResolveTildes`, `config.Catalog`, `config.Resolve`, `config.DefaultUserConfigDir`.
- Produces: `toolpod.buildSpec(opts LaunchOpts, cfg config.Config) Spec` — assembles the container spec (image, mounts, env, tools, caches, command + passthrough args, workspace). Used by `--dry-run` rendering (Task 8) and the Runtime (Plan 2).

- [ ] **Step 1: Write the failing test**

Create `pkg/toolpod/spec_test.go`:

```go
package toolpod

import (
	"testing"

	"github.com/jgillich/toolpod/internal/config"
)

func TestBuildSpecBasic(t *testing.T) {
	cfg := config.Config{
		Version: 1,
		Image:   "myimage:latest",
		Command: []string{"opencode"},
		Tools:   map[string]string{"opencode": "latest", "node": "20"},
		Mounts: map[string]config.Mount{
			"~/.config/opencode": {Source: "~/.config/opencode", ReadOnly: true},
		},
		Caches:  map[string]string{"npm": "~/.npm"},
		Network: "bridge",
	}
	opts := LaunchOpts{Args: []string{"--model", "foo"}, Workspace: "/home/me/proj"}
	spec := buildSpec(opts, cfg, "A", "/home/me", "/home/me")

	if spec.Image != "myimage:latest" {
		t.Errorf("Image = %q", spec.Image)
	}
	wantCmd := []string{"opencode", "--model", "foo"}
	if len(spec.Command) != len(wantCmd) {
		t.Fatalf("Command = %v, want %v", spec.Command, wantCmd)
	}
	for i, c := range spec.Command {
		if c != wantCmd[i] {
			t.Errorf("Command[%d] = %q, want %q", i, c, wantCmd[i])
		}
	}
	if spec.Workspace.Target != "/home/me/proj" {
		t.Errorf("workspace target in Mode A = %q, want /home/me/proj", spec.Workspace.Target)
	}
	if spec.Workspace.Mode != "A" {
		t.Errorf("workspace mode = %q, want A", spec.Workspace.Mode)
	}
	if spec.Tools["opencode"] != "latest" {
		t.Errorf("tools[opencode] = %q", spec.Tools["opencode"])
	}
	if len(spec.Caches) != 1 || spec.Caches[0].Name != "toolpod-cache-npm" {
		t.Errorf("Caches = %+v, want one entry toolpod-cache-npm", spec.Caches)
	}
	mount, ok := spec.Mounts[0], true
	_ = ok
	if mount.Target != "/home/me/.config/opencode" {
		t.Errorf("mount[0].Target = %q, want /home/me/.config/opencode", mount.Target)
	}
}

func TestBuildSpecModeBWorkspace(t *testing.T) {
	cfg := config.Config{Version: 1, Image: "x", Command: []string{"sh"}}
	opts := LaunchOpts{Workspace: "/home/me/proj"}
	spec := buildSpec(opts, cfg, "B", "/home/me", "/root")
	if spec.Workspace.Target != "/workspace" {
		t.Errorf("Mode B workspace target = %q, want /workspace", spec.Workspace.Target)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/toolpod/ -run TestBuildSpec -v`
Expected: FAIL — `buildSpec` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `pkg/toolpod/spec.go`:

```go
package toolpod

import (
	"github.com/jgillich/toolpod/internal/config"
)

// buildSpec assembles a container Spec from a resolved config and launch opts.
// mode is "A" (rootless podman) or "B" (fallback). hostHome is the host user's
// $HOME; runtimeHome is the in-container user's home (/home/<user> in Mode A,
// /root in Mode B).
func buildSpec(opts LaunchOpts, cfg config.Config, mode, hostHome, runtimeHome string) Spec {
	cfg = config.ResolveTildes(cfg, mode, hostHome, runtimeHome)

	mounts := make([]MountSpec, 0, len(cfg.Mounts))
	for target, m := range cfg.Mounts {
		mounts = append(mounts, MountSpec{
			Target:   target,
			Source:   m.Source,
			ReadOnly: m.ReadOnly,
		})
	}

	caches := make([]CacheSpec, 0, len(cfg.Caches))
	for name, target := range cfg.Caches {
		caches = append(caches, CacheSpec{
			Name:   "toolpod-cache-" + name,
			Target: target,
		})
	}

	tools := cfg.Tools
	if tools == nil {
		tools = map[string]string{}
	}

	env := cfg.Env
	if env == nil {
		env = map[string]string{}
	}

	labels := cfg.Labels
	if labels == nil {
		labels = map[string]string{}
	}

	// Workspace mount (CLI, not config) per spec §4.2
	wsTarget := opts.Workspace
	if mode == "B" {
		wsTarget = "/workspace"
	}

	// Command = config.Command + passthrough args (or args_if_none if no args)
	cmd := append([]string{}, cfg.Command...)
	if len(opts.Args) > 0 {
		cmd = append(cmd, opts.Args...)
	} else if len(cfg.ArgsIfNone) > 0 {
		cmd = append(cmd, cfg.ArgsIfNone...)
	}

	var buildSpec *BuildSpec
	if cfg.Build != nil {
		buildSpec = &BuildSpec{
			Dockerfile: cfg.Build.Dockerfile,
			Context:    cfg.Build.Context,
			DependsOn:  cfg.Build.DependsOn,
		}
	}

	return Spec{
		ProfileName: opts.ProfileName,
		Image:       cfg.Image,
		Build:        buildSpec,
		Command:      cmd,
		Mounts:       mounts,
		Env:          env,
		Tools:        tools,
		Caches:       caches,
		Network:      cfg.Network,
		Labels:       labels,
		Workspace:    WorkspaceSpec{HostPath: opts.Workspace, Target: wsTarget, Mode: mode},
		TTY:          cfg.TTY,
		RuntimeHome:  runtimeHome,
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/toolpod/ -run TestBuildSpec -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add pkg/toolpod/spec.go pkg/toolpod/spec_test.go
git commit -m "feat: buildSpec assembles container Spec from resolved config"
```

---

## Task 8: Launch — config resolution + dry-run spec rendering

**Files:**
- Modify: `pkg/toolpod/launch.go`
- Create: `pkg/toolpod/launch_test.go`
- Create: `pkg/toolpod/dryrun.go`

**Interfaces:**
- Consumes: `config.LoadCatalog`, `config.Resolve`, `buildSpec`.
- Produces: `Launch(ctx, opts) Result` — resolves config, and for `--dry-run` prints the Spec as YAML. For non-dry-run in Plan 1, returns an error "runtime not yet implemented" (Plan 2 wires the real Runtime). Also `RenderSpec(Spec) string` for dry-run output.

- [ ] **Step 1: Write the failing test**

Create `pkg/toolpod/launch_test.go`:

```go
package toolpod

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBuiltinShell(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Use a user config dir with a valid profile so LoadCatalog succeeds.
	err := os.WriteFile(filepath.Join(dir, "shell.yaml"), []byte("version: 1\nimage: myimg:latest\ncommand: [\"sh\"]\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLaunchDryRunPrintsSpec(t *testing.T) {
	dir := writeBuiltinShell(t)
	var out strings.Builder
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		DryRun:      true,
		ConfigDir:   dir,
		Workspace:   "/home/me/proj",
	}, &out)
	if res.Err != nil {
		t.Fatalf("Launch: %v", res.Err)
	}
	output := out.String()
	if !strings.Contains(output, "image: myimg:latest") {
		t.Errorf("dry-run output missing image; got:\n%s", output)
	}
	if !strings.Contains(output, "command:") {
		t.Errorf("dry-run output missing command; got:\n%s", output)
	}
	if !strings.Contains(output, "workspace:") {
		t.Errorf("dry-run output missing workspace; got:\n%s", output)
	}
}

func TestLaunchProfileNotFound(t *testing.T) {
	dir := t.TempDir()
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "nope",
		DryRun:      true,
		ConfigDir:   dir,
	}, &strings.Builder{})
	if res.Err == nil {
		t.Fatal("expected error for missing profile")
	}
	if res.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2 (config error)", res.ExitCode)
	}
}

func TestLaunchNoRuntimeReturnsError(t *testing.T) {
	dir := writeBuiltinShell(t)
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		DryRun:      false,
		ConfigDir:   dir,
	}, &strings.Builder{})
	// Plan 1 has no runtime; non-dry-run should error.
	if res.Err == nil {
		t.Fatal("expected error (runtime not implemented in Plan 1)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/toolpod/ -v`
Expected: FAIL — `LaunchWithWriter` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `pkg/toolpod/dryrun.go`:

```go
package toolpod

import (
	"fmt"
	"io"
)

// RenderSpec writes the resolved container spec as YAML to w, for --dry-run.
func RenderSpec(w io.Writer, spec Spec) error {
	_, err := fmt.Fprintf(w, "profile: %s\n", spec.ProfileName)
	if err != nil {
		return err
	}
	if spec.Image != "" {
		_, err = fmt.Fprintf(w, "image: %s\n", spec.Image)
	} else if spec.Build != nil {
		_, err = fmt.Fprintf(w, "build:\n  dockerfile: %s\n", spec.Build.Dockerfile)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "command: %v\n", spec.Command)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "workspace:\n  host: %s\n  target: %s\n  mode: %s\n", spec.Workspace.HostPath, spec.Workspace.Target, spec.Workspace.Mode)
	if err != nil {
		return err
	}
	if len(spec.Mounts) > 0 {
		_, err = fmt.Fprintln(w, "mounts:")
		for _, m := range spec.Mounts {
			ro := "ro"
			if !m.ReadOnly {
				ro = "rw"
			}
			_, err = fmt.Fprintf(w, "  %s <- %s (%s)\n", m.Target, m.Source, ro)
		}
	}
	if len(spec.Tools) > 0 {
		_, err = fmt.Fprintln(w, "tools:")
		for name, ver := range spec.Tools {
			_, err = fmt.Fprintf(w, "  %s: %s\n", name, ver)
		}
	}
	if len(spec.Caches) > 0 {
		_, err = fmt.Fprintln(w, "caches:")
		for _, c := range spec.Caches {
			_, err = fmt.Fprintf(w, "  %s -> %s\n", c.Name, c.Target)
		}
	}
	if len(spec.Env) > 0 {
		_, err = fmt.Fprintln(w, "environment:")
		for k, v := range spec.Env {
			_, err = fmt.Fprintf(w, "  %s: %q\n", k, v)
		}
	}
	if spec.Network != "" {
		_, err = fmt.Fprintf(w, "network: %s\n", spec.Network)
	}
	return err
}
```

Update `pkg/toolpod/launch.go`:

```go
package toolpod

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/jgillich/toolpod/internal/config"
)

// Launch orchestrates: resolve config → (Plan 2: Prepare + Run) → result.
// In Plan 1: resolves config and renders the Spec for --dry-run.
// Non-dry-run returns an error (runtime added in Plan 2).
func Launch(ctx context.Context, opts LaunchOpts) Result {
	return LaunchWithWriter(ctx, opts, os.Stdout)
}

// LaunchWithWriter is like Launch but takes an explicit writer for testability.
func LaunchWithWriter(ctx context.Context, opts LaunchOpts, w io.Writer) Result {
	userDir := opts.ConfigDir
	if userDir == "" {
		userDir = config.DefaultUserConfigDir()
	}
	cat, err := config.LoadCatalog(userDir)
	if err != nil {
		return Result{ExitCode: 2, Err: err}
	}
	cfg, err := config.Resolve(cat, opts.ProfileName)
	if err != nil {
		return Result{ExitCode: 2, Err: err}
	}

	// Merge --tool flags into cfg.Tools
	if len(opts.ExtraTools) > 0 {
		if cfg.Tools == nil {
			cfg.Tools = map[string]string{}
		}
		for _, t := range opts.ExtraTools {
			name, ver := parseToolFlag(t)
			cfg.Tools[name] = ver
		}
	}

	// Determine mode + homes. Plan 1 defaults to Mode B (no runtime detection yet).
	// Plan 2 replaces this with real rootless-Podman detection.
	mode := "B"
	hostHome := os.Getenv("HOME")
	if hostHome == "" {
		hostHome = "/root"
	}
	runtimeHome := "/root"
	if mode == "A" {
		runtimeHome = hostHome
	}

	spec := buildSpec(opts, cfg, mode, hostHome, runtimeHome)

	if opts.DryRun {
		if err := RenderSpec(w, spec); err != nil {
			return Result{ExitCode: 3, Err: err}
		}
		return Result{ExitCode: 0}
	}

	// Plan 2: invoke Runtime.Prepare + Runtime.Run here.
	return Result{ExitCode: 3, Err: fmt.Errorf("runtime not yet implemented (Plan 2)")}
}

func parseToolFlag(s string) (string, string) {
	for i, c := range s {
		if c == '=' {
			return s[:i], s[i+1:]
		}
	}
	return s, "latest"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/toolpod/ -v`
Expected: PASS (all 3 tests).

- [ ] **Step 5: Commit**

```bash
git add pkg/toolpod/launch.go pkg/toolpod/launch_test.go pkg/toolpod/dryrun.go
git commit -m "feat: Launch resolves config and renders dry-run spec"
```

---

## Task 9: UI — TTY detection + ProgressWriter stub

**Files:**
- Create: `internal/ui/output.go`
- Create: `internal/ui/output_test.go`

**Interfaces:**
- Produces: `ui.IsTTY(f) bool`, `ui.ProgressWriter` interface (Plan 2's runtime uses it for Prepare progress).

- [ ] **Step 1: Write the failing test**

Create `internal/ui/output_test.go`:

```go
package ui

import "testing"

func TestIsTTYOnRegularFile(t *testing.T) {
	// testing creates temp files; we test via a non-TTY path.
	// On most test runners os.Stdin is not a TTY, so this is a safe check.
	if IsTTY(nil) {
		t.Error("IsTTY(nil) should be false")
	}
}

func TestColorDisabledWhenNotTTY(t *testing.T) {
	out := NewOutput(false)
	if out.Color("green", "hi") != "hi" {
		t.Error("color should be disabled when not TTY")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ui/ -v`
Expected: FAIL — `IsTTY`, `NewOutput` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/ui/output.go`:

```go
package ui

import (
	"io"
	"os"

	"golang.org/x/term"
)

// IsTTY reports whether f is a terminal.
func IsTTY(f *os.File) bool {
	if f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// Output wraps a writer with optional color.
type Output struct {
	color bool
}

func NewOutput(color bool) *Output {
	return &Output{color: color}
}

// Color wraps s in an ANSI color code if color is enabled.
// Supported: green, red, yellow, blue, reset.
func (o *Output) Color(name, s string) string {
	if !o.color {
		return s
	}
	codes := map[string]string{
		"green":  "\033[32m",
		"red":    "\033[31m",
		"yellow": "\033[33m",
		"blue":   "\033[34m",
		"reset":  "\033[0m",
	}
	code, ok := codes[name]
	if !ok {
		return s
	}
	return code + s + codes["reset"]
}

// ProgressWriter is implemented by io.Writer types that receive progress
// lines during Prepare (image pull, tool install). Plan 2's runtime uses it.
type ProgressWriter interface {
	io.Writer
	WriteProgress(line string)
}
```

- [ ] **Step 4: Add golang.org/x/term dependency**

Run: `go get golang.org/x/term`

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/ui/ -v`
Expected: PASS (both tests).

- [ ] **Step 6: Commit**

```bash
git add internal/ui/output.go internal/ui/output_test.go go.mod go.sum
git commit -m "feat: UI output with TTY detection and color helper"
```

---

## Task 10: CLI — arg parsing with passthrough + subcommands

**Files:**
- Create: `cmd/toolpod/cli.go`
- Create: `cmd/toolpod/cli_test.go`
- Modify: `cmd/toolpod/main.go`

**Interfaces:**
- Consumes: `pkg/toolpod.Launch`, `pkg/toolpod.LaunchOpts`, `config.LoadCatalog`, `config.Resolve`, `config.Catalog`.
- Produces: `cmd/toolpod` with subcommands `config show/list/check`, `doctor` (stub), `prune` (stub), and the default launch command with global flags before the profile name.

- [ ] **Step 1: Write the failing test for arg parsing**

Create `cmd/toolpod/cli_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLaunchArgs(t *testing.T) {
	opts, args, err := parseLaunchArgs([]string{"--workspace", "/tmp/p", "opencode", "--model", "foo"})
	if err != nil {
		t.Fatalf("parseLaunchArgs: %v", err)
	}
	if opts.Workspace != "/tmp/p" {
		t.Errorf("Workspace = %q, want /tmp/p", opts.Workspace)
	}
	if opts.ProfileName != "opencode" {
		t.Errorf("ProfileName = %q, want opencode", opts.ProfileName)
	}
	if len(args) != 2 || args[0] != "--model" || args[1] != "foo" {
		t.Errorf("passthrough args = %v, want [--model foo]", args)
	}
}

func TestParseLaunchArgsToolFlag(t *testing.T) {
	opts, _, err := parseLaunchArgs([]string{"--tool", "node=20", "--tool", "rust=1.74", "shell"})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.ExtraTools) != 2 {
		t.Fatalf("ExtraTools = %v, want 2 entries", opts.ExtraTools)
	}
	if opts.ExtraTools[0] != "node=20" || opts.ExtraTools[1] != "rust=1.74" {
		t.Errorf("ExtraTools = %v", opts.ExtraTools)
	}
}

func TestConfigListSubcommand(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "custom.yaml"), []byte("version: 1\nimage: x\ncommand: [\"sh\"]\n"), 0o644)
	var out bytes.Buffer
	code := runConfigList(&out, dir)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	output := out.String()
	if !strings.Contains(output, "shell") {
		t.Errorf("list should include built-in shell; got %s", output)
	}
	if !strings.Contains(output, "custom") {
		t.Errorf("list should include user custom; got %s", output)
	}
}

func TestConfigShowResolved(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "myprof.yaml"), []byte("version: 1\nextends: shell\nimage: override:latest\ncommand: [\"x\"]\n"), 0o644)
	var out bytes.Buffer
	code := runConfigShow(&out, dir, "myprof", true)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "override:latest") {
		t.Errorf("resolved output should show overridden image; got %s", out.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/toolpod/ -v`
Expected: FAIL — `parseLaunchArgs`, `runConfigList`, `runConfigShow` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `cmd/toolpod/cli.go`:

```go
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jgillich/toolpod/internal/config"
	"github.com/jgillich/toolpod/pkg/toolpod"
	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
)

// parseLaunchArgs parses global flags (before the profile name) and returns
// the LaunchOpts (partial — ProfileName set), the passthrough args, and any error.
func parseLaunchArgs(argv []string) (toolpod.LaunchOpts, []string, error) {
	var opts toolpod.LaunchOpts
	fs := pflag.NewFlagSet("toolpod", pflag.ContinueOnError)
	fs.StringVar(&opts.Workspace, "workspace", "", "workspace to mount (default $PWD)")
	fs.StringVar(&opts.ConfigDir, "config-dir", "", "override user config dir")
	fs.StringArrayVar(&opts.ExtraTools, "tool", nil, "add a mise tool (name=version, repeatable)")
	fs.BoolVar(&opts.Rebuild, "rebuild", false, "force rebuild of the profile's image")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "print resolved spec without launching")
	fs.BoolVarP(&opts.Verbose, "verbose", "v", false, "print resolved spec then launch")

	if err := fs.Parse(argv); err != nil {
		return opts, nil, err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return opts, nil, fmt.Errorf("missing profile name (see: toolpod --help)")
	}
	opts.ProfileName = rest[0]
	return opts, rest[1:], nil
}

// dispatch routes os.Args[1:] to the correct subcommand or the launch path.
func dispatch(argv []string) int {
	if len(argv) == 0 {
		printHelp(os.Stdout)
		return 0
	}
	switch argv[0] {
	case "--help", "-h":
		printHelp(os.Stdout)
		return 0
	case "--version":
		fmt.Fprintln(os.Stdout, "toolpod v0.1.0-dev")
		return 0
	case "config":
		return runConfig(argv[1:])
	case "doctor":
		return runDoctor(argv[1:])
	case "prune":
		return runPrune(argv[1:])
	default:
		return runLaunch(argv)
	}
}

func runLaunch(argv []string) int {
	opts, args, err := parseLaunchArgs(argv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if opts.Workspace == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		opts.Workspace = wd
	}
	opts.Args = args
	res := toolpod.Launch(context.Background(), opts)
	if res.Err != nil {
		fmt.Fprintln(os.Stderr, res.Err)
	}
	return res.ExitCode
}

func runConfig(argv []string) int {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: toolpod config <show|list|check> [args]")
		return 2
	}
	switch argv[0] {
	case "show":
		return runConfigShowCmd(os.Stdout, argv[1:])
	case "list":
		return runConfigList(os.Stdout, "")
	case "check":
		return runConfigCheck(os.Stdout, "", argv[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown config subcommand: %s\n", argv[0])
		return 2
	}
}

func runConfigShowCmd(w io.Writer, argv []string) int {
	resolved := false
	args := []string{}
	for _, a := range argv {
		if a == "--resolved" {
			resolved = true
		} else {
			args = append(args, a)
		}
	}
	if len(args) == 0 {
		fmt.Fprintln(w, "usage: toolpod config show <name> [--resolved]")
		return 2
	}
	dir := config.DefaultUserConfigDir()
	return runConfigShow(w, dir, args[0], resolved)
}

func runConfigShow(w io.Writer, dir, name string, resolved bool) int {
	cat, err := config.LoadCatalog(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if resolved {
		cfg, err := config.Resolve(cat, name)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		// Render resolved config as YAML with tilde-expanded paths.
		mode := "B"
		hostHome, _ := os.UserHomeDir()
		runtimeHome := "/root"
		if mode == "A" {
			runtimeHome = hostHome
		}
		cfg = config.ResolveTildes(cfg, mode, hostHome, runtimeHome)
		printConfigYAML(w, cfg)
		return 0
	}
	rc, ok := cat.Get(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "profile not found: %s\n", name)
		return 2
	}
	printConfigYAML(w, rc.Config)
	return 0
}

func runConfigList(w io.Writer, dir string) int {
	if dir == "" {
		dir = config.DefaultUserConfigDir()
	}
	cat, err := config.LoadCatalog(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	for _, name := range cat.Names() {
		fmt.Fprintln(w, name)
	}
	return 0
}

func runConfigCheck(w io.Writer, dir string, argv []string) int {
	if dir == "" {
		dir = config.DefaultUserConfigDir()
	}
	cat, err := config.LoadCatalog(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if len(argv) > 0 {
		_, err := config.Resolve(cat, argv[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		fmt.Fprintf(w, "%s: ok\n", argv[0])
		return 0
	}
	hadErr := false
	for _, name := range cat.Names() {
		if _, err := config.Resolve(cat, name); err != nil {
			fmt.Fprintln(os.Stderr, err)
			hadErr = true
		} else {
			fmt.Fprintf(w, "%s: ok\n", name)
		}
	}
	if hadErr {
		return 2
	}
	return 0
}

func runDoctor(argv []string) int {
	fmt.Fprintln(os.Stderr, "doctor: not yet implemented (Plan 3)")
	return 3
}

func runPrune(argv []string) int {
	fmt.Fprintln(os.Stderr, "prune: not yet implemented (Plan 3)")
	return 3
}

func printConfigYAML(w io.Writer, cfg config.Config) {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		fmt.Fprintf(w, "error rendering config: %v\n", err)
		return
	}
	w.Write(data)
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, "toolpod — disposable mise-based dev containers")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  toolpod <profile-name> [args...]     launch a profile")
	fmt.Fprintln(w, "  toolpod config show <name> [--resolved]")
	fmt.Fprintln(w, "  toolpod config list")
	fmt.Fprintln(w, "  toolpod config check [name]")
	fmt.Fprintln(w, "  toolpod doctor")
	fmt.Fprintln(w, "  toolpod prune")
	fmt.Fprintln(w, "  toolpod --help | --version")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Global flags (before profile name):")
	fmt.Fprintln(w, "  --workspace <path>   workspace to mount (default $PWD)")
	fmt.Fprintln(w, "  --config-dir <path>  override user config dir")
	fmt.Fprintln(w, "  --tool name=version  add a mise tool (repeatable)")
	fmt.Fprintln(w, "  --rebuild            force image rebuild")
	fmt.Fprintln(w, "  --dry-run            print spec without launching")
	fmt.Fprintln(w, "  --verbose / -v       print spec, then launch")
}
```

Need to add `"context"` import to `cli.go`.

Update `cmd/toolpod/main.go`:

```go
package main

import "os"

func main() {
	os.Exit(dispatch(os.Args[1:]))
}
```

- [ ] **Step 4: Add the context import and fix runConfigList signature**

The test calls `runConfigList(&out, dir)` — the function signature is `runConfigList(w io.Writer, dir string) int`. That matches. Add the `context` import to `cli.go`.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./cmd/toolpod/ -v`
Expected: PASS (all 4 tests).

- [ ] **Step 6: Build the binary and do a manual smoke test**

Run:
```bash
go build -o /tmp/toolpod ./cmd/toolpod
/tmp/toolpod config list
/tmp/toolpod config show shell
/tmp/toolpod config show shell --resolved
/tmp/toolpod shell --dry-run --workspace /tmp
```
Expected: `config list` shows opencode, codex, shell. `config show shell` shows raw YAML. `--resolved` shows expanded paths. `--dry-run` prints a spec block.

- [ ] **Step 7: Commit**

```bash
git add cmd/toolpod/cli.go cmd/toolpod/cli_test.go cmd/toolpod/main.go
git commit -m "feat: CLI with arg parsing, config subcommands, and launch dispatch"
```

---

## Task 11: End-to-end test — built-in profiles resolve and dry-run

**Files:**
- Create: `cmd/toolpod/e2e_test.go`

**Interfaces:**
- Consumes: the built `toolpod` binary (via `go run`).

- [ ] **Step 1: Write the failing test**

Create `cmd/toolpod/e2e_test.go`:

```go
package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestE2EBuiltInProfilesList(t *testing.T) {
	out, err := runToolpod(t, "config", "list")
	if err != nil {
		t.Fatalf("config list: %v\n%s", err, out)
	}
	for _, name := range []string{"opencode", "codex", "shell"} {
		if !strings.Contains(out, name) {
			t.Errorf("config list missing %q; got:\n%s", name, out)
		}
	}
}

func TestE2EResolveOpencodeProfile(t *testing.T) {
	out, err := runToolpod(t, "config", "show", "opencode", "--resolved")
	if err != nil {
		t.Fatalf("config show opencode --resolved: %v\n%s", err, out)
	}
	if !strings.Contains(out, "image:") {
		t.Errorf("resolved opencode missing image; got:\n%s", out)
	}
	if !strings.Contains(out, "opencode: latest") {
		t.Errorf("resolved opencode missing tools entry; got:\n%s", out)
	}
}

func TestE2EDryRunShell(t *testing.T) {
	out, err := runToolpod(t, "--dry-run", "--workspace", "/tmp", "shell")
	if err != nil {
		t.Fatalf("dry-run shell: %v\n%s", err, out)
	}
	if !strings.Contains(out, "profile: shell") {
		t.Errorf("dry-run missing profile line; got:\n%s", out)
	}
	if !strings.Contains(out, "workspace:") {
		t.Errorf("dry-run missing workspace; got:\n%s", out)
	}
}

func runToolpod(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("go", append([]string{"run", "./"}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./cmd/toolpod/ -run TestE2E -v`
Expected: PASS (all 3 tests). These exercise the real built-in embedded profiles.

- [ ] **Step 3: Commit**

```bash
git add cmd/toolpod/e2e_test.go
git commit -m "test: end-to-end built-in profile resolution and dry-run"
```

---

## Task 12: README + finalize

**Files:**
- Create: `README.md`

- [ ] **Step 1: Write the README**

Create `README.md`:

```markdown
# toolpod

Disposable, reproducible developer environments in a container, with a
persistent [mise](https://mise.jdx.dev/) environment shared across runs.

```
container  +  workspace  +  persistent mise  =  toolpod
```

toolpod replaces ad hoc shell scripts that launch containers with the right
mounts, SSH keys, caches, tool versions, and AI agents. It's **user-owned**
(your environment, not the project's) and **profile-based** (opencode, codex,
shell, or your own).

## Status

In development. The config system and CLI skeleton are implemented; the
container runtime is next (see `docs/superpowers/plans/`).

## Install (once released)

```
go install github.com/jgillich/toolpod/cmd/toolpod@latest
```

## Quick start

```
$ cd ~/projects/myapp
$ toolpod opencode --model foo
```

## Configuration

Profiles live in `~/.config/toolpod/*.yaml` and can `extends:` built-ins:

```yaml
version: 1
extends: opencode
tools:
  opencode: "0.9.0"
mounts:
  ~/.local/share/opencode/knowledge:
    source: ~/work/shared-knowledge
    read_only: false
```

See the [design doc](docs/superpowers/specs/2026-07-30-toolpod-design.md) for
the full config schema, merge semantics, and architecture.

## License

MIT
```

- [ ] **Step 2: Run the full test suite one last time**

Run: `go test ./...`
Expected: all PASS.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: README with project overview and quick start"
```

---

## Self-Review

**Spec coverage (Plan 1 scope — config + CLI skeleton):**
- §4.1 Full schema → Task 1 (types), Task 3 (built-in YAML files)
- §4.3 Inheritance/merge (scalars, maps, null-to-delete, image/build slot, lists) → Task 4
- §4.3 Cycle detection → Task 4 Step 7-8
- §4.4 Config discovery (built-ins + user, shadowing, reserved names) → Task 3 + Task 5
- §4.5 Example user override → covered by merge tests (Task 4)
- §5.6 Tilde resolution → Task 6
- §7 CLI surface (launch, config show/list/check, doctor/prune stubs, --help/--version) → Task 10
- §7.1 Global flags (--workspace, --config-dir, --tool, --rebuild, --dry-run, --verbose) → Task 10
- §7.2 config show --resolved with expanded paths → Task 10 (runConfigShow)
- §9 Built-in configs (opencode, codex, shell) → Task 3 (YAML files)
- §10 Error handling exit code 2 for config errors → Task 2 (ConfigError.ExitCode), Task 8 (Launch returns 2)
- §4.1 `args_if_none` (renamed from default_args) → Task 1 (types), Task 7 (buildSpec)

**Deferred to Plan 2 (runtime):** §3.2 Runtime interface, §3.3 Docker SDK, §3.4 image build, §5.1-5.5 workspace/mirroring, §6 mise integration, §8 container lifecycle, non-dry-run launch, `--verbose` actual launch.
**Deferred to Plan 3 (operations):** §7.3 doctor, §7.4 prune, integration smoke test.

**Placeholder scan:** No TBD/TODO/"implement later" in any step. All code blocks contain real implementations.

**Type consistency:** `Config`, `RawConfig`, `Mount`, `Build`, `Resources` defined in Task 1, used consistently in Tasks 3-8. `Spec`, `MountSpec`, `CacheSpec`, `WorkspaceSpec` defined in Task 1 (types.go), used in Task 7-8. `ConfigError` defined in Task 2, used in Tasks 3-5. `LaunchOpts`, `Result` defined in Task 1, used in Tasks 8, 10.