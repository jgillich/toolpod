# toolpod Operations (doctor, prune) — Implementation Plan 3 of 3

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Prerequisite:** Plans 1 and 2 are complete. The config system, CLI skeleton, Runtime (Docker SDK), workspace/mirroring, mise integration, image build, and launch all work.

**Goal:** Implement `toolpod doctor` (10-check environment diagnostics) and `toolpod prune` (cleanup of toolpod-managed volumes and images), plus a comprehensive end-to-end integration smoke test that launches the real `shell` profile against a Docker daemon.

**Architecture:** `internal/doctor` runs 10 checks (spec §7.3) against the runtime, catalog, and filesystem, printing a pass/fail/warn checklist. `internal/prune` lists and removes toolpod-prefixed volumes and images with confirmation. Both are wired into `cmd/toolpod` as subcommands.

**Tech Stack:** Docker Engine Go SDK (already from Plan 2), `internal/ui` for colored output, `gopkg.in/yaml.v3` for project-tool parsing (mise.toml / .tool-versions).

## Global Constraints

- **Exit codes (spec §10):** doctor exit 0 if all critical checks pass, non-zero if any fail. prune exit 0 on success, 2 on error.
- **doctor is not a launch gate (spec §7.3):** failing checks don't prevent `toolpod <name>` from running.
- **prune safety (spec §7.4):** only touches `toolpod-` prefixed volumes and `toolpod/` prefixed images. Prompts for confirmation unless `-y`/`--force`.
- **No comments in code** unless the code itself doesn't make something apparent.
- **TDD:** Unit tests for check logic (pure functions where possible); integration tests gated on `DOCKER_HOST`.

---

## File Structure

```
toolpod/
  internal/
    doctor/
      doctor.go          # Doctor type, Run(), check orchestration
      checks.go          # Individual check functions (10 checks)
      doctor_test.go     # Unit tests for check logic
      checks_test.go
    prune/
      prune.go           # List + remove volumes/images
      prune_test.go
  cmd/toolpod/
    cli.go               # Updated: real doctor + prune dispatch
```

---

## Task 1: Doctor — check framework + output format

**Files:**
- Create: `internal/doctor/doctor.go`
- Create: `internal/doctor/doctor_test.go`

**Interfaces:**
- Produces: `doctor.Check` (struct: Name, Status, Message), `doctor.Result` (list of checks + summary), `doctor.Run(ctx, opts) Result`. Status: `pass`, `fail`, `warn`, `info`, `skip`.

- [ ] **Step 1: Write the failing test for check formatting**

Create `internal/doctor/doctor_test.go`:

```go
package doctor

import "testing"

func TestCheckPassFormat(t *testing.T) {
	c := Check{Name: "runtime", Status: Pass, Message: "docker 27.0 at unix:///var/run/docker.sock"}
	if c.Format() != "[pass] runtime: docker 27.0 at unix:///var/run/docker.sock" {
		t.Errorf("Format() = %q", c.Format())
	}
}

func TestCheckWarnFormat(t *testing.T) {
	c := Check{Name: "buildkit", Status: Warn, Message: "available"}
	if c.Format() != "[warn] buildkit: available" {
		t.Errorf("Format() = %q", c.Format())
	}
}

func TestCheckSkipFormat(t *testing.T) {
	c := Check{Name: "mise functional", Status: Skip, Message: "base image not yet pulled"}
	if c.Format() != "[skip] mise functional: base image not yet pulled" {
		t.Errorf("Format() = %q", c.Format())
	}
}

func TestResultSummary(t *testing.T) {
	r := Result{Checks: []Check{
		{Status: Pass},
		{Status: Warn},
		{Status: Pass},
		{Status: Skip},
	}}
	summary := r.Summary()
	if summary != "1 warning, all critical checks passed." {
		t.Errorf("Summary() = %q", want '1 warning, all critical checks passed.'", summary)
	}
}

func TestResultSummaryWithFailure(t *testing.T) {
	r := Result{Checks: []Check{
		{Status: Pass},
		{Status: Fail, Message: "docker daemon not running"},
	}}
	summary := r.Summary()
	if summary != "1 failure." {
		t.Errorf("Summary() = %q, want '1 failure.'", summary)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/doctor/ -v`
