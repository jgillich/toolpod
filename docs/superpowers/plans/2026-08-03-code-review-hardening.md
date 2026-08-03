# Code-Review Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Address every finding in `CODE_REVIEW.md` (C-01, H-01..H-08, M-01..M-08, L-01..L-03, cross-cutting) with ownership labels, verified downloads, byte-safe config writing, validation, concurrency locks, and visibility improvements — or justify why a finding's recommended fix is not adopted.

**Architecture:** Changes are layered by responsibility. `internal/runtime` owns engine-facing identity (ownership labels, resource limits, bounded HTTP reads, subpath race). `internal/profile` owns input validation (image refs, env keys, tool names/versions, network, profile names) and a richer `tools` schema. `internal/mise` becomes shell-injection-free (base64 TOML transport + sha256-verified AppImage backend). `internal/prune`/`internal/doctor` stop trusting names and stop doing destructive fixed-name probes. `cmd/tpd` centralizes exit-code mapping. Catalog advisories, a `gui`/`gui-runtime` split, docs, and pinned CI complete the work. Per the maintainer's scope decisions, no port reservation is held and no run/prune lock is added.

**Tech Stack:** Go 1.25+, Docker Engine API via `github.com/docker/docker`, `github.com/distribution/reference`, Lua (mise appimage backend), YAML.

## Global Constraints

- All commands: `go test ./...`, `go vet ./...`, `make install` must pass at the end of each task.
- Commit per task in conventional-commit format (`fix:`, `feat:`, `docs:`, `ci:`), staging individual files (never `git add -A`).
- The environment is an isolated worktree; implement in a worktree created via `superpowers:using-git-worktrees`.
- Do not use `force: true` for destructive removal unless the target was independently verified (label + re-check).
- Catalog stays embedded; only YAML under `internal/catalog/` changes for built-in profile updates.
- No comments unless the code doesn't make the reason apparent.
- Do not restructure packages beyond the seams listed per task.

## Scope decisions (findings where a recommendation is not adopted as written)

These items are listed up front because the reviewer asked for explicit justification when a recommendation is declined. Each task below marks `agreed` or `partial` and implements only the agreed part.

