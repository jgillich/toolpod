# Sensitive-Field Approval System Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a per-field permission gate for sensitive profile fields contributed by non-user catalog entries, with an interactive huh-based approval dialog, persistent per-profile choices keyed by a content hash, and `--yes`/`--no` flags for non-interactive use.

**Architecture:** Provenance is recorded during `extends` merge (a parallel `Provenance` struct on `RawProfile` tracking which catalog entry contributed each sensitive key). `ResolveProfileWithProv` returns a `Resolved{Profile, Prov, FullName, DisplayName}`. A new `internal/approval` package provides `Filter` (drops denied keys, computes the hash, loads/saves state), `Store` (per-profile YAML state file), and a `Prompt` contract. `pkg/tpd.LaunchWithWriter` calls `Filter` after resolve and before `buildSpec`; the CLI injects a huh-based prompt and `--yes`/`--no` flags. Advisory prints are removed.

**Tech Stack:** Go 1.25, cobra (CLI), gopkg.in/yaml.v3 (state files), github.com/charmbracelet/huh v1.0.0 (dialog, already a dep), golang.org/x/term (TTY check).

**Spec:** `docs/superpowers/specs/2026-08-05-approval-system-design.md`

## Global Constraints

- Go 1.25, CGO off in releases.
- `go test ./...` is the full suite; `go vet ./...` is the lint check.
- No comments unless the code doesn't make something apparent (AGENTS.md).
- Conventional commit format.
- Stage individual files, never `git add -A`.
- Sensitive fields (top-level): `mounts`, `devices`, `environment`, `ports`, `dbus`, `network`.
- Services are gated **coarsely, one item per service** (field `"services"`, key = service name). The item's value renders the service's schema-valid sensitive definition (privileged, exposes, the service's own mounts, env). The validator rejects `devices`/`ports`/`dbus`/`network` on services (`internal/profile/validate.go:281-301`) — they never occur and are not rendered. Approve = keep the whole service; deny = drop it from `cfg.Services` and cascade every top-level `service: <name>` mount off. The shared daemon is never filtered.
- Non-gated: `packages`, `tools`, `image`, `command`, `labels`, `resources`, `caches`, `files`, `repos`.
- Hash and store key on **pre-expansion** (literal template) values. A changed host env var must not invalidate approvals.
- State file keyed by resolved catalog `FullName`, not the display name.
- Provenance stores `Contributor{FullName, Namespace}`; `Trusted()` = `Namespace == ""`.
- `--dry-run` never persists state and never prompts; the whole flow runs against a `ReadOnlyStore` (Load delegates, Save no-ops) and the `--yes`/`--no` re-filter uses an ephemeral in-memory overlay.
- Partial dialog results fail closed (exit 2, no Save).

---

## File Structure

**New files:**
- `internal/profile/provenance.go` — `Contributor`, `Provenance`, `DbusProvenance` types; `initProvenance(rc RawProfile)` leaf init; helpers to stamp contributor during merge.
- `internal/profile/provenance_test.go` — provenance init and merge tests.
- `internal/approval/approval.go` — `Resolved` is in `profile` (see below); this file holds `SensitiveItem`, `PromptRequest`, `Filter`, the ephemeral store wrapper, and the dependent-mount cascade.
- `internal/approval/hash.go` — `ComputeApprovalHash(res profile.Resolved) string`.
- `internal/approval/store.go` — `Store` interface, `State`, `ApprovedField`, file-system impl, per-component path validation, custom `MarshalYAML`/`UnmarshalYAML`.
- `internal/approval/prompt.go` — `Prompt` type and the huh-based default prompt.
- `internal/approval/{approval,hash,store,prompt}_test.go` — unit tests.
- `internal/ui/tty.go` — `IsTTYReader(r io.Reader) bool` (moved from `internal/scaffold/scaffold.go:408`; `scaffold.IsTTY` is replaced by a call to `ui.IsTTYReader`).

**Modified files:**
- `internal/profile/types.go` — add `Provenance Provenance `yaml:"-"`` to `RawProfile`; add `Resolved` type with `Profile`, `Prov`, `FullName`, `DisplayName`.
- `internal/profile/merge.go` — stamp provenance in each merge helper; leaf init in `resolveChain`.
- `internal/profile/merge_test.go` / `extends_test.go` — assert provenance.
- `pkg/tpd/launch.go` — call `ResolveProfileWithProv`, run `approval.Filter`, wire prompt/flags.
- `pkg/tpd/types.go` — `LaunchOpts` gains `In`, `ApprovalStore`, `ApprovalPrompt`, `IsTTY`, `AssumeYes`, `AssumeNo`.
- `cmd/tpd/cli.go` — `launchFlags` gains `AssumeYes`/`AssumeNo`; `addLaunchFlags` registers `--yes`/`--no` (mutually exclusive); `runLaunch` passes them through; remove advisory call sites and `advisoryName`.
- `cmd/tpd/cli_test.go` — remove advisory tests; add `--yes`/`--no` tests.
- `internal/catalog/advisories.go` — delete.
- `internal/catalog/catalog_test.go` — remove advisory tests.
- `internal/scaffold/scaffold.go` — remove advisory print and `advisoryLeaf`.
- `internal/scaffold/scaffold_test.go` — remove advisory test.
- `pkg/tpd/launch_test.go` — end-to-end approval flow tests with fakes.
- `docs/2026-08-03-security-model.md` — update per §8 of spec.

---

## Task 1: Provenance types and leaf initialization

**Files:**
- Create: `internal/profile/provenance.go`
- Create: `internal/profile/provenance_test.go`

**Interfaces:**
- Produces: `Contributor` struct with `FullName string`, `Namespace string`, method `Trusted() bool`; `Provenance` struct; `DbusProvenance` struct; `initProvenance(rc RawProfile) Provenance`.

- [ ] **Step 1: Write the failing test for Contributor.Trusted**

Create `internal/profile/provenance_test.go`:

```go
package profile

import "testing"

func TestContributorTrusted(t *testing.T) {
	cases := []struct {
		c    Contributor
		want bool
	}{
		{Contributor{FullName: "myagent", Namespace: ""}, true},
		{Contributor{FullName: "core/creds/ssh", Namespace: "core"}, false},
		{Contributor{FullName: "github.com/foo/bar", Namespace: "github.com/foo"}, false},
		{Contributor{}, true}, // zero value: unset == trusted
	}
	for _, tc := range cases {
		if got := tc.c.Trusted(); got != tc.want {
			t.Errorf("Contributor%+v.Trusted() = %v, want %v", tc.c, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/profile/ -run TestContributorTrusted -v`
Expected: FAIL — `undefined: Contributor`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/profile/provenance.go`:

```go
package profile

// Contributor identifies a catalog entry that contributed a sensitive
// value. Stored in provenance so the approval filter can decide trust
// without access to the catalog: a user entry (Namespace == "") is
// trusted and not gated; a core or remote-namespace entry is gated.
type Contributor struct {
	FullName  string
	Namespace string
}

// Trusted reports whether this contributor is user-owned and therefore
// not subject to the approval gate.
func (c Contributor) Trusted() bool { return c.Namespace == "" }

// Provenance records, for each sensitive key, the Contributor that last
// wrote it. Keys whose final value came from a user entry are not gated;
// keys from a core/remote entry are.
type Provenance struct {
	Mounts   map[string]Contributor
	Devices  map[string]Contributor
	Env      map[string]Contributor
	Ports    map[string]Contributor
	Dbus     DbusProvenance
	Network  Contributor
	Services map[string]Contributor
}

type DbusProvenance struct {
	Talk map[string]Contributor
	Own  map[string]Contributor
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/profile/ -run TestContributorTrusted -v`
Expected: PASS.

- [ ] **Step 5: Write the failing test for initProvenance**

Append to `internal/profile/provenance_test.go`:

```go
func TestInitProvenanceStampsLeafContributor(t *testing.T) {
	rc := RawProfile{
		Profile: Profile{
			Mounts: map[string]Mount{
				"~/.ssh": {Source: "~/.ssh"},
			},
			Env: map[string]string{"DOCKER_HOST": "unix:///var/run/docker.sock"},
			Network: "host",
			Services: map[string]Service{
				"podman": {Image: "img", Command: []string{"run"}},
			},
		},
		Namespace: "core",
		Name:      "creds/ssh",
	}
	prov := initProvenance(rc)
	want := Contributor{FullName: "core/creds/ssh", Namespace: "core"}
	if got := prov.Mounts["~/.ssh"]; got != want {
		t.Errorf("Mounts[~/.ssh] = %+v, want %+v", got, want)
	}
	if got := prov.Env["DOCKER_HOST"]; got != want {
		t.Errorf("Env[DOCKER_HOST] = %+v, want %+v", got, want)
	}
	if prov.Network != want {
		t.Errorf("Network = %+v, want %+v", prov.Network, want)
	}
	if got := prov.Services["podman"]; got != want {
		t.Errorf("Services[podman] = %+v, want %+v", got, want)
	}
}

func TestInitProvenanceUserEntryIsTrusted(t *testing.T) {
	rc := RawProfile{
		Profile:  Profile{Mounts: map[string]Mount{"~/x": {Source: "~/x"}}},
		Namespace: "",
		Name:      "myagent",
	}
	prov := initProvenance(rc)
	if !prov.Mounts["~/x"].Trusted() {
		t.Errorf("user entry should be trusted, got %+v", prov.Mounts["~/x"])
	}
}

func TestInitProvenanceEmptyProfileIsEmpty(t *testing.T) {
	prov := initProvenance(RawProfile{})
	if len(prov.Mounts) != 0 || len(prov.Env) != 0 || len(prov.Services) != 0 {
		t.Errorf("empty profile should have empty provenance, got %+v", prov)
	}
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `go test ./internal/profile/ -run TestInitProvenance -v`
Expected: FAIL — `undefined: initProvenance`.

- [ ] **Step 7: Implement initProvenance**

Append to `internal/profile/provenance.go`:

```go
// initProvenance stamps the rc's own Contributor onto every sensitive key
// rc declares. Used for leaf profiles (no extends) so a built-in leaf
// like core/bash does not bypass the gate with empty provenance.
func initProvenance(rc RawProfile) Provenance {
	c := Contributor{FullName: rc.FullName(), Namespace: rc.Namespace}
	prov := Provenance{}
	if len(rc.Mounts) > 0 {
		prov.Mounts = make(map[string]Contributor, len(rc.Mounts))
		for k := range rc.Mounts {
			prov.Mounts[k] = c
		}
	}
	if len(rc.Devices) > 0 {
		prov.Devices = make(map[string]Contributor, len(rc.Devices))
		for k := range rc.Devices {
			prov.Devices[k] = c
		}
	}
	if len(rc.Env) > 0 {
		prov.Env = make(map[string]Contributor, len(rc.Env))
		for k := range rc.Env {
			prov.Env[k] = c
		}
	}
	if len(rc.Ports) > 0 {
		prov.Ports = make(map[string]Contributor, len(rc.Ports))
		for k := range rc.Ports {
			prov.Ports[k] = c
		}
	}
	if rc.Dbus != nil {
		if len(rc.Dbus.Talk) > 0 {
			prov.Dbus.Talk = make(map[string]Contributor, len(rc.Dbus.Talk))
			for k := range rc.Dbus.Talk {
				prov.Dbus.Talk[k] = c
			}
		}
		if len(rc.Dbus.Own) > 0 {
			prov.Dbus.Own = make(map[string]Contributor, len(rc.Dbus.Own))
			for k := range rc.Dbus.Own {
				prov.Dbus.Own[k] = c
			}
		}
	}
	if rc.Network != "" {
		prov.Network = c
	}
	if len(rc.Services) > 0 {
		prov.Services = make(map[string]Contributor, len(rc.Services))
		for k := range rc.Services {
			prov.Services[k] = c
		}
	}
	return prov
}
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./internal/profile/ -run TestInitProvenance -v`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/profile/provenance.go internal/profile/provenance_test.go
git commit -m "feat(profile): add Contributor and Provenance types with leaf init"
```

---

## Task 2: Stamp provenance during merge

**Files:**
- Modify: `internal/profile/merge.go`
- Modify: `internal/profile/merge_test.go`
- Modify: `internal/profile/extends_test.go`

**Interfaces:**
- Consumes: `Contributor`, `Provenance`, `initProvenance` from Task 1.
- Produces: `MergeProfiles` now populates `out.Provenance`; `resolveChain` inits provenance for the leaf case.

- [ ] **Step 1: Write the failing test for merge provenance**

Append to `internal/profile/merge_test.go`:

```go
func TestMergeProfilesStampsChildProvenance(t *testing.T) {
	parent := RawProfile{
		Profile:   Profile{Mounts: map[string]Mount{"~/.ssh": {Source: "~/.ssh"}}},
		Namespace: "core", Name: "creds/ssh",
	}
	parent = initProvenanceWrapper(parent)
	child := RawProfile{
		Profile:   Profile{Mounts: map[string]Mount{"~/aws": {Source: "~/aws"}}},
		Namespace: "", Name: "myagent",
	}
	merged := MergeProfiles(parent, child)
	if merged.Provenance.Mounts["~/.ssh"] != (Contributor{FullName: "core/creds/ssh", Namespace: "core"}) {
		t.Errorf("parent key should keep parent provenance, got %+v", merged.Provenance.Mounts["~/.ssh"])
	}
	if merged.Provenance.Mounts["~/aws"] != (Contributor{FullName: "myagent", Namespace: ""}) {
		t.Errorf("child key should get child provenance, got %+v", merged.Provenance.Mounts["~/aws"])
	}
}

func TestMergeProfilesUserShadowsCoreKey(t *testing.T) {
	parent := RawProfile{
		Profile:   Profile{Mounts: map[string]Mount{"~/.ssh": {Source: "~/.ssh"}}},
		Namespace: "core", Name: "creds/ssh",
	}
	parent = initProvenanceWrapper(parent)
	child := RawProfile{
		Profile:   Profile{Mounts: map[string]Mount{"~/.ssh": {Source: "/custom/ssh"}}},
		Namespace: "", Name: "myagent",
	}
	merged := MergeProfiles(parent, child)
	if !merged.Provenance.Mounts["~/.ssh"].Trusted() {
		t.Errorf("user shadow should be trusted, got %+v", merged.Provenance.Mounts["~/.ssh"])
	}
}

func TestMergeProfilesNullDeletesProvenance(t *testing.T) {
	parent := RawProfile{
		Profile:   Profile{Mounts: map[string]Mount{"~/.ssh": {Source: "~/.ssh"}}},
		Namespace: "core", Name: "creds/ssh",
	}
	parent = initProvenanceWrapper(parent)
	child := RawProfile{
		Profile:   Profile{},
		Namespace: "", Name: "myagent",
		NullKeys:  map[string]map[string]bool{"mounts": {"~/.ssh": true}},
	}
	merged := MergeProfiles(parent, child)
	if _, ok := merged.Provenance.Mounts["~/.ssh"]; ok {
		t.Errorf("null-deleted key should not be in provenance")
	}
}
```

Add a helper at the top of the test additions:

```go
// initProvenanceWrapper is a test helper that calls initProvenance and
// returns the rc with Prov populated, simulating what resolveChain does
// for a leaf before it enters a merge.
func initProvenanceWrapper(rc RawProfile) RawProfile {
	rc.Provenance = initProvenance(rc)
	return rc
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/profile/ -run TestMergeProfilesStamps -v`
Expected: FAIL — `Provenance` field not populated by merge.

- [ ] **Step 3: Add Provenance field to RawProfile**

Modify `internal/profile/types.go`. Find the `RawProfile` struct definition (around line 343-349) and add `Provenance`:

```go
type RawProfile struct {
	Profile
	Namespace   string                    `yaml:"-"`
	Name        string                    `yaml:"-"`
	Path        string                    `yaml:"-"`
	NullKeys    map[string]map[string]bool `yaml:"-"`
	Provenance  Provenance                `yaml:"-"`
}
```

- [ ] **Step 4: Stamp provenance in each merge helper**

Modify `internal/profile/merge.go`. The existing `MergeProfiles` calls field-specific merge helpers (`mergeMounts`, `mergePortMap`, `mergeDeviceMap`, `mergeStringMap`, `mergeDbus`, `mergeMap` for Services) — not a single generic `mergeMap` for all. Each value merge is followed by a provenance merge that takes the *child's keys* (derived from the child value map for that field), not the value map itself. `mergeProvMap` takes `childKeys map[string]bool` plus the child contributor:

```go
// mergeProvMap merges parent/child provenance maps with the same key
// semantics as the value merges: child wins per key, nullKeys deletes.
// childKeys is the set of keys the child contributed for this field
// (built via keysOf from the child's value map); nullKeys["*"] clears
// everything.
func mergeProvMap(parent map[string]Contributor, childKeys map[string]bool, nullKeys map[string]bool, childContrib Contributor) map[string]Contributor {
	if nullKeys != nil && nullKeys["*"] {
		return map[string]Contributor{}
	}
	out := make(map[string]Contributor, len(parent)+len(childKeys))
	for k, v := range parent {
		out[k] = v
	}
	for k := range childKeys {
		out[k] = childContrib
	}
	for k := range nullKeys {
		delete(out, k)
	}
	return out
}

// keysOf returns the key set of m as a map[string]bool.
func keysOf[V any](m map[string]V) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}
```

Now in `MergeProfiles`, after each existing value-merge line, add the matching provenance-merge line. The child contributor is `Contributor{FullName: child.FullName(), Namespace: child.Namespace}`:

```go
childContrib := Contributor{FullName: child.FullName(), Namespace: child.Namespace}

// Mounts (mergeMounts wraps mergeMap):
out.Mounts = mergeMounts(parent.Mounts, child.Mounts, child.NullKeys["mounts"])
out.Provenance.Mounts = mergeProvMap(parent.Provenance.Mounts, keysOf(child.Mounts), child.NullKeys["mounts"], childContrib)

// Env (mergeStringMap):
out.Env = mergeStringMap(parent.Env, child.Env, child.NullKeys["environment"])
out.Provenance.Env = mergeProvMap(parent.Provenance.Env, keysOf(child.Env), child.NullKeys["environment"], childContrib)

// Ports (mergePortMap):
out.Ports = mergePortMap(parent.Ports, child.Ports, child.NullKeys["ports"])
out.Provenance.Ports = mergeProvMap(parent.Provenance.Ports, keysOf(child.Ports), child.NullKeys["ports"], childContrib)

// Devices (mergeDeviceMap):
out.Devices = mergeDeviceMap(parent.Devices, child.Devices, child.NullKeys["devices"])
out.Provenance.Devices = mergeProvMap(parent.Provenance.Devices, keysOf(child.Devices), child.NullKeys["devices"], childContrib)
```

For `Network` (scalar — last writer wins), stamp provenance only when the child actually set it; otherwise inherit the parent's:

```go
if child.Network != "" {
	out.Network = child.Network
	out.Provenance.Network = childContrib
} else {
	out.Provenance.Network = parent.Provenance.Network
}
```

For `Dbus`, `mergeDbus` may return nil (when talk and own both end up empty). Stamp provenance for the talk/own sub-maps from the child's dbus keys, honoring `nullKeys["talk"]` and `nullKeys["own"]` (those clear the sub-map's provenance) and `nullKeys["*"]` (clears both). When `mergeDbus` returns nil, the resulting provenance talk/own maps must be empty:

```go
out.Dbus = mergeDbus(parent.Dbus, child.Dbus, child.NullKeys["dbus"])
dbNull := child.NullKeys["dbus"]
if dbNull != nil && dbNull["*"] {
	// both sub-maps cleared
	out.Provenance.Dbus.Talk = map[string]Contributor{}
	out.Provenance.Dbus.Own = map[string]Contributor{}
} else {
	var childTalk, childOwn map[string]bool
	if child.Dbus != nil {
		childTalk = keysOf(child.Dbus.Talk)
		childOwn = keysOf(child.Dbus.Own)
	}
	if dbNull != nil && dbNull["talk"] {
		out.Provenance.Dbus.Talk = map[string]Contributor{}
	} else if childTalk != nil || parent.Provenance.Dbus.Talk != nil {
		out.Provenance.Dbus.Talk = mergeProvMap(parent.Provenance.Dbus.Talk, childTalk, nil, childContrib)
	}
	if dbNull != nil && dbNull["own"] {
		out.Provenance.Dbus.Own = map[string]Contributor{}
	} else if childOwn != nil || parent.Provenance.Dbus.Own != nil {
		out.Provenance.Dbus.Own = mergeProvMap(parent.Provenance.Dbus.Own, childOwn, nil, childContrib)
	}
	// If mergeDbus returned nil (both sub-maps empty in the value), there
	// are no dbus keys to attribute — leave provenance talk/own empty.
	if out.Dbus == nil {
		out.Provenance.Dbus = DbusProvenance{}
	}
}
```

For `Services` (`mergeMap` over `map[string]Service`):

```go
out.Services = mergeMap(parent.Services, child.Services, child.NullKeys["services"])
out.Provenance.Services = mergeProvMap(parent.Provenance.Services, keysOf(child.Services), child.NullKeys["services"], childContrib)
```

(Leave `out := parent` at the top of `MergeProfiles` as-is; `mergeProvMap` allocates fresh maps and never mutates `parent.Provenance` in place, so the shallow copy is safe.)

- [ ] **Step 5: Initialize provenance in resolveChain leaf case**

In `internal/profile/merge.go`, find `resolveChain` (line ~54). After the leaf return at line 62-63 (`if len(rc.ExtendsList.Resolved) == 0 { return rc, nil }`), add provenance init:

```go
	if len(rc.ExtendsList.Resolved) == 0 {
		rc.Provenance = initProvenance(rc)
		return rc, nil
	}
```

And at the end of `resolveChain` (after `merged = MergeProfiles(merged, rc)`, before `merged.Path = rc.Path`), the final merge with `rc` stamps provenance via the merge helpers. No extra init needed there.

- [ ] **Step 6: Run merge tests to verify they pass**

Run: `go test ./internal/profile/ -run TestMergeProfilesStamps -v`
Expected: PASS.

- [ ] **Step 7: Run the full profile package test suite**

Run: `go test ./internal/profile/ -v`
Expected: PASS (no regressions).

- [ ] **Step 8: Write the failing test for provenance across a manual merge chain**

Append to `internal/profile/extends_test.go`. This test asserts provenance via `MergeProfiles` directly (no `ResolveProfileWithProv`, which arrives in Task 3):

```go
func TestMergeChainAttributionAcrossExtends(t *testing.T) {
	// Simulate the extends chain myagent -> core/lang/typescript -> core/lang/javascript
	// by calling MergeProfiles twice, the way resolveChain does. Keys from
	// javascript should be attributed to core/lang/javascript, not to
	// typescript or myagent.
	js := RawProfile{
		Profile:   Profile{Env: map[string]string{"JS": "1"}},
		Namespace: "core", Name: "lang/javascript",
	}
	js.Provenance = initProvenance(js)
	ts := RawProfile{
		Profile:   Profile{Env: map[string]string{"TS": "1"}},
		Namespace: "core", Name: "lang/typescript",
	}
	ts.Provenance = initProvenance(ts)
	merged := MergeProfiles(RawProfile{}, js)
	merged = MergeProfiles(merged, ts)
	if merged.Provenance.Env["JS"] != (Contributor{FullName: "core/lang/javascript", Namespace: "core"}) {
		t.Errorf("JS should be attributed to core/lang/javascript, got %+v", merged.Provenance.Env["JS"])
	}
	if merged.Provenance.Env["TS"] != (Contributor{FullName: "core/lang/typescript", Namespace: "core"}) {
		t.Errorf("TS should be attributed to core/lang/typescript, got %+v", merged.Provenance.Env["TS"])
	}
}
```

- [ ] **Step 9: Run test to verify it passes**

Run: `go test ./internal/profile/ -run TestMergeChainAttribution -v`
Expected: PASS — provenance is stamped by `MergeProfiles`, which Step 4 wired up. (`ResolveProfileWithProv` is not used here; that comes in Task 3.)

- [ ] **Step 10: Commit (provenance merge is testable now; ResolveProfileWithProv comes in Task 3)**

```bash
git add internal/profile/types.go internal/profile/merge.go internal/profile/merge_test.go internal/profile/extends_test.go
git commit -m "feat(profile): stamp provenance during merge and leaf init"
```

---

## Task 3: ResolveProfileWithProv and the Resolved wrapper

**Files:**
- Modify: `internal/profile/types.go` (add `Resolved`)
- Modify: `internal/profile/merge.go` (add `ResolveProfileWithProv`, `ResolveFragmentWithProv`)
- Modify: `internal/profile/extends_test.go` (add the ResolveProfileWithProv attribution test)

**Interfaces:**
- Consumes: provenance-populated `RawProfile` from Task 2.
- Produces: `profile.Resolved` struct with `Profile`, `Prov Provenance`, `FullName string`, `DisplayName string`; `ResolveProfileWithProv(cat Catalog, name string) (Resolved, error)`.

- [ ] **Step 1: Add the Resolved type**

Modify `internal/profile/types.go`. Add after the `RawProfile` definition:

```go
// Resolved is a fully merged profile plus the provenance of its sensitive
// fields and the catalog identity of the resolved entry. Returned by
// ResolveProfileWithProv. ResolveProfile is a thin wrapper that discards
// provenance for callers that don't gate (tpd show --resolved, etc.).
type Resolved struct {
	Profile
	Prov        Provenance
	FullName    string
	DisplayName string
}
```

- [ ] **Step 2: Implement ResolveProfileWithProv**

Modify `internal/profile/merge.go`. Add after `ResolveProfile`:

```go
// ResolveProfileWithProv resolves name into a fully merged Profile with
// provenance and catalog identity. The FullName is the resolved catalog
// key (e.g. "core/opencode"); DisplayName is the unqualified name for
// human-facing output.
func ResolveProfileWithProv(cat Catalog, name string) (Resolved, error) {
	ref, err := cat.ParseRefForCatalog(name)
	if err != nil {
		return Resolved{}, ProfileError{Message: err.Error()}
	}
	key, ok := cat.ResolveRef(ref)
	if !ok {
		return Resolved{}, ProfileError{Message: "profile not found: " + name}
	}
	rc, _ := cat.Get(key)
	merged, err := resolveChain(cat, key, map[string]bool{})
	if err != nil {
		return Resolved{}, err
	}
	merged.Path = rc.Path
	for name, svc := range merged.Services {
		svc.Hash = computeServiceHash(svc)
		merged.Services[name] = svc
	}
	if err := validate(merged); err != nil {
		return Resolved{}, err
	}
	return Resolved{
		Profile:     merged.Profile,
		Prov:        merged.Provenance,
		FullName:    key,
		DisplayName: rc.DisplayName(),
	}, nil
}

// ResolveFragmentWithProv is the fragment analogue of ResolveProfileWithProv.
func ResolveFragmentWithProv(cat Catalog, name string) (Resolved, error) {
	ref, err := cat.ParseRefForCatalog(name)
	if err != nil {
		return Resolved{}, ProfileError{Message: err.Error()}
	}
	key, ok := cat.ResolveRef(ref)
	if !ok {
		return Resolved{}, ProfileError{Message: "fragment not found: " + name}
	}
	rc, _ := cat.Get(key)
	merged, err := resolveChain(cat, key, map[string]bool{})
	if err != nil {
		return Resolved{}, err
	}
	merged.Path = rc.Path
	return Resolved{
		Profile:     merged.Profile,
		Prov:        merged.Provenance,
		FullName:    key,
		DisplayName: rc.DisplayName(),
	}, nil
}
```

Update the existing `ResolveProfile` to delegate:

```go
func ResolveProfile(cat Catalog, name string) (Profile, error) {
	res, err := ResolveProfileWithProv(cat, name)
	if err != nil {
		return Profile{}, err
	}
	return res.Profile, nil
}

func ResolveFragment(cat Catalog, name string) (Profile, error) {
	res, err := ResolveFragmentWithProv(cat, name)
	if err != nil {
		return Profile{}, err
	}
	return res.Profile, nil
}
```

- [ ] **Step 3: Write the failing test for extends-chain provenance across ResolveProfileWithProv**

Append to `internal/profile/extends_test.go`. `NewProfileCatalogForTest` stamps `Namespace="core"` and `Name=<key>`, so the entry key *is* the `Name` (not `core/...`). `FullName()` then returns `"core/" + Name`. Use bare keys (`"lang/javascript"`, `"lang/typescript"`, `"myagent"`) so `FullName()` is `"core/lang/javascript"` etc.:

```go
func TestResolveChainAttributionAcrossExtends(t *testing.T) {
	// myagent extends core/lang/typescript extends core/lang/javascript.
	// Keys from javascript should be attributed to core/lang/javascript,
	// not to typescript or myagent.
	//
	// NewProfileCatalogForTest stamps Namespace="core"; Name=<map key>.
	// Use bare keys so FullName() == "core/" + key. The test is about
	// attribution across extends, not user-vs-core, so all three are
	// core entries.
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"lang/javascript": {
			Profile: Profile{
				Version: 1, Image: "img", Command: []string{"run"},
				Env: map[string]string{"JS": "1"},
			},
		},
		"lang/typescript": {
			Profile: Profile{
				Version: 1, Image: "img", Command: []string{"run"},
				ExtendsList: ExtendsList{Resolved: []Ref{{Namespace: "core", Name: "lang/javascript"}}},
				Env: map[string]string{"TS": "1"},
			},
		},
		"myagent": {
			Profile: Profile{
				Version: 1, Image: "img", Command: []string{"run"},
				ExtendsList: ExtendsList{Resolved: []Ref{{Namespace: "core", Name: "lang/typescript"}}},
			},
		},
	})
	res, err := ResolveProfileWithProv(cat, "core/myagent")
	if err != nil {
		t.Fatalf("ResolveProfileWithProv: %v", err)
	}
	if res.Prov.Env["JS"] != (Contributor{FullName: "core/lang/javascript", Namespace: "core"}) {
		t.Errorf("JS should be attributed to core/lang/javascript, got %+v", res.Prov.Env["JS"])
	}
	if res.Prov.Env["TS"] != (Contributor{FullName: "core/lang/typescript", Namespace: "core"}) {
		t.Errorf("TS should be attributed to core/lang/typescript, got %+v", res.Prov.Env["TS"])
	}
	if res.FullName != "core/myagent" {
		t.Errorf("FullName = %q, want core/myagent", res.FullName)
	}
}
```

Run: `go test ./internal/profile/ -run TestResolveChainAttribution -v`
Expected: PASS — `ResolveProfileWithProv` was implemented in Step 2, and `MergeProfiles` stamps provenance (Task 2 Step 4).

- [ ] **Step 4: Run the full profile package suite**

Run: `go test ./internal/profile/ -v`
Expected: PASS (no regressions; `tpd show --resolved` still works via the wrapper).

- [ ] **Step 5: Commit**

```bash
git add internal/profile/types.go internal/profile/merge.go
git commit -m "feat(profile): add ResolveProfileWithProv returning Resolved with provenance"
```

---

## Task 4: Approval hash

**Files:**
- Create: `internal/approval/hash.go`
- Create: `internal/approval/hash_test.go`

**Interfaces:**
- Consumes: `profile.Resolved` from Task 3.
- Produces: `approval.ComputeApprovalHash(res profile.Resolved) string`.

- [ ] **Step 1: Create the package and failing test**

Create `internal/approval/hash_test.go`:

```go
package approval

import (
	"testing"

	"github.com/jgillich/tpd/internal/profile"
)

func TestHashStableForSameContent(t *testing.T) {
	res := makeResolvedWithMounts(map[string]profile.Mount{
		"~/.ssh": {Source: "~/.ssh"},
	}, profile.Contributor{FullName: "core/creds/ssh", Namespace: "core"})
	h1 := ComputeApprovalHash(res)
	h2 := ComputeApprovalHash(res)
	if h1 != h2 {
		t.Errorf("hash not stable: %q vs %q", h1, h2)
	}
	if len(h1) != 12 {
		t.Errorf("hash length = %d, want 12", len(h1))
	}
}

func TestHashChangesOnContributorSwap(t *testing.T) {
	mounts := map[string]profile.Mount{"~/.ssh": {Source: "~/.ssh"}}
	a := makeResolvedWithMounts(mounts, profile.Contributor{FullName: "core/creds/ssh", Namespace: "core"})
	b := makeResolvedWithMounts(mounts, profile.Contributor{FullName: "github.com/foo/ssh", Namespace: "github.com/foo"})
	if ComputeApprovalHash(a) == ComputeApprovalHash(b) {
		t.Error("hash should differ when contributor identity differs")
	}
}

func TestHashExcludesUserContributions(t *testing.T) {
	res := makeResolvedWithMounts(map[string]profile.Mount{"~/x": {Source: "~/x"}}, profile.Contributor{FullName: "myagent", Namespace: ""})
	h := ComputeApprovalHash(res)
	// No non-user sensitive fields → empty hash input → deterministic.
	want := ComputeApprovalHash(profile.Resolved{})
	if h != want {
		t.Errorf("user-only contributions should not affect hash; got %q, want %q", h, want)
	}
}

func TestHashPreservesTemplateLiterals(t *testing.T) {
	res := makeResolvedWithMounts(map[string]profile.Mount{
		"{{ .Env.X }}": {Source: "{{ .Env.X }}"},
	}, profile.Contributor{FullName: "core/gui", Namespace: "core"})
	h := ComputeApprovalHash(res)
	if h == ComputeApprovalHash(profile.Resolved{}) {
		t.Error("templated mount should produce a non-empty hash")
	}
}

func TestHashServiceDefinitionChangeRePrompts(t *testing.T) {
	core := profile.Contributor{FullName: "core/services/podman", Namespace: "core"}
	base := profile.Resolved{
		Profile: profile.Profile{Services: map[string]profile.Service{
			"podman": {
				Image: "img", Command: []string{"run"},
				Privileged: true,
				Exposes:    map[string]string{"podman": "/run/podman/podman.sock"},
			},
		}},
		Prov: profile.Provenance{Services: map[string]profile.Contributor{"podman": core}},
	}
	hBase := ComputeApprovalHash(base)

	// privileged flip → different hash.
	flip := base
	flip.Services = map[string]profile.Service{
		"podman": {Image: "img", Command: []string{"run"}, Exposes: map[string]string{"podman": "/run/podman/podman.sock"}},
	}
	if ComputeApprovalHash(flip) == hBase {
		t.Error("hash should change when service privileged flips")
	}

	// new expose socket → different hash.
	newExp := base
	newExp.Services = map[string]profile.Service{
		"podman": {Image: "img", Command: []string{"run"}, Privileged: true, Exposes: map[string]string{
			"podman":  "/run/podman/podman.sock",
			"registry": "/run/podman/registry.sock",
		}},
	}
	if ComputeApprovalHash(newExp) == hBase {
		t.Error("hash should change when a service expose socket is added")
	}

	// new service mount key → different hash.
	newMnt := base
	newMnt.Services = map[string]profile.Service{
		"podman": {Image: "img", Command: []string{"run"}, Privileged: true,
			Exposes: map[string]string{"podman": "/run/podman/podman.sock"},
			Mounts:  map[string]profile.Mount{"/var/lib/containers": {Source: "/var/lib/containers"}},
		},
	}
	if ComputeApprovalHash(newMnt) == hBase {
		t.Error("hash should change when a service mount key is added")
	}

	// new service env key → different hash.
	newEnv := base
	newEnv.Services = map[string]profile.Service{
		"podman": {Image: "img", Command: []string{"run"}, Privileged: true,
			Exposes: map[string]string{"podman": "/run/podman/podman.sock"},
			Env:     map[string]string{"PODMAN": "1"},
		},
	}
	if ComputeApprovalHash(newEnv) == hBase {
		t.Error("hash should change when a service env key is added")
	}
}
```

Add a test helper at the top of the test file:

```go
func makeResolvedWithMounts(mounts map[string]profile.Mount, c profile.Contributor) profile.Resolved {
	p := profile.Profile{Mounts: mounts}
	prov := profile.Provenance{Mounts: map[string]profile.Contributor{}}
	for k := range mounts {
		prov.Mounts[k] = c
	}
	return profile.Resolved{Profile: p, Prov: prov}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/approval/ -v`
Expected: FAIL — package doesn't exist / `undefined: ComputeApprovalHash`.

- [ ] **Step 3: Implement ComputeApprovalHash**

Create `internal/approval/hash.go`:

```go
package approval

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/jgillich/tpd/internal/profile"
)

// ComputeApprovalHash returns a 12-hex-char hash of the non-user
// sensitive fields of res, pre-template-expansion. Contributor identity
// (FullName + Namespace) is included so approvals don't silently
// transfer across contributors with identical field values.
func ComputeApprovalHash(res profile.Resolved) string {
	h := sha256.New()
	emit := func(field, key string, c profile.Contributor, value string) {
		if c.Trusted() {
			return
		}
		fmt.Fprintf(h, "%s\n%s\n%s\n%s\n%s\n", field, key, c.FullName, c.Namespace, value)
	}

	for _, k := range sortedKeys(res.Mounts) {
		m := res.Mounts[k]
		c := res.Prov.Mounts[k]
		emit("mounts", k, c, fmt.Sprintf("mount %s %s %s %s %v %v %v", k, m.Source, m.Service, m.Socket, m.ReadOnly, m.Optional, m.Create))
	}
	for _, k := range sortedKeys(res.Devices) {
		d := res.Devices[k]
		c := res.Prov.Devices[k]
		emit("devices", k, c, fmt.Sprintf("device %s %s %s %v", k, d.Source, d.Permissions, d.Cgroup))
	}
	for _, k := range sortedKeys(res.Env) {
		c := res.Prov.Env[k]
		emit("env", k, c, fmt.Sprintf("env %s %s", k, res.Env[k]))
	}
	for _, k := range sortedKeys(res.Ports) {
		p := res.Ports[k]
		c := res.Prov.Ports[k]
		emit("ports", k, c, fmt.Sprintf("port %s %s %s %s", k, p.Host, p.HostIP, p.Protocol))
	}
	for _, k := range sortedKeys(res.Prov.Dbus.Talk) {
		emit("dbus.talk", k, res.Prov.Dbus.Talk[k], "talk")
	}
	for _, k := range sortedKeys(res.Prov.Dbus.Own) {
		emit("dbus.own", k, res.Prov.Dbus.Own[k], "own")
	}
	if res.Network != "" && !res.Prov.Network.Trusted() {
		emit("network", "", res.Prov.Network, res.Network)
	}
	for _, svcName := range sortedKeys(res.Services) {
		svc := res.Services[svcName]
		c := res.Prov.Services[svcName]
		if c.Trusted() {
			continue
		}
		fmt.Fprintf(h, "services\n%s\n%s\n%s\n%s\n", svcName, c.FullName, c.Namespace, renderServiceDefinition(svc))
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:])[:12]
}