Expected: FAIL — `Check`, `Result`, `Pass`, `Warn`, `Skip`, `Fail` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/doctor/doctor.go`:

```go
package doctor

import (
	"context"
	"strconv"
)

// Status values for a check result.
type Status int

const (
	Pass Status = iota
	Fail
	Warn
	Info
	Skip
)

func (s Status) String() string {
	switch s {
	case Pass:
		return "pass"
	case Fail:
		return "fail"
	case Warn:
		return "warn"
	case Info:
		return "info"
	case Skip:
		return "skip"
	}
	return "unknown"
}

// Check is the result of a single diagnostic check.
type Check struct {
	Name    string
	Status  Status
	Message string
}

// Format returns the one-line display string for this check.
func (c Check) Format() string {
	return "[" + c.Status.String() + "] " + c.Name + ": " + c.Message
}

// Result is the full doctor output: all checks + summary.
type Result struct {
	Checks []Check
}

// Summary returns a summary line based on the checks.
func (r Result) Summary() string {
	fails, warns := 0, 0
	for _, c := range r.Checks {
		switch c.Status {
		case Fail:
			fails++
		case Warn:
			warns++
		}
	}
	if fails > 0 {
		return pluralize(fails, "failure") + "."
	}
	if warns > 0 {
		return pluralize(warns, "warning") + ", all critical checks passed."
	}
	return "all checks passed."
}

func pluralize(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// HasFailure returns true if any check has status Fail.
func (r Result) HasFailure() bool {
	for _, c := range r.Checks {
		if c.Status == Fail {
			return true
		}
	}
	return false
}

// Options controls which checks to run and with what parameters.
type Options struct {
	Workspace string
	ConfigDir  string
}

// Run executes all doctor checks and returns the result.
func Run(ctx context.Context, opts Options) Result {
	rt, err := newRuntime()
	if err != nil {
		return Result{Checks: []Check{
			{Name: "runtime", Status: Fail, Message: "cannot connect to Docker: " + err.Error()},
		}}
	}
	return runChecks(ctx, rt, opts)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/doctor/ -run TestCheck -v`
Expected: PASS (format + summary tests).

- [ ] **Step 5: Commit**

```bash
git add internal/doctor/doctor.go internal/doctor/doctor_test.go
git commit -m "feat: doctor check framework with format and summary"
```

---

## Task 2: Doctor — the 10 checks

**Files:**
- Create: `internal/doctor/checks.go`
- Create: `internal/doctor/checks_test.go`

**Interfaces:**
- Produces: `runChecks(ctx, rt, opts) Result` — runs all 10 checks from spec §7.3.

- [ ] **Step 1: Add TOML parser dependency**

Run: `go get github.com/BurntSushi/toml`

- [ ] **Step 2: Write the failing test for check logic (pure parts)**

Create `internal/doctor/checks_test.go`:

```go
package doctor

import (
	"context"
	"testing"
)

func TestCheckWorkspaceWritable(t *testing.T) {
	dir := t.TempDir()
	c := checkWorkspaceWritable(context.Background(), dir)
	if c.Status != Pass {
		t.Errorf("writable dir: status = %s, want pass", c.Status)
	}
}

func TestCheckWorkspaceNotWritable(t *testing.T) {
	dir := t.TempDir()
	err := os.Chmod(dir, 0o444)
	if err != nil {
		t.Skip("cannot chmod on this OS")
	}
	defer os.Chmod(dir, 0o755)
	c := checkWorkspaceWritable(context.Background(), dir)
	if c.Status == Pass {
		t.Error("read-only dir should not pass")
	}
}

func TestCheckProjectTools(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".tool-versions"), []byte("node 22\npython 3.13\n"), 0o644)
	c := checkProjectTools(context.Background(), dir)
	if c.Status != Pass {
		t.Errorf("status = %s, want pass", c.Status)
	}
	if !strings.Contains(c.Message, "node@22") {
		t.Errorf("message should list node@22; got %q", c.Message)
	}
}