1. **H-05 (partial) — "resolve and record exact package versions ... in the cache key" is declined.** The derived tag is deliberately content-addressed from *requested* package names + repo descriptors (`DerivedTag`), and AGENTS.md records that "the derived-tag hash stays name-based, so `prune` needs no network." Including *resolved* apt versions in the hash would make the tag depend on network state at build time, break the offline prune guarantee, and make the tag non-deterministic across rebuilds. Implemented instead: a `--pull` refresh policy for mutable base tags and a build-provenance label on derived images. A follow-up lockfile for mise tools is documented, not implemented (mise's own `--lockfile` flow is out of scope).

2. **M-01 (declined) — neither recommendation is implemented.** Maintainer decision: ports are OS-allocated ephemeral ports, so a collision requires another process to bind that exact port in the window between release and the engine's bind at container start — unlikely on a single-user rootless host. Holding a reservation until launch would only narrow that release→bind window (the engine binds the host port at `ContainerStart` regardless of how long `mise install` then takes inside the container), and the added `PortAllocator`-signature/closer machinery is not worth it. The residual race is accepted and documented in the security-model doc.

3. **H-08 (partial) — embedding a pinned catalog digest is declined.** The extrepo index changes as repos are added/removed; pinning it would break new-repo adoption and require constant shipped updates. TLS to `pages.debian.net` remains the trust anchor. Implemented instead: bounded response reads, a redirect cap, and consistent status checks (also covers M-02). The residual self-referential key-checksum model is documented in the security-model doc.

4. **L-03 (partial) — "retry boundedly" is declined.** Cleanup already runs once inside a 10s bounded background context (`docker_run.go:127-131`, `docker_prepare.go:111-115`); retrying inside that window adds latency without meaningful success (the engine state that caused the failure rarely clears in 10s). Implemented instead: cleanup errors are surfaced to stderr, and doctor gains leaked-container and stale-bus-socket checks.

5. **Cross-cutting — the `setpriv`-absent root fallback is kept (not fail-closed).** Failing closed would break documented Mode B (rootful Docker runs as root by design, `AGENTS.md` "Runtime notes"). The existing in-container stderr warning is retained and the identity trade-off is documented in the security-model doc.

6. **M-05 (partial) — the host-level flock is declined.** Maintainer decision: rootless mode is single-user; a user does not meaningfully run `tpd prune` concurrently with `tpd launch` on their own engine, so serializing them with a lock file adds machinery without real benefit. The other half of the recommendation is kept: prune never removes a resource referenced by a currently-running container, and it re-checks liveness immediately before deletion.

7. **H-04 (revised) — AppImages are NOT checked-in pins; they stay `latest`.** The review recommended "require a pinned version and verified checksum." A pinned catalog would force maintainers to bump versions/digests on every upstream release — the burden this project deliberately avoids. Instead (Task 5): built-ins keep `latest`; the backend resolves `latest` to a concrete release at install time, verifies the downloaded artifact against a *published* digest (GitHub's per-asset `digest`, a checksum sidecar, or an explicit profile `sha256`), fails closed when none is available, and caches the resolved (tag, asset, digest) next to the install so mise's install-path check keeps later launches stable. Trade-off: the upstream-API digest is self-referential (the same response supplies the URL and its hash), so this protects against download/CDN tampering, not a compromised upstream/API; and fresh machines may initially resolve different releases than older machines. Both are documented in the security-model doc.

---

### Task 1: Ownership labels on tpd-managed resources + prune requires them (C-01)

**Files:**
- Create: `internal/runtime/labels.go`
- Modify: `internal/mise/volume.go` (delete; move `EnsureVolume` into `internal/runtime/docker_prepare.go`), `internal/runtime/docker_build.go`, `internal/prune/prune.go`, `pkg/tpd/spec.go`
- Test: `internal/prune/prune_test.go`, `internal/runtime/docker_build_test.go`

**Interfaces:**
- Produces: `runtime.OwnershipLabel = "tpd.managed"` (const); every volume and derived image created by tpd carries `map[string]string{runtime.OwnershipLabel: "true"}`.
- Consumes: `EnsureVolume` (signature unchanged, now defined in `internal/runtime` and labels internally); `buildDerivedImage` (unchanged signature, now labels the built image via `ImageBuildOptions.Labels`).
- Design note: `EnsureVolume`'s only caller is `docker_prepare.go`, so it moves from `internal/mise` to `internal/runtime` — this keeps the label constant in `runtime` without creating a `mise` → `runtime` import cycle (runtime already imports mise).

- [ ] **Step 1: Write the failing tests.** In `prune_test.go`, extend `fakeClient.VolumeList`/`ImageList` so volumes/images can carry labels, and assert `run()` removes a *labeled* `tpd-*` volume but does NOT remove an *unlabeled* `tpd-important-data` volume (currently both are removed). In `docker_build_test.go`, assert `synthesizeDockerfile`/build options carry the label where testable.

- [ ] **Step 2: Run tests to verify they fail.**

Run: `go test ./internal/prune/ ./internal/runtime/ -run 'TestPrune|TestBuild'`
Expected: FAIL — unlabeled volume is still removed.

- [ ] **Step 3: Implement.**

`internal/runtime/labels.go`:
```go
package runtime

const OwnershipLabel = "tpd.managed"

func OwnershipLabels() map[string]string {
	return map[string]string{OwnershipLabel: "true"}
}
```

Move `EnsureVolume` into `internal/runtime` (delete `internal/mise/volume.go`) and add the label to `VolumeCreate` in the new location:
```go
func EnsureVolume(ctx context.Context, cli *client.Client, name string) error {
	_, err := cli.VolumeCreate(ctx, volume.CreateOptions{
		Name:   name,
		Labels: OwnershipLabels(),
	})
	return err
}
```
(It must not stay in `mise`: `runtime` imports `mise` in `docker_prepare.go`/`docker_run.go`, so `mise` cannot import `runtime` for the label constant. Its only caller is `docker_prepare.go`, so the move is safe.)

`internal/runtime/docker_build.go` — in `buildDerivedImage`, add `Labels: OwnershipLabels()` to the `ImageBuildOptions` literal.

Containers get the label too, so leak detection (Task 13) and prune's running-container protection (Task 10) can filter by label instead of name prefix. `pkg/tpd/spec.go` — in `buildSpec`, next to the existing `labels["profile"] = opts.ProfileName`, add:
```go
labels[runtime.OwnershipLabel] = "true"
```
(the `labels` map already flows into `container.Config.Labels` in `docker_run.go`).

`internal/prune/prune.go`:
- `listTpdVolumes`: require `isTpdVolume(v.Name) && v.Labels[OwnershipLabel] == "true"`.
- `listTpdImages`: require `img.Labels[OwnershipLabel] == "true"` in addition to `DerivedRef`.
- Report unlabeled `tpd-*` resources (that were skipped) to stderr as `warning: skipping unlabeled tpd-* resource <name> (not tpd-owned)` so legacy cruft is discoverable.

- [ ] **Step 4: Verify tests pass.**

Run: `go test ./internal/prune/ ./internal/runtime/ ./internal/mise/`
Expected: PASS.

- [ ] **Step 5: Vet and commit.**

Run: `go vet ./... && go test ./...`
Commit: `fix(prune): only remove volumes/images carrying the tpd ownership label`

---

### Task 2: Doctor probes become unique, exclusive, and non-destructive (H-01, L-02)

**Files:**
- Modify: `internal/doctor/checks.go`
- Test: `internal/doctor/checks_test.go`

**Interfaces:**
- Consumes: `runtime.OwnershipLabel`.
- Produces: helper `randomSuffix(n int) string` in `internal/doctor` (crypto/rand hex).

- [ ] **Step 1: Write the failing tests.** In `checks_test.go` (add if absent): (a) volume probe creates a *uniquely* named volume and removes only that name; (b) a pre-existing `tpd-perm-test` volume is left untouched by `checkPermissions`; (c) the workspace probe uses an exclusively-created unique file; (d) the container-creation probe uses an existing local image or reports `Info` when none exists (no `alpine:latest` reference anywhere).

- [ ] **Step 2: Run tests to verify they fail.**

Run: `go test ./internal/doctor/`
Expected: FAIL — tests reference the old fixed-name/fixed-path behavior.

- [ ] **Step 3: Implement.**

`checkPermissions` rewrite — probes use a dedicated `tpd-diag-` namespace so an uncleaned probe can never be confused with a managed or legacy `tpd-*` resource, and failed cleanup is surfaced:
```go
func checkPermissions(ctx context.Context, rt *dockerRT) Check {
	probe := "tpd-diag-" + randomSuffix(8)
	if _, err := rt.cli.VolumeCreate(ctx, volume.CreateOptions{Name: probe}); err != nil {
		return Check{Name: "permissions", Status: Fail, Message: "cannot create volumes: " + err.Error()}
	}
	if err := rt.cli.VolumeRemove(ctx, probe, true); err != nil {
		return Check{Name: "permissions", Status: Warn, Message: "created probe volume but could not remove " + probe + " (remove manually): " + err.Error()}
	}

	images, err := rt.cli.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return Check{Name: "permissions", Status: Fail, Message: "cannot list images: " + err.Error()}
	}
	ref := firstRunableImage(images) // first image with a non-empty ID
	if ref == "" {
		return Check{Name: "permissions", Status: Info, Message: "volume creation OK; container-creation probe skipped (no local image)"}
	}
	resp, err := rt.cli.ContainerCreate(ctx, &container.Config{Image: ref, Cmd: []string{"true"}}, nil, nil, nil, "")
	if err != nil {
		return Check{Name: "permissions", Status: Fail, Message: "cannot create containers: " + err.Error()}
	}
	if err := rt.cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true}); err != nil {
		return Check{Name: "permissions", Status: Warn, Message: "created container but could not remove probe: " + err.Error()}
	}
	return Check{Name: "permissions", Status: Pass, Message: "can create containers and volumes"}
}
```
`firstRunableImage` iterates `images` and returns `img.ID` for the first entry whose `ID != ""` (IDs work as a create-time image reference without any tag). Add `"crypto/rand"`/`"encoding/hex"` imports and `randomSuffix`.

`checkWorkspaceWritable` rewrite:
```go
func checkWorkspaceWritable(ctx context.Context, workspace string) Check {
	probe := filepath.Join(workspace, ".tpd-write-test-"+randomSuffix(4))
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW|os.O_WRONLY, 0o644)
	if err != nil {
		return Check{Name: "workspace", Status: Fail, Message: workspace + " is not writable: " + err.Error()}
	}
	f.Close()
	os.Remove(probe)
	return Check{Name: "workspace", Status: Pass, Message: workspace + " is writable"}
}
```
(`unix` is `golang.org/x/sys/unix`, already a module dependency. `O_EXCL` + a random name makes clobbering a pre-existing file impossible; `O_NOFOLLOW` rejects symlinks.)

Add a new check `checkUnlabeledLegacyResources` to `runChecks`: list volumes/images matching the `tpd-`/`tpd/packages` name pattern but missing `runtime.OwnershipLabel`, report as `Info` ("may not be tpd-owned; not pruned automatically") so users can clean up pre-label cruft. Exclude the `tpd-diag-` namespace so a stale diagnostic probe is not reported as legacy tpd cruft.

- [ ] **Step 4: Verify tests pass.**

Run: `go test ./internal/doctor/`
Expected: PASS.

- [ ] **Step 5: Vet and commit.**

Run: `go vet ./... && go test ./...`
Commit: `fix(doctor): use unique, exclusive probe resources instead of fixed names`

---

### Task 3: Rich `tools` schema + validation (H-04 part 1, H-07 part 1, M-07 part 1)

**Files:**
- Modify: `internal/profile/types.go`, `internal/profile/merge.go`, `internal/profile/validate.go`, `internal/mise/types.go` (new), `internal/mise/mise.go`, `internal/runtime/runtime.go`, `pkg/tpd/spec.go`, `pkg/tpd/dryrun.go`, `pkg/tpd/launch.go`
- Test: `internal/profile/types_test.go`, `internal/profile/validate_test.go`, `internal/profile/merge_test.go`, `internal/mise/mise_test.go`, `pkg/tpd/spec_test.go`

**Interfaces:**
- Produces:
  - `profile.Tool{Version string; SHA256 string; SHA256ByArch map[string]string}` with `UnmarshalYAML` (scalar → version; map → `{version, sha256}` where `sha256` is a scalar digest or a per-arch map) and `MarshalYAML`.
  - `profile.Profile.Tools map[string]Tool` (was `map[string]string`).
  - `mise.Tool{Version string; SHA256 string; SHA256ByArch map[string]string}` and `runtime.Spec.Tools map[string]mise.Tool` — replaces the earlier `Spec.ToolSHA256` parallel-map idea; the richer type is required because AppImage digests are per-architecture (Task 5).
- Consumes: `mergeStringMap` replaced for tools by `mergeMap` (already generic). This task ONLY changes the `mise` function signatures (`map[string]string` → `map[string]Tool`) and adds generic tool-name/control-character validation; the appimage checksum-format rule is added in Task 5 so every intermediate `go test ./...` stays green.

- [ ] **Step 1: Write the failing tests.**

`types_test.go`: `Tool` decodes from `"latest"` (scalar), from `{version: v1, sha256: <64 hex>}` (scalar digest), and from `{version: v1, sha256: {amd64: <hex>, aarch64: <hex>}}` (per-arch); marshals back to scalar when no checksum and to a map when one is present.

`validate_test.go`: (a) tool names with control characters/newlines rejected; (b) valid names pass; (c) `containsControl` on versions rejects newlines. (Appimage checksum-format rules are tested in Task 5.)

`merge_test.go`: `tools` merge still does key-by-key child-wins and null-to-delete (`tools: {node: ~}` drops an inherited node).

`mise_test.go` + `spec_test.go`: existing `map[string]string` literals for tools become `map[string]mise.Tool{...}`; `buildSpec` maps `cfg.Tools` into `Spec.Tools` carrying `Version`/`SHA256`/`SHA256ByArch`.

- [ ] **Step 2: Run tests to verify they fail.**

Run: `go test ./internal/profile/ ./internal/mise/ ./pkg/tpd/`
Expected: FAIL — `Tool` type does not exist; `Tools` is still `map[string]string`.

- [ ] **Step 3: Implement.**

`internal/profile/types.go`:
```go
// Tool is a single mise tool: the version plus optional verification
// metadata. SHA256 is a universal asset digest; SHA256ByArch keys are the
// backend's RUNTIME.archType values ("amd64", "aarch64"). Decodes from a
// YAML scalar (the version) or a map ({version, sha256}).
type Tool struct {
	Version      string
	SHA256       string
	SHA256ByArch map[string]string
}

func (t *Tool) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		return node.Decode(&t.Version)
	case yaml.MappingNode:
		var raw struct {
			Version string    `yaml:"version"`
			SHA256  yaml.Node `yaml:"sha256"`
		}
		if err := node.Decode(&raw); err != nil {
			return err
		}
		t.Version = raw.Version
		switch raw.SHA256.Kind {
		case yaml.ScalarNode:
			return raw.SHA256.Decode(&t.SHA256)
		case yaml.MappingNode:
			return raw.SHA256.Decode(&t.SHA256ByArch)
		}
	}
	return fmt.Errorf("tools value must be a version string or a {version, sha256} map")
}

func (t Tool) MarshalYAML() (interface{}, error) {
	if len(t.SHA256ByArch) > 0 {
		return struct {
			Version string            `yaml:"version"`
			SHA256  map[string]string `yaml:"sha256"`
		}{t.Version, t.SHA256ByArch}, nil
	}
	if t.SHA256 == "" {
		return t.Version, nil
	}
	return struct {
		Version string `yaml:"version"`
		SHA256  string `yaml:"sha256"`
	}{t.Version, t.SHA256}, nil
}
```
Change `Profile.Tools` to `map[string]Tool` (yaml tag unchanged).

`internal/profile/merge.go` — `out.Tools = mergeMap(parent.Tools, child.Tools, child.NullKeys["tools"])` (remove the `mergeStringMap` call for tools).

`internal/profile/validate.go` — add to `validate()` and new helpers. Only generic checks here (no appimage-specific rules — those land in Task 5):
```go
var (
	envKeyRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	toolNameRe  = regexp.MustCompile(`^[A-Za-z0-9_@./:-]+$`)
	hexSHA256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)
	networkRe   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
)

func validateTools(rc RawProfile) error {
	for name, tool := range rc.Tools {
		if !toolNameRe.MatchString(name) || containsControl(name) || containsControl(tool.Version) {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("tools: invalid tool name/version %q", name)}
		}
	}
	return nil
}

func containsControl(s string) bool {
	for _, r := range s {
		if r == 0 || r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
```
Add `validateEnv` (envKeyRe + containsControl on key and value), `validateNetwork` (networkRe + containsControl), and wire all into `validate()`.

`internal/mise/types.go` (new):
```go
package mise

// Tool is a mise tool as seen by the runtime: the version plus optional
// verification metadata. SHA256 is a universal digest; SHA256ByArch keys are
// the appimage backend's RUNTIME.archType values. At most one form is set.
type Tool struct {
	Version      string
	SHA256       string
	SHA256ByArch map[string]string
}
```

`internal/mise/mise.go` — change the three signatures from `map[string]string` to `map[string]Tool`:
- `ActivateCommand(configDir string, tools map[string]Tool) string`
- `BackendRuntimesCommand(configDir string, tools map[string]Tool) string` — `needNode := tools["node"].Version == ""`, `needUV := tools["uv"].Version == "" && tools["pipx"].Version == ""`
- `NeedsEmbeddedPlugin(tools map[string]Tool) bool`
(Bodies are otherwise unchanged here; the base64/TOML emission rewrite is Task 4.)

`internal/runtime/runtime.go` — change `Spec.Tools` to `map[string]mise.Tool` (runtime already imports mise).

`pkg/tpd/spec.go` — in `buildSpec`:
```go
tools := map[string]mise.Tool{}
for name, t := range cfg.Tools {
	tools[name] = mise.Tool{Version: t.Version, SHA256: t.SHA256, SHA256ByArch: t.SHA256ByArch}
}
```
and set `Tools: tools` on the returned `Spec`.

`pkg/tpd/dryrun.go` — `fmt.Fprintf(w, "  %s: %s\n", name, spec.Tools[name].Version)`.

`pkg/tpd/launch.go` `parseToolFlag` — assign `cfg.Tools[name] = profile.Tool{Version: ver}` (and the `nil` init becomes `map[string]profile.Tool{}`).

- [ ] **Step 4: Verify tests pass.**

Run: `go test ./internal/profile/ ./internal/mise/ ./pkg/tpd/ ./internal/runtime/`
Expected: PASS (existing `map[string]string` usages in tests updated in the same task).

- [ ] **Step 5: Vet and commit.**

Run: `go vet ./... && go test ./...`
Commit: `feat(profile): rich tools schema with verification metadata for appimage tools`

---

### Task 4: Byte-safe mise config writing (H-07 part 2)

**Files:**
- Modify: `internal/mise/mise.go`, `internal/runtime/docker_run.go`
- Test: `internal/mise/mise_test.go`

**Interfaces:**
- Consumes: `mise.Tool` / `runtime.Spec.Tools map[string]mise.Tool` (Task 3).
- Produces: `mise.ActivateCommand(configDir string, tools map[string]Tool) string` — writes each tool's TOML entry as `{ version = ..., sha256 = ... }` (scalar) or `{ version = ..., sha256 = { amd64 = ..., aarch64 = ... } }` (per-arch), so Task 5's Lua backend receives the digest via `ctx.options.sha256`.

- [ ] **Step 1: Write the failing tests.** In `mise_test.go`, add: (a) a tool version containing `'` (e.g. `x' && touch /tmp/pwn`) produces a command that, when run through `sh -c`, writes the literal value into config.toml and does NOT create `/tmp/pwn`; (b) a tool with a scalar `sha256` produces a TOML `{ version = ..., sha256 = ... }` entry; (c) a tool with `SHA256ByArch` produces `sha256 = { amd64 = "...", aarch64 = "..." }`.

- [ ] **Step 2: Run tests to verify they fail.**

Run: `go test ./internal/mise/`
Expected: FAIL — current `printf '%s' '...'` transport is injectable and emits only the version.

- [ ] **Step 3: Implement.**

`internal/mise/mise.go` — replace the TOML+shell emission with a base64 transport (same pattern as `PluginInstallCommand`):
```go
func ActivateCommand(configDir string, tools map[string]Tool) string {
	if len(tools) == 0 {
		return ""
	}
	var cfg strings.Builder
	cfg.WriteString("[tools]\n")
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		tool := tools[name]
		key := strconv.Quote(name)
		switch {
		case len(tool.SHA256ByArch) > 0:
			var arch []string
			for _, a := range []string{"amd64", "aarch64"} {
				if sum := tool.SHA256ByArch[a]; sum != "" {
					arch = append(arch, fmt.Sprintf("%s = %s", a, strconv.Quote(sum)))
				}
			}
			fmt.Fprintf(&cfg, "%s = { version = %s, sha256 = { %s } }\n", key, strconv.Quote(tool.Version), strings.Join(arch, ", "))
		case tool.SHA256 != "":
			fmt.Fprintf(&cfg, "%s = { version = %s, sha256 = %s }\n", key, strconv.Quote(tool.Version), strconv.Quote(tool.SHA256))
		default:
			fmt.Fprintf(&cfg, "%s = %s\n", key, strconv.Quote(tool.Version))
		}
	}
	configFile := filepath.Join(configDir, "config.toml")
	encoded := base64.StdEncoding.EncodeToString([]byte(cfg.String()))
	return fmt.Sprintf("mkdir -p %s && printf '%%s' '%s' | base64 -d > %s", shq(configDir), encoded, shq(configFile))
}
```
`strconv.Quote` produces valid TOML basic strings and escapes control characters, so even unvalidated input cannot break out of the TOML; base64 removes the shell-injection surface entirely.

`internal/runtime/docker_run.go` — call site stays `mise.ActivateCommand(configDir, spec.Tools)` (Task 3 already changed the signature). Update any other callers in tests.

- [ ] **Step 4: Verify tests pass.**

Run: `go test ./internal/mise/ ./internal/runtime/ ./cmd/tpd/`
Expected: PASS.

- [ ] **Step 5: Vet and commit.**

Run: `go vet ./... && go test ./...`
Commit: `fix(mise): write tool config via base64 transport, removing shell injection`

---

### Task 5: AppImage backend resolves `latest`, verifies against a published digest, and caches the resolution (H-04 part 2, M-08)

Design (per maintainer): keep built-in profiles on `latest` — requiring checked-in version pins would turn the catalog into a manually-maintained lockfile. Instead the backend resolves `latest` to a concrete release at install time, verifies the downloaded `.AppImage` against a *published* digest (GitHub's per-asset `digest`, a checksum sidecar, or an explicit profile `sha256`), and fails closed if no digest is obtainable. The resolved (version, asset, digest) is cached next to the installed tool, and mise only reinstalls when the version directory is absent — so a machine stays on its first resolution while fresh machines get the current release.

**Files:**
- Modify: `internal/mise/plugins/appimage/hooks/backend_install.lua`, `internal/profile/validate.go`, `internal/mise/plugins_test.go`
- Test: `internal/profile/validate_test.go`, `internal/catalog/catalog_test.go` (canary: built-ins keep `latest` and still validate)

**Interfaces:**
- Consumes: `profile.Tool.SHA256`/`SHA256ByArch` (Task 3) → written to the mise config by Task 4; read by the backend as `options.sha256` and treated as an *explicit author pin* that overrides upstream metadata.
- Produces: optional appimage checksum validation in `profile.validateTools` (format checks only — no version-pin or mandatory-checksum rules, so the existing `latest` built-ins stay valid).

- [ ] **Step 1: Write the failing tests.** `validate_test.go`: `appimage:owner/repo: latest` with no checksum passes; `appimage:owner/repo: v1` with a malformed sha256 (not 64 hex) is rejected; a per-arch map with an unknown arch key (`riscv64`) is rejected; a valid scalar or per-arch checksum passes. `catalog_test.go`: the built-in `t3code`/`buzz` profiles still load and validate with `latest` (canary for the removed pin requirement).

- [ ] **Step 2: Run tests to verify they fail.**

Run: `go test ./internal/profile/ ./internal/catalog/`
Expected: FAIL — current (Task 5 draft) rules reject `latest` without a checksum.

- [ ] **Step 3: Update validation.** In `internal/profile/validate.go`, replace the Task 5 pin+mandatory-checksum rules with format-only checks:
```go
if strings.HasPrefix(name, "appimage:") {
	if tool.SHA256 != "" && !hexSHA256Re.MatchString(tool.SHA256) {
		return ProfileError{Path: rc.Path, Message: fmt.Sprintf("tools: %s: invalid universal sha256", name)}
	}
	for arch, sum := range tool.SHA256ByArch {
		if arch != "amd64" && arch != "aarch64" {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("tools: %s: unknown arch %q (want amd64 or aarch64)", name, arch)}
		}
		if !hexSHA256Re.MatchString(sum) {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("tools: %s: invalid sha256 for arch %q", name, arch)}
		}
	}
}
```

- [ ] **Step 4: Update the Lua backend** to resolve `latest`, resolve a digest, verify, and cache. The current `find_release` never handles `latest` (it looks up `releases/tags/vlatest`, then matches listed tags against the literal string) — so today the `appimage:...: latest` built-ins fail at install. Add a `latest` branch:
```lua
local function find_release(repo, version)
  local url
  if version == "latest" then
    url = "https://api.github.com/repos/" .. repo .. "/releases/latest"
  else
    url = "https://api.github.com/repos/" .. repo .. "/releases/tags/v" .. version
  end
  local resp, err = http.get({ url = url })
  if err == nil then
    local release = json.decode(resp.body)
    if type(release) == "table" and release.assets then
      return release
    end
  end
  -- fallback for pinned versions: list releases and match v(%d[%w%._+-]*)$
  -- ("latest" intentionally never matches here)
  ...
end
```
After the existing asset-selection logic, resolve the expected digest in priority order — explicit author pin, then GitHub's published per-asset `digest` (sha256, added to the releases API), then a checksum sidecar asset (`SHA256SUMS` or `<asset>.sha256`):
```lua
local function sidecar_digest(assets, asset_name)
  for _, a in ipairs(assets or {}) do
    local is_summary = a.name:match("SHA256SUMS$")
    local is_single = a.name == asset_name .. ".sha256"
    if is_summary or is_single then
      local body, herr = http.get({ url = a.browser_download_url })
      if herr == nil and body and body.body then
        for line in body.body:gmatch("[^\r\n]+") do
          local sum, fname = line:match("^(%x+)%s+[%*]?(.+)%s*$")
          if sum and (fname == asset_name or fname:match("[^/]*$") == asset_name) then
            return sum
          end
        end
      end
    end
  end
  return nil
end

local expected = options.sha256          -- explicit author pin (scalar or per-arch)
if type(expected) == "table" then
  expected = expected[RUNTIME.archType]
end
if not expected or expected == "" then
  expected = asset.digest               -- GitHub-published sha256 of the asset
end
if not expected or expected == "" then
  expected = sidecar_digest(release.assets, asset.name)
end
if not expected or expected == "" then
  error("appimage: no published digest for " .. repo .. "@" .. version ..
        "; set an explicit sha256 in the tool config")
end
```
Then verify the downloaded file and cache the resolution (quoting every path per M-08):
```lua
local actual = cmd.output("sha256sum " .. shq(appimage)):match("^(%x+)")
if not actual or actual:lower() ~= expected:lower() then
  error("appimage: sha256 mismatch for " .. repo .. ": got " .. tostring(actual) ..
        ", want " .. expected)
end
file.write(file.join_path(install_path, ".tpd-resolved"),
           json.encode({ repo = repo, version = release.tag_name:gsub("^v", ""),
                         asset = asset.name, digest = expected }) .. "\n")
```
(If the installed mise version lacks `file.write`, write the state via a `cmd.exec` heredoc with the path quoted, matching the existing launcher pattern.) Add the `shq` helper and quote every path in the remaining `cmd.exec` calls (`chmod +x`, the `cd` + extract, `mkdir -p`, `cp -a`, the launcher `cat >` target). Validate `options.asset_pattern` with a bounded regex and reject control characters in `options.name`/`options.exe`.

Stability after first resolution: mise only invokes `BackendInstall` when the version directory under the shared mise volume is absent, so an installed `latest` is not re-resolved on later launches; `install_path` is `.../appimage/<repo>/latest`. The state file records the concrete tag/asset/digest for audit. A fresh machine (empty shared volume) resolves the current `latest` automatically.

- [ ] **Step 5: Verify the catalog canary + backend tests.**

Run: `go test ./internal/profile/ ./internal/catalog/ ./internal/mise/`
Expected: PASS — built-ins keep `latest` and validate. The Lua backend has no unit harness in this repo, so additionally run a manual `tpd launch t3code`/`buzz` on an empty mise volume and confirm: the release resolves, the digest verifies, `.tpd-resolved` is written, and `mise install` skips on the second launch.

- [ ] **Step 6: Vet and commit.**

Run: `go vet ./... && go test ./...`
Commit: `feat(mise): verify appimage downloads against published digests and resolve latest with local caching`

---

### Task 6: Image-ref, network, env, and profile-name validation (H-06, M-07)

**Files:**
- Modify: `internal/profile/validate.go`, `internal/profile/catalog.go`
- Test: `internal/profile/validate_test.go`, `internal/profile/catalog_test.go`

**Interfaces:**
- Consumes: `github.com/distribution/reference` (already a module dependency).
- Produces: `validateFilenameName(name, path string) error`.

- [ ] **Step 1: Write the failing tests.**

`validate_test.go`: (a) `image: "debian:13-slim\nRUN id"` rejected (parse + control-char); (b) `image: "debian"` (name-only, no tag) still accepted — engines resolve it; (c) `image: "../evil"` rejected; (d) `environment: {"bad-key": v}` rejected; (e) `network: "host"` accepted, `network: "bad/name"` rejected.

`catalog_test.go`: a user profile file named `foo bar.yaml` fails to load with a ProfileError.

- [ ] **Step 2: Run tests to verify they fail.**

Run: `go test ./internal/profile/`
Expected: FAIL — newline image passes; bad env key passes; space filename loads.

- [ ] **Step 3: Implement.**

In `validate()`, extend the `Image` check:
```go
if rc.Image != "" {
	if strings.ContainsAny(rc.Image, "\x00\n\r") {
		return ProfileError{Path: rc.Path, Message: "image: must not contain control characters"}
	}
	if _, err := reference.ParseNormalizedNamed(rc.Image); err != nil {
		return ProfileError{Path: rc.Path, Message: fmt.Sprintf("image: invalid image reference %q: %v", rc.Image, err)}
	}
}
```
Add `validateEnv`:
```go
func validateEnv(rc RawProfile) error {
	for k, v := range rc.Env {
		if !envKeyRe.MatchString(k) || containsControl(k) || containsControl(v) {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("environment: invalid key %q", k)}
		}
	}
	return nil
}
```
Add `validateNetwork`:
```go
func validateNetwork(rc RawProfile) error {
	if rc.Network != "" && (!networkRe.MatchString(rc.Network) || containsControl(rc.Network)) {
		return ProfileError{Path: rc.Path, Message: fmt.Sprintf("network: invalid network name %q", rc.Network)}
	}
	return nil
}
```
Wire `validateEnv`/`validateNetwork`/`validateTools` into `validate()`.

In `catalog.go`, add the filename check to `loadUserDir` and `loadUserFragments` (and their tolerant variants) right after `name := strings.TrimSuffix(...)`:
```go
if err := validateFilenameName(name, path); err != nil {
	return err
}
```
```go
// profileNameRe is the single strict grammar for profile names. It matches
// Docker's container-name charset, so the derived container name
// (tpd-<name>-<rand> in docker_run.go) and Hostname are always valid:
// [a-zA-Z0-9][a-zA-Z0-9._-]*. Rejects ':', '\', '/', whitespace, and control
// characters. Applied uniformly to CLI input (ValidateName), names derived
// from user filenames, and the container name/hostname construction.
var profileNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// validateFilenameName applies profileNameRe to names derived from filenames,
// additionally rejecting ".." for path safety.
func validateFilenameName(name, path string) error {
	if !profileNameRe.MatchString(name) || strings.Contains(name, "..") {
		return ProfileError{Path: path, Message: "invalid profile name derived from filename: " + name}
	}
	return nil
}
```
`ValidateName` (the `init` CLI path) is tightened to the same grammar:
```go
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("profile name is required")
	}
	if !profileNameRe.MatchString(name) || strings.Contains(name, "..") {
		return fmt.Errorf("invalid profile name %q: must match %s and must not contain '..'", name, profileNameRe)
	}
	if reservedNames[name] {
		return fmt.Errorf("profile name %q is reserved (collides with a subcommand)", name)
	}
	return nil
}
```
Both now reject the characters (`:`, `\`) the old whitespace/slash check allowed, so every accepted name is a valid container name and hostname.

- [ ] **Step 4: Verify tests pass.**

Run: `go test ./internal/profile/`
Expected: PASS.

- [ ] **Step 5: Vet and commit.**

Run: `go vet ./... && go test ./...`
Commit: `fix(profile): validate image references, env keys, network names, and profile names`

---

### Task 7: `--pull` refresh policy + derived-image provenance (H-05)

**Files:**
- Modify: `cmd/tpd/cli.go`, `pkg/tpd/types.go`, `pkg/tpd/launch.go`, `internal/runtime/docker_prepare.go`, `internal/runtime/docker_build.go`, `internal/runtime/runtime.go` (`Runtime.Prepare` interface), `internal/runtime/fake.go` (`FakeRuntime.Prepare`)
- Test: `internal/runtime/docker_prepare_test.go`, `cmd/tpd/cli_test.go`

**Interfaces:**
- Produces: `LaunchOpts.Pull bool`; `ensureImagePulled(ctx, cli, ref string, w ProgressWriter, force bool) error`.
- Consumes: `runtime.OwnershipLabel` for the provenance label (Task 1).

- [ ] **Step 1: Write the failing tests.** In `docker_prepare_test.go`: (a) with `force=false` and the image present, no pull occurs (existing behavior); (b) with `force=true` and the image present, `ImagePull` IS invoked. In `cli_test.go`, `tpd launch --pull` sets `LaunchOpts.Pull`.

- [ ] **Step 2: Run tests to verify they fail.**

Run: `go test ./internal/runtime/ ./cmd/tpd/`
Expected: FAIL — no `force`/`Pull` exists yet.

- [ ] **Step 3: Implement.**

`docker_prepare.go`:
```go
func (d *DockerRuntime) Prepare(ctx context.Context, spec Spec, w ProgressWriter, pull bool) (string, error) {
	baseRef := spec.Image
	if err := ensureImagePulled(ctx, d.cli, baseRef, w, pull); err != nil {
		...
	}
	...
}
func ensureImagePulled(ctx context.Context, cli *client.Client, ref string, w ProgressWriter, force bool) error {
	if !force {
		exists, err := imageExists(ctx, cli, ref)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
	}
	w.WriteProgress("pull: " + ref)
	...
}
```
`runtime.Runtime.Prepare` signature gains `pull bool` — update `FakeRuntime` and all callers/tests.

`pkg/tpd/launch.go`: add `Pull bool` to `LaunchOpts` and thread it into `rt.Prepare(ctx, spec, progress, opts.Pull)`.

`cmd/tpd/cli.go` `LaunchCmd`:
```go
Pull bool `help:"Pull the base image even if already present (refresh mutable tags)."`
```
and pass `Pull: l.Pull` in `LaunchOpts`.

`docker_build.go` — add build provenance labels to the derived image:
```go
labels := OwnershipLabels()
labels["tpd.build"] = "1"
```
in the `ImageBuildOptions.Labels` map (the ownership label is Task 1's; add provenance as a stable marker that the image was tpd-built).

- [ ] **Step 4: Verify tests pass.**

Run: `go test ./internal/runtime/ ./pkg/tpd/ ./cmd/tpd/`
Expected: PASS.

- [ ] **Step 5: Vet and commit.**

Run: `go vet ./... && go test ./...`
Commit: `feat(launch): add --pull to refresh mutable base image tags`

---

### Task 8: Resource limits implemented and reported (M-03)

**Files:**
- Modify: `internal/profile/types.go`, `internal/profile/validate.go`, `internal/runtime/runtime.go`, `internal/runtime/docker_run.go`, `pkg/tpd/spec.go`, `pkg/tpd/dryrun.go`, `internal/doctor/checks.go`
- Test: `internal/profile/validate_test.go`, `pkg/tpd/spec_test.go`, `internal/doctor/checks_test.go`

**Interfaces:**
- Produces: `profile.ParseMemoryBytes(string) (int64, error)` (via `docker/go-units`) and `profile.ParseNanoCPUs(string) (int64, error)` (defined in this task); `runtime.ResourceSpec{MemoryBytes, NanoCPUs int64}`; `Spec.Resources ResourceSpec`.
- Consumes: `github.com/docker/go-units` (already in `go.mod` as indirect; promoted to direct by this task).

- [ ] **Step 1: Write the failing tests.**

`validate_test.go`: `resources: {memory: "512m"}` accepted; `memory: "512mb"` accepted (regression for the suffix-order bug); `memory: "bogus"` rejected; `cpus: "2"` accepted; `cpus: "1.5"` accepted; `cpus: "-1"`, `cpus: "NaN"`, `cpus: "Inf"`, and `cpus: "1e10"` (overflows int64 after scaling) rejected.

`spec_test.go`: `buildSpec` maps `cfg.Resources` into `Spec.Resources` (bytes/nanocpus).

`checks_test.go`: `checkProfileValidity`'s single Check message names profiles declaring `resources`.

- [ ] **Step 2: Run tests to verify they fail.**

Run: `go test ./internal/profile/ ./pkg/tpd/ ./internal/doctor/`
Expected: FAIL — no parsers, no mapping.

- [ ] **Step 3: Implement.**

`internal/profile/validate.go` — parsers + validation. Memory parsing delegates to `github.com/docker/go-units.RAMInBytes`, the exact parser Docker uses for `--memory`, so profile values match engine semantics (including `512mb`). The module is already in `go.mod` as an indirect dependency; promote it with `go mod tidy`:
```go
func validateResources(rc RawProfile) error {
	if rc.Resources == nil {
		return nil
	}
	if rc.Resources.Memory != "" {
		if _, err := ParseMemoryBytes(rc.Resources.Memory); err != nil {
			return ProfileError{Path: rc.Path, Message: "resources: memory: " + err.Error()}
		}
	}
	if rc.Resources.CPUs != "" {
		if _, err := ParseNanoCPUs(rc.Resources.CPUs); err != nil {
			return ProfileError{Path: rc.Path, Message: "resources: cpus: " + err.Error()}
		}
	}
	return nil
}

// ParseMemoryBytes converts a Docker-style memory string to bytes using
// docker/go-units, the same parser Docker's --memory uses. Rejects empty and
// unparseable values.
func ParseMemoryBytes(s string) (int64, error) {
	if strings.TrimSpace(s) == "" {
		return 0, errors.New("empty memory value")
	}
	b, err := units.RAMInBytes(s)
	if err != nil || b <= 0 {
		return 0, fmt.Errorf("invalid memory value %q", s)
	}
	return b, nil
}

// ParseNanoCPUs converts a CPU-count string ("2", "1.5") to nanos, matching
// Docker's --cpus semantics. Rejects NaN, infinities, values <= 0, and values
// that would overflow int64 after scaling (a fractional count above ~9.2e9).
func ParseNanoCPUs(s string) (int64, error) {
	s = strings.TrimSpace(s)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
		return 0, fmt.Errorf("invalid cpu count %q", s)
	}
	n := f * 1e9
	if n > math.MaxInt64 {
		return 0, fmt.Errorf("cpu count %q out of range", s)
	}
	return int64(n), nil
}
```
Imports added: `github.com/docker/go-units`, `math`, `errors`. Wire `validateResources` into `validate()`.

`internal/runtime/runtime.go`:
```go
type ResourceSpec struct {
	MemoryBytes int64
	NanoCPUs    int64
}
```
and add `Resources ResourceSpec` to `Spec`.

`pkg/tpd/spec.go` — in `buildSpec`:
```go
resources := ResourceSpec{}
if cfg.Resources != nil {
	if cfg.Resources.Memory != "" {
		resources.MemoryBytes, _ = profile.ParseMemoryBytes(cfg.Resources.Memory)
	}
	if cfg.Resources.CPUs != "" {
		resources.NanoCPUs, _ = profile.ParseNanoCPUs(cfg.Resources.CPUs)
	}
}
```
and set `Resources: resources` on the `Spec`.

`docker_run.go` `HostConfig.Resources`:
```go
Resources: container.Resources{
	Devices:           devices,
	DeviceCgroupRules: cgroupRules,
	Memory:            spec.Resources.MemoryBytes,
	NanoCPUs:          spec.Resources.NanoCPUs,
},
```

`pkg/tpd/dryrun.go` — after `network`, add:
```go
if spec.Resources.MemoryBytes > 0 || spec.Resources.NanoCPUs > 0 {
	fmt.Fprintf(w, "resources:\n")
	if spec.Resources.MemoryBytes > 0 {
		fmt.Fprintf(w, "  memory: %d\n", spec.Resources.MemoryBytes)
	}
	if spec.Resources.NanoCPUs > 0 {
		fmt.Fprintf(w, "  cpus: %d (nano)\n", spec.Resources.NanoCPUs)
	}
}
```

`internal/doctor/checks.go` — `checkProfileValidity` returns a single `Check`, so fold the resource-limit reporting into its existing message instead of appending a new check: when iterating resolved profiles, collect those whose `Resources` is set and append `", resources: <name>(<memory>,<cpus>)"` to the `Message` (empty when none declare limits). This keeps the function's one-Check signature unchanged.

- [ ] **Step 4: Verify tests pass.**

Run: `go test ./internal/profile/ ./pkg/tpd/ ./internal/runtime/ ./internal/doctor/`
Expected: PASS.

- [ ] **Step 5: Vet and commit.**

Run: `go vet ./... && go test ./...`
Commit: `feat(runtime): enforce profile resource limits and report them in dry-run/doctor`

---

### Task 9: Subpath detection race (M-04)

**Files:**
- Modify: `internal/runtime/docker.go`
- Test: `internal/runtime/docker_test.go`

**Interfaces:**
- Consumes: `supportsVolumeSubpath`.
- Produces: unchanged `subpathSupported(ctx) bool`, now guarded by `sync.Once`.

- [ ] **Step 1: Write the failing test.** A `sync.Once`-based probe means concurrent `Prepare` calls on one `DockerRuntime` never race. Add a test running `subpathSupported` from 8 goroutines under `-race` and asserting all see the same value (a fake cli returns a fixed version). This test fails only under `-race` against the current implementation.

- [ ] **Step 2: Run test to verify it fails (race).**

Run: `go test -race ./internal/runtime/ -run TestSubpathSupportedConcurrent`
Expected: FAIL — data race on `d.subpath`.

- [ ] **Step 3: Implement.** Add a `sync.Once` + a value field to `DockerRuntime`:
```go
type DockerRuntime struct {
	...
	subpathOnce sync.Once
	subpath     bool
}

func (d *DockerRuntime) subpathSupported(ctx context.Context) bool {
	d.subpathOnce.Do(func() {
		d.subpath = supportsVolumeSubpath(ctx, d.cli)
	})
	return d.subpath
}
```

- [ ] **Step 4: Verify the race test passes.**

Run: `go test -race ./internal/runtime/ -run TestSubpathSupportedConcurrent`
Expected: PASS.

- [ ] **Step 5: Vet and commit.**

Run: `go vet ./... && go test -race ./internal/runtime/`
Commit: `fix(runtime): initialize subpath support detection once under a mutex`

---

### Task 10: Prune never removes resources in use by running containers (M-05)

The host-level flock is dropped per the scope decision above (single-user rootless mode). What remains is the cheap, always-on safety half of M-05: prune re-checks liveness after building its candidate list and skips anything referenced by a running container.

**Files:**
- Modify: `internal/prune/prune.go`
- Test: `internal/prune/prune_test.go`

**Interfaces:**
- Consumes: `runtime.OwnershipLabel`; `client.ContainerList`.
- Produces: `runningContainerRefs(ctx, cli) (volumes map[string]bool, images map[string]bool, err error)` in `internal/prune`.

- [ ] **Step 1: Write the failing tests.**

`prune_test.go`: extend `fakeClient` with `ContainerList` + `ContainerInspectWithRaw`; a labeled volume mounted by a currently-running container is NOT removed by `run()` (with and without `--all`), while a labeled volume not referenced by any container is still removed.

- [ ] **Step 2: Run tests to verify they fail.**

Run: `go test ./internal/prune/`
Expected: FAIL — running-container protection absent.

- [ ] **Step 3: Implement.**

`internal/prune/prune.go` — add a running-container protection pass after computing candidates and before removal. Containers are filtered by the ownership label (Task 1) rather than the `tpd-` name prefix, so only tpd's own containers are considered:
```go
func runningContainerRefs(ctx context.Context, cli dockerClient) (volumes map[string]bool, images map[string]bool, err error) {
	f := filters.NewArgs()
	f.Add("label", runtime.OwnershipLabel+"=true")
	containers, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil, nil, err
	}
	volumes, images = map[string]bool{}, map[string]bool{}
	for _, c := range containers {
		insp, _, err := cli.ContainerInspectWithRaw(ctx, c.ID)
		if err != nil {
			continue
		}
		for _, m := range insp.Mounts {
			if m.Type == "volume" {
				volumes[m.Name] = true
			}
		}
		images[insp.Image] = true
	}
	return volumes, images, nil
}
```
Skip any candidate whose volume name is in `volumes` or whose derived ref's image ID is in `images`; print `skipping <name>: in use by a running container`. (`dockerClient` gains `ContainerList`; add it to the interface and the test fake.) Liveness is re-checked immediately before each removal by recomputing the used set with the current profile catalog (cheap — no network).

- [ ] **Step 4: Verify tests pass.**

Run: `go test ./internal/prune/ ./internal/runtime/ ./pkg/tpd/`
Expected: PASS.

- [ ] **Step 5: Vet and commit.**

Run: `go vet ./... && go test ./...`
Commit: `fix(prune): never remove resources in use by running containers`

---

### Task 11: Exit-code centralization (M-06)

**Files:**
- Modify: `cmd/tpd/cli.go`, `cmd/tpd/cli_test.go`
- Test: `cmd/tpd/cli_test.go`

**Interfaces:**
- Produces: `type exitError struct { code int; err error }` in `cmd/tpd` with `Error()` and `Unwrap()`; `main()` maps `errors.As(profile.ExitCoder)` to exit 2.

- [ ] **Step 1: Write the failing tests.** `cli_test.go`: (a) a profile parse error surfaced through `show` exits 2 (currently 1); (b) a successful `prune` with nothing to remove exits 0; (c) a launch whose container exits 5 propagates 5; (d) a plain runtime error exits 1. (Kong's `FatalIfErrorf` behavior is replaced, so these test the new `main` error mapping — extract the mapping into `func exitCodeFor(err error) int` for testability.)

- [ ] **Step 2: Run tests to verify they fail.**

Run: `go test ./cmd/tpd/`
Expected: FAIL — profile errors from show exit 1.

- [ ] **Step 3: Implement.**

Add `exitError`:
```go
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *exitError) Unwrap() error { return e.err }
```

Rewrite `main()` — stop calling `parser.FatalIfErrorf` for Run errors:
```go
func main() {
	var cli CLI
	parser := kong.Must(&cli,
		kong.Name("tpd"),
		kong.Description("ephemeral dev environments"),
	)
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"--help"}
	}
	ctx, err := parser.Parse(args)
	if err != nil {
		parser.FatalIfErrorf(err)
	}
	if err := ctx.Run(); err != nil {
		os.Exit(exitCodeFor(err))
	}
}

