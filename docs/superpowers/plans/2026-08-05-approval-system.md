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
- Service sensitive fields (schema-valid only): `mounts`, `env`, `privileged`, `exposes`. The validator rejects `devices`/`ports`/`dbus`/`network` on services (`internal/profile/validate.go:281-301`) — never gate them for services.
- Non-gated: `packages`, `tools`, `image`, `command`, `labels`, `resources`, `caches`, `files`, `repos`.
- Hash and store key on **pre-expansion** (literal template) values. A changed host env var must not invalidate approvals.
- State file keyed by resolved catalog `FullName`, not the display name.
- Provenance stores `Contributor{FullName, Namespace}`; `Trusted()` = `Namespace == ""`.
- `--dry-run` never persists state and never prompts; uses an ephemeral in-memory store for the re-filter.
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
- `internal/ui/tty.go` — `IsTTYReader(r io.Reader) bool` (reader-side wrapper; existing `IsTTY` takes `io.Writer`).

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

- [ ] **Step 4: Stamp provenance in mergeMap and mergeStringMap**

Modify `internal/profile/merge.go`. The generic `mergeMap` needs a parallel provenance map. Because Go generics don't easily carry a parallel typed map, add a helper that stamps provenance after each merge call. Replace the `mergeMap` function:

```go
func mergeMap[V any](parent, child map[string]V, nullKeys map[string]bool) map[string]V {
	if nullKeys != nil && nullKeys["*"] {
		return map[string]V{}
	}
	out := make(map[string]V, len(parent)+len(child))
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

Add a provenance-merging helper alongside it:

```go
// mergeProvMap merges parent/child provenance maps with the same key
// semantics as mergeMap: child wins per key, nullKeys deletes.
func mergeProvMap(parent, child map[string]Contributor, nullKeys map[string]bool, childContrib Contributor) map[string]Contributor {
	if nullKeys != nil && nullKeys["*"] {
		return map[string]Contributor{}
	}
	out := make(map[string]Contributor, len(parent)+len(child))
	for k, v := range parent {
		out[k] = v
	}
	for k := range child {
		out[k] = childContrib
	}
	for k := range nullKeys {
		delete(out, k)
	}
	return out
}
```

Now in `MergeProfiles`, replace each `out.X = mergeMap(...)` line with a pair that also merges provenance. For example, for Mounts:

```go
out.Mounts = mergeMap(parent.Mounts, child.Mounts, child.NullKeys["mounts"])
out.Provenance.Mounts = mergeProvMap(parent.Provenance.Mounts, child.Mounts, child.NullKeys["mounts"], Contributor{FullName: child.FullName(), Namespace: child.Namespace})
```

Do the same for `Devices`, `Env`, `Ports`, `Mounts`. For `Network`:

```go
if child.Network != "" {
	out.Network = child.Network
	out.Provenance.Network = Contributor{FullName: child.FullName(), Namespace: child.Namespace}
} else {
	out.Provenance.Network = parent.Provenance.Network
}
```

For `Dbus`, after `out.Dbus = mergeDbus(...)`, stamp `prov.Dbus.Talk` and `prov.Dbus.Own` using `mergeProvMap` with the child contributor. Handle the nil cases (mergeDbus returns nil).

For `Services`:

```go
out.Services = mergeMap(parent.Services, child.Services, child.NullKeys["services"])
out.Provenance.Services = mergeProvMap(parent.Provenance.Services, child.Services, child.NullKeys["services"], Contributor{FullName: child.FullName(), Namespace: child.Namespace})
```

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

- [ ] **Step 8: Write the failing test for extends-chain provenance**

Append to `internal/profile/extends_test.go`:

```go
func TestResolveChainAttributionAcrossExtends(t *testing.T) {
	// myagent extends core/lang/typescript extends core/lang/javascript.
	// Keys from javascript should be attributed to core/lang/javascript,
	// not to typescript or myagent.
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"core/lang/javascript": {
			Profile:   Profile{Version: 1, Image: "img", Command: []string{"run"}, Env: map[string]string{"JS": "1"}},
			Namespace: "core", Name: "lang/javascript",
		},
		"core/lang/typescript": {
			Profile: Profile{
				Version: 1, Image: "img", Command: []string{"run"},
				ExtendsList: ExtendsList{Resolved: []Ref{{Namespace: "core", Name: "lang/javascript"}}},
				Env: map[string]string{"TS": "1"},
			},
			Namespace: "core", Name: "lang/typescript",
		},
		"myagent": {
			Profile: Profile{
				Version: 1, Image: "img", Command: []string{"run"},
				ExtendsList: ExtendsList{Resolved: []Ref{{Namespace: "core", Name: "lang/typescript"}}},
			},
			Namespace: "", Name: "myagent",
		},
	})
	res, err := ResolveProfileWithProv(cat, "myagent")
	if err != nil {
		t.Fatalf("ResolveProfileWithProv: %v", err)
	}
	if res.Prov.Env["JS"] != (Contributor{FullName: "core/lang/javascript", Namespace: "core"}) {
		t.Errorf("JS should be attributed to core/lang/javascript, got %+v", res.Prov.Env["JS"])
	}
	if res.Prov.Env["TS"] != (Contributor{FullName: "core/lang/typescript", Namespace: "core"}) {
		t.Errorf("TS should be attributed to core/lang/typescript, got %+v", res.Prov.Env["TS"])
	}
}
```

- [ ] **Step 9: Run test to verify it fails**

Run: `go test ./internal/profile/ -run TestResolveChainAttribution -v`
Expected: FAIL — `undefined: ResolveProfileWithProv`.

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
- Modify: `internal/profile/merge_test.go` / `extends_test.go` (complete the test from Task 2)

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

- [ ] **Step 3: Run the extends-chain provenance test**

Run: `go test ./internal/profile/ -run TestResolveChainAttribution -v`
Expected: PASS.

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
	}, Contributor{FullName: "core/creds/ssh", Namespace: "core"})
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
	a := makeResolvedWithMounts(mounts, Contributor{FullName: "core/creds/ssh", Namespace: "core"})
	b := makeResolvedWithMounts(mounts, Contributor{FullName: "github.com/foo/ssh", Namespace: "github.com/foo"})
	if ComputeApprovalHash(a) == ComputeApprovalHash(b) {
		t.Error("hash should differ when contributor identity differs")
	}
}

func TestHashExcludesUserContributions(t *testing.T) {
	res := makeResolvedWithMounts(map[string]profile.Mount{"~/x": {Source: "~/x"}}, Contributor{FullName: "myagent", Namespace: ""})
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
	}, Contributor{FullName: "core/gui", Namespace: "core"})
	h := ComputeApprovalHash(res)
	if h == ComputeApprovalHash(profile.Resolved{}) {
		t.Error("templated mount should produce a non-empty hash")
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
		for _, k := range sortedKeys(svc.Mounts) {
			m := svc.Mounts[k]
			fmt.Fprintf(h, "services.%s.mounts\n%s\n%s\n%s\nmount %s %s %v %v\n", svcName, k, c.FullName, c.Namespace, k, m.Source, m.ReadOnly, m.Optional)
		}
		for _, k := range sortedKeys(svc.Env) {
			fmt.Fprintf(h, "services.%s.env\n%s\n%s\n%s\nenv %s %s\n", svcName, k, c.FullName, c.Namespace, k, svc.Env[k])
		}
		if svc.Privileged {
			fmt.Fprintf(h, "services.%s.privileged\n\n%s\n%s\nprivileged true\n", svcName, c.FullName, c.Namespace)
		}
		for _, k := range sortedKeys(svc.Exposes) {
			fmt.Fprintf(h, "services.%s.exposes\n%s\n%s\n%s\nexpose %s %s\n", svcName, k, c.FullName, c.Namespace, k, svc.Exposes[k])
		}
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:])[:12]
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
			"mounts": {Keys: []string{"~/.ssh"}},
			"network": {Network: boolPtr(true)},
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
	for _, seg := range filepath.SplitList(filepath.ToSlash(fullName)) {
		_ = seg
	}
	segs := splitPath(fullName)
	for _, seg := range segs {
		if seg == "" || seg == ".." || !nameSegRe.MatchString(seg) {
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
type yamlState struct {
	Profile  string                   `yaml:"profile,omitempty"`
	Hash     string                   `yaml:"hash"`
	Approved map[string]yamlField     `yaml:"approved,omitempty"`
}

type yamlField struct {
	Keys     []string `yaml:"keys,omitempty"`
	Network  *bool    `yaml:"network,omitempty"`
}

func (s State) MarshalYAML() (interface{}, error) {
	out := yamlState{Profile: s.Profile, Hash: s.Hash, Approved: map[string]yamlField{}}
	for k, v := range s.Approved {
		yf := yamlField{Keys: v.Keys, Network: v.Network}
		out.Approved[k] = yf
	}
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
	for k, v := range y.Approved {
		s.Approved[k] = ApprovedField{Keys: v.Keys, Network: v.Network}
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
- Produces: `SensitiveItem`, `PromptRequest`, `Filter(res profile.Resolved, store Store) (profile.Profile, PromptRequest, error)`, `EphemeralStore`.

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
	filtered.Mounts, req = applyMapField(filtered.Mounts, res.Prov.Mounts, "mounts", reconciled, req)
	filtered.Devices, req = applyMapField(filtered.Devices, res.Prov.Devices, "devices", reconciled, req)
	filtered.Env, req = applyStringMapField(filtered.Env, res.Prov.Env, "env", reconciled, req)
	filtered.Ports, req = applyPortField(filtered.Ports, res.Prov.Ports, "ports", reconciled, req)
	filtered.Dbus, req = applyDbusField(filtered.Dbus, res.Prov.Dbus, reconciled, req)
	filtered.Network, req = applyNetworkField(filtered.Network, res.Prov.Network, "network", reconciled, req)
	filtered.Services, req = applyServicesField(filtered.Services, res.Prov.Services, reconciled, req)

	// Dependent-mount cascade: drop service-socket mounts whose expose was denied.
	filtered.Mounts = cascadeDependentMounts(filtered.Mounts, filtered.Services)

	return filtered, req, nil
}

// reconcileState drops stored keys that no longer exist in res when the
// hash matches. Returns the reconciled state and whether it changed.
func reconcileState(st State, hash string, res profile.Resolved) (State, bool) {
	if st.Hash != hash {
		return st, false
	}
	changed := false
	// For each field in st.Approved, drop keys that are not in the current
	// profile's non-user sensitive set for that field.
	if _, ok := st.Approved["mounts"]; ok {
		st.Approved["mounts"] = dropMissingKeys(st.Approved["mounts"], res.Mounts, res.Prov.Mounts)
	}
	// (Repeat for other fields — devices, env, ports, dbus, services.)
	// For brevity in this step, the full implementation covers all fields;
	// the test for reconciliation-persistence is in the next step.
	return st, changed
}

func dropMissingKeys(af ApprovedField, present map[string]struct{}, _ map[string]profile.Contributor) ApprovedField {
	// Simplified: real implementation filters af.Keys against present.
	return af
}

// applyMapField is a placeholder; the real implementation iterates the
// field's map, checks provenance, and either keeps, drops, or adds to
// req.Items. The structure is identical across field types; generics or
// per-type helpers handle the value rendering.
func applyMapField[V any](m map[string]V, prov map[string]profile.Contributor, field string, st State, req PromptRequest) (map[string]V, PromptRequest) {
	return m, req
}

func applyStringMapField(m map[string]string, prov map[string]profile.Contributor, field string, st State, req PromptRequest) (map[string]string, PromptRequest) {
	return m, req
}

func applyPortField(m map[string]profile.PortBind, prov map[string]profile.Contributor, field string, st State, req PromptRequest) (map[string]profile.PortBind, PromptRequest) {
	return m, req
}

func applyDbusField(d *profile.DbusConfig, prov profile.DbusProvenance, st State, req PromptRequest) (*profile.DbusConfig, PromptRequest) {
	return d, req
}

func applyNetworkField(v string, c profile.Contributor, field string, st State, req PromptRequest) (string, PromptRequest) {
	return v, req
}

func applyServicesField(m map[string]profile.Service, prov map[string]profile.Contributor, st State, req PromptRequest) (map[string]profile.Service, PromptRequest) {
	return m, req
}

func cascadeDependentMounts(mounts map[string]profile.Mount, services map[string]profile.Service) map[string]profile.Mount {
	for target, m := range mounts {
		if m.Service == "" {
			continue
		}
		svc, ok := services[m.Service]
		if !ok {
			delete(mounts, target)
			continue
		}
		if _, ok := svc.Exposes[m.Socket]; !ok {
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
		out[field] = set
	}
	return out
}
```