func TestCheckProjectToolsNone(t *testing.T) {
	dir := t.TempDir()
	c := checkProjectTools(context.Background(), dir)
	if c.Status != Info {
		t.Errorf("no tool files: status = %s, want info", c.Status)
	}
}
```

Add imports `"os"`, `"path/filepath"`, `"strings"` to the test file.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/doctor/ -run TestCheck -v`
Expected: FAIL — `checkWorkspaceWritable`, `checkProjectTools` undefined.

- [ ] **Step 3: Write the checks implementation**

Create `internal/doctor/checks.go`:

```go
package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/jgillich/toolpod/internal/config"
	"github.com/jgillich/toolpod/internal/runtime"
)

type dockerRT struct {
	cli *client.Client
}

func newRuntime() (*dockerRT, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &dockerRT{cli: cli}, nil
}

// runChecks executes all 10 checks from spec §7.3.
func runChecks(ctx context.Context, rt *dockerRT, opts Options) Result {
	var checks []Check

	checks = append(checks, checkRuntimeReachable(ctx, rt))
	checks = append(checks, checkRootless(ctx, rt))
	checks = append(checks, checkBuildKit(ctx, rt))
	checks = append(checks, checkMiseBaseImage(ctx, rt))
	checks = append(checks, checkVolumes(ctx, rt))
	checks = append(checks, checkPermissions(ctx, rt))

	// Config validity (uses the catalog)
	userDir := opts.ConfigDir
	if userDir == "" {
		userDir = config.DefaultUserConfigDir()
	}
	checks = append(checks, checkConfigValidity(userDir))

	// Project tools (from workspace)
	ws := opts.Workspace
	if ws == "" {
		ws, _ = os.Getwd()
	}
	checks = append(checks, checkProjectTools(ctx, ws))
	checks = append(checks, checkWorkspaceWritable(ctx, ws))

	return Result{Checks: checks}
}

func checkRuntimeReachable(ctx context.Context, rt *dockerRT) Check {
	info, err := rt.cli.Info(ctx)
	if err != nil {
		return Check{Name: "runtime", Status: Fail, Message: "unreachable: " + err.Error()}
	}
	engine := "docker"
	if info.OSType == "" || strings.Contains(info.Name, "podman") {
		engine = "podman"
	}
	return Check{Name: "runtime", Status: Pass, Message: fmt.Sprintf("%s at %s", engine, rt.cli.DaemonHost())}
}

func checkRootless(ctx context.Context, rt *dockerRT) Check {
	rootless, err := runtime.QueryRootless(ctx, rt.cli)
	if err != nil {
		return Check{Name: "rootless", Status: Fail, Message: err.Error()}
	}
	if !rootless {
		return Check{Name: "rootless", Status: Pass, Message: "no → Mode B (/workspace fallback)"}
	}
	return Check{Name: "rootless", Status: Pass, Message: "yes → Mode A (full mirroring)"}
}

func checkBuildKit(ctx context.Context, rt *dockerRT) Check {
	info, err := rt.cli.Info(ctx)
	if err != nil {
		return Check{Name: "buildkit", Status: Warn, Message: "unreachable"}
	}
	// Docker's Info struct has a BuildkitVersion field (since Docker 23.0+).
	// If it's non-empty, BuildKit is available. Podman may not expose this.
	if info.BuildkitVersion != "" {
		return Check{Name: "buildkit", Status: Pass, Message: "available (" + info.BuildkitVersion + ")"}
	}
	return Check{Name: "buildkit", Status: Warn, Message: "not detected (build: profiles require it)"}
}

func checkMiseBaseImage(ctx context.Context, rt *dockerRT) Check {
	image := "ghcr.io/jdx/mise:latest"
	_, _, err := rt.cli.ImageInspectWithRaw(ctx, image)
	if err != nil {
		if client.IsErrNotFound(err) {
			return Check{Name: "mise base image", Status: Info, Message: "not present (will pull on first launch)"}
		}
		return Check{Name: "mise base image", Status: Warn, Message: err.Error()}
	}
	return Check{Name: "mise base image", Status: Pass, Message: "present"}
}

func checkVolumes(ctx context.Context, rt *dockerRT) Check {
	volumes, err := rt.cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return Check{Name: "volumes", Status: Warn, Message: err.Error()}
	}
	var found []string
	for _, v := range volumes.Volumes {
		if strings.HasPrefix(v.Name, "toolpod-") {
			found = append(found, v.Name)
		}
	}
	if len(found) == 0 {
		return Check{Name: "volumes", Status: Pass, Message: "none yet (will create on first launch)"}
	}
	return Check{Name: "volumes", Status: Pass, Message: strings.Join(found, ", ")}
}

func checkPermissions(ctx context.Context, rt *dockerRT) Check {
	// Test: can we create a volume?
	_, err := rt.cli.VolumeCreate(ctx, volume.CreateOptions{Name: "toolpod-perm-test"})
	if err != nil {
		return Check{Name: "permissions", Status: Fail, Message: "cannot create volumes: " + err.Error()}
	}
	_ = rt.cli.VolumeRemove(ctx, "toolpod-perm-test", true)

	// Test: can we create a container? Use empty name to let Docker auto-generate
	// one, avoiding name-collision if a previous run crashed between create and remove.
	resp, err := rt.cli.ContainerCreate(ctx, &container.Config{
		Image: "alpine:latest",
		Cmd:   []string{"echo", "ok"},
	}, nil, nil, nil, "")
	if err != nil {
		return Check{Name: "permissions", Status: Fail, Message: "cannot create containers: " + err.Error()}
	}
	_ = rt.cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})

	return Check{Name: "permissions", Status: Pass, Message: "can create containers and volumes"}
}

func checkConfigValidity(userDir string) Check {
	cat, err := config.LoadCatalog(userDir)
	if err != nil {
		return Check{Name: "configs", Status: Fail, Message: err.Error()}
	}
	hadErr := false
	for _, name := range cat.Names() {
		if _, err := config.Resolve(cat, name); err != nil {
			hadErr = true
		}
	}
	if hadErr {
		return Check{Name: "configs", Status: Fail, Message: "some configs invalid"}
	}
	return Check{Name: "configs", Status: Pass, Message: fmt.Sprintf("%d profiles, all valid", len(cat.Names()))}
}

func checkProjectTools(ctx context.Context, workspace string) Check {
	toolsFile := filepath.Join(workspace, ".tool-versions")
	miseFile := filepath.Join(workspace, "mise.toml")

	var tools []string
	if data, err := os.ReadFile(toolsFile); err == nil {
		tools = parseToolVersions(string(data))
	}
	if data, err := os.ReadFile(miseFile); err == nil {
		tools = append(tools, parseMiseToml(string(data))...)
	}

	if len(tools) == 0 {
		return Check{Name: "project tools", Status: Info, Message: "none detected (no mise.toml or .tool-versions)"}
	}
	return Check{Name: "project tools", Status: Pass, Message: strings.Join(tools, ", ") + " (from project)"}
}

func checkWorkspaceWritable(ctx context.Context, workspace string) Check {
	testFile := filepath.Join(workspace, ".toolpod-write-test")
	if err := os.WriteFile(testFile, []byte("x"), 0o644); err != nil {
		return Check{Name: "workspace", Status: Fail, Message: workspace + " is not writable"}
	}
	os.Remove(testFile)
	return Check{Name: "workspace", Status: Pass, Message: workspace + " is writable"}
}

// parseToolVersions parses a .tool-versions file (lines of "name version").
func parseToolVersions(content string) []string {
	var tools []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			tools = append(tools, parts[0]+"@"+parts[1])
		}
	}
	return tools
}

// parseMiseToml parses a mise.toml file for [tools] entries using a real TOML
// parser. Handles inline tables, quoted keys, array versions, and nested sections.
func parseMiseToml(content string) []string {
	var data struct {
		Tools map[string]any `toml:"tools"`
	}
	if _, err := toml.Decode(content, &data); err != nil {
		return nil
	}
	var tools []string
	for name, val := range data.Tools {
		switch v := val.(type) {
		case string:
			tools = append(tools, name+"@"+v)
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok {
					tools = append(tools, name+"@"+s)
				}
			}
		case map[string]any:
			// Inline table: node = { version = "20" }
			if ver, ok := v["version"].(string); ok {
				tools = append(tools, name+"@"+ver)
			}
		}
	}
	sort.Strings(tools)
	return tools
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/doctor/ -v`
Expected: PASS (workspace writable, project tools tests pass).