// exitCodeFor maps a Run error to the process exit code: exitError carries
// its own code; profile-layer errors exit 2; everything else exits 1.
func exitCodeFor(err error) int {
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.err != nil {
			fmt.Fprintln(os.Stderr, ee.err)
		}
		return ee.code
	}
	var pc profile.ExitCoder
	if errors.As(err, &pc) {
		fmt.Fprintln(os.Stderr, err)
		return pc.ExitCode()
	}
	fmt.Fprintln(os.Stderr, err)
	return 1
}
```

Handlers return errors instead of calling `os.Exit`:
- `LaunchCmd.Run`: `if result.Err != nil { return &exitError{code: result.ExitCode, err: result.Err} }` and `if result.ExitCode != 0 { return &exitError{code: result.ExitCode} }`.
- `InitCmd.Run`: `return &exitError{code: 2, err: err}`.
- `PruneCmd.Run`: `return &exitError{code: 3, err: err}`.
- `DoctorCmd.Run`: `if result.HasFailure() { return &exitError{code: 1} }`.
- `show`/`edit`/`list`: wrap "not found" in `profile.ProfileError` so `errors.As(ExitCoder)` yields 2.

- [ ] **Step 4: Verify tests pass.**

Run: `go test ./cmd/tpd/`
Expected: PASS.

- [ ] **Step 5: Vet and commit.**

Run: `go vet ./... && go test ./...`
Commit: `fix(cli): centralize error-to-exit-code mapping in main`

---

### Task 12: HTTP hardening: bounded reads, redirect caps, status checks (M-02, H-08, L-01)

**Files:**
- Modify: `internal/runtime/extrepo.go`, `internal/runtime/docker.go`
- Test: `internal/runtime/extrepo_test.go`, `internal/runtime/docker_test.go`

**Interfaces:**
- Produces: `httpGet(ctx, url string, max int64) ([]byte, error)` (new signature) — same-origin HTTPS redirects only; `QueryRootless` uses `DialContext`, checks 2xx, and rejects a body over 64 KiB instead of truncating.

- [ ] **Step 1: Write the failing tests.** `extrepo_test.go`: an oversized index response (> 8 MiB via a fake server) errors; a 3xx-without-location errors; a 5xx error is surfaced; a redirect to a foreign host errors. `docker_test.go`: `QueryRootless` against a fake unix server returning 500 errors; a 200 with a body over 64 KiB is rejected.

- [ ] **Step 2: Run tests to verify they fail.**

Run: `go test ./internal/runtime/`
Expected: FAIL — no limits/status handling.

- [ ] **Step 3: Implement.**

`extrepo.go`:
```go
const (
	maxExtrepoIndexSize = 8 << 20
	maxExtrepoKeySize   = 256 << 10
)