// renderServiceDefinition builds a deterministic, sorted canonical string of
// the service's schema-valid sensitive sub-fields (privileged, exposes,
// the service's own mounts, env) — the shape the user is asked to approve
// under "use podman". A change to any of these (privileged flip, new expose
// socket, new service mount/env key) changes the rendered form and therefore
// the hash, re-prompting with the prior choice pre-checked. Devices, ports,
// dbus, and network never occur on services (validateServices rejects them)
// and are not rendered.
func renderServiceDefinition(svc profile.Service) string {
	var b strings.Builder
	fmt.Fprintf(&b, "privileged=%v;", svc.Privileged)
	exposes := sortedKeys(svc.Exposes)
	expParts := make([]string, 0, len(exposes))
	for _, k := range exposes {
		expParts = append(expParts, k+"="+svc.Exposes[k])
	}
	fmt.Fprintf(&b, "exposes={%s};", strings.Join(expParts, ","))
	mountKeys := sortedKeys(svc.Mounts)
	mntParts := make([]string, 0, len(mountKeys))
	for _, k := range mountKeys {
		m := svc.Mounts[k]
		mntParts = append(mntParts, k+"="+m.Source)
	}
	fmt.Fprintf(&b, "mounts={%s};", strings.Join(mntParts, ","))
	envKeys := sortedKeys(svc.Env)
	envParts := make([]string, 0, len(envKeys))
	for _, k := range envKeys {
		envParts = append(envParts, k+"="+svc.Env[k])
	}
	fmt.Fprintf(&b, "env={%s}", strings.Join(envParts, ","))
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/approval/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/approval/hash.go internal/approval/hash_test.go
git commit -m "feat(approval): add ComputeApprovalHash with contributor identity"
```

---

## Task 5: Approval Store (state file)

**Files:**
- Create: `internal/approval/store.go`
- Create: `internal/approval/store_test.go`

**Interfaces:**
- Produces: `approval.Store` interface; `State`, `ApprovedField` types; `FSStore` (file-system impl); per-component path validation; custom `MarshalYAML`/`UnmarshalYAML`.

- [ ] **Step 1: Write the failing test for round-trip and three-state semantics**

Create `internal/approval/store_test.go`:

```go
package approval

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewFSStore(dir)
	st := State{
		Profile: "ssh",
		Hash:    "abc123",
		Approved: map[string]ApprovedField{
			"mounts":   {Keys: []string{"~/.ssh"}},
			"network":  {Network: boolPtr(true)},
			"services": {Keys: []string{"podman"}},
		},
	}
	if err := s.Save("core/creds/ssh", st); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("core/creds/ssh")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Hash != st.Hash {
		t.Errorf("Hash = %q, want %q", got.Hash, st.Hash)
	}
	if len(got.Approved["mounts"].Keys) != 1 || got.Approved["mounts"].Keys[0] != "~/.ssh" {
		t.Errorf("mounts keys = %+v", got.Approved["mounts"].Keys)
	}
	if got.Approved["network"].Network == nil || !*got.Approved["network"].Network {
		t.Errorf("network should be approved")
	}
	if len(got.Approved["services"].Keys) != 1 || got.Approved["services"].Keys[0] != "podman" {
		t.Errorf("services keys = %+v, want [podman]", got.Approved["services"].Keys)
	}
}