- [ ] **Step 5: Commit**

```bash
git add internal/doctor/checks.go internal/doctor/checks_test.go
git commit -m "feat: doctor 10 checks (runtime, rootless, volumes, configs, workspace, project tools)"
```

---

## Task 3: Wire doctor into CLI

**Files:**
- Modify: `cmd/toolpod/cli.go`

- [ ] **Step 1: Replace the doctor stub with the real implementation**

Edit `cmd/toolpod/cli.go` — replace `runDoctor`:

```go
func runDoctor(argv []string) int {
	opts := doctor.Options{}
	fs := pflag.NewFlagSet("doctor", pflag.ContinueOnError)
	fs.StringVar(&opts.Workspace, "workspace", "", "workspace to check")
	fs.StringVar(&opts.ConfigDir, "config-dir", "", "override user config dir")
	if err := fs.Parse(argv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if opts.Workspace == "" {
		wd, _ := os.Getwd()
		opts.Workspace = wd
	}

	result := doctor.Run(context.Background(), opts)

	out := ui.NewOutput(ui.IsTTY(os.Stdout))
	for _, c := range result.Checks {
		color := "reset"
		switch c.Status {
		case doctor.Pass:
			color = "green"
		case doctor.Fail:
			color = "red"
		case doctor.Warn:
			color = "yellow"
		case doctor.Info, doctor.Skip:
			color = "blue"
		}
		fmt.Println(out.Color(color, c.Format()))
	}
	fmt.Println()
	fmt.Println(out.Color("reset", result.Summary()))

	if result.HasFailure() {
		return 1
	}
	return 0
}
```