func fetchExtrepoIndex(ctx context.Context, codename string) (map[string]extrepoEntry, error) {
	data, err := httpGet(ctx, extrepoCatalogBase+"/"+codename+"/index.yaml", maxExtrepoIndexSize)
	...
}

func fetchExtrepoKey(ctx context.Context, codename, keyFile string) ([]byte, error) {
	data, err := httpGet(ctx, extrepoCatalogBase+"/"+codename+"/"+keyFile, maxExtrepoKeySize)
	...
}

// httpGet fetches url with a size cap and same-origin, HTTPS-only redirects.
// A redirect to any other host (e.g. an attacker-controlled CDN) is rejected
// rather than followed.
func httpGet(ctx context.Context, url string, max int64) ([]byte, error) {
	u, err := urlparse.Parse(url)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" || req.URL.Host != u.Host {
				return fmt.Errorf("refusing redirect to %s (want https://%s)", req.URL, u.Host)
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf("GET %s: response exceeds %d bytes", url, max)
	}
	return body, nil
}
```
(`urlparse` is the alias for `net/url`.) Also cap `readImageFileEntry` content with `io.LimitReader(tr, 1<<20)` and a size check (os-release is tiny; reject larger).

`docker.go` `QueryRootless`:
```go
if len(host) > 7 && host[:7] == "unix://" {
	dialer := &net.Dialer{}
	httpClient = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", host[7:])
			},
		},
	}
	url = "http://localhost/info"
} else {
	httpClient = http.DefaultClient
	url = host + "/info"
}
...
if resp.StatusCode < 200 || resp.StatusCode >= 300 {
	return false, fmt.Errorf("GET /info: %s", resp.Status)
}
body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10+1))
if err != nil {
	return false, err
}
if len(body) > 64<<10 {
	return false, errors.New("GET /info: response exceeds 64 KiB")
}
```

- [ ] **Step 4: Verify tests pass.**

Run: `go test ./internal/runtime/`
Expected: PASS.

- [ ] **Step 5: Vet and commit.**

Run: `go vet ./... && go test ./...`
Commit: `fix(runtime): cap HTTP/daemon response sizes and check status codes`

---

### Task 13: Cleanup-error diagnostics + doctor leak checks (L-03)

**Files:**
- Modify: `internal/runtime/docker_run.go`, `internal/runtime/docker_prepare.go`, `pkg/tpd/dbusproxy.go`, `internal/doctor/checks.go`, `internal/doctor/doctor.go`
- Test: `internal/doctor/checks_test.go`

**Interfaces:**
- Produces: `checkLeakedContainers(ctx, rt) Check` and `checkStaleBusSockets() Check` in `internal/doctor`.

- [ ] **Step 1: Write the failing tests.** `checks_test.go`: with a fake client returning one container carrying `runtime.OwnershipLabel`, `checkLeakedContainers` is `Warn`; with none, `Pass`; a container lacking the label is ignored. `checkStaleBusSockets` warns when `$XDG_RUNTIME_DIR/tpd-bus-123.sock` exists and passes when the dir has none. (`internal/doctor/checks.go` gains a `filters` import from the docker API.)

- [ ] **Step 2: Run tests to verify they fail.**

Run: `go test ./internal/doctor/`
Expected: FAIL — checks do not exist.

- [ ] **Step 3: Implement.**

Surface cleanup failures to stderr:
- `docker_run.go` defer: `if err := d.cli.ContainerRemove(cleanupCtx, resp.ID, container.RemoveOptions{Force: true}); err != nil { fmt.Fprintf(os.Stderr, "tpd: warning: remove container %s: %v\n", resp.ID, err) }`
- `docker_prepare.go` `ensureCacheSubpaths` defer: same pattern with a warning prefix.
- `dbusproxy.go` cleanup: warn when `Kill`/`Wait`/`Remove` fails.

Add doctor checks and wire into `runChecks`. Leak detection filters containers by the ownership label (Task 1) rather than the `tpd-` name prefix, consistent with the ownership design:
```go
func checkLeakedContainers(ctx context.Context, rt *dockerRT) Check {
	f := filters.NewArgs()
	f.Add("label", runtime.OwnershipLabel+"=true")
	containers, err := rt.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return Check{Name: "leaked containers", Status: Warn, Message: err.Error()}
	}
	var leaked []string
	for _, c := range containers {
		leaked = append(leaked, strings.Join(c.Names, ","))
	}
	sort.Strings(leaked)
	if len(leaked) == 0 {
		return Check{Name: "leaked containers", Status: Pass, Message: "none"}
	}
	return Check{Name: "leaked containers", Status: Warn, Message: strings.Join(leaked, ", ") + " (remove with: docker rm -f ...)"}
}

