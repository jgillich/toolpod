# `tpd show --provenance` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `tpd show --provenance <name>` so users can see which catalog entry (file) contributed each piece of a resolved profile, making user-shadowing surprises like the `kubectl`-leak visible.

**Architecture:** Extend the existing per-key `Provenance` struct (currently limited to sensitive fields for the approval gate) to cover every merged profile field, capture the resolved extends chain in a new `Resolved.Chain`, and render one YAML section per chain entry showing only the keys that entry owns.

**Tech Stack:** Go 1.25, cobra, `gopkg.in/yaml.v3`, `internal/profile`, `cmd/tpd`.

## Global Constraints

- Attribute per-key last-writer-wins for maps (child wins; null deletes), matching `mergeProvMap` semantics exactly so the breakdown can never disagree with the resolved profile.
- `packages` is append+dedup: attribute each package to its **first declarer** in merge order; a whole-field `packages: null` resets the declarer set.
- Scalars (`image`, `command`, `tty`, `network`) and `resources.memory`/`resources.cpus` have a single owner: the last entry that set them.
- Chain dedup uses a whole-resolution `chainSeen` set, distinct from the cycle-detection `seen` stack and the per-level `resolved` map.
- Headers use the **FullName** (`core/claude`), never the bare display name (`claude`).
- `--provenance` implies resolution and errors identically to `--resolved`.
- No comments unless the code doesn't make something apparent.
- Run `go test ./...` and `go vet ./...` before committing.

---

### Task 1: Extend `Provenance` struct and `initProvenance`

**Files:**
- Modify: `internal/profile/provenance.go` (struct + `initProvenance`)
- Test: `internal/profile/provenance_test.go` (append)

**Interfaces:**
- Produces: `Provenance` gains fields `Tools, Caches, Repos, Files, Labels, Packages map[string]Contributor`, `Resources ResourcesProvenance`, and `Image, Command, TTY Contributor`; new type `ResourcesProvenance{Memory, CPUs Contributor}`. `initProvenance(rc RawProfile) Provenance` now stamps these from `rc`'s own declared keys.

- [ ] **Step 1: Write the failing test**

Append to `internal/profile/provenance_test.go`:

```go
func TestInitProvenanceStampsAllFields(t *testing.T) {
	rc := RawProfile{
		Profile: Profile{
			Tools:   map[string]Tool{"kubectl": {Version: "latest"}},
			Caches:  map[string]CachePaths{"go": {"~/go"}},
			Repos:   map[string]Repo{"mise": {ExtRepo: "mise"}},
			Files:   map[string]File{"/etc/x": {Content: "x"}},
			Labels:  map[string]string{"a": "b"},
			Packages: []string{"git"},
			Image:   "debian:13-slim",
			Command: []string{"sh"},
			TTY:     "true",
			Resources: &Resources{Memory: "1g", CPUs: "2"},
		},
		Namespace: "core",
		Name:      "mise",
	}
	prov := initProvenance(rc)
	c := Contributor{FullName: "core/mise", Namespace: "core"}
	if prov.Tools["kubectl"] != c {
		t.Errorf("Tools provenance = %+v, want %+v", prov.Tools["kubectl"], c)
	}
	if prov.Caches["go"] != c {
		t.Errorf("Caches provenance = %+v, want %+v", prov.Caches["go"], c)
	}
	if prov.Repos["mise"] != c {
		t.Errorf("Repos provenance = %+v, want %+v", prov.Repos["mise"], c)
	}
	if prov.Files["/etc/x"] != c {
		t.Errorf("Files provenance = %+v, want %+v", prov.Files["/etc/x"], c)
	}
	if prov.Labels["a"] != c {
		t.Errorf("Labels provenance = %+v, want %+v", prov.Labels["a"], c)
	}
	if prov.Packages["git"] != c {
		t.Errorf("Packages provenance = %+v, want %+v", prov.Packages["git"], c)
	}
	if prov.Image != c || prov.Command != c || prov.TTY != c {
		t.Errorf("scalar provenance = {image:%+v command:%+v tty:%+v}, want %+v for all", prov.Image, prov.Command, prov.TTY, c)
	}
	if prov.Resources.Memory != c || prov.Resources.CPUs != c {
		t.Errorf("resources provenance = %+v, want %+v", prov.Resources, c)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/profile/ -run TestInitProvenanceStampsAllFields`
