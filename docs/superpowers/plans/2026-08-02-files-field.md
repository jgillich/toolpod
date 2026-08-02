# Launch-time files (`files:`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `files:` map to profiles/fragments that writes inline-content files into the ephemeral container at launch, owned by the execution user.

**Architecture:** `files:` entries (target path → content/mode) merge like `mounts` and get `~`/`{{ }}` resolution in `ResolveTildes`. In `Runtime.Run`, after `ContainerCreate` and before `ContainerStart`, a pure `tarFiles` function builds a tar (relative paths, execution-user uid/gid, mode) fed to `cli.CopyToContainer` at `/`. Missing parent dirs are auto-created by the daemon's untar at 0755 (verified on Docker + Podman — see Task 5); since they're root-owned, the bootstrap chown set is extended with each file target's parent under `$HOME` so the execution user can write them.

**Tech Stack:** Go 1.25, `archive/tar` (stdlib), `github.com/docker/docker/client`, `gopkg.in/yaml.v3`.

## Global Constraints

- Go 1.25, CGO off in releases.
- No new external dependencies — `archive/tar` is stdlib; the docker client is already used.
- No comments unless the code doesn't make something apparent.
- Follow existing patterns: `mergeMap` for merge, `collectNullKeys` for null-to-delete, `tarDockerfile` (docker_build.go:251) as the tar-building reference, `reservedNames`/`ProfileError` style from validate.go.
- Every task ends with `go test ./...` + `go vet ./...` passing.
- The `files:` feature never touches the host filesystem or the image — only the ephemeral container filesystem.

---

### Task 1: Profile schema — `File` struct, `Files` field, merge, validation

**Files:**
- Modify: `internal/profile/types.go` (add `File` struct + `Files` field on `Profile`)
- Modify: `internal/profile/catalog.go` (add `"files"` to `collectNullKeys`)
- Modify: `internal/profile/merge.go` (merge `Repos`... add `Files` line)
- Modify: `internal/profile/validate.go` (add `validateFiles`, call from `validate()`)
- Test: `internal/profile/merge_test.go`
- Test: `internal/profile/validate_test.go`

**Interfaces:**
- Consumes: existing `mergeMap[V any](parent, child map[string]V, nullKeys map[string]bool)` (merge.go:195), `ProfileError{Path, Line, Message}` (validate.go), `packageNameRe`-style helpers.
- Produces:
  - `type File struct { Content string; Mode uint32 }` with YAML tags `content`, `mode,omitempty`
  - `Profile.Files map[string]File` with YAML tag `files,omitempty`
  - `validateFiles(rc RawProfile) error`

- [ ] **Step 1: Write the failing merge tests**

In `internal/profile/merge_test.go`, append:

```go
func TestResolveFilesMergeAcrossExtends(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "frag1.yaml", "version: 1\nimage: frag1:1\ncommand: [\"x\"]\nfiles:\n  ~/.config/a: {content: \"one\"}\n")
	mustWriteProfile(t, dir, "frag2.yaml", "version: 1\nimage: frag2:1\ncommand: [\"y\"]\nfiles:\n  ~/.config/b: {content: \"two\"}\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: [frag1, frag2]\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Files["~/.config/a"].Content != "one" {
		t.Errorf("files[~/.config/a].Content = %q, want one (from frag1)", cfg.Files["~/.config/a"].Content)
	}
	if cfg.Files["~/.config/b"].Content != "two" {
		t.Errorf("files[~/.config/b].Content = %q, want two (from frag2)", cfg.Files["~/.config/b"].Content)
	}
}

func TestResolveFilesOverrideByTarget(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\nfiles:\n  ~/.config/a: {content: \"one\"}\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\nfiles:\n  ~/.config/a: {content: \"two\", mode: 0600}\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Files["~/.config/a"].Content != "two" {
		t.Errorf("files[~/.config/a].Content = %q, want two (child wins per key)", cfg.Files["~/.config/a"].Content)
	}
	if cfg.Files["~/.config/a"].Mode != 0o600 {
		t.Errorf("files[~/.config/a].Mode = %o, want 600", cfg.Files["~/.config/a"].Mode)
	}
}

func TestResolveFilesNullDelete(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\nfiles:\n  ~/.config/a: {content: \"one\"}\n  ~/.config/b: {content: \"two\"}\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\nfiles:\n  ~/.config/a: null\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, exists := cfg.Files["~/.config/a"]; exists {
		t.Error("files[~/.config/a] should be deleted by null-to-delete")
	}
	if cfg.Files["~/.config/b"].Content != "two" {
		t.Errorf("files[~/.config/b].Content = %q, want two (inherited unchanged)", cfg.Files["~/.config/b"].Content)
	}
}

func TestResolveFilesWholeFieldNull(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\nfiles:\n  ~/.config/a: {content: \"one\"}\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\nfiles: null\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Files) != 0 {
		t.Errorf("whole-field null should drop all inherited files, got %v", cfg.Files)
	}
}
```