func checkStaleBusSockets() Check {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return Check{Name: "stale dbus sockets", Status: Pass, Message: "no XDG_RUNTIME_DIR"}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "tpd-bus-*.sock"))
	if len(matches) == 0 {
		return Check{Name: "stale dbus sockets", Status: Pass, Message: "none"}
	}
	return Check{Name: "stale dbus sockets", Status: Warn, Message: strings.Join(matches, ", ")}
}
```
(`internal/doctor/checks.go` already imports `container`.)

- [ ] **Step 4: Verify tests pass.**

Run: `go test ./internal/doctor/ ./internal/runtime/ ./pkg/tpd/`
Expected: PASS.

- [ ] **Step 5: Vet and commit.**

Run: `go vet ./... && go test ./...`
Commit: `fix: surface cleanup failures and add doctor leak checks`

---

### Task 14: Sensitive-fragment advisories (H-02)

**Files:**
- Create: `internal/catalog/advisories.go`
- Modify: `cmd/tpd/cli.go`, `internal/scaffold/scaffold.go`
- Test: `cmd/tpd/cli_test.go`, `internal/scaffold/scaffold_test.go`

**Interfaces:**
- Produces: `catalog.Advisory(name string) string` — returns a human-readable host-access warning for a fragment name, or `""`.

- [ ] **Step 1: Write the failing tests.** `cli_test.go`: `show docker` prints the advisory for the docker fragment to stderr; `edit docker` prints it too. `scaffold_test.go`: scaffolding a profile that extends `docker` includes the advisory line in output.

- [ ] **Step 2: Run tests to verify they fail.**

Run: `go test ./cmd/tpd/ ./internal/scaffold/`
Expected: FAIL — no advisories.

- [ ] **Step 3: Implement.**

`internal/catalog/advisories.go`:
```go
package catalog