Add imports `"github.com/jgillich/toolpod/internal/doctor"` and `"github.com/jgillich/toolpod/internal/ui"` and `"github.com/spf13/pflag"` (already imported) and `"context"` (already imported).

- [ ] **Step 2: Build and smoke-test doctor**

Run:
```bash
go build -o /tmp/toolpod ./cmd/toolpod
/tmp/toolpod doctor
```
Expected: prints a checklist with runtime pass/fail, rootless, volumes, configs, workspace checks.

- [ ] **Step 3: Commit**

```bash
git add cmd/toolpod/cli.go
git commit -m "feat: wire doctor command into CLI with colored output"
```

---

## Task 4: Prune — list + remove volumes and images

**Files:**
- Create: `internal/prune/prune.go`
- Create: `internal/prune/prune_test.go`

**Interfaces:**
- Produces: `prune.Run(ctx, opts) error` — lists toolpod-prefixed volumes + images, prompts, removes. `prune.Options { Volumes, Images, Force bool }`.

- [ ] **Step 1: Write the failing test (list logic)**

Create `internal/prune/prune_test.go`:

```go
package prune

import "testing"

func TestIsToolpodVolume(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"toolpod-mise", true},
		{"toolpod-cache-npm", true},
		{"toolpod-cache-cargo", true},
		{"my-volume", false},
		{"docker-volumes", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isToolpodVolume(tt.name); got != tt.want {
			t.Errorf("isToolpodVolume(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestIsToolpodImage(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"toolpod/opencode:latest", true},
		{"toolpod/shell:latest", true},
		{"toolpod/myprof:latest", true},
		{"alpine:latest", false},
		{"ghcr.io/jdx/mise:latest", false},
	}
	for _, tt := range tests {
		if got := isToolpodImage(tt.ref); got != tt.want {
			t.Errorf("isToolpodImage(%q) = %v, want %v", tt.ref, got, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/prune/ -v`