- [ ] **Step 2: Write the failing validation tests**

In `internal/profile/validate_test.go`, append:

```go
func TestValidateFiles(t *testing.T) {
	base := Profile{Version: 1, Image: "x", Command: []string{"sh"}}
	valid := []struct {
		name   string
		target string
		f      File
	}{
		{"absolute target", "/etc/tpod.conf", File{Content: "hi"}},
		{"tilde target", "~/.config/foo", File{Content: "hi"}},
		{"explicit mode", "/tmp/x", File{Content: "hi", Mode: 0o600}},
		{"tilde alone", "~", File{Content: "hi"}},
	}
	for _, tt := range valid {
		rc := RawProfile{Profile: base}
		rc.Files = map[string]File{tt.target: tt.f}
		if err := validate(rc); err != nil {
			t.Errorf("validate(files[%q]) = %v, want nil", tt.name, err)
		}
	}
	invalid := []struct {
		name   string
		target string
		f      File
	}{
		{"relative target", "relative/path", File{Content: "hi"}},
		{"tilde-username form", "~user/x", File{Content: "hi"}},
		{"path traversal", "~/../etc/passwd", File{Content: "hi"}},
		{"traversal absolute", "/etc/../../x", File{Content: "hi"}},
		{"mode too large", "~/.config/x", File{Content: "hi", Mode: 0o10000}},
	}
	for _, tt := range invalid {
		rc := RawProfile{Profile: base}
		rc.Files = map[string]File{tt.target: tt.f}
		if err := validate(rc); err == nil {
			t.Errorf("validate(files[%q]) = nil, want error", tt.name)
		}
	}
}

func TestValidateFilesAllowsEmptyContent(t *testing.T) {
	rc := RawProfile{Profile: Profile{Version: 1, Image: "x", Command: []string{"sh"}}}
	rc.Files = map[string]File{"~/.hushlogin": {Content: ""}}
	if err := validate(rc); err != nil {
		t.Errorf("empty content must be a valid empty file, got %v", err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/profile/ -run 'TestResolveFiles|TestValidateFiles'`
Expected: FAIL — `File` undefined, `validateFiles` missing.

- [ ] **Step 4: Add the `File` struct and `Files` field**

In `internal/profile/types.go`, add after the `Repo` struct:

```go
// File is a single file written into the container at launch, keyed by its
// target path. Content is embedded inline and rendered as a {{ }} template;
// Mode is the raw permission bits (default 0644).
type File struct {
	Content string `yaml:"content"`
	Mode    uint32 `yaml:"mode,omitempty"`
}
```

Add to the `Profile` struct, after `Repos`:

```go
	Repos       map[string]Repo        `yaml:"repos,omitempty"`
	Files       map[string]File        `yaml:"files,omitempty"`
```

Run `gofmt -w internal/profile/types.go`.

- [ ] **Step 5: Add `"files"` to `collectNullKeys`**

In `internal/profile/catalog.go`, in the `nulls` map literal (starts at the line `"mounts":      {},`), add:

```go
		"repos":       {},
		"files":       {},
```

- [ ] **Step 6: Add the merge line**

In `internal/profile/merge.go`, in `MergeProfiles`, immediately after the `Repos` merge line (`out.Repos = mergeMap(parent.Repos, child.Repos, child.NullKeys["repos"])`), add:

```go
	out.Files = mergeMap(parent.Files, child.Files, child.NullKeys["files"])
```

- [ ] **Step 7: Add `validateFiles`**

In `internal/profile/validate.go`:
1. In `validate()`, after the `validateRepos(rc)` block, add:

```go
	if err := validateFiles(rc); err != nil {
		return err
	}
```

2. Append the function (after `validateRepos`):

```go
// validateFiles checks each file target: absolute or ~-prefixed, and free of
// ".." segments. The tar is rooted at "/", so a ".." target could traverse
// outside the intended location; "~" expands later to a clean absolute
// runtimeHome (no ".."), so rejecting raw ".." segments also guarantees the
// expanded path is clean.
func validateFiles(rc RawProfile) error {
	for target, f := range rc.Files {
		if target != "~" && !strings.HasPrefix(target, "~/") && !strings.HasPrefix(target, "/") {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("files: target %q must be an absolute path or ~-prefixed", target)}
		}
		for _, seg := range strings.Split(target, "/") {
			if seg == ".." {
				return ProfileError{Path: rc.Path, Message: fmt.Sprintf("files: target %q must not contain '..' segments", target)}
			}
		}
		if f.Mode > 0o7777 {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("files: target %q: mode %o out of range (want 0-07777)", target, f.Mode)}
		}
	}
	return nil
}
```

`filepath.Clean` is applied at resolve time (Task 2) to normalize cosmetic
redundancy (`/a//b` → `/a/b`, `/a/./b` → `/a/b`) before the entry path is
emitted; a `..`-free target can only clean to a safe path under the container
root.

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./internal/profile/`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/profile/types.go internal/profile/catalog.go internal/profile/merge.go internal/profile/validate.go internal/profile/merge_test.go internal/profile/validate_test.go
git commit -m "feat(profile): add files: field with merge and validation"
```

---

### Task 2: Tilde + template resolution for file targets and content

**Files:**
- Modify: `internal/profile/paths.go` (extend `ResolveTildes`)
- Modify: `internal/profile/paths_test.go` (append new tests; already exists)

**Interfaces:**
- Consumes: `File{Content string, Mode uint32}`, `ResolveTildes(cfg Profile, mode, hostHome, runtimeHome string, ports map[string]string) (Profile, error)`, `expandTarget(path, runtimeHome string, data tmplData) (string, error)`, `renderTemplate(s string, data tmplData) (string, error)`.
- Produces: `ResolveTildes` also expands `Files` keys (`~` → runtimeHome, `{{ }}`) and renders each `File.Content` through `renderTemplate`. An empty rendered target is a config error (mirrors the `caches` handling).

- [ ] **Step 1: Write the failing test**

Append to `internal/profile/paths_test.go`:

```go
package profile

import "testing"

func TestResolveFilesTildeAndTemplate(t *testing.T) {
	cfg := Profile{
		Files: map[string]File{
			"~/.config/foo": {
				Content: "port={{ index .Ports \"8080\" }} uid={{ uid }}",
			},
			"~/.config/bar": {Content: "plain"},
		},
	}
	out, err := ResolveTildes(cfg, "A", "/home/me", "/home/me", map[string]string{"8080": "5173"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Files["/home/me/.config/foo"]; !ok {
		t.Fatalf("~ target should expand to runtimeHome, got %v", out.Files)
	}
	got := out.Files["/home/me/.config/foo"].Content
	want := "port=5173 uid=" + currentUID()
	if got != want {
		t.Errorf("content = %q, want %q (template rendered)", got, want)
	}
	if out.Files["/home/me/.config/bar"].Content != "plain" {
		t.Errorf("plain content must pass through unchanged, got %q", out.Files["/home/me/.config/bar"].Content)
	}
}

func TestResolveFilesEmptyRenderedTargetRejected(t *testing.T) {
	cfg := Profile{
		Files: map[string]File{
			"{{ .Env.MISSING_VAR }}": {Content: "x"},
		},
	}
	_, err := ResolveTildes(cfg, "A", "/home/me", "/home/me", nil)
	if err == nil {
		t.Fatal("expected error for file target that renders empty, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/profile/ -run TestResolveFiles -v`
Expected: FAIL — files not handled in `ResolveTildes`.

- [ ] **Step 3: Extend `ResolveTildes`**

In `internal/profile/paths.go`, in `ResolveTildes`, after the `Caches` block (ends `out.Caches = expanded`), add:

