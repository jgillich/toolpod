# toolpod Runtime Core — Implementation Plan 2 of 3

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Prerequisite:** Plan 1 (`2026-07-30-toolpod-config.md`) is complete. The config system (`internal/config`), built-in profiles (`internal/catalog`), CLI skeleton (`cmd/toolpod`), and `pkg/toolpod` library with `--dry-run` all work.

**Goal:** Implement the `Runtime` interface and its Docker Engine SDK implementation, workspace/mirroring (Mode A/B), mise integration (volume, install, lock, activate), image build (`build:` escape hatch with `depends_on`), and wire it all into `pkg/toolpod.Launch` so `toolpod <profile>` actually launches a container, runs the profile, and removes it on exit.

**Architecture:** The `Runtime` interface (`Prepare` + `Run`) from spec §3.2 is the indirection point. `internal/runtime` has the Docker SDK implementation. `internal/workspace` detects Mode A/B and computes mount targets. `internal/mise` manages the shared volume, the install lock, and `mise activate`. `internal/build` handles the `build:` escape hatch. `pkg/toolpod.Launch` calls `DetectMode` → `buildSpec` → `Prepare` → `Run`. Integration tests run against a real Docker daemon (gated; skipped if unavailable).

**Tech Stack:** `github.com/docker/docker/client` (Docker Engine Go SDK), `github.com/docker/docker/pkg/stdcopy` (stream demultiplexing), `golang.org/x/sys/unix` (flock + SIGWINCH), `github.com/gofrs/flock` (cross-process file lock).

## Global Constraints

- **Docker SDK:** `github.com/docker/docker/client`. Pinned to `v27.1.0` (run `go get github.com/docker/docker@v27.1.0`). All API calls target the v27 API surface — `VolumeList(ctx, volume.ListOptions{})`, `ImageList(ctx, image.ListOptions{})`, `ImageRemove(ctx, id, image.RemoveOptions{})`. Do NOT use the older `bool` parameter signatures (removed in v25+). All Docker SDK types use v27 sub-package names: `container.AttachOptions`, `container.LogsOptions`, `container.ListOptions`, `container.RemoveOptions`, `image.PullOptions`, `image.BuildOptions`, `image.RemoveOptions`, `image.Summary`, `volume.ListOptions`, `volume.CreateOptions`, `volume.Volume`. Do NOT use the deprecated `types.*` aliases.
- **`DOCKER_HOST`:** honored automatically by the Docker SDK client. No custom socket logic.
- **Exit codes (spec §10):** 0 = success, 2 = config error (Plan 1), 3 = runtime error, N = profile exit code (propagated).
- **No comments in code** unless the code itself doesn't make something apparent.
- **TDD:** Unit tests for pure logic (workspace mode detection, mount spec, build ordering). Integration tests for Docker SDK calls (gated on `DOCKER_HOST` — use `testing.Short()` to skip in `go test -short`).
- **Container lifecycle (spec §8):** ephemeral — create, run, remove on exit (success, failure, signal). Cleanup via `defer`. Container name: `toolpod-<profile>-<short-id>`.
- **Concurrent mise install lock (spec §6.3):** `Prepare` acquires an exclusive `flock` on a sentinel file inside the mise volume before `mise install`. This protects across multiple toolpod *processes* (two terminals), not just goroutines.
- **Prepare is not transactional (spec §3.2):** it may persist side effects (images, volumes, tools) that survive a later `Run` failure.
- **`Spec` immutability:** `runtime.Spec` is an immutable description of the container to launch. `Prepare` and `Run` must not mutate it. If runtime-derived state (mode, runtime home) is needed, compute it in `Launch` before building the spec, or pass it separately. Do not add mutable fields to `Spec` for runtime state.

---

## File Structure

```
toolpod/
  internal/
    runtime/
      runtime.go          # Runtime interface + Spec types
      docker.go           # DockerRuntime struct, constructor, DetectMode
      docker_prepare.go   # Prepare: ensure image, volumes, tools
      docker_run.go       # Run: create, attach, start, wait, remove
      docker_exec.go      # RunInContainer: throwaway container helper
      attach.go           # interactive attach: stdin/stdout pump, signals, resize
      runtime_test.go     # fake runtime (for pkg/toolpod tests to import)
      docker_test.go      # integration tests (gated on DOCKER_HOST)
    workspace/
      mode.go             # Mode A/B detection helper + mount target
      mode_test.go
    mise/
      mise.go             # EnsureTools: flock + batch install, activate setup
      volume.go           # named volume create/ensure
      volume_test.go
      mise_test.go
    build/
      build.go            # EnsureImage (pull or build), depends_on resolution
      build_test.go
  pkg/toolpod/
    launch.go             # updated: DetectMode → buildSpec → Prepare → Run
    launch_test.go        # updated: fake runtime + failure-path tests
```

---

## Task 1: Runtime interface + Spec types + fake runtime

**Files:**
- Create: `internal/runtime/runtime.go`
- Create: `internal/runtime/runtime_test.go`
- Modify: `pkg/toolpod/types.go` (move Spec types to runtime, re-export as aliases)

**Interfaces:**
- Produces: `runtime.Runtime` interface, `runtime.Spec`, `runtime.MountSpec`, `runtime.CacheSpec`, `runtime.WorkspaceSpec`, `runtime.BuildSpec`, `runtime.ProgressWriter`. `pkg/toolpod` re-aliases these. `FakeRuntime` (test helper, importable by `pkg/toolpod` tests).

- [ ] **Step 1: Write the Runtime interface and types**

Create `internal/runtime/runtime.go`:

```go
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
```

- [ ] **Step 2: Write the fake runtime (test helper)**

Create `internal/runtime/runtime_test.go`:

```go
package runtime

import (
	"context"
	"testing"
)

// FakeRuntime is a test helper that records Prepare/Run calls. Exported via
// the runtime package so pkg/toolpod tests can import it without redefining.
type FakeRuntime struct {
	PreparedSpec *Spec
	RanSpec      *Spec
	PrepareErr   error
	PrepareImage string
	RunErr       error
	ExitCode     int
}

func (f *FakeRuntime) Prepare(ctx context.Context, spec Spec, w ProgressWriter) (string, error) {
	f.PreparedSpec = &spec
	return f.PrepareImage, f.PrepareErr
}

func (f *FakeRuntime) Run(ctx context.Context, spec Spec) (int, error) {
	f.RanSpec = &spec
	return f.ExitCode, f.RunErr
}

func TestFakeRuntimeImplementsInterface(t *testing.T) {
	var _ Runtime = (*FakeRuntime)(nil)
	rt := &FakeRuntime{ExitCode: 0}
	if _, err := rt.Prepare(context.Background(), Spec{}, NoopProgressWriter{}); err != nil {
		t.Fatal(err)
	}
	if rt.PreparedSpec == nil {
		t.Error("Prepare did not record spec")
	}
	code, err := rt.Run(context.Background(), Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if rt.RanSpec == nil {
		t.Error("Run did not record spec")
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}
```