// Advisory returns a one-line warning about the host access a sensitive
// built-in fragment grants, or "" for fragments that need none. Shown by
// `tpd show`, `tpd edit`, and `tpd init` so the capability is visible
// whenever a sensitive fragment is named, not just after launch. Kept as a
// curated table: per-mount "high risk" labels can go stale and the review
// specifically declined them.
func Advisory(name string) string {
	switch name {
	case "docker":
		return "mounts the Docker socket read-write — container processes can administer the daemon (host-root access on a rootful daemon)"
	case "podman":
		return "mounts the Podman socket read-write — container processes can control the container engine"
	case "gui":
		return "exposes the host display, /dev/dri, and X11/Wayland sockets to container processes"
	case "gui-runtime":
		return "mounts the entire $XDG_RUNTIME_DIR — exposes audio, compositor, notification, and agent sockets to container processes"
	case "ssh", "netrc", "aws", "azure", "gcloud", "github", "gitlab", "vault":
		return "mounts host credentials read-only — any process in the profile can read them"
	default:
		return ""
	}
}
```

`cmd/tpd/cli.go` — print the advisory wherever the fragment is surfaced by name: in `ProfileShowCmd.Run` after rendering, and in `ProfileEditCmd.Run` for the name being edited:
```go
if msg := catalog.Advisory(c.Name); msg != "" {
	fmt.Fprintln(os.Stderr, "warning: "+msg)
}
```
(Edit already resolves and displays the profile, so the warning makes the capability visible there too.)

`internal/scaffold/scaffold.go` — when writing a profile that extends any fragment with a non-empty advisory, print each advisory to the console (`fmt.Fprintf(os.Stderr, "note: %s grants: %s\n", name, catalog.Advisory(name))`).

- [ ] **Step 4: Verify tests pass.**

Run: `go test ./cmd/tpd/ ./internal/scaffold/`
Expected: PASS.

- [ ] **Step 5: Vet and commit.**

Run: `go vet ./... && go test ./...`
Commit: `feat(catalog): surface sensitive-fragment advisories in show and init`

---

### Task 15: Split GUI runtime access + dbus fail-closed (H-03)

**Files:**
- Create: `internal/catalog/fragments/gui-runtime.yaml`
- Modify: `internal/catalog/fragments/gui.yaml`, `internal/catalog/profiles/buzz.yaml`, `internal/catalog/profiles/t3code.yaml`, `pkg/tpd/dbusproxy.go`, `pkg/tpd/launch.go`
- Test: `pkg/tpd/dbusproxy_test.go`, `internal/catalog/catalog_test.go`

**Interfaces:**
- Produces: `startBusProxy(cfg) (func(), string, error)` — returns an error when a profile requires filtered D-Bus but no host bus/proxy is available; `Launch` fails closed in that case.

- [ ] **Step 1: Write the failing tests.** `dbusproxy_test.go`: with a dbus-enabled profile and `xdg-dbus-proxy` absent from PATH, `startBusProxy` returns an error (currently a silent nil). `catalog_test.go`: `gui` no longer mounts `$XDG_RUNTIME_DIR` wholesale and `gui-runtime` does; both built-in GUI profiles resolve with the new extends list. `paths_test.go`: the guarded wayland mount renders empty (and is skipped) when `WAYLAND_DISPLAY` or `XDG_RUNTIME_DIR` is unset, and renders the joined path when both are set.

- [ ] **Step 2: Run tests to verify they fail.**

Run: `go test ./pkg/tpd/ ./internal/catalog/`
Expected: FAIL — old signature/behavior.

- [ ] **Step 3: Implement.**

`gui.yaml` — remove the broad runtime-dir mount and the `XDG_RUNTIME_DIR` env; mount only the specific Wayland socket, guarded so an unset variable yields an *empty* path (which `ResolveTildes` skips as an optional mount) rather than `/`:
```yaml
mounts:
  /tmp/.X11-unix:
    source: /tmp/.X11-unix
    optional: true
  '{{ if and .Env.XDG_RUNTIME_DIR .Env.WAYLAND_DISPLAY }}{{ .Env.XDG_RUNTIME_DIR }}/{{ .Env.WAYLAND_DISPLAY }}{{ end }}':
    source: '{{ if and .Env.XDG_RUNTIME_DIR .Env.WAYLAND_DISPLAY }}{{ .Env.XDG_RUNTIME_DIR }}/{{ .Env.WAYLAND_DISPLAY }}{{ end }}'
    optional: true