```go
	if out.Files != nil {
		expanded := make(map[string]File, len(out.Files))
		for target, f := range out.Files {
			newTarget, err := expandTarget(target, runtimeHome, data)
			if err != nil {
				return out, err
			}
			if newTarget == "" {
				return out, fmt.Errorf("file %q resolved to an empty target after template expansion (is the host variable set?)", target)
			}
			f.Content, err = renderTemplate(f.Content, data)
			if err != nil {
				return out, fmt.Errorf("file %s: %w", target, err)
			}
			expanded[newTarget] = f
		}
		out.Files = expanded
	}
```

Note the `f.Content, err =` — Go permits reassigning the field then reusing `err`; this is the same shape as the `Caches` block's `expanded[name], err = ...`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/profile/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/profile/paths.go internal/profile/paths_test.go
git commit -m "feat(profile): resolve ~ and templates in files: targets and content"
```

---

### Task 3: Runtime spec plumbing — `FileSpec`, `Spec.Files`, `buildSpec` mapping

**Files:**
- Modify: `internal/runtime/runtime.go` (add `FileSpec` struct + `Files` field)
- Modify: `pkg/tpod/types.go` (add `FileSpec` alias)
- Modify: `pkg/tpod/spec.go` (map `cfg.Files` → `spec.Files`, default mode, sort)
- Test: `pkg/tpod/spec_test.go`

**Interfaces:**
- Consumes: `profile.File{Content string, Mode uint32}`; `buildSpec(opts LaunchOpts, cfg profile.Profile, mode, hostHome, runtimeHome string) (Spec, error)`.
- Produces:
  - `type FileSpec struct { Target, Content string; Mode uint32 }`
  - `Spec.Files []FileSpec`
  - `pkg/tpod.FileSpec = runtime.FileSpec` (alias)
  - `buildSpec` sets `Spec.Files` — targets already `~`-expanded and content already rendered by `ResolveTildes`.

- [ ] **Step 1: Write the failing test**

Append to `pkg/tpod/spec_test.go` (create if absent):

```go
func TestBuildSpecMapsFiles(t *testing.T) {
	cfg := profile.Profile{
		Version: 1,
		Image:   "img:1",
		Command: []string{"sh"},
		Files: map[string]profile.File{
			"/root/.config/foo": {Content: "hello", Mode: 0o600},
		},
	}
	spec, err := buildSpec(LaunchOpts{ProfileName: "p"}, cfg, "B", "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Files) != 1 {
		t.Fatalf("spec.Files = %v, want 1 entry", spec.Files)
	}
	f := spec.Files[0]
	if f.Target != "/root/.config/foo" || f.Content != "hello" || f.Mode != 0o600 {
		t.Errorf("spec.Files[0] = %+v, want {/root/.config/foo hello 384}", f)
	}
}

func TestBuildSpecFilesDefaultMode(t *testing.T) {
	cfg := profile.Profile{
		Version: 1,
		Image:   "img:1",
		Command: []string{"sh"},
		Files:   map[string]profile.File{"/root/.config/foo": {Content: "x"}},
	}
	spec, err := buildSpec(LaunchOpts{ProfileName: "p"}, cfg, "B", "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Files[0].Mode != 0o644 {
		t.Errorf("default mode = %o, want 644", spec.Files[0].Mode)
	}
}
```

Check the imports at the top of `spec_test.go` — it must already import `github.com/jgillich/tpod/internal/profile` (it does in the existing suite; add if not).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tpod/ -run TestBuildSpecFiles -v`
Expected: FAIL — `FileSpec` undefined.

- [ ] **Step 3: Add `FileSpec` to the runtime spec**

In `internal/runtime/runtime.go`, after the `Repo` struct, add:

```go
// FileSpec is a single file written into the container at launch.
type FileSpec struct {
	Target  string
	Content string
	Mode    uint32
}
```

Add to the `Spec` struct, after `Repos`:

```go
	Repos       map[string]Repo
	Files       []FileSpec
```

- [ ] **Step 4: Add the `FileSpec` alias**

In `pkg/tpod/types.go`, in the type-alias block, add:

```go
	Repo          = runtime.Repo
	FileSpec      = runtime.FileSpec
```

- [ ] **Step 5: Map `cfg.Files` in `buildSpec`**

In `pkg/tpod/spec.go`, after the `repos := make(map[string]Repo, ...)` block (which ends with the `for name, r := range cfg.Repos` loop), add:

```go
	files := make([]FileSpec, 0, len(cfg.Files))
	for target, f := range cfg.Files {
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		files = append(files, FileSpec{Target: target, Content: f.Content, Mode: mode})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Target < files[j].Target })
```

Add `Files: files,` to the returned `Spec` literal (next to `Repos: repos,`).

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./pkg/tpod/ ./internal/runtime/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/runtime/runtime.go pkg/tpod/types.go pkg/tpod/spec.go pkg/tpod/spec_test.go
git commit -m "feat(runtime): plumb files: through Spec and buildSpec"
```

---

### Task 4: `tarFiles` pure function

**Files:**
- Modify: `internal/runtime/docker_run.go` (add `tarFiles`)
- Test: `internal/runtime/docker_run_test.go` (create if absent; otherwise `docker_build_test.go`)

**Interfaces:**
- Consumes: `FileSpec{Target, Content string, Mode uint32}`, `archive/tar`.
- Produces: `tarFiles(files []FileSpec, uid, gid int) ([]byte, error)` — an in-memory tar with one entry per file, relative paths (leading `/` stripped), mode/uid/gid in headers.

- [ ] **Step 1: Write the failing test**

Create `internal/runtime/docker_run_test.go`:

```go
package runtime

import (
	"archive/tar"
	"bytes"
	"io"
	"testing"
)

func TestTarFiles(t *testing.T) {
	files := []FileSpec{
		{Target: "/root/.config/foo", Content: "hello\n", Mode: 0o600},
		{Target: "/etc/tpod.conf", Content: "x", Mode: 0o644},
	}
	data, err := tarFiles(files, 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(data))
	var entries []*tar.Header
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, hdr)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 tar entries, got %d", len(entries))
	}
	if entries[0].Name != "root/.config/foo" {
		t.Errorf("entry name = %q, want root/.config/foo (relative, no leading slash)", entries[0].Name)
	}
	if entries[0].Uid != 1000 || entries[0].Gid != 1000 {
		t.Errorf("entry uid/gid = %d/%d, want 1000/1000", entries[0].Uid, entries[0].Gid)
	}
	if entries[0].Mode != 0o600 {
		t.Errorf("entry mode = %o, want 600", entries[0].Mode)
	}
	if entries[0].Typeflag != tar.TypeReg {
		t.Errorf("entry typeflag = %d, want TypeReg", entries[0].Typeflag)
	}
	if entries[0].Format != tar.FormatPAX {
		t.Errorf("entry format = %d, want PAX", entries[0].Format)
	}
}