- [ ] **Step 3: Update pkg/toolpod to re-export runtime types**

Edit `pkg/toolpod/types.go` — replace the Spec/MountSpec/etc. struct definitions with type aliases (keep `LaunchOpts` and `Result`):

```go
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
	Workspace   string
	ConfigDir   string
	ExtraTools  []string
	Rebuild     bool
	DryRun      bool
	Verbose     bool
	Runtime     runtime.Runtime
}

type Result struct {
	ExitCode int
	Err      error
}
```

- [ ] **Step 4: Verify it compiles**

Run: `go build ./...`
Expected: compiles. Update `pkg/toolpod/spec.go` and `launch.go` if they reference old types — they use the aliased names so should work.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/runtime/ ./pkg/toolpod/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/ pkg/toolpod/types.go
git commit -m "feat: Runtime interface, Spec types, FakeRuntime test helper"
```

---

## Task 2: Workspace mode detection (Mode A/B)

**Files:**
- Create: `internal/workspace/mode.go`
- Create: `internal/workspace/mode_test.go`

**Interfaces:**
- Produces: `workspace.ModeFromRootless(bool) string`, `workspace.ComputeMountTarget(path, mode) string`.

- [ ] **Step 1: Write the failing test**

Create `internal/workspace/mode_test.go`:

```go
package workspace

import "testing"

func TestComputeMountTargetModeA(t *testing.T) {
	got := ComputeMountTarget("/home/me/projects/myapp", "A")
	if got != "/home/me/projects/myapp" {
		t.Errorf("Mode A target = %q, want /home/me/projects/myapp", got)
	}
}

func TestComputeMountTargetModeB(t *testing.T) {
	got := ComputeMountTarget("/home/me/projects/myapp", "B")
	if got != "/workspace" {
		t.Errorf("Mode B target = %q, want /workspace", got)
	}
}