Expected: FAIL — `isToolpodVolume`, `isToolpodImage` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/prune/prune.go`:

```go
package prune

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"golang.org/x/term"
)

// Options controls what prune removes.
type Options struct {
	Volumes bool // remove toolpod- prefixed volumes
	Images  bool // remove toolpod/ prefixed images
	Force   bool // skip confirmation prompt
}

// Result is what prune did.
type Result struct {
	VolumesRemoved []string
	ImagesRemoved  []string
}

// Run lists toolpod-managed volumes and images, prompts for confirmation
// (unless Force), removes them, and returns the result.
func Run(ctx context.Context, opts Options) (Result, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return Result{}, fmt.Errorf("docker client: %w", err)
	}

	var result Result

	if opts.Volumes || (!opts.Volumes && !opts.Images) {
		vols, err := listToolpodVolumes(ctx, cli)
		if err != nil {
			return result, fmt.Errorf("list volumes: %w", err)
		}
		if len(vols) > 0 {
			if !opts.Force {
				if !confirm("volumes", volNames(vols), os.Stdin) {
					return result, nil
				}
			}
			for _, v := range vols {
				if err := cli.VolumeRemove(ctx, v.Name, true); err != nil {
					fmt.Fprintf(os.Stderr, "  failed to remove volume %s: %v\n", v.Name, err)
				} else {
					result.VolumesRemoved = append(result.VolumesRemoved, v.Name)
				}
			}
		}
	}

	if opts.Images || (!opts.Volumes && !opts.Images) {
		imgs, err := listToolpodImages(ctx, cli)
		if err != nil {
			return result, fmt.Errorf("list images: %w", err)
		}
		if len(imgs) > 0 {
			if !opts.Force {
				if !confirm("images", imgTags(imgs), os.Stdin) {
					return result, nil
				}
			}
			for _, img := range imgs {
				if err := cli.ImageRemove(ctx, img.ID, image.RemoveOptions{Force: true}); err != nil {
					fmt.Fprintf(os.Stderr, "  failed to remove image %s: %v\n", img.ID, err)
				} else {
					result.ImagesRemoved = append(result.ImagesRemoved, img.ID)
				}
			}
		}
	}

	return result, nil
}

func listToolpodVolumes(ctx context.Context, cli *client.Client) ([]*volume.Volume, error) {
	resp, err := cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return nil, err
	}
	var found []*volume.Volume
	for _, v := range resp.Volumes {
		if isToolpodVolume(v.Name) {
			found = append(found, v)
		}
	}
	return found, nil
}

func listToolpodImages(ctx context.Context, cli *client.Client) ([]image.Summary, error) {
	imgs, err := cli.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return nil, err
	}
	var found []image.Summary
	for _, img := range imgs {
		for _, tag := range img.RepoTags {
			if isToolpodImage(tag) {
				found = append(found, img)
				break
			}
		}
	}
	return found, nil
}

func isToolpodVolume(name string) bool {
	return strings.HasPrefix(name, "toolpod-")
}

func isToolpodImage(ref string) bool {
	return strings.HasPrefix(ref, "toolpod/")
}

func confirm(kind string, items []string, r io.Reader) bool {
	if f, ok := r.(*os.File); ok && !term.IsTerminal(int(f.Fd())) {
		fmt.Fprintln(os.Stderr, "Error: cannot prompt for confirmation in non-interactive shell. Use --force.")
		return false
	}
	fmt.Printf("The following %s will be removed:\n", kind)
	for _, item := range items {
		fmt.Printf("  %s\n", item)
	}
	fmt.Print("Proceed? [y/N] ")
	scanner := bufio.NewScanner(r)
	scanner.Scan()
	return strings.ToLower(strings.TrimSpace(scanner.Text())) == "y"
}