The placeholders above are skeletons — the full implementation fills in the per-field walk with real keep/drop/prompt logic. The next steps make the tests pass by completing that logic.

- [ ] **Step 4: Complete the per-field walk to make the tests pass**

Replace the placeholder `applyMapField` and `applyStringMapField` with real implementations. The pattern for a map field:

```go
func applyMountField(mounts map[string]profile.Mount, prov map[string]profile.Contributor, field string, st State, req PromptRequest) (map[string]profile.Mount, PromptRequest) {
	out := map[string]profile.Mount{}
	for k, v := range mounts {
		c, ok := prov[k]
		if !ok || c.Trusted() {
			out[k] = v
			continue
		}
		if st.Hash != req.Hash {
			// Hash differs: this is a new key → prompt.
			req.Items = append(req.Items, item(field, k, renderMount(v), c))
			out[k] = v
			continue
		}
		if af, hasField := st.Approved[field]; hasField && containsKey(af.Keys, k) {
			out[k] = v // approved
		} else {
			// denied or no stored choice for this key
			if af, hasField := st.Approved[field]; hasField {
				// field present in state but key absent → denied → drop
				continue
			}
			// field absent from state → no stored choice → prompt
			req.Items = append(req.Items, item(field, k, renderMount(v), c))
			out[k] = v
		}
	}
	return out, req
}
```