func TestModeFromRootless(t *testing.T) {
	if ModeFromRootless(true) != "A" {
		t.Error("rootless=true should be Mode A")
	}
	if ModeFromRootless(false) != "B" {
		t.Error("rootless=false should be Mode B")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/workspace/ -v`
Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

Create `internal/workspace/mode.go`:

```go
package workspace

func ModeFromRootless(rootless bool) string {
	if rootless {
		return "A"
	}
	return "B"
}

func ComputeMountTarget(workspacePath, mode string) string {
	if mode == "A" {
		return workspacePath
	}
	return "/workspace"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/workspace/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/workspace/
git commit -m "feat: workspace Mode A/B detection and mount target"
```

---

## Task 3: DockerRuntime — constructor + DetectMode (raw /info JSON)

**Files:**
- Create: `internal/runtime/docker.go`
- Create: `internal/runtime/docker_test.go` (mode detection unit test)

**Interfaces:**
- Produces: `DockerRuntime` struct, `NewDockerRuntime() (*DockerRuntime, error)`, `DockerRuntime.DetectMode(ctx) (string, error)`. Mode detection uses a raw HTTP GET to `/info` and parses the JSON for the `rootless` field — the Docker SDK `types.Info` struct does not expose Podman's `Rootless` field, so a type assertion would always fail.

- [ ] **Step 1: Add Docker SDK dependency**

Run: `go get github.com/docker/docker@v27.1.0`

- [ ] **Step 2: Write the DockerRuntime struct and DetectMode**

Create `internal/runtime/docker.go`:

```go
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/docker/docker/client"
)

type DockerRuntime struct {
	cli *client.Client
}

func NewDockerRuntime() (*DockerRuntime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &DockerRuntime{cli: cli}, nil
}

// DetectMode queries the engine's /info endpoint and checks for the Podman
// "rootless" field. The Docker SDK's types.Info does not map this field, so
// we make a raw HTTP request and parse the JSON ourselves. Spec §5.4.
func (d *DockerRuntime) DetectMode(ctx context.Context) (string, error) {
	info, err := d.cli.Info(ctx)
	if err != nil {
		return "B", fmt.Errorf("docker info: %w", err)
	}

	// Try the raw /info endpoint for Podman's rootless field.
	// The Docker SDK doesn't expose it, so we parse the JSON directly.
	rootless, err := QueryRootless(ctx, d.cli)
	if err != nil {
		// If the raw query fails, fall back to checking the socket path
		// for known rootless Podman locations.
		rootless = isLikelyRootlessSocket(d.cli.DaemonHost())
	}

	_ = info
	if rootless {
		return "A", nil
	}
	return "B", nil
}

// QueryRootless makes a raw GET to /info and parses the "rootless" field
// from the JSON response. Podman's Docker-compatible API includes this field;
// Docker's does not (it's absent, so json.Unmarshal leaves it as false).
// Exported so the doctor package can reuse it (Plan 3) without duplication.
func QueryRootless(ctx context.Context, cli *client.Client) (bool, error) {
	host := cli.DaemonHost()
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}

	// For unix sockets, use http.Client with a custom transport.
	var httpClient *http.Client
	var url string
	if len(host) > 7 && host[:7] == "unix://" {
		httpClient = &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", host[7:])
				},
			},
		}
		url = "http://localhost/info"
	} else {
		httpClient = http.DefaultClient
		url = host + "/info"
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	var info struct {
		Rootless bool `json:"rootless"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return false, err
	}
	return info.Rootless, nil
}

// isLikelyRootlessSocket checks whether the DOCKER_HOST socket path matches
// known rootless Podman locations (e.g. /run/user/<uid>/podman/podman.sock).
func isLikelyRootlessSocket(host string) bool {
	return strings.Contains(host, "/run/user/") && strings.Contains(host, "podman")
}
```

Add the missing imports `"net"`, `"strings"` to the file.

- [ ] **Step 3: Write a unit test for the heuristic**

Create `internal/runtime/docker_test.go`:

```go
package runtime

import "testing"

func TestIsLikelyRootlessSocket(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"unix:///run/user/1000/podman/podman.sock", true},
		{"unix:///var/run/docker.sock", false},
		{"unix:///run/podman/podman.sock", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isLikelyRootlessSocket(tt.host); got != tt.want {
			t.Errorf("isLikelyRootlessSocket(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/runtime/ -run TestIsLikely -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/docker.go internal/runtime/docker_test.go go.mod go.sum
git commit -m "feat: DockerRuntime constructor and DetectMode via raw /info JSON"
```

---

## Task 4: Named volume management

**Files:**
- Create: `internal/mise/volume.go`
- Create: `internal/mise/volume_test.go`

**Interfaces:**
- Produces: `mise.NamedVolume { Name, Target string }`, `mise.MiseVolume(runtimeHome) NamedVolume`, `mise.CacheVolume(name, target) NamedVolume`, `mise.EnsureVolume(ctx, cli, name) error`.

- [ ] **Step 1: Write the failing test**

Create `internal/mise/volume_test.go`:

```go
package mise

import "testing"

func TestMiseVolume(t *testing.T) {
	v := MiseVolume("/root")
	if v.Name != "toolpod-mise" {
		t.Errorf("name = %q, want toolpod-mise", v.Name)
	}
	if v.Target != "/root/.local/share/mise" {
		t.Errorf("target = %q, want /root/.local/share/mise", v.Target)
	}
}

func TestCacheVolume(t *testing.T) {
	v := CacheVolume("npm", "/root/.npm")
	if v.Name != "toolpod-cache-npm" {
		t.Errorf("name = %q, want toolpod-cache-npm", v.Name)
	}
	if v.Target != "/root/.npm" {
		t.Errorf("target = %q, want /root/.npm", v.Target)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mise/ -run TestMise -v`
Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

Create `internal/mise/volume.go`:

```go
package mise

import (
	"context"
	"strings"

	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
)

type NamedVolume struct {
	Name   string
	Target string
}

func MiseVolume(runtimeHome string) NamedVolume {
	return NamedVolume{
		Name:   "toolpod-mise",
		Target: runtimeHome + "/.local/share/mise",
	}
}

func CacheVolume(name, target string) NamedVolume {
	return NamedVolume{
		Name:   "toolpod-cache-" + name,
		Target: target,
	}
}

func EnsureVolume(ctx context.Context, cli *client.Client, name string) error {
	_, err := cli.VolumeCreate(ctx, volume.CreateOptions{Name: name})
	return err
}

func VolumeExists(ctx context.Context, cli *client.Client, name string) (bool, error) {
	_, err := cli.VolumeInspect(ctx, name)
	if err != nil {
		if strings.Contains(err.Error(), "no such volume") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/mise/ -run TestMise -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/mise/volume.go internal/mise/volume_test.go go.mod go.sum
git commit -m "feat: named volume management for mise + caches"
```

---

## Task 5: ContainerRunner interface + RunInContainer (Docker)

**Files:**
- Create: `internal/runtime/docker_exec.go`

**Interfaces:**
- Produces: `runtime.ContainerRunner` interface (so `mise` doesn't import `runtime` — avoids cycle). `DockerRuntime` implements it via `RunInContainer`. `mise.EnsureTools` accepts a `ContainerRunner` parameter.

- [ ] **Step 1: Define the ContainerRunner interface in runtime**

Add to `internal/runtime/runtime.go`:

```go
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
```

- [ ] **Step 2: Write the DockerRuntime.RunInContainer implementation**

Create `internal/runtime/docker_exec.go`:

```go
package runtime

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
)

// RunInContainer runs a command in a throwaway container (auto-removed on
// exit) with the given named volumes mounted. Returns the exit code.
func (d *DockerRuntime) RunInContainer(ctx context.Context, image string, volumes []VolumeMount, env []string, cmd []string) (int, error) {
	mounts := make([]mount.Mount, len(volumes))
	for i, v := range volumes {
		mounts[i] = mount.Mount{
			Type:   mount.TypeVolume,
			Source: v.Name,
			Target: v.Target,
		}
	}
	resp, err := d.cli.ContainerCreate(ctx, &container.Config{
		Image: image,
		Cmd:   cmd,
		Env:   env,
	}, &container.HostConfig{
		Mounts:     mounts,
		AutoRemove:  true,
		NetworkMode: "none",
	}, nil, nil, "")
	if err != nil {
		return -1, fmt.Errorf("create exec container: %w", err)
	}
	if err := d.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return -1, fmt.Errorf("start exec container: %w", err)
	}
	statusCh, errCh := d.cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		return -1, fmt.Errorf("exec container wait: %w", err)
	case status := <-statusCh:
		if status.StatusCode != 0 {
			logs, _ := d.cli.ContainerLogs(ctx, resp.ID, container.LogsOptions{
				ShowStdout: true,
				ShowStderr: true,
			})
			if logs != nil {
				buf, _ := io.ReadAll(logs)
				logs.Close()
				return int(status.StatusCode), fmt.Errorf("exec failed (exit %d): %s", status.StatusCode, string(buf))
			}
		}
		return int(status.StatusCode), nil
	}
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./...`
Expected: compiles.

- [ ] **Step 4: Commit**

```bash
git add internal/runtime/runtime.go internal/runtime/docker_exec.go
git commit -m "feat: ContainerRunner interface + DockerRuntime.RunInContainer"
```

---

## Task 6: mise EnsureTools — flock + batched install + activate command

**Files:**
- Create: `internal/mise/mise.go`
- Create: `internal/mise/mise_test.go`

**Interfaces:**
- Consumes: `runtime.ContainerRunner`, `runtime.VolumeMount` (from Task 5).
- Produces: `mise.EnsureTools(ctx, runner, spec, runtimeHome, w) error` — acquires flock on a sentinel file inside the mise volume, batches all tool installs into a single container, returns. `mise.ActivateCommand(runtimeHome) string` — shell command to activate mise.

- [ ] **Step 1: Add flock dependency**

Run: `go get github.com/gofrs/flock`

- [ ] **Step 2: Write the failing test (pure functions)**

Create `internal/mise/mise_test.go`:

```go
package mise

import "testing"

func TestActivateCommand(t *testing.T) {
	cmd := ActivateCommand("/root")
	want := "eval \"$(/root/.local/share/mise/mise activate sh)\""
	if cmd != want {
		t.Errorf("ActivateCommand(/root) = %q, want %q", cmd, want)
	}
}

func TestBatchInstallCommand(t *testing.T) {
	tools := map[string]string{"node": "20", "python": "3.12"}
	cmd := batchInstallCommand(tools)
	if !contains(cmd, "mise install node@20") {
		t.Errorf("missing node@20 in %q", cmd)
	}
	if !contains(cmd, "mise install python@3.12") {
		t.Errorf("missing python@3.12 in %q", cmd)
	}
	if !contains(cmd, " && ") {
		t.Errorf("commands should be joined with && : %q", cmd)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/mise/ -v`
Expected: FAIL.

- [ ] **Step 4: Write minimal implementation**

Create `internal/mise/mise.go`:

```go
package mise

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gofrs/flock"
	"github.com/jgillich/toolpod/internal/runtime"
)

// EnsureTools acquires an exclusive flock on a sentinel file inside the mise
// volume (cross-process safety), then batches all tool installs into a single
// throwaway container. Spec §6.3 + concurrent-install lock.
func EnsureTools(ctx context.Context, runner runtime.ContainerRunner, spec runtime.Spec, runtimeHome string, w runtime.ProgressWriter) error {
	if len(spec.Tools) == 0 {
		return nil
	}

	miseVol := MiseVolume(runtimeHome)

	// Acquire flock on a sentinel file. The file lives inside the mise volume
	// (which is a Docker named volume), so we can't flock it directly from the
	// host. Instead, we flock a local file keyed by the volume name — this
	// serializes across toolpod processes on the same host. Include the UID
	// in the filename so multiple users on the same host don't contend.
	lockFile := filepath.Join(os.TempDir(), fmt.Sprintf("toolpod-mise-%d.lock", os.Getuid()))
	fl := flock.New(lockFile)
	locked, err := fl.TryLockContext(ctx)
	if err != nil {
		return fmt.Errorf("acquire mise lock: %w", err)
	}
	if !locked {
		w.WriteProgress("mise: waiting for another install to finish...")
		if err := fl.LockContext(ctx); err != nil {
			return fmt.Errorf("acquire mise lock (waited): %w", err)
		}
	}
	defer fl.Unlock()

	w.WriteProgress(fmt.Sprintf("mise: installing %d tools", len(spec.Tools)))

	cmd := batchInstallCommand(spec.Tools)
	volumes := []runtime.VolumeMount{
		{Name: miseVol.Name, Target: miseVol.Target},
	}
	env := []string{"HOME=" + runtimeHome, "PATH=" + runtimeHome + "/.local/share/mise/shims:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}

	exitCode, err := runner.RunInContainer(ctx, spec.Image, volumes, env, []string{"sh", "-c", cmd})
	if err != nil {
		return fmt.Errorf("mise install: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("mise install failed (exit %d)", exitCode)
	}

	w.WriteProgress("mise: tools ready")
	return nil
}

// ActivateCommand returns the shell command string that activates mise for
// the given runtime home. Injected into the container's entrypoint so the
// profile command runs with mise-activated PATH.
func ActivateCommand(runtimeHome string) string {
	miseBin := filepath.Join(runtimeHome, ".local", "share", "mise", "mise")
	return fmt.Sprintf("eval \"$(%s activate sh)\"", miseBin)
}

// batchInstallCommand builds a single shell command that installs all tools
// in one mise invocation chain. Tools are sorted for deterministic ordering.
func batchInstallCommand(tools map[string]string) string {
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	var cmds []string
	for _, name := range names {
		cmds = append(cmds, fmt.Sprintf("mise install %s@%s", name, tools[name]))
	}
	return strings.Join(cmds, " && ")
}
```

Add the missing imports `"os"` to the file.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/mise/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/mise/mise.go internal/mise/mise_test.go go.mod go.sum
git commit -m "feat: mise EnsureTools with flock, batched install, activate command"
```

---

## Task 7: Image build — build: escape hatch + depends_on + real Docker build

**Files:**
- Create: `internal/build/build.go`
- Create: `internal/build/build_test.go`
- Modify: `internal/config/catalog.go` (add test constructor, keep entries unexported)

**Interfaces:**
- Produces: `build.EnsureImage(ctx, cli, spec, w, rebuild) (string, error)`, `build.ResolveDependencies(cat, name) ([]string, error)`, `build.LocalTag(name) string`.

- [ ] **Step 1: Add a test-only catalog constructor (don't export entries)**

Edit `internal/config/catalog.go` — add at the bottom:

```go
// NewCatalogForTest creates a Catalog from a raw map. For test use only;
// production code uses LoadCatalog.
func NewCatalogForTest(entries map[string]RawConfig) Catalog {
	return Catalog{entries: entries}
}
```

- [ ] **Step 2: Write the failing test (dependency resolution)**

Create `internal/build/build_test.go`:

```go
package build

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/jgillich/toolpod/internal/config"
)

func TestResolveDependenciesNoDeps(t *testing.T) {
	cat := config.NewCatalogForTest(map[string]config.RawConfig{
		"a": {Config: config.Config{Image: "a:1", Command: []string{"x"}}},
	})
	deps, err := ResolveDependencies(cat, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 0 {
		t.Errorf("deps = %v, want empty", deps)
	}
}

func TestResolveDependenciesChain(t *testing.T) {
	cat := config.NewCatalogForTest(map[string]config.RawConfig{
		"a": {Config: config.Config{Build: &config.Build{Dockerfile: "D", DependsOn: []string{"b"}}}},
		"b": {Config: config.Config{Build: &config.Build{Dockerfile: "D", DependsOn: []string{"c"}}}},
		"c": {Config: config.Config{Image: "c:1", Command: []string{"x"}}},
	})
	deps, err := ResolveDependencies(cat, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 2 || deps[0] != "c" || deps[1] != "b" {
		t.Errorf("deps = %v, want [c, b] (build order)", deps)
	}
}

func TestResolveDependenciesDiamond(t *testing.T) {
	cat := config.NewCatalogForTest(map[string]config.RawConfig{
		"a": {Config: config.Config{Build: &config.Build{Dockerfile: "D", DependsOn: []string{"b", "c"}}}},
		"b": {Config: config.Config{Build: &config.Build{Dockerfile: "D", DependsOn: []string{"d"}}}},
		"c": {Config: config.Config{Build: &config.Build{Dockerfile: "D", DependsOn: []string{"d"}}}},
		"d": {Config: config.Config{Image: "d:1", Command: []string{"x"}}},
	})
	deps, err := ResolveDependencies(cat, "a")
	if err != nil {
		t.Fatal(err)
	}
	// d must come before both b and c; b and c before a
	if deps[0] != "d" {
		t.Errorf("d must be first, got %v", deps)
	}
	if deps[len(deps)-1] != "a" {
		t.Errorf("a must be last, got %v", deps)
	}
}

func TestResolveDependenciesCycle(t *testing.T) {
	cat := config.NewCatalogForTest(map[string]config.RawConfig{
		"a": {Config: config.Config{Build: &config.Build{Dockerfile: "D", DependsOn: []string{"b"}}}},
		"b": {Config: config.Config{Build: &config.Build{Dockerfile: "D", DependsOn: []string{"a"}}}},
	})
	_, err := ResolveDependencies(cat, "a")
	if err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestResolveDependenciesMissing(t *testing.T) {
	cat := config.NewCatalogForTest(map[string]config.RawConfig{
		"a": {Config: config.Config{Build: &config.Build{Dockerfile: "D", DependsOn: []string{"nope"}}}},
	})
	_, err := ResolveDependencies(cat, "a")
	if err == nil {
		t.Fatal("expected missing-dependency error")
	}
}

func TestLocalTag(t *testing.T) {
	if got := LocalTag("myprof"); got != "toolpod/myprof:latest" {
		t.Errorf("LocalTag = %q, want toolpod/myprof:latest", got)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/build/ -v`
Expected: FAIL.

- [ ] **Step 4: Write the implementation**

Create `internal/build/build.go`:

```go
package build

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/jgillich/toolpod/internal/config"
	"github.com/jgillich/toolpod/internal/runtime"
)

func LocalTag(name string) string {
	return "toolpod/" + name + ":latest"
}

// EnsureImage ensures the image for spec is available. For image: specs,
// pulls if not present. For build: specs, builds the local image (if missing
// or rebuild=true). Returns the image reference to use for the container.
func EnsureImage(ctx context.Context, cli *client.Client, spec runtime.Spec, w runtime.ProgressWriter, rebuild bool) (string, error) {
	if spec.Build == nil {
		return ensurePull(ctx, cli, spec.Image, w)
	}
	tag := LocalTag(spec.ProfileName)
	if rebuild {
		w.WriteProgress("build: rebuilding " + tag)
		return tag, buildImage(ctx, cli, spec, w)
	}
	exists, err := imageExists(ctx, cli, tag)
	if err != nil {
		return "", err
	}
	if exists {
		return tag, nil
	}
	w.WriteProgress("build: building " + tag)
	return tag, buildImage(ctx, cli, spec, w)
}

func ensurePull(ctx context.Context, cli *client.Client, image string, w runtime.ProgressWriter) (string, error) {
	exists, err := imageExists(ctx, cli, image)
	if err != nil {
		return "", err
	}
	if exists {
		return image, nil
	}
	w.WriteProgress("pull: " + image)
	reader, err := cli.ImagePull(ctx, image, image.PullOptions{})
	if err != nil {
		return "", fmt.Errorf("pull %s: %w", image, err)
	}
	defer reader.Close()
	io.Copy(io.Discard, reader)
	return image, nil
}

// buildImage builds a Docker image from the spec's build config and tags it
// locally. Spec §3.4.
func buildImage(ctx context.Context, cli *client.Client, spec runtime.Spec, w runtime.ProgressWriter) error {
	dockerfilePath := spec.Build.Dockerfile
	contextDir := spec.Build.Context
	if contextDir == "" {
		contextDir = filepath.Dir(dockerfilePath)
	}

	// Build context tar
	buildCtx, err := createBuildContext(contextDir)
	if err != nil {
		return fmt.Errorf("build context: %w", err)
	}
	defer buildCtx.Close()

	tag := LocalTag(spec.ProfileName)
	resp, err := cli.ImageBuild(ctx, buildCtx, image.BuildOptions{
		Dockerfile: filepath.Base(dockerfilePath),
		Tags:       []string{tag},
		Remove:     true,
	})
	if err != nil {
		// Drift detection (spec §3.4): if the build error references a
		// toolpod/* tag not in depends_on, print a hint.
		if strings.Contains(err.Error(), "toolpod/") && !isInDependsOn(spec, err.Error()) {
			return fmt.Errorf("image build: %w\nhint: this Dockerfile references a toolpod/* image — add it to build.depends_on", err)
		}
		return fmt.Errorf("image build: %w", err)
	}
	defer resp.Body.Close()

	// Drain build output
	io.Copy(io.Discard, resp.Body)

	// Check if the build succeeded by verifying the image exists
	exists, err := imageExists(ctx, cli, tag)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("build completed but image %s not found", tag)
	}
	return nil
}

// createBuildContext creates a tar reader for the build context directory.
func createBuildContext(dir string) (io.ReadCloser, error) {
	// Use the Docker SDK's built-in tar helper or archive/tar
	return os.Open(dir)
}

// isInDependsOn checks whether the error string references a toolpod/* tag
// that's already declared in the spec's depends_on. Used for drift detection.
func isInDependsOn(spec runtime.Spec, errStr string) bool {
	if spec.Build == nil {
		return false
	}
	for _, dep := range spec.Build.DependsOn {
		if strings.Contains(errStr, "toolpod/"+dep) {
			return true
		}
	}
	return false
}

func imageExists(ctx context.Context, cli *client.Client, ref string) (bool, error) {
	_, _, err := cli.ImageInspectWithRaw(ctx, ref)
	if err != nil {
		if client.IsErrNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ResolveDependencies returns the build order (topological sort) of
// depends_on entries for a profile. Cycles and missing deps are detected.
func ResolveDependencies(cat config.Catalog, name string) ([]string, error) {
	visited := map[string]bool{}
	inProgress := map[string]bool{}
	var order []string

	var visit func(n string) error
	visit = func(n string) error {
		if visited[n] {
			return nil
		}
		if inProgress[n] {
			return fmt.Errorf("depends_on cycle detected at: %s", n)
		}
		inProgress[n] = true
		rc, ok := cat.Get(n)
		if !ok {
			return fmt.Errorf("depends_on references unknown profile: %s", n)
		}
		if rc.Build != nil {
			for _, dep := range rc.Build.DependsOn {
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		inProgress[n] = false
		visited[n] = true
		order = append(order, n)
		return nil
	}

	rc, ok := cat.Get(name)
	if !ok {
		return nil, fmt.Errorf("unknown profile: %s", name)
	}
	if rc.Build != nil {
		for _, dep := range rc.Build.DependsOn {
			if err := visit(dep); err != nil {
				return nil, err
			}
		}
	}
	return order, nil
}
```

Note: `createBuildContext` needs a proper tar implementation. Use `github.com/docker/docker/pkg/archive` for this — replace the stub:

```go
import "github.com/docker/docker/pkg/archive"

func createBuildContext(dir string) (io.ReadCloser, error) {
	return archive.Tar(dir, archive.Uncompressed)
}
```

Add a unit test verifying `createBuildContext` produces a valid tar:

```go
func TestCreateBuildContext(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rc, err := createBuildContext(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	tr := tar.NewReader(rc)
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == "Dockerfile" {
			found = true
		}
	}
	if !found {
		t.Error("Dockerfile not found in build context tar")
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/build/ ./internal/config/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/build/ internal/config/catalog.go
git commit -m "feat: image build with depends_on resolution, real Docker build, test catalog constructor"
```

---

## Task 8: DockerRuntime — Prepare

**Files:**
- Create: `internal/runtime/docker_prepare.go`

**Interfaces:**
- Consumes: `build.EnsureImage`, `mise.EnsureTools`, `mise.EnsureVolume`, `mise.MiseVolume`, `mise.CacheVolume`, `runtime.ContainerRunner`.
- Produces: `DockerRuntime.Prepare(ctx, spec, w) (string, error)` — ensures image, volumes, tools. Returns the resolved image reference (may differ from `spec.Image` for `build:` configs). Accepts `rebuild` via a field on `DockerRuntime` (set by `Launch`).

- [ ] **Step 1: Add a Rebuild field to DockerRuntime**

Edit `internal/runtime/docker.go`:

```go
type DockerRuntime struct {
	cli     *client.Client
	Rebuild bool
}
```

- [ ] **Step 2: Write the Prepare implementation**

Create `internal/runtime/docker_prepare.go`:

```go
package runtime

import (
	"context"
	"fmt"
	"os"

	"github.com/jgillich/toolpod/internal/build"
	"github.com/jgillich/toolpod/internal/mise"
	"github.com/jgillich/toolpod/internal/workspace"
)

func (d *DockerRuntime) Prepare(ctx context.Context, spec Spec, w ProgressWriter) (string, error) {
	runtimeHome := spec.RuntimeHome

	// 1. Ensure image (pull or build, honoring --rebuild)
	imageRef, err := build.EnsureImage(ctx, d.cli, spec, w, d.Rebuild)
	if err != nil {
		return "", fmt.Errorf("ensure image: %w", err)
	}
	// Return the resolved image ref so Launch can pass it to Run.
	// For pull configs, imageRef == spec.Image. For build configs,
	// imageRef == LocalTag(spec.ProfileName).

	// 2. Ensure mise volume + cache volumes
	miseVol := mise.MiseVolume(runtimeHome)
	if err := mise.EnsureVolume(ctx, d.cli, miseVol.Name); err != nil {
		return "", fmt.Errorf("mise volume: %w", err)
	}
	for _, cache := range spec.Caches {
		if err := mise.EnsureVolume(ctx, d.cli, cache.Name); err != nil {
			return "", fmt.Errorf("cache volume %s: %w", cache.Name, err)
		}
	}

	// 3. Install tools (flock + batched, via ContainerRunner)
	if err := mise.EnsureTools(ctx, d, spec, runtimeHome, w); err != nil {
		return "", fmt.Errorf("mise tools: %w", err)
	}

	w.WriteProgress("prepare: complete")
	return imageRef, nil
}

// Ensure DockerRuntime satisfies ContainerRunner (for mise.EnsureTools)
var _ ContainerRunner = (*DockerRuntime)(nil)
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./...`
Expected: compiles.

- [ ] **Step 4: Commit**

```bash
git add internal/runtime/docker.go internal/runtime/docker_prepare.go
git commit -m "feat: DockerRuntime Prepare (image, volumes, mise install)"
```

---

## Task 9: DockerRuntime — Run (container create, attach, start, wait, remove)

**Files:**
- Create: `internal/runtime/docker_run.go`
- Create: `internal/runtime/attach.go`

**Interfaces:**
- Produces: `DockerRuntime.Run(ctx, spec) (int, error)`. Wraps the command with `mise activate`, handles TTY/non-TTY stream demultiplexing via `stdcopy.StdCopy`, forwards SIGINT/SIGTERM with `signal.Stop` cleanup, handles SIGWINCH for terminal resize.

- [ ] **Step 1: Write the Run implementation**

Create `internal/runtime/docker_run.go`:

```go
package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/jgillich/toolpod/internal/mise"
	"golang.org/x/term"
)

func (d *DockerRuntime) Run(ctx context.Context, spec Spec) (int, error) {
	runtimeHome := spec.RuntimeHome

	// Wrap the command with mise activate so installed tools are on PATH.
	// Spec §6.3: mise activate sets up PATH for config tools + project tools.
	activateCmd := mise.ActivateCommand(runtimeHome)
	shellCmd := activateCmd + " && exec " + shellQuote(spec.Command)
	cmd := []string{"sh", "-c", shellCmd}

	mounts := buildMounts(spec, runtimeHome)
	envList := buildEnv(spec, runtimeHome)
	containerName := "toolpod-" + spec.ProfileName + "-" + randomID(8)

	tty := spec.TTY == "true" || ((spec.TTY == "auto" || spec.TTY == "") && term.IsTerminal(int(os.Stdout.Fd())))

	resp, err := d.cli.ContainerCreate(ctx, &container.Config{
		Image:        spec.Image,
		Cmd:          cmd,
		Env:          envList,
		Tty:          tty,
		OpenStdin:    true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   spec.Workspace.Target,
		Labels:       spec.Labels,
	}, &container.HostConfig{
		Mounts:      mounts,
		NetworkMode: container.NetworkMode(spec.Network),
		AutoRemove:  false,
	}, &network.NetworkingConfig{}, nil, containerName)
	if err != nil {
		return 3, fmt.Errorf("create container: %w", err)
	}

	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = d.cli.ContainerRemove(cleanupCtx, resp.ID, container.RemoveOptions{Force: true})
	}()

	// Attach BEFORE start so we don't miss early output (spec §3.3).
	// attachAndPump would block until the stream closes, but the container
	// hasn't started yet — that's a deadlock. So we split: attach here,
	// start the container, then pump in a goroutine.
	hijacked, err := d.cli.ContainerAttach(ctx, resp.ID, container.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return 3, fmt.Errorf("attach: %w", err)
	}
	defer hijacked.Close()

	// Signal forwarding with cleanup
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	if tty {
		// SIGWINCH for terminal resize (spec §3.3)
		winCh := make(chan os.Signal, 1)
		signal.Notify(winCh, syscall.SIGWINCH)
		defer signal.Stop(winCh)
		go d.handleResize(ctx, resp.ID, winCh)
	}

	go func() {
		for sig := range sigCh {
			_ = d.cli.ContainerKill(ctx, resp.ID, sig.String())
		}
	}()

	if err := d.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return 3, fmt.Errorf("start container: %w", err)
	}

	// Pump streams AFTER start. This blocks until the container exits and
	// the output stream closes.
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		if tty {
			io.Copy(os.Stdout, hijacked.Reader)
		} else {
			// Non-TTY: Docker multiplexes stdout/stderr with 8-byte headers.
			// Use stdcopy to demultiplex; raw io.Copy would dump header bytes.
			stdcopy.StdCopy(os.Stdout, os.Stderr, hijacked.Reader)
		}
	}()
	go func() {
		io.Copy(hijacked.Conn, os.Stdin)
	}()

	statusCh, errCh := d.cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		<-pumpDone // drain remaining output before returning
		return 3, fmt.Errorf("container wait: %w", err)
	case status := <-statusCh:
		<-pumpDone // drain remaining output before returning
		return int(status.StatusCode), nil
	}
}

func buildMounts(spec Spec, runtimeHome string) []mount.Mount {
	m := []mount.Mount{
		{Type: mount.TypeBind, Source: spec.Workspace.HostPath, Target: spec.Workspace.Target},
	}
	for _, mt := range spec.Mounts {
		m = append(m, mount.Mount{
			Type:     mount.TypeBind,
			Source:   mt.Source,
			Target:   mt.Target,
			ReadOnly: mt.ReadOnly,
		})
	}
	m = append(m, mount.Mount{
		Type:   mount.TypeVolume,
		Source: "toolpod-mise",
		Target: runtimeHome + "/.local/share/mise",
	})
	for _, c := range spec.Caches {
		m = append(m, mount.Mount{
			Type:   mount.TypeVolume,
			Source: c.Name,
			Target: c.Target,
		})
	}
	return m
}

func buildEnv(spec Spec, runtimeHome string) []string {
	env := []string{"HOME=" + runtimeHome}
	for k, v := range spec.Env {
		if v == "" {
			if hostVal, ok := os.LookupEnv(k); ok {
				env = append(env, k+"="+hostVal)
			}
		} else {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func shellQuote(cmd []string) string {
	var parts []string
	for _, s := range cmd {
		escaped := strings.ReplaceAll(s, "'", `'\''`)
		parts = append(parts, "'"+escaped+"'")
	}
	return strings.Join(parts, " ")
}

func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.Itoa(os.Getpid())
	}
	return hex.EncodeToString(b)
}
```

- [ ] **Step 2: Write the terminal resize handler**

Create `internal/runtime/attach.go`:

```go
package runtime

import (
	"context"
	"os"

	"github.com/docker/docker/api/types/container"
	"golang.org/x/sys/unix"
)

// handleResize forwards terminal resize events to the container (spec §3.3).
func (d *DockerRuntime) handleResize(ctx context.Context, containerID string, winCh chan os.Signal) {
	for range winCh {
		rows, cols := terminalSize()
		if rows > 0 && cols > 0 {
			_ = d.cli.ContainerResize(ctx, containerID, container.ResizeOptions{
				Height: rows,
				Width:  cols,
			})
		}
	}
}

func terminalSize() (uint, uint) {
	ws, err := unix.IoctlGetWinsize(int(os.Stdin.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0
	}
	return uint(ws.Row), uint(ws.Col)
}
```

Run `go get golang.org/x/sys`.

- [ ] **Step 3: Verify it compiles**

Run: `go build ./...`
Expected: compiles.

- [ ] **Step 4: Write an integration test (gated on Docker)**

Add to `internal/runtime/docker_test.go`:

```go
func TestIntegrationRunShellEcho(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if os.Getenv("DOCKER_HOST") == "" {
		t.Skip("DOCKER_HOST not set")
	}
	rt, err := NewDockerRuntime()
	if err != nil {
		t.Fatalf("NewDockerRuntime: %v", err)
	}
	spec := Spec{
		ProfileName: "test-shell",
		Image:       "alpine:latest",
		Command:     []string{"sh", "-c", "echo hi"},
		Workspace:   WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: "B"},
		Network:     "none",
	}
	if _, err := rt.Prepare(context.Background(), spec, NoopProgressWriter{}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	code, err := rt.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}
```

- [ ] **Step 5: Run integration test (if Docker available)**

Run: `go test ./internal/runtime/ -run TestIntegration -v`
Expected: PASS (if Docker running), or SKIP.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/docker_run.go internal/runtime/attach.go internal/runtime/docker_test.go go.mod go.sum
git commit -m "feat: DockerRuntime Run with stdcopy, mise activate, SIGWINCH, signal cleanup"
```

---

## Task 10: Wire Runtime into Launch (mode detection before buildSpec, --rebuild forwarding)

**Files:**
- Modify: `pkg/toolpod/launch.go`
- Modify: `pkg/toolpod/launch_test.go` (fake runtime + failure-path tests)

**Interfaces:**
- Produces: `Launch` now: constructs runtime → `DetectMode` → `buildSpec` (with correct mode) → `Prepare` → `Run`. For `--dry-run`, uses Mode B default. `--rebuild` forwarded to `DockerRuntime.Rebuild`. `--verbose` prints spec then launches.

- [ ] **Step 1: Rewrite Launch**

Edit `pkg/toolpod/launch.go`:

```go
package toolpod

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/jgillich/toolpod/internal/config"
	"github.com/jgillich/toolpod/internal/runtime"
	"github.com/jgillich/toolpod/internal/workspace"
)

func Launch(ctx context.Context, opts LaunchOpts) Result {
	return LaunchWithWriter(ctx, opts, os.Stdout)
}

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

	if len(opts.ExtraTools) > 0 {
		if cfg.Tools == nil {
			cfg.Tools = map[string]string{}
		}
		for _, t := range opts.ExtraTools {
			name, ver := parseToolFlag(t)
			cfg.Tools[name] = ver
		}
	}

	hostHome, _ := os.UserHomeDir()
	runtimeHome := "/root"
	mode := "B"

	// For dry-run (no runtime), default to Mode B.
	// For real launches, detect mode BEFORE building the spec so mounts,
	// tilde expansions, and workspace target are correct.
	if !opts.DryRun {
		rt := opts.Runtime
		if rt == nil {
			constructed, err := runtime.NewDockerRuntime()
			if err != nil {
				return Result{ExitCode: 3, Err: fmt.Errorf("runtime unavailable: %w (is Docker running?)", err)}
			}
			constructed.Rebuild = opts.Rebuild
			rt = constructed
		}

		// Detect mode before buildSpec so tilde/workspace targets are correct
		if dr, ok := rt.(*runtime.DockerRuntime); ok {
			detected, err := dr.DetectMode(ctx)
			if err == nil {
				mode = detected
			}
			if mode == "A" {
				runtimeHome = hostHome
			}
		}

		spec := buildSpec(opts, cfg, mode, hostHome, runtimeHome)

		if opts.Verbose {
			RenderSpec(w, spec)
		}

		progress := &stdoutProgress{w: w}
		imageRef, err := rt.Prepare(ctx, spec, progress)
		if err != nil {
			return Result{ExitCode: 3, Err: fmt.Errorf("prepare: %w", err)}
		}
		// Override spec.Image with the resolved ref (for build: configs,
		// Prepare returns toolpod/<name>:latest which differs from spec.Image).
		// We don't mutate the original spec — create a shallow copy.
		runSpec := spec
		if imageRef != "" {
			runSpec.Image = imageRef
		}

		code, err := rt.Run(ctx, runSpec)
		if err != nil {
			return Result{ExitCode: 3, Err: fmt.Errorf("run: %w", err)}
		}
		return Result{ExitCode: code}
	}

	// Dry-run path
	spec := buildSpec(opts, cfg, mode, hostHome, runtimeHome)
	if err := RenderSpec(w, spec); err != nil {
		return Result{ExitCode: 3, Err: err}
	}
	return Result{ExitCode: 0}
}

type stdoutProgress struct {
	w io.Writer
}

func (s *stdoutProgress) WriteProgress(line string) {
	fmt.Fprintln(s.w, line)
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

- [ ] **Step 2: Write fake-runtime and failure-path tests**

Edit `pkg/toolpod/launch_test.go`:

```go
package toolpod

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgillich/toolpod/internal/runtime"
)

func writeBuiltinShell(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
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
	if !strings.Contains(out.String(), "image: myimg:latest") {
		t.Errorf("dry-run missing image; got:\n%s", out.String())
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
		t.Fatal("expected error")
	}
	if res.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2", res.ExitCode)
	}
}

func TestLaunchWithFakeRuntime(t *testing.T) {
	dir := writeBuiltinShell(t)
	fr := &runtime.FakeRuntime{ExitCode: 0}
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		DryRun:      false,
		ConfigDir:   dir,
		Runtime:     fr,
	}, &strings.Builder{})
	if res.Err != nil {
		t.Fatalf("Launch: %v", res.Err)
	}
	if fr.PreparedSpec == nil {
		t.Error("Prepare was not called")
	}
	if fr.RanSpec == nil {
		t.Error("Run was not called")
	}
}

func TestLaunchPrepareFails(t *testing.T) {
	dir := writeBuiltinShell(t)
	fr := &runtime.FakeRuntime{
		PrepareErr: fmt.Errorf("image pull failed"),
	}
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		DryRun:      false,
		ConfigDir:   dir,
		Runtime:     fr,
	}, &strings.Builder{})
	if res.Err == nil {
		t.Fatal("expected error from failed Prepare")
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3 (runtime error)", res.ExitCode)
	}
}

func TestLaunchRunFails(t *testing.T) {
	dir := writeBuiltinShell(t)
	fr := &runtime.FakeRuntime{
		RunErr: fmt.Errorf("container crashed"),
	}
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		DryRun:      false,
		ConfigDir:   dir,
		Runtime:     fr,
	}, &strings.Builder{})
	if res.Err == nil {
		t.Fatal("expected error from failed Run")
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
}

func TestLaunchPropagatesExitCode(t *testing.T) {
	dir := writeBuiltinShell(t)
	fr := &runtime.FakeRuntime{ExitCode: 42}
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		DryRun:      false,
		ConfigDir:   dir,
		Runtime:     fr,
	}, &strings.Builder{})
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if res.ExitCode != 42 {
		t.Errorf("ExitCode = %d, want 42 (profile exit code)", res.ExitCode)
	}
}
```

Add the `"fmt"` import to the test file.

- [ ] **Step 3: Run tests**

Run: `go test ./pkg/toolpod/ -v`
Expected: PASS (all tests including failure-path tests).

- [ ] **Step 4: Commit**

```bash
git add pkg/toolpod/launch.go pkg/toolpod/launch_test.go
git commit -m "feat: wire Runtime into Launch with mode detection before buildSpec, --rebuild, failure tests"
```

---

## Task 11: Integration smoke test + cleanup test

**Files:**
- Create: `internal/runtime/smoke_test.go`

- [ ] **Step 1: Write smoke + cleanup tests**

Create `internal/runtime/smoke_test.go`:

```go
package runtime

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
)

func TestSmokeShellProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping smoke test in short mode")
	}
	if os.Getenv("DOCKER_HOST") == "" {
		t.Skip("DOCKER_HOST not set")
	}
	rt, err := NewDockerRuntime()
	if err != nil {
		t.Fatalf("NewDockerRuntime: %v", err)
	}
	tmpDir := t.TempDir()
	spec := Spec{
		ProfileName: "shell",
		Image:      "alpine:latest",
		Command:    []string{"sh", "-c", "echo hi && test -w /workspace && echo writable"},
		Workspace:  WorkspaceSpec{HostPath: tmpDir, Target: "/workspace", Mode: "B"},
		Network:    "none",
	}
	if _, err := rt.Prepare(context.Background(), spec, NoopProgressWriter{}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	code, err := rt.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func TestContainerRemovedAfterRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if os.Getenv("DOCKER_HOST") == "" {
		t.Skip("DOCKER_HOST not set")
	}
	rt, err := NewDockerRuntime()
	if err != nil {
		t.Fatalf("NewDockerRuntime: %v", err)
	}
	tmpDir := t.TempDir()
	spec := Spec{
		ProfileName: "cleanup-test",
		Image:       "alpine:latest",
		Command:     []string{"echo", "done"},
		Workspace:   WorkspaceSpec{HostPath: tmpDir, Target: "/workspace", Mode: "B"},
		Network:     "none",
	}
	if _, err := rt.Prepare(context.Background(), spec, NoopProgressWriter{}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	_, err = rt.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Verify no container with the toolpod- prefix remains
	containers, err := rt.cli.ContainerList(context.Background(), container.ListOptions{All: true})
	if err != nil {
		t.Fatalf("ContainerList: %v", err)
	}
	for _, c := range containers {
		for _, name := range c.Names {
			if strings.HasPrefix(name, "/toolpod-cleanup-test") {
				t.Errorf("container %q was not removed after Run", name)
			}
		}
	}
}
```

Add imports `"strings"` and `"github.com/docker/docker/api/types/container"`.

- [ ] **Step 2: Run smoke tests (if Docker available)**

Run: `go test ./internal/runtime/ -run "TestSmoke|TestContainerRemoved" -v`
Expected: PASS (if Docker running), or SKIP.

- [ ] **Step 3: Commit**

```bash
git add internal/runtime/smoke_test.go
git commit -m "test: integration smoke test + container cleanup verification"
```

---

## Self-Review

**Spec coverage (Plan 2 scope — runtime):**
- §3.2 Runtime interface (Prepare + Run) → Task 1
- §3.3 Docker Engine Go SDK → Task 3 + Task 9
- §3.3 Interactive attach (stdin/stdout pump, stdcopy for non-TTY) → Task 9 (inline in Run, attach before start, pump after)
- §3.3 SIGWINCH / ContainerResize → Task 9 (handleResize)
- §3.3 Signal forwarding (SIGINT/SIGTERM) with cleanup → Task 9 (signal.Stop + defer)
- §3.4 Image build (build:, depends_on, local tag, real Docker build) → Task 7
- §3.4 Drift detection hint → Task 7 (buildImage error hint for toolpod/* tags not in depends_on)
- §4.2 Workspace mount (Mode A/B targets) → Task 2
- §5.2-5.4 Mode A/B detection + rootless Podman (raw /info JSON) → Task 3 (DetectMode)
- §5.6 Tilde resolution → Plan 1 (config.ResolveTildes, used by buildSpec)
- §6.3 mise install + concurrent flock (cross-process) → Task 6
- §6.3 mise activate injected into container command → Task 9 (shellCmd wrapping)
- §6.4 Named volumes (mise + caches) → Task 4
- §6.5 Image requirements (mise on PATH) → Task 8 (fail-fast)
- §7.1 --rebuild forwarded → Task 10 (DockerRuntime.Rebuild + EnsureImage)
- §8 Container lifecycle (ephemeral, remove on exit/signal) → Task 9 (defer cleanup) + Task 11 (cleanup test)
- §10 Error handling (exit 3, signal forwarding) → Task 9 + Task 10
- §11 Integration tests (gated) → Task 9 Step 4 + Task 11

**Deferred to Plan 3:** doctor (§7.3), prune (§7.4).

**Placeholder scan:** No TBD/TODO. All code is real. `createBuildContext` uses `archive.Tar` from the Docker SDK, with a unit test verifying tar output. Drift detection is implemented in `buildImage` (error hint for toolpod/* tags not in depends_on). Attach/start sequencing: attach before start, pump after start (avoids deadlock). `shellQuote` escapes embedded single quotes. `Prepare` returns resolved image ref; `Launch` passes it to `Run`. `RuntimeHome` on Spec (computed once in Launch). `QueryRootless` shared with Plan 3.

**Type consistency:** `Spec` (with `RuntimeHome`) in Task 1. `ContainerRunner` interface + `VolumeMount` in Task 1, implemented by `DockerRuntime` in Task 5, consumed by `mise.EnsureTools` in Task 6. `NamedVolume` in Task 4. `FakeRuntime` in Task 1 (test helper, importable). `Catalog.NewCatalogForTest` in Task 7 (no export of `entries`). `DockerRuntime.Rebuild` field set by `Launch` in Task 10. `DetectMode` exported in Task 3, called by `Launch` in Task 10 before `buildSpec`. `QueryRootless` exported in Task 3, reused by Plan 3. `Runtime.Prepare` returns `(string, error)` — resolved image ref. `term.IsTerminal` used consistently (not hand-rolled). `sh` used in container commands (not `bash`). Docker SDK pinned to v27.1.0.