environment:
  WAYLAND_DISPLAY: '{{ .Env.WAYLAND_DISPLAY }}'
  DISPLAY: '{{ .Env.DISPLAY }}'
```
The bare `{{ .Env.XDG_RUNTIME_DIR }}/{{ .Env.WAYLAND_DISPLAY }}` form is a footgun: with `WAYLAND_DISPLAY` unset it resolves to `<runtime-dir>/` (or `/` when `XDG_RUNTIME_DIR` is also unset), which exists and would bind the host root. The `{{ if and ... }}` guard renders empty when either variable is missing, and `ResolveTildes` skips empty *optional* mounts.

Add template tests for both variables unset (mount absent), both set (mount present at the joined path), and `WAYLAND_DISPLAY` set with `XDG_RUNTIME_DIR` unset (mount absent) — see `internal/profile/paths_test.go` for the existing `{{ }}` mount-test pattern.

`gui-runtime.yaml` (new):
```yaml
version: 1
mounts:
  '{{ .Env.XDG_RUNTIME_DIR }}':
    source: '{{ .Env.XDG_RUNTIME_DIR }}'
    optional: true
environment:
  XDG_RUNTIME_DIR: '{{ .Env.XDG_RUNTIME_DIR }}'
```

`buzz.yaml` extends → `[mise, gui, gui-runtime, codex, claude]`; keep `network: host` (opt-in, documented; the callback URL on a random port is the documented reason) and add a YAML comment explaining the host-network requirement.
`t3code.yaml` extends → `[mise, gui, gui-runtime, opencode, claude, codex]`.

`dbusproxy.go` — change `startBusProxy` to return an error whenever `dbusEnabled(cfg)` but no bus can be provided:
```go
func startBusProxy(cfg profile.Profile) (func(), string, error) {
	if !dbusEnabled(cfg) {
		return nil, "", nil
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	hostBus := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if hostBus == "" && runtimeDir != "" {
		hostBus = "unix:path=" + filepath.Join(runtimeDir, "bus")
	}
	if runtimeDir == "" || hostBus == "" {
		return nil, "", fmt.Errorf("profile requires filtered D-Bus but no host session bus is available")
	}
	proxy, err := exec.LookPath("xdg-dbus-proxy")
	if err != nil {
		return nil, "", fmt.Errorf("profile requires filtered D-Bus but xdg-dbus-proxy is not installed")
	}
	...
	if startup timeout {
		return nil, "", fmt.Errorf("profile requires filtered D-Bus but xdg-dbus-proxy did not start")
	}
	...
	return cleanup, "unix:path=" + sockPath, nil
}
```
`launch.go`:
```go
cleanupProxy, busAddr, err := startBusProxy(cfg)
if err != nil {
	return Result{ExitCode: 3, Err: fmt.Errorf("dbus: %w", err)}
}
```
(Trade-off: profiles with dbus config now fail on headless hosts instead of silently disabling the bus; documented in the security-model doc and the fail message tells the user to install `xdg-dbus-proxy` or remove the `dbus:` config.)

- [ ] **Step 4: Verify tests pass.**

Run: `go test ./pkg/tpd/ ./internal/catalog/ ./internal/profile/`
Expected: PASS.

- [ ] **Step 5: Vet and commit.**

Run: `go vet ./... && go test ./...`
Commit: `feat(gui): split broad runtime-dir access into an opt-in fragment; fail closed on missing dbus proxy`

---

### Task 16: Security-model documentation (cross-cutting)

**Files:**
- Create: `docs/2026-08-03-security-model.md`
- Modify: `README.md`, `AGENTS.md`

**Interfaces:** none (documentation).

- [ ] **Step 1: Write the document.** Cover: (1) trust model — profiles are trusted configuration, sensitive fragments grant real host access; (2) ownership labels and what prune will/won't remove; (3) the appimage policy — `latest` resolved at install time and verified against a published digest (upstream `asset.digest`, checksum sidecar, or explicit profile `sha256`), fail-closed when no digest exists, resolution cached per machine; note the self-referential-digest and fresh-vs-old-machine trade-offs (scope decision 7); (4) extrepo trust anchor (TLS to `pages.debian.net`, self-referential key checksum, size caps, residual risk); (5) base-image freshness semantics and `--pull`; (6) SELinux `label=disable` trade-off; (7) the `setpriv`-absent root fallback and effective identity; (8) the accepted host-port allocation race (ephemeral random ports make collisions unlikely; no reservation is held — scope decision 2); (9) concurrent `prune --all --force` guidance; (10) the credential-fragment advisories.

- [ ] **Step 2: Update README/AGENTS.** Add a "Security" pointer to the new doc; note in the tools schema table that `appimage:` tools stay on `latest` and are digest-verified at install (explicit `sha256`/per-arch `sha256` optional); note the `gui`/`gui-runtime` split; note `--pull` in the launch help/README; record the ownership-label prune rule in AGENTS.md conventions.

- [ ] **Step 3: Verify.** `git diff --check` on the docs; confirm no markdown lint is enforced in CI.

Run: `git diff --check`
Expected: no output.

- [ ] **Step 4: Commit.**

Commit: `docs: add security model and update schema/operational docs for hardening`

---

### Task 17: Release workflow pinning + SBOM (cross-cutting)

**Files:**
- Modify: `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `.goreleaser.yml` (exists; `version: 2`, so no new file is created)
- Test: none (CI YAML); validate with a YAML parse.

**Interfaces:** none.

- [ ] **Step 1: Pin actions to full commit SHAs.** Resolve current SHAs at implementation time and replace tag references:
```bash
git ls-remote https://github.com/actions/checkout refs/tags/v4 | cut -f1   # -> full SHA
git ls-remote https://github.com/actions/setup-go refs/tags/v5 | cut -f1
git ls-remote https://github.com/actions/cache refs/tags/v4 | cut -f1
git ls-remote https://github.com/goreleaser/goreleaser-action refs/tags/v6 | cut -f1
```
Use `actions/checkout@<sha>` (etc.) in both workflows.

- [ ] **Step 2: Pin goreleaser.** In `release.yml`: `uses: goreleaser/goreleaser-action@<sha>` and `version: '2.<x>.<y>'` (a specific release, not `latest`).

- [ ] **Step 3: Add SBOM generation to the existing `.goreleaser.yml`** (checksums are already produced by its `checksum:` block). The config key is the plural `sboms` (a list), each entry with `artifacts`/`document`/`cmd`/`args`:
```yaml
# Release hardening: an SBOM per archive; checksums are already enabled above.
sboms:
  - artifacts: archive
    document: "${artifact}.sbom"
    cmd: syft
    args: ["$artifact", "--file", "$document"]
```
GoReleaser is not installed in this repo, so validate this block against the pinned version's schema (the `goreleaser check`/`goreleaser schema` subcommand, or the v2 docs) before merging — the review specifically flagged the `sbom` vs `sboms` key. Note: cosign signing is intentionally NOT added here (no signing key is provisioned in this repo); the security-model doc lists artifact signing as a follow-up.

- [ ] **Step 4: Validate YAML.**
```bash
go run - <<'EOF'
package main
import ("fmt";"os";"gopkg.in/yaml.v3")
func main(){ for _, f := range os.Args[1:] { b,_ := os.ReadFile(f); var v any; if err := yaml.Unmarshal(b,&v); err != nil { fmt.Println("bad",f,err); os.Exit(1) } }; fmt.Println("yaml ok") }
EOF
.goreleaser.yml .github/workflows/ci.yml .github/workflows/release.yml
```
Expected: `yaml ok`.

- [ ] **Step 5: Commit.**

Commit: `ci: pin actions to SHAs, pin goreleaser, and add SBOM generation`

---

### Task 18: Final verification pass

**Files:** none (verification).

- [ ] **Step 1: Full suite.**
Run: `go test -race ./... && go vet ./... && make install`
Expected: all pass; `tpd` binary installs.

- [ ] **Step 2: Manual smoke (if a runtime is available).** `tpd doctor`, `tpd launch --dry-run shell`, `tpd prune` (with no runtime, verify graceful error paths). If no engine is available, rely on the fake-client tests.

- [ ] **Step 3: Spec-coverage audit.** Re-read `CODE_REVIEW.md` and confirm every finding maps to a task:
  - C-01 → Task 1; H-01 → Task 2; L-02 → Task 2; H-04 → Tasks 3+5; H-07 → Tasks 3+4; H-06/M-07 → Task 6; H-05 → Task 7; M-03 → Task 8; M-01 → declined (scope decision 2); M-04 → Task 9; M-05 → Task 10; M-06 → Task 11; M-02/H-08/L-01 → Task 12; L-03 → Task 13; H-02 → Task 14; H-03 → Task 15; cross-cutting → Tasks 16–17. Declined items are recorded in "Scope decisions" above.

- [ ] **Step 4: Finish branch.** Use `superpowers:finishing-a-development-branch` to integrate the work.