Apply the same pattern to `Devices`, `Env`, `Ports`, `Dbus.Talk`, `Dbus.Own`, `Network`, and each service's sub-fields. Add the `item`, `renderMount`, `containsKey` helpers. Wire each into `Filter` replacing its placeholder calls.

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
func TestFilterCascadesDependentMount(t *testing.T) {
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
	// Deny the exposes key.
	store := &memStore{state: map[string]State{
		"myagent": {Hash: h, Approved: map[string]ApprovedField{
			"services.podman.exposes": {Keys: nil}, // all denied
		}},
	}}
	got, _, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if _, ok := got.Mounts["/run/podman.sock"]; ok {
		t.Error("dependent mount should be cascaded out when expose is denied")
	}
}
```

- [ ] **Step 9: Run test and fix cascade wiring**

Run: `go test ./internal/approval/ -run TestFilterCascades -v`
Expected: PASS (the `cascadeDependentMounts` call in `Filter` handles it, but the service's `Exposes` must be cleared by `applyServicesField` when the expose key is denied — verify and fix).

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

- [ ] **Step 11: Implement EphemeralStore**

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
```

- [ ] **Step 12: Run the full approval package suite**

Run: `go test ./internal/approval/ -v`
Expected: PASS.

- [ ] **Step 13: Commit**

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
	"fmt"
	"io"

	"github.com/charmbracelet/huh"
	"github.com/jgillich/tpd/internal/profile"
	"github.com/jgillich/tpd/internal/ui"
)