func volNames(vols []*volume.Volume) []string {
	out := make([]string, len(vols))
	for i, v := range vols {
		out[i] = v.Name
	}
	return out
}

func imgTags(imgs []image.Summary) []string {
	var out []string
	for _, img := range imgs {
		out = append(out, img.RepoTags...)
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/prune/ -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add internal/prune/prune.go internal/prune/prune_test.go
git commit -m "feat: prune command for toolpod-managed volumes and images"
```

---

## Task 5: Wire prune into CLI

**Files:**
- Modify: `cmd/toolpod/cli.go`

- [ ] **Step 1: Replace the prune stub with the real implementation**

Edit `cmd/toolpod/cli.go` — replace `runPrune`:

```go
func runPrune(argv []string) int {
	var opts prune.Options
	fs := pflag.NewFlagSet("prune", pflag.ContinueOnError)
	fs.BoolVar(&opts.Volumes, "volumes", false, "remove toolpod-managed volumes")
	fs.BoolVar(&opts.Images, "images", false, "remove toolpod-tagged images")
	fs.BoolVar(&opts.Force, "force", false, "skip confirmation prompt")
	fs.BoolVarP(&opts.Force, "yes", "y", false, "skip confirmation prompt (short)")
	if err := fs.Parse(argv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	result, err := prune.Run(context.Background(), opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	if len(result.VolumesRemoved) > 0 {
		fmt.Printf("Removed %d volume(s):\n", len(result.VolumesRemoved))
		for _, v := range result.VolumesRemoved {
			fmt.Printf("  %s\n", v)
		}
	}
	if len(result.ImagesRemoved) > 0 {
		fmt.Printf("Removed %d image(s):\n", len(result.ImagesRemoved))
		for _, img := range result.ImagesRemoved {
			fmt.Printf("  %s\n", img)
		}
	}
	if len(result.VolumesRemoved) == 0 && len(result.ImagesRemoved) == 0 {
		fmt.Println("Nothing to prune.")
	}
	return 0
}
```

Add import `"github.com/jgillich/toolpod/internal/prune"`.

- [ ] **Step 2: Build and smoke-test prune**

Run:
```bash
go build -o /tmp/toolpod ./cmd/toolpod
/tmp/toolpod prune --force --volumes
```
Expected: lists and removes any toolpod- volumes (or "Nothing to prune.").

- [ ] **Step 3: Commit**

```bash
git add cmd/toolpod/cli.go
git commit -m "feat: wire prune command into CLI with flags and confirmation"
```

---

## Task 6: End-to-end integration smoke test

**Files:**
- Create: `cmd/toolpod/e2e_runtime_test.go`

- [ ] **Step 1: Write the E2E test**

Create `cmd/toolpod/e2e_runtime_test.go`:

```go
package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestE2EDoctor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if os.Getenv("DOCKER_HOST") == "" {
		t.Skip("DOCKER_HOST not set")
	}
	out, err := runToolpod(t, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	if !strings.Contains(out, "runtime:") {
		t.Errorf("doctor output missing runtime check; got:\n%s", out)
	}
	if !strings.Contains(out, "Summary:") {
		t.Errorf("doctor output missing summary; got:\n%s", out)
	}
}

func TestE2EPruneForce(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	out, err := runToolpod(t, "prune", "--force", "--volumes")
	if err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Nothing to prune") && !strings.Contains(out, "Removed") {
		t.Errorf("prune output should say 'Nothing to prune' or 'Removed'; got:\n%s", out)
	}
}

func TestE2EShellLaunch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if os.Getenv("DOCKER_HOST") == "" {
		t.Skip("DOCKER_HOST not set")
	}
	out, err := runToolpod(t, "shell", "-c", "echo hello-from-toolpod")
	if err != nil {
		t.Fatalf("shell launch: %v\n%s", err, out)
	}
	if !strings.Contains(out, "hello-from-toolpod") {
		t.Errorf("shell launch output missing echo; got:\n%s", out)
	}
}
```

- [ ] **Step 2: Run E2E tests (if Docker available)**

Run: `go test ./cmd/toolpod/ -run TestE2E -v`
Expected: PASS (if Docker running), or SKIP.

- [ ] **Step 3: Commit**

```bash
git add cmd/toolpod/e2e_runtime_test.go
git commit -m "test: end-to-end integration tests for doctor, prune, and shell launch"
```

---

## Task 7: Final test run + commit

- [ ] **Step 1: Run the full test suite (short mode)**

Run: `go test -short ./...`
Expected: all unit tests PASS, integration tests SKIP.

- [ ] **Step 2: Run the full test suite (integration, if Docker available)**

Run: `go test ./...`
Expected: all tests PASS (including integration).

- [ ] **Step 3: Build the final binary**

Run: `go build -o /tmp/toolpod ./cmd/toolpod && /tmp/toolpod --help`
Expected: prints help text with all subcommands.

- [ ] **Step 4: Final commit (if any cleanup needed)**

```bash
git add -A
git commit -m "chore: final test run and cleanup"
```

---

## Self-Review

**Spec coverage (Plan 3 scope — operations):**
- §7.3 doctor (10 checks, pass/fail/warn, summary, not a launch gate) → Task 1 + Task 2 + Task 3
- §7.3 check 1 (runtime reachable) → Task 2 `checkRuntimeReachable`
- §7.3 check 2 (rootless detection) → Task 2 `checkRootless`
- §7.3 check 3 (BuildKit available) → Task 2 `checkBuildKit`
- §7.3 check 4 (mise base image present) → Task 2 `checkMiseBaseImage`
- §7.3 check 5 (mise functional) → deferred (v1 simplification; `checkMiseBaseImage` verifies image presence)
- §7.3 check 6 (shared volumes) → Task 2 `checkVolumes`
- §7.3 check 7 (permissions) → Task 2 `checkPermissions`
- §7.3 check 8 (config validity) → Task 2 `checkConfigValidity`
- §7.3 check 9 (detected project tools) → Task 2 `checkProjectTools`
- §7.3 check 10 (workspace writability) → Task 2 `checkWorkspaceWritable`
- §7.3 output format (colored checklist, summary) → Task 1 (Format, Summary) + Task 3 (CLI output)
- §7.4 prune (--volumes, --images, --all, -y/--force, confirmation, prefix safety) → Task 4 + Task 5
- §11 integration smoke test (shell profile, exit 0, workspace RW) → Task 6

**Full spec coverage across all 3 plans:**
- Plan 1: §1 (purpose), §2 (scope), §3.1 (package layout), §4 (config schema), §4.3 (merge), §4.4 (discovery), §4.5 (example), §7.2 (config subcommands), §9 (built-in profiles), §10 (exit codes)
- Plan 2: §3.2 (Runtime interface), §3.3 (Docker SDK + attach), §3.4 (build system), §5 (workspace/mirroring), §6 (mise integration), §7.1 (launch command), §8 (container lifecycle)
- Plan 3: §7.3 (doctor), §7.4 (prune), §11 (integration tests)

**Placeholder scan:** `checkMiseBaseImage` verifies image presence rather than running `mise version` in a container — marked as v1 simplification. Rootless detection uses shared `runtime.QueryRootless`. All other code is real.

**Type consistency:** `doctor.Check`, `doctor.Result`, `doctor.Options`, `doctor.Status` (Pass/Fail/Warn/Info/Skip) defined in Task 1, used in Tasks 2-3. `prune.Options`, `prune.Result` defined in Task 4, used in Task 5. `config.Catalog`, `config.Resolve`, `config.LoadCatalog` from Plan 1. `runtime.DockerRuntime` from Plan 2 (not directly used; doctor creates its own client for independence).