Expected: FAIL — `provenance.Tools undefined` (fields don't exist).

- [ ] **Step 3: Implement**

In `internal/profile/provenance.go`, extend the struct:

```go
type Provenance struct {
	Mounts    map[string]Contributor
	Devices   map[string]Contributor
	Env       map[string]Contributor
	Ports     map[string]Contributor
	Dbus      DbusProvenance
	Network   Contributor
	Services  map[string]Contributor
	Tools     map[string]Contributor
	Caches    map[string]Contributor
	Repos     map[string]Contributor
	Files     map[string]Contributor
	Labels    map[string]Contributor
	Packages  map[string]Contributor
	Resources ResourcesProvenance
	Image     Contributor
	Command   Contributor
	TTY       Contributor
}

type ResourcesProvenance struct {
	Memory Contributor
	CPUs   Contributor
}
```

Extend `initProvenance` (before the closing `return prov`), following the existing per-field style:

```go
	if len(rc.Tools) > 0 {
		prov.Tools = make(map[string]Contributor, len(rc.Tools))
		for k := range rc.Tools {
			prov.Tools[k] = c
		}
	}
	if len(rc.Caches) > 0 {
		prov.Caches = make(map[string]Contributor, len(rc.Caches))
		for k := range rc.Caches {
			prov.Caches[k] = c
		}
	}
	if len(rc.Repos) > 0 {
		prov.Repos = make(map[string]Contributor, len(rc.Repos))
		for k := range rc.Repos {
			prov.Repos[k] = c
		}
	}
	if len(rc.Files) > 0 {
		prov.Files = make(map[string]Contributor, len(rc.Files))
		for k := range rc.Files {
			prov.Files[k] = c
		}
	}
	if len(rc.Labels) > 0 {
		prov.Labels = make(map[string]Contributor, len(rc.Labels))
		for k := range rc.Labels {
			prov.Labels[k] = c
		}
	}
	if len(rc.Packages) > 0 {
		prov.Packages = make(map[string]Contributor, len(rc.Packages))
		for _, p := range rc.Packages {
			prov.Packages[p] = c
		}
	}
	if rc.Resources != nil {
		if rc.Resources.Memory != "" {
			prov.Resources.Memory = c
		}
		if rc.Resources.CPUs != "" {
			prov.Resources.CPUs = c
		}
	}
	if rc.Image != "" {
		prov.Image = c
	}
	if len(rc.Command) > 0 {
		prov.Command = c
	}
	if rc.TTY != "" {
		prov.TTY = c
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/profile/ -run TestInitProvenanceStampsAllFields`
Expected: PASS.

- [ ] **Step 5: Run full suite and vet**

Run: `go test ./internal/profile/ && go vet ./internal/profile/`
Expected: all pass (existing tests construct `Provenance` positionally only via struct literals with field names, so new fields are safe).

- [ ] **Step 6: Commit**

```bash
git add internal/profile/provenance.go internal/profile/provenance_test.go
git commit -m "feat(profile): extend provenance to all profile fields"
```

---

### Task 2: Merge attribution for map fields

**Files:**
- Modify: `internal/profile/merge.go` (`MergeProfiles`)
- Test: `internal/profile/attribution_test.go` (create)

**Interfaces:**
- Consumes: `Provenance.Tools/Caches/Repos/Files/Labels` from Task 1.
- Produces: `MergeProfiles` populates `out.Provenance.Tools/Caches/Repos/Files/Labels` using `mergeProvMap` (existing helper, `merge.go:319`).

- [ ] **Step 1: Write the failing test**

Create `internal/profile/attribution_test.go`:

```go
package profile

import "testing"

func TestMergeAttributionMapFieldsLastWriter(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"base": {Profile: Profile{
			Tools:  map[string]Tool{"kubectl": {Version: "1.0"}, "k9s": {Version: "0.5"}},
			Caches: map[string]CachePaths{"go": {"~/go"}},
		}},
		"myapp": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{Raw: []string{"base"}},
			Image:       "x",
			Command:     []string{"myapp"},
			Tools:       map[string]Tool{"kubectl": {Version: "1.1"}},
		}},
	})
	res, err := ResolveProfileWithProv(cat, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	base := Contributor{FullName: "core/base", Namespace: "core"}
	app := Contributor{FullName: "core/myapp", Namespace: "core"}
	if got := res.Prov.Tools["kubectl"]; got != app {
		t.Errorf("kubectl attributed to %+v, want %+v (child override)", got, app)
	}
	if got := res.Prov.Tools["k9s"]; got != base {
		t.Errorf("k9s attributed to %+v, want %+v (parent key preserved)", got, base)
	}
	if got := res.Prov.Caches["go"]; got != base {
		t.Errorf("go cache attributed to %+v, want %+v", got, base)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/profile/ -run TestMergeAttributionMapFieldsLastWriter`
Expected: FAIL — `res.Prov.Tools["kubectl"]` is the zero `Contributor` (field never populated).

- [ ] **Step 3: Implement**

In `internal/profile/merge.go`, in `MergeProfiles`, after the existing `out.Tools = mergeMap(...)` line:

```go
	out.Provenance.Tools = mergeProvMap(parent.Provenance.Tools, child.Provenance.Tools, keysOf(child.Tools), child.NullKeys["tools"], childContrib)
```

And after the `out.Caches = mergeMap(...)`, `out.Repos = mergeMap(...)`, `out.Files = mergeMap(...)`, `out.Labels = mergeStringMap(...)` lines respectively:

```go
	out.Provenance.Caches = mergeProvMap(parent.Provenance.Caches, child.Provenance.Caches, keysOf(child.Caches), child.NullKeys["caches"], childContrib)
	out.Provenance.Repos = mergeProvMap(parent.Provenance.Repos, child.Provenance.Repos, keysOf(child.Repos), child.NullKeys["repos"], childContrib)
	out.Provenance.Files = mergeProvMap(parent.Provenance.Files, child.Provenance.Files, keysOf(child.Files), child.NullKeys["files"], childContrib)
	out.Provenance.Labels = mergeProvMap(parent.Provenance.Labels, child.Provenance.Labels, keysOf(child.Labels), child.NullKeys["labels"], childContrib)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/profile/ -run TestMergeAttributionMapFieldsLastWriter`
Expected: PASS.

- [ ] **Step 5: Full suite + commit**

Run: `go test ./internal/profile/ && go vet ./internal/profile/`

```bash
git add internal/profile/merge.go internal/profile/attribution_test.go
git commit -m "feat(profile): attribute map fields to last writer"
```

---

### Task 3: Merge attribution for scalars and resources

**Files:**
- Modify: `internal/profile/merge.go` (`MergeProfiles`)
- Test: `internal/profile/attribution_test.go` (append)

**Interfaces:**
- Consumes: `Provenance.Image/Command/TTY/Resources` from Task 1.
- Produces: `MergeProfiles` attributes `image`, `command`, `tty`, `resources.memory`, `resources.cpus` to their last writer, preferring accumulated child provenance over the direct child (mirrors the existing `Network` pattern at `merge.go:163-171`).

- [ ] **Step 1: Write the failing test**

Append to `internal/profile/attribution_test.go`:

```go
func TestMergeAttributionScalarsLastWriter(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"base": {Profile: Profile{
			Image:     "a:latest",
			Command:   []string{"a"},
			TTY:       "true",
			Network:   "none",
			Resources: &Resources{Memory: "512m", CPUs: "1"},
		}},
		"mid": {Profile: Profile{
			ExtendsList: ExtendsList{Raw: []string{"base"}},
		}},
		"myapp": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{Raw: []string{"mid"}},
			Command:     []string{"myapp"},
			Resources:   &Resources{Memory: "1g"},
		}},
	})
	res, err := ResolveProfileWithProv(cat, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	base := Contributor{FullName: "core/base", Namespace: "core"}
	app := Contributor{FullName: "core/myapp", Namespace: "core"}
	if res.Prov.Image != base {
		t.Errorf("image attributed to %+v, want %+v (base, only declarer through mid)", res.Prov.Image, base)
	}
	if res.Prov.Command != app {
		t.Errorf("command attributed to %+v, want %+v (myapp override)", res.Prov.Command, app)
	}
	if res.Prov.TTY != base {
		t.Errorf("tty attributed to %+v, want %+v", res.Prov.TTY, base)
	}
	if res.Prov.Network != base {
		t.Errorf("network attributed to %+v, want %+v", res.Prov.Network, base)
	}
	if res.Prov.Resources.Memory != app {
		t.Errorf("resources.memory attributed to %+v, want %+v (myapp override)", res.Prov.Resources.Memory, app)
	}
	if res.Prov.Resources.CPUs != base {
		t.Errorf("resources.cpus attributed to %+v, want %+v (base, not overridden)", res.Prov.Resources.CPUs, base)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/profile/ -run TestMergeAttributionScalarsLastWriter`
Expected: FAIL — `res.Prov.Image` and `res.Prov.Command` are zero values.

- [ ] **Step 3: Implement**

In `internal/profile/merge.go` `MergeProfiles`, replace the `Image` block:

```go
	if child.Image != "" {
		out.Image = child.Image
	}
```

with:

```go
	if child.Image != "" {
		out.Image = child.Image
		out.Provenance.Image = child.Provenance.Image
		if out.Provenance.Image.FullName == "" {
			out.Provenance.Image = childContrib
		}
	}
```

Replace the `Command` block:

```go
	if child.Command != nil {
		out.Command = child.Command
	}
```

with:

```go
	if child.Command != nil {
		out.Command = child.Command
		out.Provenance.Command = child.Provenance.Command
		if out.Provenance.Command.FullName == "" {
			out.Provenance.Command = childContrib
		}
	}
```

Add a `TTY` attribution after the existing `if child.TTY != "" { out.TTY = child.TTY }`:

```go
	if child.TTY != "" {
		out.TTY = child.TTY
		out.Provenance.TTY = child.Provenance.TTY
		if out.Provenance.TTY.FullName == "" {
			out.Provenance.TTY = childContrib
		}
	}
```

The existing `resources` block already copies values inside per-subfield guards (`if child.Resources.Memory != ""`, `if child.Resources.CPUs != ""`). Add the provenance lines inside those same guards, mirroring the `Network` pattern (prefer accumulated child provenance, fall back to `childContrib`); inherited subfields keep the parent's attribution because `out := parent` already carried it:

```go
		if child.Resources.Memory != "" {
			out.Provenance.Resources.Memory = child.Provenance.Resources.Memory
			if out.Provenance.Resources.Memory.FullName == "" {
				out.Provenance.Resources.Memory = childContrib
			}
		}
		if child.Resources.CPUs != "" {
			out.Provenance.Resources.CPUs = child.Provenance.Resources.CPUs
			if out.Provenance.Resources.CPUs.FullName == "" {
				out.Provenance.Resources.CPUs = childContrib
			}
		}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/profile/ -run TestMergeAttributionScalarsLastWriter`
Expected: PASS.

- [ ] **Step 5: Full suite + commit**

Run: `go test ./internal/profile/ && go vet ./internal/profile/`

```bash
git add internal/profile/merge.go internal/profile/attribution_test.go
git commit -m "feat(profile): attribute scalars and resources to last writer"
```

---

### Task 4: Packages first-declarer attribution

**Files:**
- Modify: `internal/profile/merge.go` (add `mergePackageProv`, call it in `MergeProfiles`)
- Test: `internal/profile/attribution_test.go` (append)

**Interfaces:**
- Consumes: `Provenance.Packages` from Task 1.
- Produces: `mergePackageProv(parentProv map[string]Contributor, child []string, childProv map[string]Contributor, nullKeys map[string]bool, childContrib Contributor) map[string]Contributor` — first declarer per package; a whole-field `packages: null` (`nullKeys["*"]`) resets the map. The caller passes the **parent's accumulated provenance** as `parentProv` (the attribution carried by the already-merged parent), not the parent's package list.

- [ ] **Step 1: Write the failing test**

Append to `internal/profile/attribution_test.go`:

```go
func TestMergeAttributionPackagesFirstDeclarer(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"base": {Profile: Profile{Packages: []string{"git", "curl"}}},
		"mid": {Profile: Profile{
			ExtendsList: ExtendsList{Raw: []string{"base"}},
			Packages:    []string{"curl", "vim"},
		}},
		"myapp": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{Raw: []string{"mid"}},
			Image:       "x",
			Command:     []string{"myapp"},
			Packages:    []string{"vim"},
		}},
	})
	res, err := ResolveProfileWithProv(cat, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	base := Contributor{FullName: "core/base", Namespace: "core"}
	mid := Contributor{FullName: "core/mid", Namespace: "core"}
	if res.Prov.Packages["git"] != base {
		t.Errorf("git attributed to %+v, want %+v", res.Prov.Packages["git"], base)
	}
	if res.Prov.Packages["curl"] != base {
		t.Errorf("curl attributed to %+v, want %+v (first declarer wins over later dup)", res.Prov.Packages["curl"], base)
	}
	if res.Prov.Packages["vim"] != mid {
		t.Errorf("vim attributed to %+v, want %+v", res.Prov.Packages["vim"], mid)
	}
}

func TestMergeAttributionPackagesNullResets(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"base": {Profile: Profile{Packages: []string{"git"}}},
		"mid": {Profile: Profile{
			ExtendsList: ExtendsList{Raw: []string{"base"}},
		}, NullKeys: map[string]map[string]bool{"packages": {"*": true}}},
		"myapp": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{Raw: []string{"mid"}},
			Image:       "x",
			Command:     []string{"myapp"},
			Packages:    []string{"git"},
		}},
	})
	res, err := ResolveProfileWithProv(cat, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	app := Contributor{FullName: "core/myapp", Namespace: "core"}
	if len(res.Packages) != 1 || res.Packages[0] != "git" {
		t.Fatalf("packages = %v, want [git]", res.Packages)
	}
	if got := res.Prov.Packages["git"]; got != app {
		t.Errorf("after mid's packages: null, re-declared git attributed to %+v, want %+v", got, app)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/profile/ -run 'TestMergeAttributionPackages'`
Expected: FAIL — `res.Prov.Packages` is nil/empty.

- [ ] **Step 3: Implement**

In `internal/profile/merge.go`, after `mergePackages`, add:

```go
// mergePackageProv attributes each package to its first declarer in merge
// order (packages append+dedup, so there is no override). Seeds from the
// parent's accumulated provenance so attributions survive intermediate
// merges. A whole-field "packages: null" resets the map, so a later entry
// re-declaring a package owns it.
func mergePackageProv(parentProv map[string]Contributor, child []string, childProv map[string]Contributor, nullKeys map[string]bool, childContrib Contributor) map[string]Contributor {
	if nullKeys != nil && nullKeys["*"] {
		return map[string]Contributor{}
	}
	out := make(map[string]Contributor, len(parentProv)+len(child))
	for p, c := range parentProv {
		out[p] = c
	}
	for _, p := range child {
		if _, ok := out[p]; ok {
			continue
		}
		if c, ok := childProv[p]; ok {
			out[p] = c
		} else {
			out[p] = childContrib
		}
	}
	return out
}
```

In `MergeProfiles`, after the existing `out.Packages = mergePackages(...)` line, add:

```go
	out.Provenance.Packages = mergePackageProv(parent.Provenance.Packages, child.Packages, child.Provenance.Packages, child.NullKeys["packages"], childContrib)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/profile/ -run 'TestMergeAttributionPackages'`
Expected: PASS.

- [ ] **Step 5: Full suite + commit**

Run: `go test ./internal/profile/ && go vet ./internal/profile/`

```bash
git add internal/profile/merge.go internal/profile/attribution_test.go
git commit -m "feat(profile): attribute packages to first declarer"
```

---

### Task 5: Capture the resolved chain

**Files:**
- Modify: `internal/profile/types.go` (`Resolved`, new `ChainEntry`)
- Modify: `internal/profile/merge.go` (`resolveChain`, `ResolveProfileWithProv`, `ResolveFragmentWithProv`)
- Test: `internal/profile/chain_test.go` (create)

**Interfaces:**
- Produces:
  - `type ChainEntry struct { FullName, DisplayName, Path string; Extends []string }` (Extends is the entry's own declared extends, as written)
  - `Resolved.Chain []ChainEntry`
  - `resolveChain(cat Catalog, key string, seen map[string]bool, chain *chainState) (RawProfile, error)`; `type chainState struct { entries []ChainEntry; seen map[string]bool }`

- [ ] **Step 1: Write the failing test**

Create `internal/profile/chain_test.go`:

```go
package profile

import "testing"

func TestChainEntriesInPreOrderDeduped(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"base": {Profile: Profile{Image: "base", Packages: []string{"git"}}},
		"a": {Profile: Profile{
			ExtendsList: ExtendsList{Raw: []string{"base"}},
			Tools:       map[string]Tool{"a": {Version: "1"}},
		}},
		"b": {Profile: Profile{
			ExtendsList: ExtendsList{Raw: []string{"base"}},
			Tools:       map[string]Tool{"b": {Version: "1"}},
		}},
		"myapp": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{Raw: []string{"a", "b"}},
			Command:     []string{"myapp"},
		}},
	})
	res, err := ResolveProfileWithProv(cat, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(res.Chain))
	for i, e := range res.Chain {
		names[i] = e.FullName
	}
	want := []string{"core/myapp", "core/a", "core/base", "core/b"}
	if len(names) != len(want) {
		t.Fatalf("chain = %v, want %v (shared parent once)", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("chain[%d] = %q, want %q (full chain %v)", i, names[i], want[i], names)
		}
	}
	if len(res.Chain[0].Extends) == 0 {
		t.Errorf("root ChainEntry should record its own extends: %+v", res.Chain[0])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/profile/ -run TestChainEntriesInPreOrderDeduped`
Expected: FAIL — `res.Chain undefined` (field doesn't exist).

- [ ] **Step 3: Implement**

In `internal/profile/types.go`, after the `Resolved` struct, add:

```go
// ChainEntry is one catalog entry in a resolved profile's extends chain, in
// pre-order, deduped. Extends is the entry's own declared extends as written.
// Rendered by tpd show --provenance.
type ChainEntry struct {
	FullName    string
	DisplayName string
	Path        string
	Extends     []string
}
```

Add `Chain []ChainEntry` to the `Resolved` struct:

```go
type Resolved struct {
	Profile
	Prov        Provenance
	FullName    string
	DisplayName string
	Chain       []ChainEntry
}
```

In `internal/profile/merge.go`:

```go
// chainState accumulates chain entries during resolution. seen is
// whole-resolution (unlike resolveChain's per-path cycle stack), so a parent
// shared across two sibling subtrees is recorded once.
type chainState struct {
	entries []ChainEntry
	seen    map[string]bool
}
```

Change the `resolveChain` signature and the top of the function:

```go
func resolveChain(cat Catalog, key string, seen map[string]bool, chain *chainState) (RawProfile, error) {
	rc, ok := cat.Get(key)
	if !ok {
		return RawProfile{}, ProfileError{Message: "profile not found: " + key}
	}
	if !chain.seen[key] {
		chain.seen[key] = true
		chain.entries = append(chain.entries, ChainEntry{
			FullName:    rc.FullName(),
			DisplayName: rc.DisplayName(),
			Path:        rc.Path,
			Extends:     rc.ExtendsList.Raw,
		})
	}
	if seen[key] {
		return RawProfile{}, ProfileError{Path: rc.Path, Message: "extends cycle detected at: " + key}
	}
	...
```

Update the two recursive calls to `resolveChain` inside the loop to pass `chain` through:

```go
		parent, err := resolveChain(cat, pkey, seen, chain)
```

Update the two entry points, `ResolveProfileWithProv` and `ResolveFragmentWithProv`:

```go
	cs := &chainState{seen: map[string]bool{}}
	merged, err := resolveChain(cat, key, map[string]bool{}, cs)
	...
	res.Chain = cs.entries
```

(in each function, right before `return Resolved{...}`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/profile/ -run TestChainEntriesInPreOrderDeduped`
Expected: PASS.

- [ ] **Step 5: Full suite + commit**

Run: `go test ./internal/profile/ && go vet ./internal/profile/`

```bash
git add internal/profile/types.go internal/profile/merge.go internal/profile/chain_test.go
git commit -m "feat(profile): capture resolved extends chain"
```

---

### Task 6: Render the per-file breakdown

**Files:**
- Create: `internal/profile/breakdown.go`
- Create: `internal/profile/breakdown_test.go`

**Interfaces:**
- Consumes: `Resolved{Chain, Prov, Profile}` from Tasks 1-5.
- Produces: `func (r Resolved) ProvenanceYAML() (string, error)` — one `# <FullName>  (<Path>)` header section per chain entry, body = the entry's own `extends` plus only the keys attributed to it. Keys within a section are sorted (yaml.v3 sorts map keys).

- [ ] **Step 1: Write the failing test**

Create `internal/profile/breakdown_test.go`:

```go
package profile

import (
	"strings"
	"testing"
)

func TestProvenanceYAMLShadowing(t *testing.T) {
	cat := Catalog{
		entries: map[string]RawProfile{
			"core/t3code": {Profile: Profile{
				Version:     1,
				ExtendsList: ExtendsList{Raw: []string{"claude"}},
				Image:       "debian:13-slim",
				Command:     []string{"t3code"},
			}, Namespace: "core", Name: "t3code", Path: "built-in:profiles/t3code.yaml"},
			"claude": {Profile: Profile{
				ExtendsList: ExtendsList{Raw: []string{"core/claude", "core/infra/kubernetes"}},
			}, Namespace: "", Name: "claude", Path: "/home/u/.config/tpd/profiles/claude.yaml"},
			"core/claude": {Profile: Profile{
				Tools: map[string]Tool{"claude": {Version: "latest"}},
			}, Namespace: "core", Name: "claude", Path: "built-in:profiles/claude.yaml"},
			"core/infra/kubernetes": {Profile: Profile{
				Tools: map[string]Tool{"kubectl": {Version: "latest"}},
			}, Namespace: "core", Name: "infra/kubernetes", Path: "built-in-fragment:fragments/infra/kubernetes.yaml"},
		},
		namespaces: map[string]bool{"": true, "core": true},
		fragments:  map[string]bool{"core/infra/kubernetes": true},
	}
	for _, k := range []string{"core/t3code", "claude", "core/claude", "core/infra/kubernetes"} {
		e := cat.entries[k]
		if err := e.ExtendsList.Resolve(map[string]bool{"": true, "core": true}); err != nil {
			t.Fatal(err)
		}
		cat.entries[k] = e
	}
	res, err := ResolveProfileWithProv(cat, "core/t3code")
	if err != nil {
		t.Fatal(err)
	}
	out, err := res.ProvenanceYAML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "# core/t3code  (built-in:profiles/t3code.yaml)") {
		t.Errorf("missing root header:\n%s", out)
	}
	if !strings.Contains(out, "# claude  (/home/u/.config/tpd/profiles/claude.yaml)") {
		t.Errorf("missing user shadow header (root cause):\n%s", out)
	}
	if !strings.Contains(out, "# core/infra/kubernetes  (built-in-fragment:fragments/infra/kubernetes.yaml)") {
		t.Errorf("missing fragment header:\n%s", out)
	}
	if !strings.Contains(out, "kubectl: latest") {
		t.Errorf("kubectl should appear under its declaring fragment:\n%s", out)
	}
	if strings.Count(out, "claude: latest") != 1 {
		t.Errorf("claude: latest should appear exactly once (core/claude, not overridden):\n%s", out)
	}
	if !strings.HasPrefix(out, "# ") {
		t.Errorf("output should start with the root header:\n%s", out)
	}
}

func TestProvenanceYAMLOverrideHidesParent(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"base": {Profile: Profile{
			Image:  "x",
			Tools:  map[string]Tool{"kubectl": {Version: "1.0"}},
		}},
		"myapp": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{Raw: []string{"base"}},
			Command:     []string{"myapp"},
			Tools:       map[string]Tool{"kubectl": {Version: "1.1"}},
		}},
	})
	res, err := ResolveProfileWithProv(cat, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	out, err := res.ProvenanceYAML()
	if err != nil {
		t.Fatal(err)
	}
	base := "kubectl: 1.0"
	if strings.Contains(out, base) {
		t.Errorf("parent's overridden kubectl must not appear:\n%s", out)
	}
	if !strings.Contains(out, "kubectl: 1.1") {
		t.Errorf("child's kubectl override should appear:\n%s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/profile/ -run TestProvenanceYAML`
Expected: FAIL — `res.ProvenanceYAML undefined`.

- [ ] **Step 3: Implement**

Create `internal/profile/breakdown.go`:

```go
package profile

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProvenanceYAML renders the resolved profile as one section per chain entry,
// in chain (pre-)order. Each section shows the entry's own declared extends
// plus only the keys it owns in the final merge. Sections are diagnostic
// output, not a single parseable YAML document. yaml.v3 sorts map keys, so
// key order within a section is deterministic.
func (r Resolved) ProvenanceYAML() (string, error) {
	var b strings.Builder
	for i, e := range r.Chain {
		sec := r.ownedSection(e.FullName, e.Extends)
		data, err := yaml.Marshal(sec)
		if err != nil {
			return "", err
		}
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "# %s  (%s)\n", e.FullName, e.Path)
		if len(sec) > 0 {
			b.Write(data)
		}
	}
	return b.String(), nil
}

// ownedSection returns the YAML body for one chain entry: its own extends and
// the keys attributed to it (values taken from the merged profile).
func (r Resolved) ownedSection(fullName string, extends []string) map[string]any {
	sec := map[string]any{}
	if len(extends) > 0 {
		sec["extends"] = extends
	}
	owned := func(prov map[string]Contributor, vals map[string]any) map[string]any {
		out := map[string]any{}
		for k, c := range prov {
			if c.FullName == fullName {
				out[k] = vals[k]
			}
		}
		return out
	}
	if m := owned(r.Prov.Tools, asAnyMap(r.Tools)); len(m) > 0 {
		sec["tools"] = m
	}
	if m := owned(r.Prov.Caches, asAnyMap(r.Caches)); len(m) > 0 {
		sec["caches"] = m
	}
	if m := owned(r.Prov.Repos, asAnyMap(r.Repos)); len(m) > 0 {
		sec["repos"] = m
	}
	if m := owned(r.Prov.Files, asAnyMap(r.Files)); len(m) > 0 {
		sec["files"] = m
	}
	if m := owned(r.Prov.Labels, asAnyMap(r.Labels)); len(m) > 0 {
		sec["labels"] = m
	}
	pkgs := map[string]bool{}
	for p, c := range r.Prov.Packages {
		if c.FullName == fullName {
			pkgs[p] = true
		}
	}
	if len(pkgs) > 0 {
		sec["packages"] = sortedKeys(pkgs)
	}
	if m := owned(r.Prov.Mounts, asAnyMap(r.Mounts)); len(m) > 0 {
		sec["mounts"] = m
	}
	if m := owned(r.Prov.Env, asAnyMap(r.Env)); len(m) > 0 {
		sec["environment"] = m
	}
	if m := owned(r.Prov.Ports, asAnyMap(r.Ports)); len(m) > 0 {
		sec["ports"] = m
	}
	if m := owned(r.Prov.Devices, asAnyMap(r.Devices)); len(m) > 0 {
		sec["devices"] = m
	}
	if m := owned(r.Prov.Services, asAnyMap(r.Services)); len(m) > 0 {
		sec["services"] = m
	}
	if r.Dbus != nil {
		dbus := map[string]any{}
		if m := owned(r.Prov.Dbus.Talk, asAnyMap(r.Dbus.Talk)); len(m) > 0 {
			dbus["talk"] = m
		}
		if m := owned(r.Prov.Dbus.Own, asAnyMap(r.Dbus.Own)); len(m) > 0 {
			dbus["own"] = m
		}
		if len(dbus) > 0 {
			sec["dbus"] = dbus
		}
	}
	if r.Prov.Image.FullName == fullName {
		sec["image"] = r.Image
	}
	if r.Prov.Command.FullName == fullName {
		sec["command"] = r.Command
	}
	if r.Prov.TTY.FullName == fullName {
		sec["tty"] = r.TTY
	}
	if r.Prov.Network.FullName == fullName {
		sec["network"] = r.Network
	}
	if r.Resources != nil && (r.Prov.Resources.Memory.FullName == fullName || r.Prov.Resources.CPUs.FullName == fullName) {
		rc := map[string]any{}
		if r.Prov.Resources.Memory.FullName == fullName {
			rc["memory"] = r.Resources.Memory
		}
		if r.Prov.Resources.CPUs.FullName == fullName {
			rc["cpus"] = r.Resources.CPUs
		}
		sec["resources"] = rc
	}
	return sec
}

// asAnyMap widens a typed map to map[string]any for the owned() helper.
func asAnyMap[V any](m map[string]V) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
```

(Reuse the existing generic `sortedKeys[V any]` in `hash.go` for `map[string]bool` — do NOT define a `sortedKeys(map[string]bool)` variant; a non-generic function of the same name cannot coexist with a generic one in the same package.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/profile/ -run TestProvenanceYAML`
Expected: PASS.

- [ ] **Step 5: Full suite + commit**

Run: `go test ./internal/profile/ && go vet ./internal/profile/`

```bash
git add internal/profile/breakdown.go internal/profile/breakdown_test.go
git commit -m "feat(profile): render per-file provenance breakdown"
```

---

### Task 7: Wire up the `--provenance` CLI flag

**Files:**
- Modify: `cmd/tpd/cli.go` (`newShowCommand`, `runShow`)
- Test: `cmd/tpd/profile_test.go` (append)

**Interfaces:**
- Consumes: `profile.ResolveProfileWithProv`, `profile.ResolveFragmentWithProv`, `(res Resolved).ProvenanceYAML()` from Tasks 1-6.

- [ ] **Step 1: Write the failing test**

Append to `cmd/tpd/profile_test.go`:

```go
func TestProfileShowProvenance(t *testing.T) {
	cfg := t.TempDir()
	profilesDir := filepath.Join(cfg, "tpd", "profiles")
	if err := os.MkdirAll(profilesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profilesDir, "base.yaml"), []byte("version: 1\nimage: debian:13-slim\ntools:\n  kubectl: 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profilesDir, "myapp.yaml"), []byte("version: 1\nextends: base\ncommand: [\"myapp\"]\ntools:\n  kubectl: 1.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runTpdCfg(t, cfg, "show", "--provenance", "myapp")
	if err != nil {
		t.Fatalf("show --provenance myapp: %v\n%s", err, out)
	}
	if !strings.Contains(out, "# myapp  ("+filepath.Join(profilesDir, "myapp.yaml")+")") {
		t.Errorf("expected myapp user section with its file path, got:\n%s", out)
	}
	if !strings.Contains(out, "# base  ("+filepath.Join(profilesDir, "base.yaml")+")") {
		t.Errorf("expected base section with its file path, got:\n%s", out)
	}
	if strings.Contains(out, `kubectl: "1.0"`) {
		t.Errorf("base's overridden kubectl must not appear, got:\n%s", out)
	}
	if !strings.Contains(out, `kubectl: "1.1"`) {
		t.Errorf("myapp's kubectl override should appear, got:\n%s", out)
	}
}

func TestProfileShowProvenanceNonexistent(t *testing.T) {
	out, _ := runTpd(t, "show", "--provenance", "nope")
	if !strings.Contains(out, "not found") {
		t.Errorf("expected 'not found' error for missing profile, got:\n%s", out)
	}
}

func TestProfileShowProvenanceFragment(t *testing.T) {
	cfg := t.TempDir()
	seedUserConfig(t, cfg)
	out, err := runTpdCfg(t, cfg, "show", "--provenance", "misc/util")
	if err != nil {
		t.Fatalf("show --provenance misc/util: %v\n%s", err, out)
	}
	if !strings.Contains(out, "# misc/util  ("+filepath.Join(cfg, "tpd", "fragments", "misc", "util.yaml")+")") {
		t.Errorf("expected fragment section with its file path, got:\n%s", out)
	}
	if !strings.Contains(out, "util: latest") {
		t.Errorf("expected fragment's own tool, got:\n%s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/tpd/ -run 'TestProfileShowProvenance'`
Expected: FAIL — `unknown flag: --provenance`.

- [ ] **Step 3: Implement**

In `cmd/tpd/cli.go`, change `newShowCommand`:

```go
func newShowCommand() *cobra.Command {
	var resolved, provenance bool
	cmd := &cobra.Command{
		Use:               "show <name>",
		Short:             "Print a profile (use --resolved to inline extends).",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeNamesOnce,
		RunE: func(c *cobra.Command, args []string) error {
			return runShow(args[0], resolved, provenance)
		},
	}
	cmd.Flags().BoolVar(&resolved, "resolved", false, "Inline all extends and show the fully merged profile.")
	cmd.Flags().BoolVar(&provenance, "provenance", false, "Show which catalog entry contributed each field (implies --resolved).")
	return cmd
}
```

Change `runShow` to resolve with provenance when requested (mirroring the existing `--resolved` profile/fragment branch):

```go
func runShow(name string, resolved, provenance bool) error {
	cat, err := profile.LoadProfiles(profile.DefaultProfileDir())
	if err != nil {
		return fmt.Errorf("loading profiles: %w", err)
	}
	key, ok := resolveCatalogName(cat, name)
	if !ok {
		return profile.ProfileError{Message: "profile not found: " + name}
	}
	if provenance {
		if cat.IsFragment(key) {
			resolvedProfile, err := profile.ResolveFragmentWithProv(cat, key)
			if err != nil {
				return err
			}
			out, err := resolvedProfile.ProvenanceYAML()
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		}
		resolvedProfile, err := profile.ResolveProfileWithProv(cat, key)
		if err != nil {
			return err
		}
		out, err := resolvedProfile.ProvenanceYAML()
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	}
	if resolved {
		// ...existing --resolved branch unchanged...
	}
	rc, _ := cat.Get(key)
	out, err := yaml.Marshal(rc.Profile)
	if err != nil {
		return err
	}
	fmt.Print(string(out))
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/tpd/ -run 'TestProfileShowProvenance'`
Expected: PASS.

- [ ] **Step 5: Full suite + vet + commit**

Run: `go test ./... && go vet ./...`

```bash
git add cmd/tpd/cli.go cmd/tpd/profile_test.go
git commit -m "feat(cli): add tpd show --provenance"
```

---

## Self-review notes

- Spec coverage: per-key attribution (Tasks 2-4), packages first-declarer + null reset (Task 4), scalars/resources owners (Task 3), chain capture with whole-resolution dedup (Task 5), renderer with FullName headers + source paths + sorted keys (Task 6), CLI flag implying resolution with identical errors (Task 7), shadowing test (Task 6), e2e (Task 7). All spec sections map to a task.
- No placeholders: every step has concrete code and expected output.
- Type consistency: `ProvenanceYAML` (Task 6) consumes `Resolved.Chain`/`Prov` (Tasks 1-5); `chainState` used by `resolveChain` (Task 5); `Resolved` gains `Chain` in Task 5 before Task 6 reads it; `runShow(name, resolved, provenance)` matches the Task 7 test invocation.