func TestStoreMissingFileReturnsZero(t *testing.T) {
	s := NewFSStore(t.TempDir())
	got, err := s.Load("nope")
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if got.Hash != "" || len(got.Approved) != 0 {
		t.Errorf("missing file should return zero state, got %+v", got)
	}
}

func TestStoreRejectsBadFullName(t *testing.T) {
	s := NewFSStore(t.TempDir())
	bad := []string{"../etc/passwd", "a/../b", "a//b", "a\x00b"}
	for _, name := range bad {
		if err := s.Save(name, State{}); err == nil {
			t.Errorf("Save(%q) should fail", name)
		}
	}
}

func TestStoreNestedFullName(t *testing.T) {
	dir := t.TempDir()
	s := NewFSStore(dir)
	if err := s.Save("core/creds/ssh", State{Hash: "h"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	full := filepath.Join(dir, "approvals", "core", "creds", "ssh.yaml")
	if _, err := os.Stat(full); err != nil {
		t.Errorf("expected file at %s: %v", full, err)
	}
}

func boolPtr(b bool) *bool { return &b }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/approval/ -run TestStore -v`
Expected: FAIL — `undefined: NewFSStore`, `State`, etc.

- [ ] **Step 3: Implement the Store**

Create `internal/approval/store.go`:

```go
package approval

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Store persists per-profile approval choices.
type Store interface {
	Load(profileName string) (State, error)
	Save(profileName string, s State) error
}

type State struct {
	Profile  string
	Hash     string
	Approved map[string]ApprovedField
}

// ApprovedField represents one field's approved set. Map fields use Keys;
// the scalar network uses Network (nil = never decided, true = approved,
// false = denied).
type ApprovedField struct {
	Keys    []string
	Network *bool
}

var nameSegRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// FSStore writes state files under <root>/approvals/<FullName>.yaml.
type FSStore struct {
	root string
}

func NewFSStore(root string) *FSStore {
	return &FSStore{root: root}
}

func (s *FSStore) pathFor(fullName string) (string, error) {
	segs := splitPath(fullName)
	for _, seg := range segs {
		if seg == "" || seg == ".." || strings.Contains(seg, "..") || !nameSegRe.MatchString(seg) {
			return "", fmt.Errorf("invalid profile name segment %q in %q", seg, fullName)
		}
	}
	return filepath.Join(s.root, "approvals", filepath.FromSlash(fullName)+".yaml"), nil
}

func splitPath(p string) []string {
	var segs []string
	cur := ""
	for _, r := range p {
		if r == '/' {
			segs = append(segs, cur)
			cur = ""
		} else {
			cur += string(r)
		}
	}
	segs = append(segs, cur)
	return segs
}

func (s *FSStore) Load(fullName string) (State, error) {
	path, err := s.pathFor(fullName)
	if err != nil {
		return State{}, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var st State
	if err := yaml.Unmarshal(data, &st); err != nil {
		return State{}, err
	}
	return st, nil
}

func (s *FSStore) Save(fullName string, st State) error {
	path, err := s.pathFor(fullName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/approval/ -run TestStore -v`
Expected: PASS.

- [ ] **Step 5: Write the test for custom MarshalYAML (three-state)**

Append to `store_test.go`:

```go
func TestStateMarshalDistinguishesDeniedFromAbsent(t *testing.T) {
	st := State{
		Approved: map[string]ApprovedField{
			"mounts": {Keys: []string{"~/.ssh"}},          // field present, one approved
			"devices": {Keys: nil},                          // field present, all denied
			// "env" absent → never decided
		},
	}
	data, err := yaml.Marshal(st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(data)
	if !contains(s, "mounts:") || !contains(s, "devices:") {
		t.Errorf("denied field should be present in YAML:\n%s", s)
	}
	if contains(s, "env:") {
		t.Errorf("absent field should be missing from YAML:\n%s", s)
	}
	// Round-trip
	var back State
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := back.Approved["devices"]; !ok {
		t.Error("denied field should survive round-trip as present-with-empty")
	}
	if _, ok := back.Approved["env"]; ok {
		t.Error("absent field should stay absent after round-trip")
	}
}

func TestStateMarshalNestsDbusOnly(t *testing.T) {
	yes := true
	st := State{
		Approved: map[string]ApprovedField{
			"dbus.talk": {Keys: []string{"org.freedesktop.portal.Desktop"}},
			"dbus.own":  {Keys: nil},
			"network":   {Network: &yes},
			"services":  {Keys: []string{"podman"}},
		},
	}
	data, err := yaml.Marshal(st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(data)
	// dbus is the only nested field.
	for _, want := range []string{"dbus:", "talk:", "network:", "services:"} {
		if !contains(s, want) {
			t.Errorf("YAML should contain key %q:\n%s", want, s)
		}
	}
	// dbus sub-fields must be nested, not flat dotted keys.
	if contains(s, "dbus.talk:") {
		t.Errorf("YAML should not contain flat dotted key dbus.talk:\n%s", s)
	}
	// services is a flat list of approved names, not a nested per-sub-field map.
	if contains(s, "services.podman.") {
		t.Errorf("YAML should not contain nested services.<name>.<field> keys:\n%s", s)
	}
	// Map fields marshal as bare lists (mounts: [~/.ssh]), not nested under keys:.
	if contains(s, "keys:") {
		t.Errorf("YAML should not contain a nested keys: field (map fields are bare lists):\n%s", s)
	}
	// Round-trip preserves the flat keyed State.
	var back State
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.Approved["dbus.talk"].Keys[0] != "org.freedesktop.portal.Desktop" {
		t.Errorf("dbus.talk round-trip failed: %+v", back.Approved["dbus.talk"])
	}
	if back.Approved["services"].Keys[0] != "podman" {
		t.Errorf("services round-trip failed: %+v", back.Approved["services"])
	}
	if back.Approved["network"].Network == nil || !*back.Approved["network"].Network {
		t.Errorf("network round-trip failed: %+v", back.Approved["network"])
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
```

(Add `"strings"` to the imports.)

- [ ] **Step 6: Run test to verify it fails**

Run: `go test ./internal/approval/ -run TestStateMarshal -v`
Expected: FAIL — nil slice marshals as `[]` but round-trips as empty, and absent vs empty may not distinguish cleanly depending on yaml.v3 behavior. The custom marshal must make it work.

- [ ] **Step 7: Add custom MarshalYAML/UnmarshalYAML to State**

Add to `internal/approval/store.go`:

```go
// yamlState is the on-disk representation. A field present in Approved
// with nil Keys means "field present, all denied"; a field absent from
// the map means "never decided". This distinction must survive
// round-trip, so State has custom marshal/unmarshal.
//
// The on-disk shape nests dbus sub-fields per spec §4:
//   dbus: { talk: [...], own: [...] }
// services is a flat list of approved service names (coarse model: one
// item per service, Key = service name), handled by the generic flat
// path like mounts/env/ports. Only dbus retains nested marshaling. The
// Go State.Approved map keys are flat (e.g. "dbus.talk", "services").
type yamlState struct {
	Profile  string       `yaml:"profile,omitempty"`
	Hash     string       `yaml:"hash"`
	Approved yamlApproved `yaml:"approved,omitempty"`
}

// yamlApproved is the top-level "approved:" block. Top-level scalar/map
// fields are keyed directly; dbus is the only nested sub-object.
type yamlApproved struct {
	Mounts   *yamlField `yaml:"mounts,omitempty"`
	Devices  *yamlField `yaml:"devices,omitempty"`
	Env      *yamlField `yaml:"env,omitempty"`
	Ports    *yamlField `yaml:"ports,omitempty"`
	Network  *bool      `yaml:"network,omitempty"`
	Dbus     *yamlDbus  `yaml:"dbus,omitempty"`
	Services *yamlField `yaml:"services,omitempty"`
}

// yamlField is a pointer-wrapped []string so the three-state distinction
// survives round-trip: a nil pointer (the field is absent from yamlApproved)
// means "never decided"; a non-nil pointer with an empty slice means
// "field present, all denied"; a non-nil pointer with items means
// "approved". It marshals as a bare YAML list (not nested under keys:) to
// match the spec's human-readable on-disk shape (mounts: [~/.ssh], not
// mounts: {keys: [~/.ssh]}).
type yamlField []string

// ptrField wraps an ApprovedField's Keys in a non-nil *yamlField so
// "present, all denied" (empty slice) emits an explicit key rather than
// being omitted by omitempty on the parent pointer.
func ptrField(af ApprovedField) *yamlField {
	f := yamlField(af.Keys)
	return &f
}

func (f *yamlField) UnmarshalYAML(unmarshal func(interface{}) error) error {
	return unmarshal((*[]string)(f))
}

type yamlDbus struct {
	Talk *yamlField `yaml:"talk,omitempty"`
	Own  *yamlField `yaml:"own,omitempty"`
}

func (s State) MarshalYAML() (interface{}, error) {
	out := yamlState{Profile: s.Profile, Hash: s.Hash}
	a := yamlApproved{}
	for k, v := range s.Approved {
		switch k {
		case "mounts":
			a.Mounts = ptrField(v)
		case "devices":
			a.Devices = ptrField(v)
		case "env":
			a.Env = ptrField(v)
		case "ports":
			a.Ports = ptrField(v)
		case "network":
			a.Network = v.Network
		case "services":
			a.Services = ptrField(v)
		case "dbus.talk":
			if a.Dbus == nil {
				a.Dbus = &yamlDbus{}
			}
			a.Dbus.Talk = ptrField(v)
		case "dbus.own":
			if a.Dbus == nil {
				a.Dbus = &yamlDbus{}
			}
			a.Dbus.Own = ptrField(v)
		}
	}
	out.Approved = a
	return out, nil
}

func (s *State) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var y yamlState
	if err := unmarshal(&y); err != nil {
		return err
	}
	s.Profile = y.Profile
	s.Hash = y.Hash
	s.Approved = map[string]ApprovedField{}
	a := y.Approved
	if a.Mounts != nil {
		s.Approved["mounts"] = ApprovedField{Keys: *a.Mounts}
	}
	if a.Devices != nil {
		s.Approved["devices"] = ApprovedField{Keys: *a.Devices}
	}
	if a.Env != nil {
		s.Approved["env"] = ApprovedField{Keys: *a.Env}
	}
	if a.Ports != nil {
		s.Approved["ports"] = ApprovedField{Keys: *a.Ports}
	}
	if a.Network != nil {
		s.Approved["network"] = ApprovedField{Network: a.Network}
	}
	if a.Services != nil {
		s.Approved["services"] = ApprovedField{Keys: *a.Services}
	}
	if a.Dbus != nil {
		if a.Dbus.Talk != nil {
			s.Approved["dbus.talk"] = ApprovedField{Keys: *a.Dbus.Talk}
		}
		if a.Dbus.Own != nil {
			s.Approved["dbus.own"] = ApprovedField{Keys: *a.Dbus.Own}
		}
	}
	return nil
}
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./internal/approval/ -run TestStateMarshal -v`
Expected: PASS.

- [ ] **Step 9: Run the full approval package suite**

Run: `go test ./internal/approval/ -v`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/approval/store.go internal/approval/store_test.go
git commit -m "feat(approval): add FSStore with three-state YAML and path validation"
```

---

## Task 6: Approval Filter

**Files:**
- Create: `internal/approval/approval.go`
- Create: `internal/approval/approval_test.go`

**Interfaces:**
- Consumes: `profile.Resolved` (Task 3), `ComputeApprovalHash` (Task 4), `Store`/`State` (Task 5).
- Produces: `SensitiveItem`, `PromptRequest`, `Filter(res profile.Resolved, store Store) (profile.Profile, PromptRequest, error)`, `EphemeralStore`, `ReadOnlyStore`.

- [ ] **Step 1: Write the failing test for the no-prompt short-circuit**

Create `internal/approval/approval_test.go`:

```go
package approval

import (
	"testing"

	"github.com/jgillich/tpd/internal/profile"
)

func TestFilterNoSensitiveFieldsNoPrompt(t *testing.T) {
	res := profile.Resolved{Profile: profile.Profile{Image: "img", Command: []string{"run"}}}
	store := &memStore{}
	got, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 0 {
		t.Errorf("no sensitive fields → no prompt items, got %d", len(req.Items))
	}
	if got.Image != "img" {
		t.Errorf("filtered profile should be unchanged, got %+v", got)
	}
}

func TestFilterAllUserSensitiveNoPrompt(t *testing.T) {
	res := profile.Resolved{
		Profile: profile.Profile{Mounts: map[string]profile.Mount{"~/x": {Source: "~/x"}}},
		Prov: profile.Provenance{Mounts: map[string]profile.Contributor{
			"~/x": {FullName: "myagent", Namespace: ""},
		}},
		FullName: "myagent",
	}
	store := &memStore{}
	_, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 0 {
		t.Errorf("all-user sensitive fields → no prompt items, got %d", len(req.Items))
	}
}

func TestFilterCoreSensitiveProducesPrompt(t *testing.T) {
	res := profile.Resolved{
		Profile: profile.Profile{Mounts: map[string]profile.Mount{"~/.ssh": {Source: "~/.ssh"}}},
		Prov: profile.Provenance{Mounts: map[string]profile.Contributor{
			"~/.ssh": {FullName: "core/creds/ssh", Namespace: "core"},
		}},
		FullName: "myagent", DisplayName: "myagent",
	}
	store := &memStore{}
	_, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 1 || req.Items[0].Key != "~/.ssh" {
		t.Errorf("expected one prompt item for ~/.ssh, got %+v", req.Items)
	}
}

func TestFilterStoredApprovalNoPrompt(t *testing.T) {
	res := profile.Resolved{
		Profile: profile.Profile{Mounts: map[string]profile.Mount{"~/.ssh": {Source: "~/.ssh"}}},
		Prov: profile.Provenance{Mounts: map[string]profile.Contributor{
			"~/.ssh": {FullName: "core/creds/ssh", Namespace: "core"},
		}},
		FullName: "myagent",
	}
	h := ComputeApprovalHash(res)
	store := &memStore{state: map[string]State{
		"myagent": {Hash: h, Approved: map[string]ApprovedField{
			"mounts": {Keys: []string{"~/.ssh"}},
		}},
	}}
	_, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 0 {
		t.Errorf("stored approval should produce no prompt, got %d items", len(req.Items))
	}
}

func TestFilterDeniedKeyDroppedFromProfile(t *testing.T) {
	res := profile.Resolved{
		Profile: profile.Profile{Mounts: map[string]profile.Mount{
			"~/.ssh": {Source: "~/.ssh"},
			"~/aws":  {Source: "~/aws"},
		}},
		Prov: profile.Provenance{Mounts: map[string]profile.Contributor{
			"~/.ssh": {FullName: "core/creds/ssh", Namespace: "core"},
			"~/aws":  {FullName: "core/creds/aws", Namespace: "core"},
		}},
		FullName: "myagent",
	}
	h := ComputeApprovalHash(res)
	store := &memStore{state: map[string]State{
		"myagent": {Hash: h, Approved: map[string]ApprovedField{
			"mounts": {Keys: []string{"~/.ssh"}}, // ~/.ssh approved, ~/aws denied (absent)
		}},
	}}
	got, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 0 {
		t.Errorf("all keys have stored choices, got %d items", len(req.Items))
	}
	if _, ok := got.Mounts["~/aws"]; ok {
		t.Error("denied key ~/aws should be dropped from filtered profile")
	}
	if _, ok := got.Mounts["~/.ssh"]; !ok {
		t.Error("approved key ~/.ssh should remain")
	}
}

// memStore is an in-memory Store for tests.
type memStore struct {
	state map[string]State
}

func (m *memStore) Load(name string) (State, error) {
	if m.state == nil {
		m.state = map[string]State{}
	}
	return m.state[name], nil
}
func (m *memStore) Save(name string, s State) error {
	if m.state == nil {
		m.state = map[string]State{}
	}
	m.state[name] = s
	return nil
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/approval/ -run TestFilter -v`
Expected: FAIL — `undefined: Filter`, `SensitiveItem`, `PromptRequest`.

- [ ] **Step 3: Implement Filter**

Create `internal/approval/approval.go`:

```go
package approval

import (
	"fmt"

	"github.com/jgillich/tpd/internal/profile"
)

// SensitiveItem is one gated key the user must decide on.
type SensitiveItem struct {
	Field  string
	Key    string
	Value  string
	Source profile.Contributor
}

// PromptRequest is what the dialog renders. Empty Items = no prompt.
type PromptRequest struct {
	ProfileName  string
	FullName     string
	Hash         string
	Items        []SensitiveItem
	PriorChoices map[string]map[string]bool
}

// Filter returns the profile with denied/dropped fields removed and a
// PromptRequest describing any still-unapproved sensitive fields.
func Filter(res profile.Resolved, store Store) (profile.Profile, PromptRequest, error) {
	hash := ComputeApprovalHash(res)
	st, err := store.Load(res.FullName)
	if err != nil {
		return profile.Profile{}, PromptRequest{}, err
	}
	// Reconcile: if hash matches, drop stored keys that no longer exist
	// in the profile. Persist if the state changed.
	reconciled, changed := reconcileState(st, hash, res)
	if changed {
		if err := store.Save(res.FullName, reconciled); err != nil {
			return profile.Profile{}, PromptRequest{}, err
		}
	}

	filtered := res.Profile
	req := PromptRequest{
		ProfileName:  res.DisplayName,
		FullName:     res.FullName,
		Hash:         hash,
		PriorChoices: priorChoices(reconciled),
		Items:        nil,
	}

	// Walk sensitive fields; drop denied, collect unapproved into Items.
	filtered.Mounts, req = applyMountField(filtered.Mounts, res.Prov.Mounts, "mounts", reconciled, req)
	filtered.Devices, req = applyDeviceField(filtered.Devices, res.Prov.Devices, "devices", reconciled, req)
	filtered.Env, req = applyEnvField(filtered.Env, res.Prov.Env, "env", reconciled, req)
	filtered.Ports, req = applyPortField(filtered.Ports, res.Prov.Ports, "ports", reconciled, req)
	filtered.Dbus, req = applyDbusField(filtered.Dbus, res.Prov.Dbus, reconciled, req)
	filtered.Network, req = applyNetworkField(filtered.Network, res.Prov.Network, "network", reconciled, req)
	filtered.Services, req = applyServicesField(filtered.Services, res.Prov.Services, reconciled, req)

	// Dependent-mount cascade: drop top-level mounts whose service was
	// denied (absent from filtered.Services).
	filtered.Mounts = cascadeDependentMounts(filtered.Mounts, filtered.Services)

	return filtered, req, nil
}

// reconcileState drops stored keys that no longer exist in res when the
// stored hash matches the current hash. Returns the reconciled state and
// whether it changed. Network is a scalar stored in ApprovedField.Network;
// reconcileKeys skips it and it is handled inline. Services is a flat map
// field (Keys = service names) and reconciles like mounts/env/ports.
func reconcileState(st State, hash string, res profile.Resolved) (State, bool) {
	if st.Hash != hash {
		return st, false
	}
	changed := false
	approve := st.Approved
	maybeSet := func(field string, af ApprovedField, ch bool) {
		if ch {
			approve[field] = af
			changed = true
		}
	}

	if af, ok := approve["mounts"]; ok {
		n, ch := reconcileKeys(af, res.Mounts)
		maybeSet("mounts", n, ch)
	}
	if af, ok := approve["devices"]; ok {
		n, ch := reconcileKeys(af, res.Devices)
		maybeSet("devices", n, ch)
	}
	if af, ok := approve["env"]; ok {
		n, ch := reconcileKeys(af, res.Env)
		maybeSet("env", n, ch)
	}
	if af, ok := approve["ports"]; ok {
		n, ch := reconcileKeys(af, res.Ports)
		maybeSet("ports", n, ch)
	}
	if af, ok := approve["dbus.talk"]; ok {
		talk := map[string]struct{}{}
		if res.Dbus != nil {
			for k := range res.Dbus.Talk {
				talk[k] = struct{}{}
			}
		}
		n, ch := reconcileKeys(af, talk)
		maybeSet("dbus.talk", n, ch)
	}
	if af, ok := approve["dbus.own"]; ok {
		own := map[string]struct{}{}
		if res.Dbus != nil {
			for k := range res.Dbus.Own {
				own[k] = struct{}{}
			}
		}
		n, ch := reconcileKeys(af, own)
		maybeSet("dbus.own", n, ch)
	}
	// network is a scalar stored in ApprovedField.Network; nothing to
	// reconcile by key. It is dropped only when the hash changes.
	if af, ok := approve["services"]; ok {
		n, ch := reconcileKeys(af, res.Services)
		maybeSet("services", n, ch)
	}
	st.Approved = approve
	return st, changed
}

// reconcileKeys drops af.Keys entries that are not present in current and
// reports whether anything was dropped. The Network scalar slot is skipped
// (handled separately).
func reconcileKeys[V any](af ApprovedField, current map[string]V) (ApprovedField, bool) {
	if af.Network != nil {
		return af, false
	}
	currentKeys := map[string]bool{}
	for k := range current {
		currentKeys[k] = true
	}
	kept := af.Keys[:0]
	changed := false
	for _, k := range af.Keys {
		if currentKeys[k] {
			kept = append(kept, k)
		} else {
			changed = true
		}
	}
	af.Keys = kept
	return af, changed
}

// cascadeDependentMounts drops top-level mounts referencing a service that
// was denied (absent from the filtered services map). A denied service is
// gone from cfg.Services, so its socket mounts must also be dropped —
// validateMountServices (validate.go:374-382) would otherwise fail at
// service binding, and the main container cannot bind a socket the service
// isn't exposing to it. A kept service keeps all its exposes intact, so its
// socket mounts survive.
func cascadeDependentMounts(mounts map[string]profile.Mount, services map[string]profile.Service) map[string]profile.Mount {
	for target, m := range mounts {
		if m.Service == "" {
			continue
		}
		if _, ok := services[m.Service]; !ok {
			delete(mounts, target)
		}
	}
	return mounts
}

func priorChoices(st State) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for field, af := range st.Approved {
		set := map[string]bool{}
		for _, k := range af.Keys {
			set[k] = true
		}
		// The network scalar is stored in ApprovedField.Network (*bool);
		// surface it so the dialog can pre-check a previously-approved
		// scalar on re-prompt.
		if af.Network != nil {
			set[""] = *af.Network
		}
		out[field] = set
	}
	return out
}
```

The per-field `apply*Field` helpers are written in Step 4.

- [ ] **Step 4: Complete the per-field walk to make the tests pass**

Add the per-field `apply*Field` functions and helpers below to `internal/approval/approval.go`. Each map field uses the same keep/drop/prompt decision: a trusted contributor (or a missing contributor) is kept ungated; a non-user contributor is kept if its key is in the stored approved set for the current hash, dropped if the field has stored state for this hash but the key is absent (denied), and prompted otherwise (kept in the profile until the dialog resolves). The network scalar uses a `*bool` slot. Services are coarse — one gated item per service, field `"services"`, Key = service name: approve keeps the whole service; deny drops it from the output map, and the cascade step in `Filter` then drops top-level mounts referencing the denied service.

```go
// decide is the shared keep/drop/prompt decision for one non-user key.
// Returns keep=true if the key should remain in the filtered profile, and
// appends a SensitiveItem to req.Items when the user must still decide.
//
// NOTE: a missing provenance entry (c is the zero value, Trusted()) is
// treated as user-trusted and skips the gate. This is deliberate fail-open
// — a provenance bug must not brick launches — but it means a provenance
// regression in the merge would silently bypass the approval gate. The
// provenance unit tests in internal/profile guard against that.
func decide(field, key, value string, c profile.Contributor, st State, req PromptRequest) (bool, PromptRequest) {
	if c.Trusted() {
		return true, req
	}
	// Hash mismatch or no stored state for this field → prompt.
	af, hasField := st.Approved[field]
	if st.Hash != req.Hash || !hasField {
		req.Items = append(req.Items, item(field, key, value, c))
		return true, req
	}
	if containsKey(af.Keys, key) {
		return true, req
	}
	return false, req
}

func applyMountField(mounts map[string]profile.Mount, prov map[string]profile.Contributor, field string, st State, req PromptRequest) (map[string]profile.Mount, PromptRequest) {
	out := make(map[string]profile.Mount, len(mounts))
	for k, v := range mounts {
		c := prov[k]
		keep, r := decide(field, k, renderMount(v), c, st, req)
		req = r
		if keep {
			out[k] = v
		}
	}
	return out, req
}

func applyDeviceField(devices map[string]profile.DeviceBind, prov map[string]profile.Contributor, field string, st State, req PromptRequest) (map[string]profile.DeviceBind, PromptRequest) {
	out := make(map[string]profile.DeviceBind, len(devices))
	for k, v := range devices {
		c := prov[k]
		keep, r := decide(field, k, renderDevice(v), c, st, req)
		req = r
		if keep {
			out[k] = v
		}
	}
	return out, req
}

func applyEnvField(env map[string]string, prov map[string]profile.Contributor, field string, st State, req PromptRequest) (map[string]string, PromptRequest) {
	out := make(map[string]string, len(env))
	for k, v := range env {
		c := prov[k]
		keep, r := decide(field, k, renderEnv(k, v), c, st, req)
		req = r
		if keep {
			out[k] = v
		}
	}
	return out, req
}

func applyPortField(ports map[string]profile.PortBind, prov map[string]profile.Contributor, field string, st State, req PromptRequest) (map[string]profile.PortBind, PromptRequest) {
	out := make(map[string]profile.PortBind, len(ports))
	for k, v := range ports {
		c := prov[k]
		keep, r := decide(field, k, renderPort(v), c, st, req)
		req = r
		if keep {
			out[k] = v
		}
	}
	return out, req
}

func applyDbusField(d *profile.DbusConfig, prov profile.DbusProvenance, st State, req PromptRequest) (*profile.DbusConfig, PromptRequest) {
	if d == nil {
		return d, req
	}
	out := &profile.DbusConfig{}
	if len(d.Talk) > 0 {
		out.Talk = make(map[string]*struct{}, len(d.Talk))
		for k, v := range d.Talk {
			c := prov.Talk[k]
			keep, r := decide("dbus.talk", k, "talk", c, st, req)
			req = r
			if keep {
				out.Talk[k] = v
			}
		}
	}
	if len(d.Own) > 0 {
		out.Own = make(map[string]*struct{}, len(d.Own))
		for k, v := range d.Own {
			c := prov.Own[k]
			keep, r := decide("dbus.own", k, "own", c, st, req)
			req = r
			if keep {
				out.Own[k] = v
			}
		}
	}
	if len(out.Talk) == 0 && len(out.Own) == 0 {
		return nil, req
	}
	return out, req
}

// applyNetworkField gates the scalar network value. The stored choice is
// kept in ApprovedField.Network (*bool): nil → prompt, true → keep, false →
// drop (set to ""). The item key for network is the empty string.
func applyNetworkField(v string, c profile.Contributor, field string, st State, req PromptRequest) (string, PromptRequest) {
	if v == "" || c.Trusted() {
		return v, req
	}
	af, hasField := st.Approved[field]
	if st.Hash == req.Hash && hasField && af.Network != nil {
		if *af.Network {
			return v, req
		}
		return "", req
	}
	req.Items = append(req.Items, item(field, "", v, c))
	return v, req
}

// applyServicesField gates each service as a single coarse item under the
// service's one contributor. Field is "services", Key = service name, Value =
// the rendered service definition (privileged, exposes, mounts, env). A
// trusted contributor (or a missing one) keeps the service ungated. A denied
// service is dropped from the output map, and the cascade step in Filter then
// drops top-level mounts referencing it. A kept service keeps its full
// definition — the shared daemon is never filtered.
func applyServicesField(services map[string]profile.Service, prov map[string]profile.Contributor, st State, req PromptRequest) (map[string]profile.Service, PromptRequest) {
	out := make(map[string]profile.Service, len(services))
	for name, svc := range services {
		c := prov[name]
		keep, r := decide("services", name, renderServiceDefinition(svc), c, st, req)
		req = r
		if keep {
			out[name] = svc
		}
	}
	return out, req
}

func item(field, key, value string, source profile.Contributor) SensitiveItem {
	return SensitiveItem{Field: field, Key: key, Value: value, Source: source}
}

func renderMount(m profile.Mount) string {
	return fmt.Sprintf("mount %s %s %v %v", m.Source, m.Service, m.ReadOnly, m.Optional)
}

func renderDevice(d profile.DeviceBind) string {
	return fmt.Sprintf("device %s %s", d.Source, d.Permissions)
}

func renderEnv(k, v string) string {
	return fmt.Sprintf("env %s=%s", k, v)
}

func renderPort(p profile.PortBind) string {
	return fmt.Sprintf("port %s %s %s", p.Host, p.HostIP, p.Protocol)
}

func containsKey(keys []string, k string) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run the filter tests**

Run: `go test ./internal/approval/ -run TestFilter -v`
Expected: PASS.

- [ ] **Step 6: Write the test for reconciliation persistence**

Append to `approval_test.go`:

```go
func TestFilterReconcilesAndPersists(t *testing.T) {
	res := profile.Resolved{
		Profile: profile.Profile{Mounts: map[string]profile.Mount{"~/.ssh": {Source: "~/.ssh"}}},
		Prov: profile.Provenance{Mounts: map[string]profile.Contributor{
			"~/.ssh": {FullName: "core/creds/ssh", Namespace: "core"},
		}},
		FullName: "myagent",
	}
	h := ComputeApprovalHash(res)
	// State has a stale key ~/aws that's no longer in the profile.
	store := &memStore{state: map[string]State{
		"myagent": {Hash: h, Approved: map[string]ApprovedField{
			"mounts": {Keys: []string{"~/.ssh", "~/aws"}},
		}},
	}}
	_, _, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	saved := store.state["myagent"]
	if containsKey(saved.Approved["mounts"].Keys, "~/aws") {
		t.Error("stale key ~/aws should be dropped from persisted state")
	}
}
```

- [ ] **Step 7: Run test and complete reconcileState**

Run: `go test ./internal/approval/ -run TestFilterReconciles -v`
Expected: may FAIL — complete `reconcileState` to actually drop stale keys and set `changed = true` when it does. The implementation must check each stored field's keys against the current profile's keys for that field and remove orphans.

- [ ] **Step 8: Write the test for the dependent-mount cascade**

Append to `approval_test.go`:

```go
func TestFilterCoarseServiceDenyCascadesMounts(t *testing.T) {
	res := profile.Resolved{
		Profile: profile.Profile{
			Services: map[string]profile.Service{
				"podman": {
					Image: "img", Command: []string{"run"},
					Exposes: map[string]string{"registry": "/run/podman.sock"},
				},
			},
			Mounts: map[string]profile.Mount{
				"/run/podman.sock": {Service: "podman", Socket: "registry"},
			},
		},
		Prov: profile.Provenance{
			Services: map[string]profile.Contributor{
				"podman": {FullName: "core/services/podman", Namespace: "core"},
			},
		},
		FullName: "myagent",
	}
	h := ComputeApprovalHash(res)
	// Deny the service: "services" field present in state, hash matches,
	// "podman" absent from Keys → denied → dropped from cfg.Services.
	store := &memStore{state: map[string]State{
		"myagent": {Hash: h, Approved: map[string]ApprovedField{
			"services": {Keys: nil}, // all services denied
		}},
	}}
	got, _, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if _, ok := got.Services["podman"]; ok {
		t.Error("denied service should be dropped from filtered Services")
	}
	if _, ok := got.Mounts["/run/podman.sock"]; ok {
		t.Error("dependent mount should be cascaded off when its service is denied")
	}
}

func TestFilterCoarseServiceApproveKeepsService(t *testing.T) {
	res := profile.Resolved{
		Profile: profile.Profile{
			Services: map[string]profile.Service{
				"podman": {
					Image: "img", Command: []string{"run"},
					Exposes: map[string]string{"registry": "/run/podman.sock"},
				},
			},
			Mounts: map[string]profile.Mount{
				"/run/podman.sock": {Service: "podman", Socket: "registry"},
			},
		},
		Prov: profile.Provenance{
			Services: map[string]profile.Contributor{
				"podman": {FullName: "core/services/podman", Namespace: "core"},
			},
		},
		FullName: "myagent",
	}
	h := ComputeApprovalHash(res)
	// Approve the service: "podman" in Keys → kept.
	store := &memStore{state: map[string]State{
		"myagent": {Hash: h, Approved: map[string]ApprovedField{
			"services": {Keys: []string{"podman"}},
		}},
	}}
	got, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 0 {
		t.Errorf("approved service should produce no prompt, got %d items", len(req.Items))
	}
	if _, ok := got.Services["podman"]; !ok {
		t.Error("approved service should remain in filtered Services")
	}
	if _, ok := got.Mounts["/run/podman.sock"]; !ok {
		t.Error("dependent mount should remain when its service is approved")
	}
}

func TestFilterCoarseServicePromptItemShape(t *testing.T) {
	res := profile.Resolved{
		Profile: profile.Profile{
			Services: map[string]profile.Service{
				"podman": {
					Image: "img", Command: []string{"run"}, Privileged: true,
					Exposes: map[string]string{"podman": "/run/podman/podman.sock"},
				},
			},
		},
		Prov: profile.Provenance{
			Services: map[string]profile.Contributor{
				"podman": {FullName: "core/services/podman", Namespace: "core"},
			},
		},
		FullName: "myagent",
	}
	store := &memStore{}
	_, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 1 {
		t.Fatalf("expected one prompt item for the service, got %d", len(req.Items))
	}
	it := req.Items[0]
	if it.Field != "services" {
		t.Errorf("item Field = %q, want \"services\"", it.Field)
	}
	if it.Key != "podman" {
		t.Errorf("item Key = %q, want \"podman\"", it.Key)
	}
	if it.Value != renderServiceDefinition(res.Services["podman"]) {
		t.Errorf("item Value = %q, want rendered service definition", it.Value)
	}
}
```

- [ ] **Step 9: Run test and verify cascade wiring**

Run: `go test ./internal/approval/ -run TestFilterCoarseService -v`
Expected: PASS — a denied service is dropped from `filtered.Services` by `applyServicesField`, and `cascadeDependentMounts` then drops top-level mounts referencing the missing service.

- [ ] **Step 10: Write the test for EphemeralStore**

Append to `approval_test.go`:

```go
func TestEphemeralStoreDoesNotPersist(t *testing.T) {
	base := &memStore{state: map[string]State{}}
	eph := NewEphemeralStore(base, State{
		Hash: "h",
		Approved: map[string]ApprovedField{"mounts": {Keys: []string{"~/.ssh"}}},
	})
	// Load returns the ephemeral overlay.
	got, _ := eph.Load("any")
	if got.Hash != "h" {
		t.Errorf("ephemeral Load should return overlay, got %+v", got)
	}
	// Save is a no-op (does not write to base).
	_ = eph.Save("any", State{Hash: "new"})
	if base.state["any"].Hash == "new" {
		t.Error("ephemeral Save should not persist to base store")
	}
}
```

- [ ] **Step 11: Implement EphemeralStore and ReadOnlyStore**

Add to `internal/approval/approval.go`:

```go
// EphemeralStore wraps a base Store with an in-memory overlay. Load
// returns the overlay (ignoring the base); Save is a no-op. Used by the
// --dry-run --yes/--no path so the re-filter sees the choice without
// persisting.
type EphemeralStore struct {
	base    Store
	overlay State
}

func NewEphemeralStore(base Store, overlay State) *EphemeralStore {
	return &EphemeralStore{base: base, overlay: overlay}
}

func (e *EphemeralStore) Load(string) (State, error) { return e.overlay, nil }
func (e *EphemeralStore) Save(string, State) error   { return nil }

// ReadOnlyStore wraps a base Store: Load delegates to the base; Save is a
// no-op. Used for the whole --dry-run flow so the initial Filter can read
// stored approvals (an approved profile must not prompt) but a
// reconciliation write-back never touches disk.
type ReadOnlyStore struct {
	base Store
}

func NewReadOnlyStore(base Store) *ReadOnlyStore {
	return &ReadOnlyStore{base: base}
}

func (r *ReadOnlyStore) Load(name string) (State, error) { return r.base.Load(name) }
func (r *ReadOnlyStore) Save(string, State) error        { return nil }
```

- [ ] **Step 12: Write the test for ReadOnlyStore**

Append to `approval_test.go`:

```go
func TestReadOnlyStoreDelegatesLoadButNotSave(t *testing.T) {
	base := &memStore{state: map[string]State{"p": {Hash: "h"}}}
	ro := NewReadOnlyStore(base)
	got, err := ro.Load("p")
	if err != nil || got.Hash != "h" {
		t.Fatalf("Load should delegate to base, got %+v err=%v", got, err)
	}
	_ = ro.Save("p", State{Hash: "other"})
	if base.state["p"].Hash != "h" {
		t.Error("Save should not persist to base store")
	}
}
```

- [ ] **Step 13: Run the full approval package suite**

Run: `go test ./internal/approval/ -v`
Expected: PASS.

- [ ] **Step 14: Commit**

```bash
git add internal/approval/approval.go internal/approval/approval_test.go
git commit -m "feat(approval): add Filter with reconciliation, cascade, and ephemeral store"
```

---

## Task 7: huh-based approval prompt

**Files:**
- Create: `internal/approval/prompt.go`
- Create: `internal/approval/prompt_test.go` (contract test only; no TTY test)

**Interfaces:**
- Produces: `approval.Prompt` type; `DefaultPrompt` (huh-based).

- [ ] **Step 1: Write the contract test for DefaultPrompt (non-TTY → error)**

Create `internal/approval/prompt_test.go`:

```go
package approval

import (
	"bytes"
	"strings"
	"testing"
)

func TestDefaultPromptNonTTYReturnsError(t *testing.T) {
	req := PromptRequest{ProfileName: "test", Items: []SensitiveItem{
		{Field: "mounts", Key: "~/.ssh", Value: "~/.ssh", Source: testContrib()},
	}}
	_, err := DefaultPrompt(req, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("DefaultPrompt on non-TTY should error")
	}
	if !strings.Contains(err.Error(), "not a TTY") {
		t.Errorf("error should mention TTY, got %v", err)
	}
}

func testContrib() (c profile.Contributor) {
	return profile.Contributor{FullName: "core/creds/ssh", Namespace: "core"}
}
```

(Add `"github.com/jgillich/tpd/internal/profile"` to imports.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/approval/ -run TestDefaultPrompt -v`
Expected: FAIL — `undefined: DefaultPrompt`.

- [ ] **Step 3: Implement DefaultPrompt**

Create `internal/approval/prompt.go`:

```go
package approval

import (
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/charmbracelet/huh"
	"github.com/jgillich/tpd/internal/ui"
)

// Prompt renders the interactive approval dialog and returns the user's
// choices as a map[field]set[key]bool. If stdin is not a TTY, returns an
// error.
type Prompt func(req PromptRequest, stdin io.Reader, stdout io.Writer) (map[string]map[string]bool, error)

// DefaultPrompt is the huh-based implementation. Items are grouped by
// contributing leaf (Source.FullName); each contributor gets its own
// huh.Group with a title naming the contributor. Options for keys that
// PriorChoices marks approved are pre-selected. An abort (Esc/Ctrl+C) is
// surfaced as "approval declined".
func DefaultPrompt(req PromptRequest, stdin io.Reader, stdout io.Writer) (map[string]map[string]bool, error) {
	if !ui.IsTTYReader(stdin) {
		return nil, fmt.Errorf("approval prompt: stdin is not a TTY")
	}

	// Group items by contributor, preserving a stable order.
	groups := map[string][]SensitiveItem{}
	var order []string
	for _, it := range req.Items {
		src := it.Source.FullName
		if _, ok := groups[src]; !ok {
			order = append(order, src)
		}
		groups[src] = append(groups[src], it)
	}
	sort.Strings(order)
	// Items within a group arrive in map-iteration order (nondeterministic);
	// sort by (Field, Key) so the dialog renders deterministically.
	for src := range groups {
		sort.Slice(groups[src], func(i, j int) bool {
			a, b := groups[src][i], groups[src][j]
			if a.Field != b.Field {
				return a.Field < b.Field
			}
			return a.Key < b.Key
		})
	}

	// Pre-select any item whose key PriorChoices marks approved.
	preSelected := map[string]bool{}
	for field, set := range req.PriorChoices {
		for k, v := range set {
			if v {
				preSelected[field+"\x00"+k] = true
			}
		}
	}

	// One *[]string per group, retained so we can read the final selection.
	sels := make([][]string, len(order))
	selPtrs := make([]*[]string, len(order))
	var huhGroups []*huh.Group
	for i, src := range order {
		items := groups[src]
		opts := make([]huh.Option[string], 0, len(items))
		for _, it := range items {
			id := itemID(it)
			opts = append(opts, huh.NewOption(fmt.Sprintf("%s = %s (%s)", it.Key, it.Value, it.Field), id))
			if preSelected[id] {
				sels[i] = append(sels[i], id)
			}
		}
		selPtrs[i] = &sels[i]
		huhGroups = append(huhGroups,
			huh.NewGroup(
				huh.NewMultiSelect[string]().
					Title(fmt.Sprintf("tpd: %s wants the following from %s", req.ProfileName, src)).
					Options(opts...).
					Value(selPtrs[i]),
			))
	}

	form := huh.NewForm(huhGroups...).WithInput(stdin).WithOutput(stdout)
	if err := form.Run(); err != nil {
		// huh returns a specific error on user abort (Esc/Ctrl+C);
		// distinguish it from a real I/O failure.
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, fmt.Errorf("approval declined")
		}
		return nil, fmt.Errorf("approval prompt: %w", err)
	}

	// Map the per-group selected IDs back to (field, key) choices.
	choices := map[string]map[string]bool{}
	for _, it := range req.Items {
		set, ok := choices[it.Field]
		if !ok {
			set = map[string]bool{}
			choices[it.Field] = set
		}
		set[it.Key] = false
	}
	for i := range order {
		for _, id := range sels[i] {
			f, k := splitItemID(id)
			if choices[f] == nil {
				choices[f] = map[string]bool{}
			}
			choices[f][k] = true
		}
	}
	return choices, nil
}

func itemID(it SensitiveItem) string {
	return it.Field + "\x00" + it.Key
}

func splitItemID(id string) (field, key string) {
	for i := 0; i < len(id); i++ {
		if id[i] == '\x00' {
			return id[:i], id[i+1:]
		}
	}
	return id, ""
}

- [ ] **Step 4: Add IsTTYReader to internal/ui**

Create `internal/ui/tty.go` (moving `IsTTY` from `internal/scaffold/scaffold.go:408` where it's a package-local helper, to `internal/ui` where both `scaffold` and `approval` can share it):

```go
package ui

import (
	"io"
	"os"

	"golang.org/x/term"
)

// IsTTYReader reports whether r is an interactive terminal.
func IsTTYReader(r io.Reader) bool {
	if f, ok := r.(*os.File); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	return false
}
```

Then update `internal/scaffold/scaffold.go`: delete the local `IsTTY` function (lines ~405-413) and replace its call site (in `scaffold.Run` or wherever `scaffold.IsTTY(os.Stdin)` is called) with `ui.IsTTYReader`. Add `"github.com/jgillich/tpd/internal/ui"` to scaffold's imports, and **drop `"golang.org/x/term"`** — `term` is only referenced inside the deleted `IsTTY`, so leaving it is an unused-import compile error. Verify no other `scaffold.IsTTY` references remain.

- [ ] **Step 5: Run the contract test and scaffold tests**

Run: `go test ./internal/approval/ -run TestDefaultPrompt -v && go test ./internal/scaffold/ -v`
Expected: PASS (scaffold tests still pass with the moved helper).

- [ ] **Step 6: Commit**

```bash
git add internal/approval/prompt.go internal/approval/prompt_test.go internal/ui/tty.go internal/scaffold/scaffold.go
git commit -m "feat(approval): add huh-based DefaultPrompt; move IsTTY to ui.IsTTYReader"
```

---

## Task 8: Wire approval into LaunchWithWriter and LaunchOpts

**Files:**
- Modify: `pkg/tpd/types.go`
- Modify: `pkg/tpd/launch.go`
- Modify: `pkg/tpd/launch_test.go`

**Interfaces:**
- Consumes: `approval.Filter`, `approval.Store`, `approval.Prompt`, `approval.NewEphemeralStore`, `approval.NewReadOnlyStore`, `profile.ResolveProfileWithProv`, `ui.IsTTYReader`.
- Produces: `LaunchOpts` with new fields; `LaunchWithWriter` runs the gate.

- [ ] **Step 1: Add fields to LaunchOpts**

Modify `pkg/tpd/types.go`. Add imports and fields:

```go
import (
	"io"

	"github.com/jgillich/tpd/internal/approval"
	"github.com/jgillich/tpd/internal/runtime"
)
```

Add to `LaunchOpts`:

```go
	In             io.Reader
	ApprovalStore  approval.Store
	ApprovalPrompt approval.Prompt
	IsTTY          func(io.Reader) bool
	AssumeYes      bool
	AssumeNo       bool
```

- [ ] **Step 2: Write the failing end-to-end test**

Append to `pkg/tpd/launch_test.go`. A user profile's *own* mount is trusted (Namespace "") and is not gated, so a plain user `bash` profile with a direct `mounts:` would never trigger the gate and the non-interactive test would wrongly pass. The test instead writes a *user* profile that `extends: core/opencode`. `core/opencode` extends `core/mise`, which mounts `~/.config/mise` — a core-contributed sensitive mount that the gate must surface. Built-in profiles (core/opencode, core/mise) load from the embedded catalog automatically via `LoadProfiles`; only the user profile is written to the temp profile dir.

```go
func TestLaunchApprovalNonInteractiveErrors(t *testing.T) {
	dir := t.TempDir()
	// User profile extending core/opencode inherits core/mise's ~/.config/mise
	// mount (Namespace "core") → gated. The user profile's own Namespace is ""
	// but the inherited mount stays attributed to core/mise, so the gate fires.
	writeProfile(t, dir, "myagent.yaml", []byte("version: 1\nextends: core/opencode\n"))
	opts := LaunchOpts{
		ProfileName:   "myagent",
		ProfileDir:    dir,
		DryRun:        true,
		In:            &bytes.Buffer{},
		IsTTY:         func(io.Reader) bool { return false },
		ApprovalStore: approval.NewFSStore(t.TempDir()),
	}
	res := LaunchWithWriter(context.Background(), opts, &bytes.Buffer{})
	if res.Err == nil || res.ExitCode != 2 {
		t.Fatalf("expected exit 2 for unapproved non-interactive, got %+v", res)
	}
}

func TestLaunchApprovalAssumeYesPersistsAndProceeds(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "myagent.yaml", []byte("version: 1\nextends: core/opencode\n"))
	// Fixture guard: a dry-run without --yes must error, proving the gate
	// fires. If the embedded catalog loses core/mise's mount, this fails
	// loudly instead of the test silently passing as a no-op.
	guard := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName:   "myagent",
		ProfileDir:    dir,
		DryRun:        true,
		In:            &bytes.Buffer{},
		IsTTY:         func(io.Reader) bool { return false },
		ApprovalStore: approval.NewFSStore(t.TempDir()),
	}, &bytes.Buffer{})
	if guard.Err == nil || guard.ExitCode != 2 {
		t.Fatalf("fixture guard: expected exit 2 for unapproved dry-run (embedded catalog changed?), got %+v", guard)
	}
	storeDir := t.TempDir()
	store := approval.NewFSStore(storeDir)
	opts := LaunchOpts{
		ProfileName:   "myagent",
		ProfileDir:    dir,
		DryRun:        true,
		In:            &bytes.Buffer{},
		IsTTY:         func(io.Reader) bool { return false },
		ApprovalStore: store,
		AssumeYes:     true,
	}
	res := LaunchWithWriter(context.Background(), opts, &bytes.Buffer{})
	if res.Err != nil {
		t.Fatalf("AssumeYes dry-run should succeed, got %v", res.Err)
	}
	// dry-run --yes uses the ephemeral store, so no state file is written.
	// The user profile myagent resolves to FullName "myagent" (Namespace ""),
	// so the would-be state file is approvals/myagent.yaml.
	if _, err := os.Stat(filepath.Join(storeDir, "approvals", "myagent.yaml")); !os.IsNotExist(err) {
		t.Errorf("dry-run --yes should not persist, file exists or err=%v", err)
	}
}

func TestLaunchApprovalInteractiveApprove(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "myagent.yaml", []byte("version: 1\nextends: core/opencode\n"))
	fr := &runtime.FakeRuntime{ExitCode: 0}
	opts := LaunchOpts{
		ProfileName:   "myagent",
		ProfileDir:    dir,
		In:            &bytes.Buffer{},
		IsTTY:         func(io.Reader) bool { return true },
		ApprovalStore: approval.NewFSStore(t.TempDir()),
		ApprovalPrompt: func(req approval.PromptRequest, stdin io.Reader, stdout io.Writer) (map[string]map[string]bool, error) {
			choices := map[string]map[string]bool{}
			for _, it := range req.Items {
				set := map[string]bool{}
				choices[it.Field] = set
				set[it.Key] = true
			}
			return choices, nil
		},
		Runtime: fr,
	}
	res := LaunchWithWriter(context.Background(), opts, &bytes.Buffer{})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("interactive approve should launch, got %+v", res)
	}
	if fr.RanSpec == nil || len(fr.RanSpec.Mounts) == 0 {
		t.Errorf("approved mounts should survive filtering (RunSpec mounts = %+v)", fr.RanSpec)
	}
}

func TestLaunchApprovalInteractiveDeny(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "myagent.yaml", []byte("version: 1\nextends: core/opencode\n"))
	fr := &runtime.FakeRuntime{ExitCode: 0}
	opts := LaunchOpts{
		ProfileName:   "myagent",
		ProfileDir:    dir,
		In:            &bytes.Buffer{},
		IsTTY:         func(io.Reader) bool { return true },
		ApprovalStore: approval.NewFSStore(t.TempDir()),
		ApprovalPrompt: func(req approval.PromptRequest, stdin io.Reader, stdout io.Writer) (map[string]map[string]bool, error) {
			choices := map[string]map[string]bool{}
			for _, it := range req.Items {
				set := map[string]bool{}
				choices[it.Field] = set
				set[it.Key] = false
			}
			return choices, nil
		},
		Runtime: fr,
	}
	res := LaunchWithWriter(context.Background(), opts, &bytes.Buffer{})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("interactive deny should launch (drop-and-continue), got %+v", res)
	}
	if fr.RanSpec != nil && len(fr.RanSpec.Mounts) != 0 {
		t.Errorf("denied mounts should be dropped from the run spec, got %d mounts", len(fr.RanSpec.Mounts))
	}
}

func TestLaunchApprovalAssumeNoPersistsAndProceeds(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "myagent.yaml", []byte("version: 1\nextends: core/opencode\n"))
	storeDir := t.TempDir()
	store := approval.NewFSStore(storeDir)
	fr := &runtime.FakeRuntime{ExitCode: 0}
	opts := LaunchOpts{
		ProfileName:   "myagent",
		ProfileDir:    dir,
		In:            &bytes.Buffer{},
		IsTTY:         func(io.Reader) bool { return false },
		ApprovalStore: store,
		AssumeNo:      true,
		Runtime:       fr,
	}
	res := LaunchWithWriter(context.Background(), opts, &bytes.Buffer{})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("--no should launch with denied fields dropped, got %+v", res)
	}
	// --no persists: the state file exists with the mounts field present
	// but empty (all denied).
	data, err := os.ReadFile(filepath.Join(storeDir, "approvals", "myagent.yaml"))
	if err != nil {
		t.Fatalf("--no should persist state: %v", err)
	}
	if !bytes.Contains(data, []byte("mounts:")) {
		t.Errorf("state should contain the mounts field (present, all denied):\n%s", data)
	}
	if fr.RanSpec != nil && len(fr.RanSpec.Mounts) != 0 {
		t.Errorf("denied mounts should be dropped, got %d mounts", len(fr.RanSpec.Mounts))
	}
}