func TestTarFilesEmpty(t *testing.T) {
	data, err := tarFiles(nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 1024 { // tar terminator is two zero blocks (1024 bytes)
		t.Errorf("empty tar should be the 1024-byte terminator, got %d bytes", len(data))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runtime/ -run TestTarFiles -v`
Expected: FAIL — `tarFiles` undefined.

- [ ] **Step 3: Implement `tarFiles`**

In `internal/runtime/docker_run.go`, add near `buildMounts` (top of file, after the imports):

```go
// tarFiles renders the container-file tar stream: one regular file entry per
// target with a relative path (CopyToContainer untars at "/"), the file's
// mode, and the execution user's uid/gid. PAX format + explicit TypeReg avoid
// relying on tar defaults across engines.
func tarFiles(files []FileSpec, uid, gid int) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		rel := strings.TrimPrefix(f.Target, "/")
		if err := tw.WriteHeader(&tar.Header{
			Name:     rel,
			Typeflag: tar.TypeReg,
			Mode:     int64(f.Mode),
			Uid:      uid,
			Gid:      gid,
			Size:     int64(len(f.Content)),
			Format:   tar.FormatPAX,
		}); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(f.Content)); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
```

Add `"archive/tar"`, `"bytes"`, and `"github.com/docker/docker/client"` to the imports of `docker_run.go` (the first two are needed by `tarFiles`/`writeContainerFiles`; the client package is referenced by the `writeContainerFiles` signature). `container` is already imported (used by `container.Config`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/runtime/ -run TestTarFiles`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/docker_run.go internal/runtime/docker_run_test.go
git commit -m "feat(runtime): add tarFiles for launch-time container files"
```

---

### Task 5: Write files at launch + extend bootstrap chown + integration test

**Files:**
- Modify: `internal/runtime/docker_run.go` (add `writeContainerFiles`, `fileTargets`; call in `Run` between create and start; extend `writable` set)
- Test: `internal/runtime/docker_test.go` (integration)

**Interfaces:**
- Consumes: `tarFiles(files []FileSpec, uid, gid int) ([]byte, error)` (Task 4), `homeParents(home string, targets []string) []string` (docker_run.go:263), `spec.Files []FileSpec`.
- Produces: `writeContainerFiles(ctx context.Context, cli *client.Client, containerID string, files []FileSpec, uid, gid int) error`; `fileTargets(spec Spec) []string`. `Run` writes files after `ContainerCreate` and before `ContainerAttach`/`ContainerStart`, and adds each file target's `$HOME` parent dir to the bootstrap chown set.

- [ ] **Step 1: Write the failing integration test**

Append to `internal/runtime/docker_test.go` (after `TestIntegrationReposEnablesMiseRepo`):

```go
func TestIntegrationFilesWrittenIntoContainer(t *testing.T) {
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
	// Target under /root/.config with a parent dir that does not exist in the
	// base image — exercises implied-directory creation. The target uses the
	// resolved path (/root = Mode B RuntimeHome), since a Spec's Files targets
	// are post-ResolveTildes.
	spec := Spec{
		ProfileName: "test-files",
		Image:       integrationImage,
		Files: []FileSpec{
			{Target: "/root/.config/tpod-test/deep.conf", Content: "hello-files\n", Mode: 0o644},
		},
		// Existence + content + permissions are all exercised end-to-end.
		Command:     []string{"sh", "-c", `test "$(cat /root/.config/tpod-test/deep.conf)" = "hello-files" && test "$(stat -c %a /root/.config/tpod-test/deep.conf)" = "644"`},
		Workspace:   WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: "B"},
		RuntimeHome: "/root",
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
		t.Errorf("cat-check exit code = %d, want 0", code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runtime/ -run TestIntegrationFilesWrittenIntoContainer -v`
Expected: FAIL — the file is never written, so `cat` fails and exit code is non-zero.

- [ ] **Step 3: Implement `writeContainerFiles` and `fileTargets`**

In `internal/runtime/docker_run.go`, add after `tarFiles`:

```go
// writeContainerFiles untars the profile's files into the created-but-not-
// yet-started container, so they exist before the command runs and are owned
// by the execution user.
func writeContainerFiles(ctx context.Context, cli *client.Client, containerID string, files []FileSpec, uid, gid int) error {
	data, err := tarFiles(files, uid, gid)
	if err != nil {
		return err
	}
	return cli.CopyToContainer(ctx, containerID, "/", bytes.NewReader(data), container.CopyToContainerOptions{})
}

// fileTargets lists every container file target for a spec.
func fileTargets(spec Spec) []string {
	targets := make([]string, 0, len(spec.Files))
	for _, f := range spec.Files {
		targets = append(targets, f.Target)
	}
	return targets
}
```

- [ ] **Step 4: Call it in `Run` and extend the chown set**

In `internal/runtime/docker_run.go`, in `Run`:
1. Extend the writable set — after the existing line
   `writable = append(writable, homeParents(runtimeHome, mountTargets(spec))...)`, add:

```go
	writable = append(writable, homeParents(runtimeHome, fileTargets(spec))...)
```

2. After the `defer` block that removes the container on exit (the `defer func() { ... ContainerRemove ... }()` that follows `ContainerCreate`), insert:

```go
	if len(spec.Files) > 0 {
		if err := writeContainerFiles(ctx, d.cli, resp.ID, spec.Files, hostUID, hostGID); err != nil {
			return 3, fmt.Errorf("write profile files: %w", err)
		}
	}
```

This is between `ContainerCreate` and `ContainerAttach` — the container exists (so the tar can be untarred into it) but hasn't started. The deferred removal still covers a failure here.

> **Why the chown is needed (verified):** the daemon's untar creates implied
> parents at 0755 owned by **root** (confirmed experimentally on this
> engine). The file itself gets the tar header's uid/gid (execution user). So
> under `$HOME`, a root-owned 0755 parent would let the user read but not
> write the file — the bootstrap chown fixes that. Without it, the
> integration test's `cat` would still pass (read) but a later write would
> fail; the chown keeps the "owned by execution user" guarantee complete.
> Do **not** emit explicit dir entries instead — tested and rejected: an
> explicit `root/` dir entry clobbers `/root`'s ownership.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/runtime/ -run TestIntegrationFilesWrittenIntoContainer -count=1 -v`
Expected: PASS (the file exists, is owned by the execution user, mode 644, and `cat` matches).

- [ ] **Step 6: Run the full suite + vet**

Run: `go test ./... && go vet ./...`
Expected: all green.

- [ ] **Step 7: Commit**

```bash
git add internal/runtime/docker_run.go internal/runtime/docker_test.go
git commit -m "feat(runtime): write files: into the container at launch"
```

---

### Task 6: Docs

**Files:**
- Modify: `README.md` (schema row + merge semantics + a `files:` example section)
- Modify: `AGENTS.md` (Conventions note)
- Modify: `docs/superpowers/specs/2026-08-02-files-field-design.md` (mark as implemented: no change needed unless a decision differs)

**Interfaces:**
- Consumes: the final field name/semantics from Tasks 1-5.

- [ ] **Step 1: Add the schema row to README**

In `README.md`, in the profile schema table, after the `repos` row, add:

```
| `files` | map | Files written into the container at launch, keyed by target path (absolute or `~`, which resolves to the in-container home). Each entry: `content` (inline, `{{ }}` templates resolved), `mode` (octal, default `0644`). Files are owned by the execution user and live only for the launch. |
```

- [ ] **Step 2: Add to merge semantics**

In `README.md`'s "Merge semantics" section, update the **Maps** bullet to include `files`:

```
- **Maps** (`mounts`, `environment`, `tools`, `caches`, `labels`, `dbus`, `repos`, `files`): merged key-by-key. Set a key to `null` to delete an inherited entry.
```

- [ ] **Step 3: Add a `files:` example section**

In `README.md`, near the "System packages (`packages:`) and derived images" section, add a short section:

```markdown
### Writing files at launch (`files:`)

`files:` writes inline-content files into the ephemeral container before the
profile command runs — owned by the execution user, gone when the container
exits. Useful for a profile's own runtime config that doesn't belong on the
host.

```yaml
files:
  ~/.config/mytool/config.toml:
    content: |
      [settings]
      task_output = "parses"
    mode: 0600
```

Targets are absolute or `~`-prefixed; `~` resolves to the in-container home.
Content is rendered as a `{{ }}` template (`.Env`, `uid`, `.Ports`), so a
config can embed an auto-allocated host port.
```

- [ ] **Step 4: Update AGENTS.md**

In `AGENTS.md`, in the Conventions section, after the `repos:`-related bullet, add:

```
- **`files:`:** profiles/fragments write inline-content files into the
  ephemeral container at launch (between ContainerCreate and ContainerStart
  via CopyToContainer with a tar built by `internal/runtime/docker_run.go`'s
  `tarFiles`). Content supports `{{ }}` templates; targets are absolute or
  `~`-prefixed and must not contain `..`. Files are owned by the execution
  user; missing parent dirs are created automatically and the bootstrap chown
  is extended with `$HOME` parents of file targets so the execution user can
  write them.
```

Note: this wording avoids asserting which layer creates the missing parents
(the daemon's untar does today; whether it's toolpod or the engine isn't a
contract).

- [ ] **Step 5: Verify + commit**

Run: `go test ./... && go vet ./...`

```bash
git add README.md AGENTS.md
git commit -m "docs: document files: field"
```

---

## Self-review notes

- **Spec coverage:** Goal (Task 1-5), rationale (runtime ownership/missing-parents baked into Task 5), schema (Task 1), merge/validation/templates (Tasks 1-2), runtime (Tasks 4-5), plumbing (Task 3), tests (all), out-of-scope (nothing implemented that crosses it).
- **`..` rejection** lives in `validateFiles` (Task 1) on the raw target — since `~` expands to a clean runtimeHome, post-expansion is guaranteed clean by construction.
- **Mode default** applied in `buildSpec` (Task 3) so `tarFiles` receives a concrete value; the pure function stays dumb.
- **Integration test target** uses `/root/...` directly (Mode B `RuntimeHome`) rather than `~`, because `Spec.Files` is post-resolution.