// Prompt renders the interactive approval dialog and returns the user's
// choices as a map[field]set[key]bool. If stdin is not a TTY, returns an
// error.
type Prompt func(req PromptRequest, stdin io.Reader, stdout io.Writer) (map[string]map[string]bool, error)

// DefaultPrompt is the huh-based implementation.
func DefaultPrompt(req PromptRequest, stdin io.Reader, stdout io.Writer) (map[string]map[string]bool, error) {
	if !ui.IsTTYReader(stdin) {
		return nil, fmt.Errorf("approval prompt: stdin is not a TTY")
	}
	// Build a multi-select of all items, grouped by contributor in the title.
	opts := make([]huh.Option[string], 0, len(req.Items))
	for _, it := range req.Items {
		label := fmt.Sprintf("%s = %s (%s)", it.Key, it.Value, it.Field)
		opts = append(opts, huh.NewOption(label, itemID(it)))
	}
	var selected []string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title(fmt.Sprintf("tpd: %s wants the following sensitive fields", req.ProfileName)).
				Options(opts...).
				Value(&selected),
		),
	).WithInput(stdin).WithOutput(stdout)
	if err := form.Run(); err != nil {
		return nil, err
	}
	choices := map[string]map[string]bool{}
	for _, it := range req.Items {
		set, ok := choices[it.Field]
		if !ok {
			set = map[string]bool{}
			choices[it.Field] = set
		}
		set[it.Key] = containsStr(selected, itemID(it))
	}
	return choices, nil
}