func TestLaunchApprovalPartialPromptFailsClosed(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "myagent.yaml", []byte("version: 1\nextends: core/opencode\n"))
	storeDir := t.TempDir()
	store := approval.NewFSStore(storeDir)
	opts := LaunchOpts{
		ProfileName:   "myagent",
		ProfileDir:    dir,
		In:            &bytes.Buffer{},
		IsTTY:         func(io.Reader) bool { return true },
		ApprovalStore: store,
		ApprovalPrompt: func(req approval.PromptRequest, stdin io.Reader, stdout io.Writer) (map[string]map[string]bool, error) {
			if len(req.Items) < 2 {
				t.Fatalf("fixture must produce at least 2 gated items, got %d (embedded catalog changed?)", len(req.Items))
			}
			// Decide only the first item; leave the rest undecided.
			it := req.Items[0]
			return map[string]map[string]bool{it.Field: {it.Key: true}}, nil
		},
		Runtime: &runtime.FakeRuntime{ExitCode: 0},
	}
	res := LaunchWithWriter(context.Background(), opts, &bytes.Buffer{})
	if res.Err == nil || res.ExitCode != 2 {
		t.Fatalf("partial prompt should fail closed with exit 2, got %+v", res)
	}
	if _, err := os.Stat(filepath.Join(storeDir, "approvals", "myagent.yaml")); !os.IsNotExist(err) {
		t.Errorf("partial prompt should not persist state")
	}
}

func writeProfile(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
```

(Add imports: `"bytes"`, `"context"`, `"io"`, `"os"`, `"path/filepath"`, `"github.com/jgillich/tpd/internal/approval"`, `"github.com/jgillich/tpd/internal/runtime"` (for `runtime.FakeRuntime`). The `"github.com/jgillich/tpd/internal/profile"` import is not needed by these tests.)

The e2e tests are load-bearing on the embedded catalog (`core/opencode` → `core/mise` must keep the `~/.config/mise` mount). They fail loudly, not silently: the non-interactive/partial tests assert exit 2 (degrade → exit 0 → FAIL), and the deny/`--no` tests assert `RanSpec.Mounts` is empty (no gated mounts → mounts present → FAIL). The interactive tests assert mounts survive/are dropped. A catalog edit that removes all mounts from `core/mise` breaks these tests visibly; keep them in sync with the embedded catalog.

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./pkg/tpd/ -run TestLaunchApproval -v`
Expected: FAIL — the gate isn't wired yet; the non-interactive test will not see exit 2.

- [ ] **Step 4: Wire the gate into LaunchWithWriter**

Modify `pkg/tpd/launch.go`. Replace the block at lines 71-74 (`cfg, err := profile.ResolveProfile(...)`) with:

```go
	resolved, err := profile.ResolveProfileWithProv(cat, opts.ProfileName)
	if err != nil {
		return Result{ExitCode: 2, Err: err}
	}

	// Approval gate.
	store := opts.ApprovalStore
	if store == nil {
		store = approval.NewFSStore(defaultApprovalDir())
	}
	// In dry-run the store is read-only: the initial Filter must read
	// stored approvals (so an already-approved profile doesn't prompt)
	// but its reconciliation write-back must never touch disk.
	gateStore := store
	if opts.DryRun {
		gateStore = approval.NewReadOnlyStore(store)
	}
	isTTY := opts.IsTTY
	if isTTY == nil {
		isTTY = ui.IsTTYReader
	}
	in := opts.In
	if in == nil {
		in = os.Stdin
	}

	cfg, promptReq, err := approval.Filter(resolved, gateStore)
	if err != nil {
		return Result{ExitCode: 2, Err: err}
	}

	if len(promptReq.Items) > 0 {
		if opts.AssumeYes || opts.AssumeNo {
			choices := buildChoices(promptReq, opts.AssumeYes)
			prior, _ := gateStore.Load(resolved.FullName)
			merged := mergeChoicesIntoState(prior, promptReq, choices)
			effectiveStore := gateStore
			if opts.DryRun {
				effectiveStore = approval.NewEphemeralStore(gateStore, merged)
			} else {
				if err := store.Save(resolved.FullName, merged); err != nil {
					return Result{ExitCode: 2, Err: err}
				}
			}
			cfg, _, err = approval.Filter(resolved, effectiveStore)
			if err != nil {
				return Result{ExitCode: 2, Err: err}
			}
		} else if opts.DryRun || !isTTY(in) {
			return Result{ExitCode: 2, Err: fmt.Errorf("unapproved sensitive fields require --yes or --no: %s", summarizeItems(promptReq.Items))}
		} else {
			prompt := opts.ApprovalPrompt
			if prompt == nil {
				prompt = approval.DefaultPrompt
			}
			choices, err := prompt(promptReq, in, w)
			if err != nil {
				return Result{ExitCode: 2, Err: fmt.Errorf("approval: %w", err)}
			}
			if incomplete(promptReq, choices) {
				return Result{ExitCode: 2, Err: fmt.Errorf("approval incomplete: %s", summarizeUndecided(promptReq, choices))}
			}
			prior, _ := gateStore.Load(resolved.FullName)
			merged := mergeChoicesIntoState(prior, promptReq, choices)
			if err := store.Save(resolved.FullName, merged); err != nil {
				return Result{ExitCode: 2, Err: err}
			}
			cfg, _, err = approval.Filter(resolved, store)
			if err != nil {
				return Result{ExitCode: 2, Err: err}
			}
		}
	}
```

`w` is the `io.Writer` argument to `LaunchWithWriter`; the approval dialog routes its I/O through `w` (spec §5), not `os.Stderr`.

Add the helper functions and `defaultApprovalDir`:

```go
func defaultApprovalDir() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "tpd")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".local", "share", "tpd")
}

func buildChoices(req approval.PromptRequest, yes bool) map[string]map[string]bool {
	choices := map[string]map[string]bool{}
	for _, it := range req.Items {
		set, ok := choices[it.Field]
		if !ok {
			set = map[string]bool{}
			choices[it.Field] = set
		}
		set[it.Key] = yes
	}
	return choices
}

// mergeChoicesIntoState folds the dialog's per-field choices into the prior
// stored state, merging per-field: a decided field REPLACES its stored
// ApprovedField (the dialog returned the complete allowed-set for it);
// fields not present in choices keep their stored choices untouched. For
// map fields (mounts, devices, env, ports, dbus.talk, dbus.own, services),
// Keys is the approved set (denied = absent). For the scalar "network"
// field, the choice is stored in ApprovedField.Network (*bool): true → &true,
// false → &false. Hash and Profile are refreshed to the current request.
func mergeChoicesIntoState(prior approval.State, req approval.PromptRequest, choices map[string]map[string]bool) approval.State {
	st := prior
	st.Hash = req.Hash
	st.Profile = req.ProfileName
	if st.Approved == nil {
		st.Approved = map[string]approval.ApprovedField{}
	}
	for field, set := range choices {
		if field == "network" {
			b := false
			for _, v := range set {
				if v {
					b = true
					break
				}
			}
			af := st.Approved[field]
			af.Network = &b
			st.Approved[field] = af
			continue
		}
		var keys []string
		for k, v := range set {
			if v {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		st.Approved[field] = approval.ApprovedField{Keys: keys}
	}
	return st
}

func incomplete(req approval.PromptRequest, choices map[string]map[string]bool) bool {
	for _, it := range req.Items {
		set, ok := choices[it.Field]
		if !ok {
			return true
		}
		if _, decided := set[it.Key]; !decided {
			return true
		}
	}
	return false
}

func summarizeItems(items []approval.SensitiveItem) string {
	var parts []string
	for _, it := range items {
		parts = append(parts, it.Field+"."+it.Key)
	}
	return strings.Join(parts, ", ")
}

func summarizeUndecided(req approval.PromptRequest, choices map[string]map[string]bool) string {
	var parts []string
	for _, it := range req.Items {
		set, ok := choices[it.Field]
		if !ok {
			parts = append(parts, it.Field+"."+it.Key)
			continue
		}
		if _, decided := set[it.Key]; !decided {
			parts = append(parts, it.Field+"."+it.Key)
		}
	}
	return strings.Join(parts, ", ")
}
```

(Add imports: `"github.com/jgillich/tpd/internal/approval"`, `"github.com/jgillich/tpd/internal/ui"`, `"path/filepath"`, `"sort"`.)

- [ ] **Step 5: Run the end-to-end tests**

Run: `go test ./pkg/tpd/ -run TestLaunchApproval -v`
Expected: PASS.

- [ ] **Step 6: Run the full pkg/tpd suite**

Run: `go test ./pkg/tpd/ -v`
Expected: PASS (existing tests may need `In`/`IsTTY` defaults; the nil-defaults handle it).

- [ ] **Step 7: Commit**

```bash
git add pkg/tpd/types.go pkg/tpd/launch.go pkg/tpd/launch_test.go
git commit -m "feat(tpd): wire approval gate into LaunchWithWriter"
```

---

## Task 9: CLI flags --yes/--no and advisory removal

**Files:**
- Modify: `cmd/tpd/cli.go`
- Modify: `cmd/tpd/cli_test.go`
- Delete: `internal/catalog/advisories.go`
- Modify: `internal/catalog/catalog_test.go`
- Modify: `internal/scaffold/scaffold.go`
- Modify: `internal/scaffold/scaffold_test.go`

**Interfaces:**
- Consumes: `LaunchOpts.AssumeYes`/`AssumeNo` from Task 8.
- Produces: `--yes`/`--no` flags on the launch commands; no more advisory prints.

- [ ] **Step 1: Add --yes/--no to launchFlags and addLaunchFlags**

Modify `cmd/tpd/cli.go`. Add fields to `launchFlags`:

```go
type launchFlags struct {
	Command   string
	Workspace string
	DryRun    bool
	Verbose   bool
	Pull      bool
	AssumeYes bool
	AssumeNo  bool
}
```

In `addLaunchFlags`, add:

```go
	cmd.Flags().BoolVar(&o.AssumeYes, "yes", false, "Auto-approve all unapproved sensitive fields and persist the choice.")
	cmd.Flags().BoolVar(&o.AssumeNo, "no", false, "Auto-deny all unapproved sensitive fields and persist the choice.")
	cmd.MarkFlagsMutuallyExclusive("yes", "no")
```

- [ ] **Step 2: Pass AssumeYes/AssumeNo through runLaunch**

In `runLaunch`, add to the `tpd.LaunchOpts{...}` literal:

```go
		AssumeYes: o.AssumeYes,
		AssumeNo:  o.AssumeNo,
```

- [ ] **Step 3: Write the CLI test for --yes/--no mutual exclusion**

Append to `cmd/tpd/cli_test.go`:

```go
func TestYesNoMutuallyExclusive(t *testing.T) {
	cmd := newRootCommand()
	cmd.SetArgs([]string{"--yes", "--no", "bash"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for --yes and --no together")
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/tpd/ -run TestYesNo -v`
Expected: PASS.

- [ ] **Step 5: Remove advisory call sites from cli.go**

In `cmd/tpd/cli.go`, delete:
- The `if msg := catalog.Advisory(advisoryName(key)); msg != "" { ... }` blocks. `runShow` has THREE (lines 160, 174, and 185 — note line 185 is the non-`--resolved` branch of `runShow`, not `runEdit`); `runEdit` has ONE (line 228).
- The `advisoryName` helper (around lines 551-554).
- Keep the `"github.com/jgillich/tpd/internal/catalog"` import — `runEdit` still uses `catalog.Profiles`/`catalog.Fragments` (cli.go:241,244) to seed built-in edits. Only the `catalog.Advisory` calls are removed.

- [ ] **Step 6: Remove advisory call sites from scaffold.go**

In `internal/scaffold/scaffold.go`, delete:
- The `if msg := catalog.Advisory(advisoryLeaf(b)); msg != "" { ... }` block (around lines 205-206).
- The `advisoryLeaf` helper (around lines 396-398).
- Remove the `catalog` import if unused.

- [ ] **Step 7: Delete advisories.go and its tests**

```bash
git rm internal/catalog/advisories.go
```

In `internal/catalog/catalog_test.go`, remove any tests that reference `Advisory`.

- [ ] **Step 8: Remove advisory tests from cli_test.go and scaffold_test.go**

In `cmd/tpd/cli_test.go`, delete `TestShowDockerPrintsSensitiveAdvisory` (line ~138) and `TestEditDockerPrintsSensitiveAdvisory` (line ~151).

In `internal/scaffold/scaffold_test.go`, delete `TestScaffoldPrintsAdvisoryForSensitiveFragments` (line ~593).

- [ ] **Step 9: Run the full test suite**

Run: `go test ./... `
Expected: PASS (no references to `catalog.Advisory` remain).

- [ ] **Step 10: Run vet**

Run: `go vet ./...`
Expected: PASS.

- [ ] **Step 11: Commit**

```bash
git add cmd/tpd/cli.go cmd/tpd/cli_test.go internal/catalog/advisories.go internal/catalog/catalog_test.go internal/scaffold/scaffold.go internal/scaffold/scaffold_test.go
git commit -m "feat(cli): add --yes/--no flags and remove advisory prints"
```

---

## Task 10: Update security-model doc and final verification

**Files:**
- Modify: `docs/2026-08-03-security-model.md`

- [ ] **Step 1: Update the security-model doc**

Per spec §8, update `docs/2026-08-03-security-model.md`:
- Intro line ~8: append the approval-system date.
- "Trust model" section: add a note that user-vs-core contributions are now distinguished by the approval system.
- "Credential-fragment advisories" section: rewrite to point at the approval dialog as the single source of sensitivity information.

- [ ] **Step 2: Run the full test suite one final time**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add docs/2026-08-03-security-model.md
git commit -m "docs: update security model for approval system"
```

- [ ] **Step 4: Manual smoke test (documented, not automated)**

On a fresh state dir, run a sensitive profile and verify the dialog appears. `core/opencode` extends `core/mise`, which mounts `~/.config/mise` — a core-contributed sensitive mount that triggers the gate:
```bash
rm -rf ~/.local/share/tpd/approvals
go install ./cmd/tpd
tpd --dry-run --yes core/opencode 2>&1 | head
```
Confirm: the dry-run prints the spec with the mount present (approved by --yes via the ephemeral store), and no approval file is written at `~/.local/share/tpd/approvals/core/opencode.yaml`.

Then:
```bash
tpd --dry-run core/opencode 2>&1 | head
```
Confirm: exit 2, "unapproved sensitive fields require --yes or --no".

---

## Self-Review

**Spec coverage:**
- §1 Goals & trust model shift → Task 10 (doc), Tasks 1-9 (implementation).
- §2 Provenance tracking → Tasks 1, 2.
- §3 Resolve API & filter → Tasks 3, 6.
- §4 Hash & state file → Tasks 4, 5.
- §5 Launch integration → Task 8.
- §6 Dialog → Task 7.
- §7 CLI flags → Task 9.
- §8 Advisory removal → Task 9.
- §9 Testing → each task's TDD steps.
- §10 Out of scope → no task (correct).
- §11 Resolved questions → reflected in the constraints.

**Placeholder scan:** Task 6 Step 4 contains the complete per-field walk (Mounts, Devices, Env, Ports, Dbus.Talk/Own, Network scalar, and coarse Services — one item per service) with real keep/drop/prompt logic and helpers (`decide`, `item`, `render*`, `renderServiceDefinition`, `containsKey`). No "TBD"/"TODO" or "apply the same pattern" strings remain.

**Type consistency:** `Contributor`, `Provenance`, `Resolved`, `SensitiveItem`, `PromptRequest`, `Store`, `State`, `ApprovedField`, `EphemeralStore`, `ComputeApprovalHash`, `Filter`, `DefaultPrompt`, `IsTTYReader` are used consistently across tasks.