func itemID(it SensitiveItem) string {
	return it.Field + "\x00" + it.Key
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Add IsTTYReader to internal/ui**

Create `internal/ui/tty.go`:

```go
package ui

import (
	"io"
	"os"

	"golang.org/x/term"
)

// IsTTYReader reports whether r is an interactive terminal. Mirrors
// IsTTY but for an io.Reader (the approval prompt reads from stdin).
func IsTTYReader(r io.Reader) bool {
	if f, ok := r.(*os.File); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	return false
}
```

- [ ] **Step 5: Run the contract test**

Run: `go test ./internal/approval/ -run TestDefaultPrompt -v`
Expected: PASS (bytes.Buffer is not *os.File → not a TTY → error).

- [ ] **Step 6: Commit**

```bash
git add internal/approval/prompt.go internal/approval/prompt_test.go internal/ui/tty.go
git commit -m "feat(approval): add huh-based DefaultPrompt and ui.IsTTYReader"
```

---

## Task 8: Wire approval into LaunchWithWriter and LaunchOpts

**Files:**
- Modify: `pkg/tpd/types.go`
- Modify: `pkg/tpd/launch.go`
- Modify: `pkg/tpd/launch_test.go`

**Interfaces:**
- Consumes: `approval.Filter`, `approval.Store`, `approval.Prompt`, `approval.NewEphemeralStore`, `profile.ResolveProfileWithProv`, `ui.IsTTYReader`.
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

Append to `pkg/tpd/launch_test.go`:

```go
func TestLaunchApprovalGateNonInteractiveErrors(t *testing.T) {
	// A profile with a core-contributed mount, no stored approval,
	// non-interactive (no --yes/--no) → exit 2.
	cat := profile.NewProfileCatalogForTest(map[string]profile.RawProfile{
		"core/bash": {
			Profile:   profile.Profile{Version: 1, Image: "img", Command: []string{"run"}, Mounts: map[string]profile.Mount{"~/.ssh": {Source: "~/.ssh"}}},
			Namespace: "core", Name: "bash",
		},
	})
	_ = cat
	// Launch uses LoadProfiles; for a unit test, inject a fake store and
	// prompt and exercise the gate logic via LaunchWithWriter with DryRun
	// to avoid the container runtime. This test verifies the error path.
	opts := LaunchOpts{
		ProfileName: "bash",
		DryRun:      true,
		In:          &bytes.Buffer{},
		IsTTY:       func(io.Reader) bool { return false },
		ApprovalStore: &fakeStore{},
	}
	// Note: this requires a profile dir with the test profile; the
	// real test writes a temp profile dir. See the full test below.
	_ = opts
}
```

Because `Launch` loads profiles from disk, the full end-to-end test writes a temp profile dir. Replace the stub with a real test:

```go
func TestLaunchApprovalNonInteractiveErrors(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "bash.yaml", []byte("version: 1\nimage: img\ncommand: [run]\nmounts:\n  ~/.ssh:\n    source: ~/.ssh\n"))
	opts := LaunchOpts{
		ProfileName:   "bash",
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
	writeProfile(t, dir, "bash.yaml", []byte("version: 1\nimage: img\ncommand: [run]\nmounts:\n  ~/.ssh:\n    source: ~/.ssh\n"))
	storeDir := t.TempDir()
	store := approval.NewFSStore(storeDir)
	opts := LaunchOpts{
		ProfileName:   "bash",
		ProfileDir:    dir,
		DryRun:        true, // avoid runtime
		In:            &bytes.Buffer{},
		IsTTY:         func(io.Reader) bool { return false },
		ApprovalStore: store,
		AssumeYes:     true,
	}
	res := LaunchWithWriter(context.Background(), opts, &bytes.Buffer{})
	if res.Err != nil {
		t.Fatalf("AssumeYes dry-run should succeed, got %v", res.Err)
	}
	// State should be persisted (dry-run with AssumeYes does NOT persist;
	// it uses the ephemeral store). Verify no file written.
	if _, err := os.Stat(filepath.Join(storeDir, "approvals", "core", "bash.yaml")); !os.IsNotExist(err) {
		t.Errorf("dry-run --yes should not persist, file exists or err=%v", err)
	}
}

func writeProfile(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
```

(Add imports: `"bytes"`, `"context"`, `"os"`, `"path/filepath"`, `"github.com/jgillich/tpd/internal/approval"`, `"github.com/jgillich/tpd/internal/profile"`.)

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
	isTTY := opts.IsTTY
	if isTTY == nil {
		isTTY = ui.IsTTYReader
	}
	in := opts.In
	if in == nil {
		in = os.Stdin
	}

	cfg, promptReq, err := approval.Filter(resolved, store)
	if err != nil {
		return Result{ExitCode: 2, Err: err}
	}

	if len(promptReq.Items) > 0 {
		if opts.AssumeYes || opts.AssumeNo {
			choices := buildChoices(promptReq, opts.AssumeYes)
			effectiveStore := store
			if opts.DryRun {
				effectiveStore = approval.NewEphemeralStore(store, mergeChoicesIntoState(promptReq, choices))
			} else {
				if err := store.Save(resolved.FullName, mergeChoicesIntoState(promptReq, choices)); err != nil {
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
			choices, err := prompt(promptReq, in, os.Stderr)
			if err != nil {
				return Result{ExitCode: 2, Err: fmt.Errorf("approval: %w", err)}
			}
			if incomplete(promptReq, choices) {
				return Result{ExitCode: 2, Err: fmt.Errorf("approval incomplete: %s", summarizeUndecided(promptReq, choices))}
			}
			if err := store.Save(resolved.FullName, mergeChoicesIntoState(promptReq, choices)); err != nil {
				return Result{ExitCode: 2, Err: err}
			}
			cfg, _, err = approval.Filter(resolved, store)
			if err != nil {
				return Result{ExitCode: 2, Err: err}
			}
		}
	}
```

Add the helper functions and `defaultApprovalDir`:

```go
func defaultApprovalDir() string {
	share, err := os.UserShareDir() // or os.UserHomeDir + "/.local/share"
	if err != nil || share == "" {
		share = filepath.Join(os.Getenv("HOME"), ".local", "share")
	}
	return share
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

func mergeChoicesIntoState(req approval.PromptRequest, choices map[string]map[string]bool) approval.State {
	st := approval.State{Hash: req.Hash, Profile: req.ProfileName}
	st.Approved = map[string]approval.ApprovedField{}
	for field, set := range choices {
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
		if set, ok := choices[it.Field]; ok {
			if _, d := set[it.Key]; d {
				continue
			}
		}
		parts = append(parts, it.Field+"."+it.Key)
	}
	return strings.Join(parts, ", ")
}
```

(Add imports: `"github.com/jgillich/tpd/internal/approval"`, `"github.com/jgillich/tpd/internal/ui"`, `"path/filepath"`, `"sort"`.)

Note: `os.UserShareDir` may not exist in Go 1.25; if not, use `os.UserConfigDir` parent or `$XDG_DATA_HOME`. Check the Go version and use the right call. The implementer should verify with `go doc os.UserShareDir`.

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
- The `if msg := catalog.Advisory(advisoryName(key)); msg != "" { ... }` blocks in `runShow` (around lines 160 and 174) and `runEdit` (around lines 185 and 228).
- The `advisoryName` helper (around lines 551-554).
- Remove the `"github.com/jgillich/tpd/internal/catalog"` import if no other use remains (check — `completeProfileNames` may still use it).

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

On a fresh state dir, run a sensitive profile and verify the dialog appears:
```bash
rm -rf ~/.local/share/tpd/approvals
go install ./cmd/tpd
tpd --dry-run --yes core/creds/ssh 2>&1 | head
```
Confirm: the dry-run prints the spec with the mount present (approved by --yes via the ephemeral store), and no approval file is written.

Then:
```bash
tpd --dry-run core/creds/ssh 2>&1 | head
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

**Placeholder scan:** Task 6 has skeleton helpers (`applyMapField` etc.) that Step 4 replaces with real logic. This is intentional — the full per-field walk is long and repetitive, and the test in Step 1 drives the implementation. The implementer must complete Step 4 fully; the plan calls this out. No "TBD"/"TODO" strings remain.

**Type consistency:** `Contributor`, `Provenance`, `Resolved`, `SensitiveItem`, `PromptRequest`, `Store`, `State`, `ApprovedField`, `EphemeralStore`, `ComputeApprovalHash`, `Filter`, `DefaultPrompt`, `IsTTYReader` are used consistently across tasks.