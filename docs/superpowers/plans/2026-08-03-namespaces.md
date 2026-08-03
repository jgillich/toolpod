# Profile/fragment namespaces Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give catalog entries a structured `(Namespace, Name)` identity so `core/mise` always refers to the embedded built-in, unqualified `mise` resolves user-first then `core/`, and the `extends: <self>` shadow special-case is replaced by explicit `extends: core/<name>`.

**Architecture:** Every `RawProfile` gains `Namespace`/`Name` fields; the `Catalog` keys its `entries` map by `FullName()` (`core/mise`, bare `mise`) and tracks a `namespaces` registry. A `Ref{Namespace, Name}` is the structured form of an extends/CLI reference; `ParseRef` does longest-prefix splitting against the registry, and `ResolveRef` does the user-first-then-core fallback for unqualified names. `ExtendsList` becomes a struct `{Raw, Resolved []Ref}` that is split by `parseRaw` after YAML decode. The `IsUserShadow`/`GetBuiltin`/`resolveBuiltinChain` special-case is deleted; self-reference falls out of the existing `seen` cycle check once unqualified self-names resolve to the entry itself.

**Tech Stack:** Go 1.25, `gopkg.in/yaml.v3`, existing `kong` CLI, `embed.FS` catalog. No new external dependencies.

## Global Constraints

- Go 1.25, CGO off in releases.
- No new external dependencies.
- No comments unless the code doesn't make something apparent (business rules, design rationale, edge cases — per `AGENTS.md`).
- Follow existing patterns: `ProfileError{Path, Line, Message}`, `mergeMap`/`collectNullKeys`, `NewProfileCatalogForTest`, the `kong` CLI passthrough in `cmd/tpd/cli.go`.
- Local names stay single-segment (file basenames); namespaces carry any path prefix. `ValidateName` still rejects `/` for the file/display name.
- Every task ends with `go test ./...` + `go vet ./...` passing.
- Built-in **profile** files keep unqualified `extends: mise` (they intend to pick up user customizations). Built-in **fragment** files that extend another built-in fragment get `core/`-qualified refs (only `typescript.yaml` today).
- `entries` is always keyed by `FullName`. For the empty namespace `FullName` is the bare name (`mise`, never `/mise`).

---

## File Structure

**Modified (production):**
- `internal/profile/types.go` — `ExtendsList` struct (`Raw`, `Resolved []Ref`), `RawProfile.Namespace`/`Name` fields, `FullName()`/`DisplayName()`, `Ref` struct.
- `internal/profile/ref.go` (new) — `ParseRef`, `ResolveRef`.
- `internal/profile/catalog.go` — `Catalog` fields (`entries`, `namespaces`, `fragments`), new APIs (`Get`, `IsFragment`, `AddRaw(ns,name,rc)`, `DisplayNames`, `ProfileDisplayNames`, `Source`, `FragmentByDisplayName`, `Names`, `ProfileNames`), loaders stamp `Namespace`/`Name`, `NewProfileCatalogForTest` stamps `core`.
- `internal/profile/merge.go` — delete `IsUserShadow` branch + `resolveBuiltinChain`; `resolveChain`/`ResolveProfile`/`ResolveFragment` take a canonical key and resolve refs via `ResolveRef`; `MergeProfiles` handles the `ExtendsList` struct.
- `internal/profile/validate.go` — `ValidateName` unchanged; document that qualified CLI args strip the namespace prefix before validation.
- `internal/catalog/fragments/typescript.yaml` — `extends: core/javascript`.
- `internal/scaffold/scaffold.go` — emit `core/<name>` for built-in bases and fragment refs; `FragmentByDisplayName` mapping; `AddRaw("", ...)` signature.
- `internal/scaffold/fragments.go` — `FragmentNames()` unchanged (display names); the mapping to canonical happens in `scaffold.go`.
- `internal/doctor/checks.go` — `GetBuiltin("mise")` → `Get("core/mise")`; `Names`/`ProfileNames` iterate canonical keys; `Source` helper replaces `IsUserShadow`/path-prefix heuristic.
- `internal/prune/prune.go` — `ProfileNames()` returns canonical keys (already what it needs).
- `cmd/tpd/cli.go` — `LaunchCmd`/`ProfileShowCmd`/`ProfileEditCmd`/`ProfileListCmd` resolve CLI args via `ParseRef`+`ResolveRef`; `tpd edit core/mise` strips `core/` for the file path; `builtinEditSeed` emits `extends: core/<name>`; `tpd list` uses `DisplayName`+`Source`.

**Modified (tests):**
- `internal/profile/extends_test.go`, `merge_test.go`, `merge_multi_test.go`, `catalog_test.go`, `catalog_fragments_test.go`, `types_test.go`, `validate_test.go`
- `internal/scaffold/scaffold_test.go`, `new_profile_test.go`
- `cmd/tpd/profile_test.go`

**New:**
- `internal/profile/ref_test.go` — `ParseRef`/`ResolveRef` unit tests.

---

### Task 1: `Ref` struct, `ParseRef`, and `ResolveRef` (new `ref.go`)

**Files:**
- Create: `internal/profile/ref.go`
- Test: `internal/profile/ref_test.go`

**Interfaces:**
- Consumes: `Catalog` (constructed in Task 2; for this task a minimal interface `namespaces map[string]bool` and `entries map[string]RawProfile` is enough — define `ResolveRef` on `Catalog` but write the test using a hand-built `Catalog` via the existing literal constructor, which still works at this point because Task 2 hasn't changed the fields yet).
- Produces:
  - `type Ref struct { Namespace string; Name string }` (in `types.go` — added here to avoid a cycle; see Step 1)
  - `func ParseRef(s string, namespaces map[string]bool) (Ref, error)`
  - `func (c Catalog) ResolveRef(ref Ref) (string, bool)` — returns canonical `FullName` (an `entries` key), `ok=false` if no entry.

- [ ] **Step 1: Add `Ref` to `types.go`**

In `internal/profile/types.go`, add at the top (after the package/import block, before `ExtendsList`):

```go
// Ref is a parsed-but-not-yet-resolved reference to a profile or fragment.
// Namespace == "" means unqualified (resolve via user-first-then-core fallback);
// any other value ("core", a future remote namespace) means qualified (direct
// lookup, no fallback).
type Ref struct {
	Namespace string
	Name      string
}
```

- [ ] **Step 2: Write the failing `ParseRef`/`ResolveRef` tests**

Create `internal/profile/ref_test.go`:

```go
package profile

import (
	"reflect"
	"testing"
)

func TestParseRefUnqualified(t *testing.T) {
	r, err := ParseRef("mise", map[string]bool{"": true, "core": true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Namespace != "" || r.Name != "mise" {
		t.Errorf("got %+v, want {Namespace: \"\", Name: \"mise\"}", r)
	}
}

func TestParseRefQualifiedCore(t *testing.T) {
	r, err := ParseRef("core/mise", map[string]bool{"": true, "core": true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Namespace != "core" || r.Name != "mise" {
		t.Errorf("got %+v, want {Namespace: \"core\", Name: \"mise\"}", r)
	}
}

func TestParseRefRejectsEmptyLocalName(t *testing.T) {
	_, err := ParseRef("core/", map[string]bool{"": true, "core": true})
	if err == nil {
		t.Fatal("expected error for empty local name")
	}
}

func TestParseRefRejectsMultiSegmentLocalName(t *testing.T) {
	// Local names are single-segment; core/foo/bar is not a valid ref even
	// though "core" is a registered namespace.
	_, err := ParseRef("core/foo/bar", map[string]bool{"": true, "core": true})
	if err == nil {
		t.Fatal("expected error for multi-segment local name")
	}
}

func TestParseRefRejectsUnknownNamespace(t *testing.T) {
	_, err := ParseRef("corexy/foo", map[string]bool{"": true, "core": true})
	if err == nil {
		t.Fatal("expected error for unknown namespace corexy (segment-boundary check)")
	}
}

func TestParseRefLongestPrefix(t *testing.T) {
	// Synthetic multi-segment namespace; longest prefix must win.
	ns := map[string]bool{"": true, "core": true, "github.com/user/project": true}
	r, err := ParseRef("github.com/user/project/foo", ns)
	if err != nil {
		t.Fatal(err)
	}
	if r.Namespace != "github.com/user/project" || r.Name != "foo" {
		t.Errorf("got %+v, want {Namespace: \"github.com/user/project\", Name: \"foo\"}", r)
	}
}

func TestParseRefEmptyString(t *testing.T) {
	_, err := ParseRef("", map[string]bool{"": true, "core": true})
	if err == nil {
		t.Fatal("expected error for empty reference")
	}
}

func TestResolveRefUnqualifiedUserShadowsCore(t *testing.T) {
	cat := Catalog{
		entries: map[string]RawProfile{
			"core/mise": {Profile: Profile{Image: "builtin"}, Namespace: "core", Name: "mise"},
			"mise":      {Profile: Profile{Image: "user"}, Namespace: "", Name: "mise"},
		},
		namespaces: map[string]bool{"": true, "core": true},
	}
	got, ok := cat.ResolveRef(Ref{Name: "mise"})
	if !ok {
		t.Fatal("expected ok")
	}
	if got != "mise" {
		t.Errorf("ResolveRef unqualified = %q, want %q (user wins)", got, "mise")
	}
}

func TestResolveRefUnqualifiedFallsBackToCore(t *testing.T) {
	cat := Catalog{
		entries: map[string]RawProfile{
			"core/mise": {Profile: Profile{Image: "builtin"}, Namespace: "core", Name: "mise"},
		},
		namespaces: map[string]bool{"": true, "core": true},
	}
	got, ok := cat.ResolveRef(Ref{Name: "mise"})
	if !ok {
		t.Fatal("expected ok")
	}
	if got != "core/mise" {
		t.Errorf("ResolveRef unqualified fallback = %q, want %q", got, "core/mise")
	}
}

func TestResolveRefQualifiedBypassesUser(t *testing.T) {
	cat := Catalog{
		entries: map[string]RawProfile{
			"core/mise": {Profile: Profile{Image: "builtin"}, Namespace: "core", Name: "mise"},
			"mise":      {Profile: Profile{Image: "user"}, Namespace: "", Name: "mise"},
		},
		namespaces: map[string]bool{"": true, "core": true},
	}
	got, ok := cat.ResolveRef(Ref{Namespace: "core", Name: "mise"})
	if !ok {
		t.Fatal("expected ok")
	}
	if got != "core/mise" {
		t.Errorf("ResolveRef qualified = %q, want %q", got, "core/mise")
	}
}

func TestResolveRefNotFound(t *testing.T) {
	cat := Catalog{
		entries:    map[string]RawProfile{},
		namespaces: map[string]bool{"": true, "core": true},
	}
	if _, ok := cat.ResolveRef(Ref{Name: "nope"}); ok {
		t.Error("expected ok=false for missing name")
	}
	if _, ok := cat.ResolveRef(Ref{Namespace: "core", Name: "nope"}); ok {
		t.Error("expected ok=false for missing qualified name")
	}
}

func TestParseRefRoundTrip(t *testing.T) {
	cases := []string{"mise", "core/mise", "github.com/u/p/mise"}
	ns := map[string]bool{"": true, "core": true, "github.com/u/p": true}
	for _, s := range cases {
		r, err := ParseRef(s, ns)
		if err != nil {
			t.Fatalf("ParseRef(%q): %v", s, err)
		}
		if got := r.FullName(); got != s {
			t.Errorf("ParseRef(%q).FullName() = %q, want %q", s, got, s)
		}
	}
}
```

Note: `Ref.FullName()` is added in Step 3 below; move the `TestParseRefRoundTrip` to the end of the file and it will compile after Step 3. For Step 2, run the test first to confirm it fails with "ParseRef undefined".

- [ ] **Step 3: Add `Ref.FullName()` to `types.go`**

In `internal/profile/types.go`, add a method on `Ref`:

```go
// FullName returns the canonical string form: "ns/name", or the bare name
// when Namespace is "".
func (r Ref) FullName() string {
	if r.Namespace == "" {
		return r.Name
	}
	return r.Namespace + "/" + r.Name
}
```

- [ ] **Step 4: Run the test to verify it fails**

Run: `go test ./internal/profile/ -run 'TestParseRef|TestResolveRef' -v`
Expected: FAIL — `ParseRef` and `ResolveRef` undefined.

- [ ] **Step 5: Write `ref.go`**

Create `internal/profile/ref.go`:

```go
package profile

import (
	"fmt"
	"sort"
	"strings"
)

// ParseRef splits a reference string against the registered namespaces into a
// Ref. A string with no "/" is unqualified (Ref{Namespace: "", Name: s}). A
// string with "/" is matched against the longest registered namespace prefix
// at a segment boundary (ns + "/"); the remainder is the local name. An
// unregistered prefix is a parse error (the "/" belongs to an unknown
// namespace); an empty local name ("core/") is rejected.
func ParseRef(s string, namespaces map[string]bool) (Ref, error) {
	if s == "" {
		return Ref{}, fmt.Errorf("empty reference")
	}
	if !strings.Contains(s, "/") {
		return Ref{Namespace: "", Name: s}, nil
	}
	// Longest-prefix match over registered namespaces. "" has no prefix and
	// never matches a qualified string, so only non-empty namespaces compete.
	prefixes := make([]string, 0, len(namespaces))
	for ns := range namespaces {
		if ns != "" {
			prefixes = append(prefixes, ns)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(prefixes)))
	for _, ns := range prefixes {
		if strings.HasPrefix(s, ns+"/") {
			local := s[len(ns)+1:]
			if local == "" {
				return Ref{}, fmt.Errorf("empty local name in extends: %s", s)
			}
			// Local names are single-segment file basenames; the namespace
			// carries any path prefix. A remaining "/" means the local name
			// has multiple segments, which is not allowed.
			if strings.Contains(local, "/") {
				return Ref{}, fmt.Errorf("invalid local name %q in extends: %s (must be a single segment)", local, s)
			}
			return Ref{Namespace: ns, Name: local}, nil
		}
	}
	return Ref{}, fmt.Errorf("unknown namespace in extends: %s", s)
}

// ResolveRef resolves a Ref to a canonical catalog FullName (an entries key).
// For unqualified names (ref.Namespace == ""), returns the user key (bare name)
// if present, else the core key ("core/"+name). For qualified names, returns
// the qualified key directly (no fallback). Returns ok=false if no entry
// matches.
func (c Catalog) ResolveRef(ref Ref) (string, bool) {
	if ref.Namespace == "" {
		if _, ok := c.entries[ref.Name]; ok {
			return ref.Name, true
		}
		coreKey := "core/" + ref.Name
		if _, ok := c.entries[coreKey]; ok {
			return coreKey, true
		}
		return "", false
	}
	key := ref.FullName()
	_, ok := c.entries[key]
	return key, ok
}
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./internal/profile/ -run 'TestParseRef|TestResolveRef' -v`
Expected: PASS.

Note: `Catalog` does not yet have a `namespaces` field (it's added in Task 2). The literal-construction tests in Step 2 use a `Catalog` struct literal with `namespaces`; this will fail to compile until Task 2. To keep Task 1 self-contained and green, **add the `namespaces` field to `Catalog` in this step** (Step 5b):

- [ ] **Step 5b: Add `namespaces` field to `Catalog`; remove `builtins` field**

In `internal/profile/catalog.go`, change the `Catalog` struct:

```go
type Catalog struct {
	entries    map[string]RawProfile // keyed by FullName: "core/mise", "mise", "github.com/u/p/mise"
	namespaces map[string]bool       // registered prefixes: "core", "", future remotes
	fragments  map[string]bool       // FullNames that are fragments
}
```

Delete the `builtins` field, `GetBuiltin`, and `IsUserShadow` (L37-52) in this step. Deleting `builtins` now avoids the transitional duplicate-key problem (populating both bare `mise` and `core/mise` would violate the namespace model). The `IsUserShadow` special-case in `resolveChain` is deleted in Step 4h below — it cannot survive without `builtins`, and once entries are keyed by `FullName` the unqualified `extends: <self>` resolves to the entry itself (a cycle), which is the spec's intended behavior.

Existing callers of `GetBuiltin`/`IsUserShadow` (`internal/doctor/checks.go` L84, `cmd/tpd/cli.go` L367) are fixed in Step 4i.

- [ ] **Step 7: Run full package tests + vet**

Run: `go test ./internal/profile/ && go vet ./internal/profile/`
Expected: PASS (no behavior change yet — only new code + a new struct field).

- [ ] **Step 8: Commit**

```bash
git add internal/profile/ref.go internal/profile/ref_test.go internal/profile/types.go internal/profile/catalog.go
git commit -m "feat(profile): add Ref, ParseRef, ResolveRef for namespace-aware resolution"
```

---

### Task 2: `RawProfile.Namespace`/`Name`, `FullName()`/`DisplayName()`, and namespace stamping in loaders

**Files:**
- Modify: `internal/profile/types.go` (add fields + methods to `RawProfile`)
- Modify: `internal/profile/catalog.go` (stamp `Namespace`/`Name` in all four loaders + `NewProfileCatalogForTest`; register `core`/`""` namespaces; collision check by `FullName`)
- Test: `internal/profile/catalog_test.go` (update existing tests to expect `core/` keys)

**Interfaces:**
- Consumes: `Ref.FullName()` (Task 1).
- Produces:
  - `RawProfile.Namespace string` (`yaml:"-"`), `RawProfile.Name string` (`yaml:"-"`)
  - `func (rc RawProfile) FullName() string` — `ns + "/" + name`, or bare `name` when `ns == ""`.
  - `func (rc RawProfile) DisplayName() string` — `rc.Name` (unqualified).
  - Loaders stamp `Namespace`/`Name` on every entry; `Catalog.entries` keyed by `FullName`.
  - `Catalog` registers `namespaces["core"] = true` and `namespaces[""] = true` at load.

- [ ] **Step 1: Write failing tests for `FullName`/`DisplayName` and namespace stamping**

Append to `internal/profile/catalog_test.go`:

```go
func TestRawProfileFullName(t *testing.T) {
	if got := (RawProfile{Namespace: "core", Name: "mise"}).FullName(); got != "core/mise" {
		t.Errorf("core/mise FullName = %q", got)
	}
	if got := (RawProfile{Namespace: "", Name: "mise"}).FullName(); got != "mise" {
		t.Errorf("user mise FullName = %q, want \"mise\"", got)
	}
}

func TestRawProfileDisplayName(t *testing.T) {
	if got := (RawProfile{Namespace: "core", Name: "mise"}).DisplayName(); got != "mise" {
		t.Errorf("DisplayName = %q, want mise", got)
	}
}

func TestLoadProfilesStampsCoreNamespace(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	rc, ok := cat.Get("core/mise")
	if !ok {
		t.Fatal("core/mise not keyed under FullName")
	}
	if rc.Namespace != "core" || rc.Name != "mise" {
		t.Errorf("core/mise identity = {%q, %q}, want {core, mise}", rc.Namespace, rc.Name)
	}
	// Bare "mise" (user namespace) must not exist when there's no user file.
	if _, ok := cat.Get("mise"); ok {
		t.Error("bare \"mise\" should not exist without a user file")
	}
}

func TestLoadProfilesUserEntryStampsEmptyNamespace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rustdev.yaml"), []byte("version: 1\nextends: shell\ntools:\n  rust: \"1.74\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	rc, ok := cat.Get("rustdev")
	if !ok {
		t.Fatal("user rustdev not found under bare FullName")
	}
	if rc.Namespace != "" || rc.Name != "rustdev" {
		t.Errorf("user identity = {%q, %q}, want {\"\", rustdev}", rc.Namespace, rc.Name)
	}
	if rc.Path == "" {
		t.Error("user entry has empty Path")
	}
}

func TestLoadProfilesUserShadowsCoreCoexist(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shell.yaml"), []byte("version: 1\nimage: my/custom:latest\ncommand: [\"bash\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Both must coexist under distinct FullNames.
	if rc, ok := cat.Get("shell"); !ok || rc.Namespace != "" {
		t.Errorf("user shell = {%q, %q}, want {\"\", shell}", rc.Namespace, rc.Name)
	}
	if rc, ok := cat.Get("core/shell"); !ok || rc.Namespace != "core" {
		t.Errorf("core/shell = {%q, %q}, want {core, shell}", rc.Namespace, rc.Name)
	}
}

func TestLoadProfilesRejectsCrossTypeDisplayNameCollision(t *testing.T) {
	// A user fragment named "shell" and core/shell (profile) share the display
	// name "shell"; unqualified resolution and ProfileDisplayNames can't
	// disambiguate. This must be a hard error.
	dir := t.TempDir()
	fragDir := filepath.Join(filepath.Dir(dir), "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "shell.yaml"), []byte("version: 1\ntools:\n  x: \"1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProfiles(dir)
	if err == nil {
		t.Fatal("expected cross-type display-name collision error, got nil")
	}
	if !strings.Contains(err.Error(), "shell") || !strings.Contains(err.Error(), "fragment") {
		t.Errorf("error should name shell and fragment, got: %v", err)
	}
}
```

Also update the existing `TestLoadProfilesBuiltinsOnly` (L11-21) to look up `core/opencode` etc. instead of bare `opencode`:

```go
func TestLoadProfilesBuiltinsOnly(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatalf("LoadProfiles(\"\"): %v", err)
	}
	for _, name := range []string{"core/opencode", "core/codex", "core/shell"} {
		if _, ok := cat.Get(name); !ok {
			t.Errorf("built-in %q missing from catalog", name)
		}
	}
}
```

And `TestLoadProfilesUserShadowsBuiltin` (L23-43): change the `cat.Get("shell")` lookup to expect the **user** entry under `"shell"` and separately check `core/shell` exists.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/profile/ -run 'TestRawProfileFullName|TestRawProfileDisplayName|TestLoadProfilesStampsCoreNamespace|TestLoadProfilesUserEntryStampsEmptyNamespace|TestLoadProfilesUserShadowsCoreCoexist|TestLoadProfilesBuiltinsOnly|TestLoadProfilesUserShadowsBuiltin' -v`
Expected: FAIL — `FullName`/`DisplayName` undefined; entries still keyed by bare name.

- [ ] **Step 3: Add `Namespace`/`Name` fields and `FullName`/`DisplayName` methods to `RawProfile`**

In `internal/profile/types.go`, replace the `RawProfile` struct (L182-186):

```go
// RawProfile is a profile as loaded from disk, before extends-merge. It
// carries its source identity (Namespace + Name) and file path. Namespace is
// "core" for embedded built-ins, "" for user files, or a future remote
// namespace ("github.com/user/project"). Name is the local single-segment name
// (file basename). FullName is the canonical catalog key; DisplayName is the
// unqualified name used in user-facing output.
type RawProfile struct {
	Profile
	Namespace string                    `yaml:"-"`
	Name      string                    `yaml:"-"`
	Path      string                    `yaml:"-"`
	NullKeys  map[string]map[string]bool `yaml:"-"`
}

// FullName is the canonical catalog key and the qualified YAML/string form.
func (rc RawProfile) FullName() string {
	if rc.Namespace == "" {
		return rc.Name
	}
	return rc.Namespace + "/" + rc.Name
}

// DisplayName is the unqualified name used in user-facing output (list, wizard).
func (rc RawProfile) DisplayName() string {
	return rc.Name
}
```

- [ ] **Step 4: Stamp `Namespace`/`Name` in the four loaders and key `entries` by `FullName`**

In `internal/profile/catalog.go`:

a. `loadBuiltins` (L240-263): after `name :=` and before `entries[name] = rc`, stamp identity and insert under `FullName`:

```go
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		rc, err := parseRaw(data, "built-in:"+path)
		if err != nil {
			return err
		}
		if err := validateReservedName(rc, name); err != nil {
			return err
		}
		rc.Namespace = "core"
		rc.Name = name
		entries[rc.FullName()] = rc
		return nil
```

b. `loadBuiltinFragments` (L453-480): stamp `Namespace`/`Name` **first**, then check collisions and insert by `FullName` (using the unstamped `rc` for collision would produce an empty/bare key):

```go
		rc.Namespace = "core"
		rc.Name = name
		if _, exists := entries[rc.FullName()]; exists {
			return ProfileError{Path: rc.Path, Message: "name collision: fragment and profile share name " + rc.FullName()}
		}
		entries[rc.FullName()] = rc
		fragmentNames[rc.FullName()] = true
		return nil
```

c. `loadUserDir` (L266-302): stamp `Namespace: ""`/`Name` first, then collision-check by `FullName`:

```go
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		rc, err := parseRaw(data, path)
		if err != nil {
			return err
		}
		if err := validateReservedName(rc, name); err != nil {
			return err
		}
		rc.Namespace = ""
		rc.Name = name
		if fragmentNames[rc.FullName()] {
			return ProfileError{Path: rc.Path, Message: "name collision: profile and fragment share name " + rc.FullName()}
		}
		entries[rc.FullName()] = rc
		return nil
```

d. `loadUserFragments` (L482-512): same — `Namespace: ""`, `Name: name`, collision + insert by `FullName`.

e. Apply the identical `Namespace`/`Name` stamping to the tolerant variants (`loadUserDirTolerant` L161-201, `loadUserFragmentsTolerant` L203-238): set `rc.Namespace = ""`, `rc.Name = name`, and use `rc.FullName()` for collision checks and insert.

f. `LoadProfiles` (L84-124) and `LoadProfilesTolerant` (L132-159): remove the `builtins` map entirely (delete the `builtins := map[string]RawProfile{}` line, the `loadBuiltins(builtins)` call becomes `loadBuiltins(entries)` — `loadBuiltins` now stamps `core` directly into `entries` — and the `builtins: builtins` field in the returned `Catalog`). Register the namespaces and return:

```go
	namespaces := map[string]bool{"": true, "core": true}
	return Catalog{entries: entries, namespaces: namespaces, fragments: fragmentNames}, nil
```

(Do the same in `LoadProfilesTolerant`.) The `loadBuiltins` helper signature keeps `entries map[string]RawProfile` and stamps `Namespace: "core"` + `Name` + `entries[rc.FullName()] = rc` (no separate `builtins` map).

g. `NewProfileCatalogForTest` (L413-419): stamp `Namespace: "core"` on every entry (preserving current test behavior where entries act as built-ins) and set `namespaces` (no `builtins` field):

```go
func NewProfileCatalogForTest(entries map[string]RawProfile) Catalog {
	out := make(map[string]RawProfile, len(entries))
	for k, v := range entries {
		v.Namespace = "core"
		v.Name = k
		if err := v.ExtendsList.Resolve(map[string]bool{"": true, "core": true}); err != nil {
			panic("NewProfileCatalogForTest: bad extends in " + k + ": " + err.Error())
		}
		out[v.FullName()] = v
	}
	return Catalog{entries: out, namespaces: map[string]bool{"": true, "core": true}}
}
```

(The `Resolve` call fills `ExtendsList.Resolved` for literals that supply `Raw`; tests that construct `ExtendsList{Raw: ...}` get `Resolved` automatically. `parseRaw`-loaded entries already have `Resolved` from `parseRaw`.)

h. **Cross-type display-name collision check.** The spec says names are globally unique — a name clash is a hard catalog-load error. With `FullName`-keyed entries, a user fragment `shell` (`FullName` `shell`) and `core/shell` (profile) coexist under distinct keys, but they share the display name `shell`, and unqualified `shell` would resolve to the user fragment while `ProfileDisplayNames` would still expose `shell` (from `core/shell`) as a launchable profile — an ambiguity. After all loaders complete, in `LoadProfiles` (and `LoadProfilesTolerant`), validate that no display name appears as both a fragment and a profile across namespaces:

```go
	// A display name may not be both a fragment (in any namespace) and a
	// profile (in any namespace); unqualified resolution and the display APIs
	// can't disambiguate that. The intra-namespace collision checks above
	// already reject fragment/profile clashes within the same FullName.
	dnIsFragment := map[string]bool{}
	dnIsProfile := map[string]bool{}
	for _, rc := range entries {
		dn := rc.DisplayName()
		if fragmentNames[rc.FullName()] {
			dnIsFragment[dn] = true
		} else {
			dnIsProfile[dn] = true
		}
	}
	for dn := range dnIsFragment {
		if dnIsProfile[dn] {
			return Catalog{}, fmt.Errorf("display name %q is both a fragment and a profile across namespaces", dn)
		}
	}
```

In `LoadProfilesTolerant`, record these as warnings (via the `warn` callback) and drop the conflicting entry rather than aborting, mirroring the tolerant pattern for other collisions. The strict path aborts.

**Important:** `merge_multi_test.go` constructs `RawProfile` literals keyed by bare names like `"base"`, `"myprofile"`. After this change, `NewProfileCatalogForTest` stamps them as `core/<name>`, so `ResolveProfile(cat, "myprofile")` must resolve via `ResolveRef` (unqualified → `core/myprofile`). `ResolveProfile` still calls `cat.Get(name)` directly until Step 4g. Because entries are now keyed `core/myprofile`, `ResolveProfile` must resolve its `name` argument via `ResolveRef` — done in Step 4g. The `IsUserShadow`/`resolveBuiltinChain` special-case is deleted in Step 4h (it cannot work without `builtins`, and the spec replaces it with the `seen` cycle check).

- [ ] **Step 4g: Add `ParseRefForCatalog`; route `ResolveProfile`/`ResolveFragment`/`resolveChain` through `ResolveRef`**

In `internal/profile/ref.go`, add a convenience method:

```go
// ParseRefForCatalog parses s against the catalog's registered namespaces.
func (c Catalog) ParseRefForCatalog(s string) (Ref, error) {
	return ParseRef(s, c.namespaces)
}
```

In `internal/profile/merge.go`, `ResolveProfile` (L7-21):

```go
func ResolveProfile(cat Catalog, name string) (Profile, error) {
	ref, err := cat.ParseRefForCatalog(name)
	if err != nil {
		return Profile{}, ProfileError{Message: err.Error()}
	}
	key, ok := cat.ResolveRef(ref)
	if !ok {
		return Profile{}, ProfileError{Message: "profile not found: " + name}
	}
	rc, _ := cat.Get(key)
	merged, err := resolveChain(cat, key, map[string]bool{})
	if err != nil {
		return Profile{}, err
	}
	merged.Path = rc.Path
	if err := validate(merged); err != nil {
		return Profile{}, err
	}
	return merged.Profile, nil
}
```

`ResolveFragment` (L27-38): same pattern — `ParseRefForCatalog` → `ResolveRef` → `resolveChain(cat, key, ...)`. Skip `validate` (fragments don't carry image/command).

`resolveChain` (L40-113): the `name` parameter is now always a canonical `FullName` (callers pass `key`). The `seen` map keys by `FullName`. The fragment type-check loop and the parent-resolution loop must resolve each parent name via `ResolveRef`. **For Task 2**, `rc.ExtendsList` is still `[]string` (Task 3 changes it to the struct). Iterate `rc.ExtendsList` and parse each via `ParseRef`:

```go
	for _, parentName := range rc.ExtendsList {
		pref, perr := cat.ParseRefForCatalog(parentName)
		if perr != nil {
			return RawProfile{}, withParentPath(ProfileError{Message: perr.Error()}, rc)
		}
		pkey, ok := cat.ResolveRef(pref)
		if !ok {
			return RawProfile{}, withParentPath(ProfileError{Message: "profile not found: " + parentName}, rc)
		}
		if resolved[pkey] {
			continue
		}
		resolved[pkey] = true
		p, err := resolveChain(cat, pkey, seen)
		if err != nil {
			return RawProfile{}, withParentPath(err, rc)
		}
		merged = MergeProfiles(merged, p)
	}
```

Apply the same `ParseRefForCatalog`+`ResolveRef` to the fragment type-check loop (L54-58), checking `IsFragment(pkey)`.

- [ ] **Step 4h: Delete the `IsUserShadow` special-case and `resolveBuiltinChain`**

In `internal/profile/merge.go`, delete the `IsUserShadow` branch in `resolveChain` (L64-89) and the entire `resolveBuiltinChain` function (L119-153). The special-case is obsolete: once entries are keyed by `FullName` and unqualified self-names resolve to the entry itself via `ResolveRef`, `extends: <self>` is caught by the `seen[key]` cycle check at the top of `resolveChain`. To extend a built-in of the same name, the user writes `extends: core/<name>` (qualified → direct `core/<name>`, skips the user entry).

`MergeProfiles` (L168-214) still references `child.ExtendsList` as a slice; leave it for Task 3 (the struct change). No change here.

- [ ] **Step 4i: Fix `GetBuiltin`/`IsUserShadow` callers in `doctor` and `cli`**

- `internal/doctor/checks.go` L84: `base, ok := cat.GetBuiltin("mise")` → `base, ok := cat.Get("core/mise")`.
- `cmd/tpd/cli.go` L367 (`tpd list`): `cat.IsUserShadow(name)` is deleted. Replace the SOURCE-column logic with `cat.Source(...)` — but `Source` takes a display name and `Names()` returns canonical keys. Defer the full `tpd list` rewrite to Task 5; for Task 2, replace `cat.IsUserShadow(name)` with a canonical-key-aware check:

```go
		source := "built-in"
		if rc.Namespace == "" {
			source = "user"
			if _, ok := cat.Get("core/" + name); ok {
				source = "user shadow"
			}
		}
```

(`name` is a canonical `FullName` from `cat.Names()`; `rc.Namespace == ""` means user entry; a `core/<name>` counterpart exists for shadows.) Task 5 replaces this with the `Source` helper + `DisplayNames`.

- [ ] **Step 5: Run the new + existing catalog tests**

Run: `go test ./internal/profile/ -run 'TestRawProfileFullName|TestRawProfileDisplayName|TestLoadProfilesStampsCoreNamespace|TestLoadProfilesUserEntryStampsEmptyNamespace|TestLoadProfilesUserShadowsCoreCoexist|TestLoadProfilesBuiltinsOnly|TestLoadProfilesUserShadowsBuiltin' -v`
Expected: PASS for the new/updated tests.

Run: `go test ./internal/profile/ -run 'TestMulti|TestResolve' -v`
Expected: PASS — `merge_multi_test.go` now resolves `core/myprofile` via `ResolveRef`.

Some existing tests may break because they call `cat.Get("opencode")` (bare) expecting the built-in. Fix them in Step 5b.

- [ ] **Step 5b: Update existing tests that look up built-ins by bare name**

In `internal/profile/catalog_test.go`:
- `TestBuiltinsDoNotMountUserDirs` (L127-146) and `TestBuiltinsDoNotMountGitconfig` (L148-165) iterate `cat.Names()` and call `cat.Get(name)`. `Names()` (still returning `entries` keys, now `FullName`s) returns `core/...` for built-ins, so these still work as long as `Names()` returns the keys. Verify they pass; if not, no change needed (they use the returned `name` consistently with `Get`).
- `TestLoadProfilesUserShadowsBuiltin` (L23-43): the `cat.Get("shell")` now returns the **user** entry (bare `shell`). The assertion `rc.Image != "my/custom:latest"` still holds. Add a check that `cat.Get("core/shell")` returns the built-in. (Already covered by `TestLoadProfilesUserShadowsCoreCoexist`.)
- `TestResolveUserShadowMergesAllBuiltinExtends` (L45-98) constructs a `Catalog` literal directly with `entries`, `builtins`, `fragments`. `builtins` no longer exists. Rewrite the test in Step 4j (below) using `NewProfileCatalogForTest` + a `core/`-qualified shadow; the special-case it relied on is gone.

- [ ] **Step 4j: Rewrite `TestResolveUserShadowMergesAllBuiltinExtends` for the new model**

The test asserts that a shadow extending the built-in of the same name inherits all of the built-in's parents. The `IsUserShadow` special-case is gone; the shadow must now qualify its extends (`extends: core/t3`):

```go
func TestResolveUserShadowMergesAllBuiltinExtends(t *testing.T) {
	// A shadow extending the builtin of the same name must inherit all of its
	// parents; the qualified core/ prefix reaches the built-in without a cycle.
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"t3":    {Profile: Profile{Version: 1, Image: "img", Command: []string{"t3"}, ExtendsList: ExtendsList{Raw: []string{"a", "gui", "b", "c"}}}},
		"a":     {Profile: Profile{Env: map[string]string{"XDG_RUNTIME_DIR": "{{ .Env.XDG_RUNTIME_DIR }}"}}},
		"b":     {Profile: Profile{Mounts: map[string]Mount{"/b": {Source: "~/.b"}}}},
		"c":     {Profile: Profile{Tools: map[string]string{"c": "latest"}}},
		"extra": {Profile: Profile{Tools: map[string]string{"extra": "1"}}},
		"gui":   {Profile: Profile{Env: map[string]string{"WAYLAND_DISPLAY": "{{ .Env.WAYLAND_DISPLAY }}"}}},
	})
	cat.fragments["core/gui"] = true
	// Overlay the user shadow under the bare "t3" key, extending core/t3 + extra.
	shadow := RawProfile{
		Profile:     Profile{Version: 1, ExtendsList: ExtendsList{Raw: []string{"core/t3", "extra"}}},
		Namespace: "", Name: "t3", Path: "user:/home/u/t3.yaml",
	}
	if err := shadow.ExtendsList.Resolve(cat.namespaces); err != nil {
		t.Fatal(err)
	}
	cat.entries["t3"] = shadow

	merged, err := ResolveProfile(cat, "t3")
	if err != nil {
		t.Fatal(err)
	}
	if merged.Env["XDG_RUNTIME_DIR"] == "" {
		t.Error("missing env from builtin parent 'a'")
	}
	if merged.Env["WAYLAND_DISPLAY"] == "" {
		t.Error("missing env from builtin fragment parent 'gui'")
	}
	if _, ok := merged.Mounts["/b"]; !ok {
		t.Error("missing mount from builtin parent 'b'")
	}
	if merged.Tools["c"] != "latest" {
		t.Error("missing tool from builtin parent 'c'")
	}
	if merged.Tools["extra"] != "1" {
		t.Error("missing tool from second extends entry 'extra'")
	}
}
```

This test now lives in Task 2 (not Task 3) because the special-case deletion happens here.

In `internal/profile/catalog_fragments_test.go`: update any `cat.Get("ssh")` (bare) to `cat.Get("core/ssh")` since built-in fragments are now `core/ssh`. Read the file and update both `cat.Get` call sites (L16, L68) and the `ResolveProfile`/`ResolveFragment` calls (L96, L141) — the latter take a user-supplied string and go through `ResolveRef`, so `ResolveFragment(cat, "myfrag")` still works if `myfrag` is a user fragment; but `ResolveFragment(cat, "ssh")` resolves unqualified to `core/ssh` automatically. Check each test's intent and update only where the test directly indexes `entries` or calls `Get` with a bare built-in name.

In `internal/profile/types_test.go`: any `cat.Get(...)` on built-ins needs `core/` qualification. Read the file and update call sites (around L113, L144, L168, L197, L231).

- [ ] **Step 6: Run the full profile package + vet**

Run: `go test ./internal/profile/ && go vet ./internal/profile/`
Expected: PASS.

- [ ] **Step 7: Run the full repo tests + vet**

Run: `go test ./... && go vet ./...`
Expected: PASS. (Callers in `cmd/tpd`, `internal/scaffold`, `internal/doctor`, `internal/prune` still pass because `ResolveProfile`/`ResolveFragment` accept a user string and resolve it internally. `cat.Get`/`cat.Names`/`cat.ProfileNames`/`cat.IsFragment` still work, returning `FullName`s — but `cmd/tpd/cli.go` `tpd list` iterates `cat.Names()` and prints `name` directly, so it will print `core/mise` until Task 5 fixes the display. That's a cosmetic regression in `tpd list` output only; tests that assert list output may fail. Check `cmd/tpd/profile_test.go` `TestProfileList` and update expectations to `core/...` temporarily, with a note that Task 5 restores bare names. Alternatively, defer the `Names()`/`ProfileNames()` display-API split to Task 4 and do it before Task 2's commit. **Decision: do the display-API split in Task 4, and in Task 2 keep `Names()`/`ProfileNames()` returning `FullName`s.** Update `TestProfileList` expectations in Step 7b.)

- [ ] **Step 7b: Update `cmd/tpd/profile_test.go` `TestProfileList` for `core/` keys**

Read `cmd/tpd/profile_test.go` `TestProfileList` (L58-88). The test asserts rows like `opencode\tprofile\tbuilt-in`. Under Task 2, `cat.Names()` returns `core/opencode`, so the row becomes `core/opencode\tprofile\tbuilt-in`. Update the expected rows to the `core/`-prefixed form. Task 5 will restore bare names via `DisplayName` and the test will be updated again. Add a comment in the test: `// Task 5 restores bare display names; these expectations are temporary.`

- [ ] **Step 8: Commit**

```bash
git add internal/profile/types.go internal/profile/catalog.go internal/profile/merge.go internal/profile/ref.go internal/profile/catalog_test.go internal/profile/catalog_fragments_test.go internal/profile/types_test.go internal/doctor/checks.go cmd/tpd/cli.go cmd/tpd/profile_test.go
git commit -m "feat(profile): namespace identity (core/\"\") + FullName-keyed catalog entries; delete IsUserShadow/resolveBuiltinChain"
```

---

### Task 3: `ExtendsList` struct (`Raw`, `Resolved`), `Resolve` in `parseRaw`, `resolveChain` iterates `Resolved`

**Files:**
- Modify: `internal/profile/types.go` (`ExtendsList` becomes a struct; `UnmarshalYAML`/`MarshalYAML`; `Resolve` method)
- Modify: `internal/profile/catalog.go` (`parseRaw` calls `list.Resolve(builtinNamespaces)`)
- Modify: `internal/profile/merge.go` (`resolveChain` iterates `rc.ExtendsList.Resolved`; `MergeProfiles` handles the struct)
- Modify: `internal/profile/extends_test.go` (update for struct form)
- Modify: `internal/profile/merge_test.go` (self-shadow test → `extends: core/opencode`; self-cycle covers `core/foo`)
- Modify: `internal/profile/merge_multi_test.go` (construct `ExtendsList` via struct)
- Test: `internal/profile/merge_test.go` (new self-reference tests)

**Interfaces:**
- Consumes: `Ref`, `ParseRef`, `ResolveRef`, `Catalog.namespaces`, `ParseRefForCatalog` (Task 2).
- Produces:
  - `type ExtendsList struct { Raw []string; Resolved []Ref }` with `UnmarshalYAML`/`MarshalYAML`/`Resolve`.
  - `resolveChain` iterates `rc.ExtendsList.Resolved` (already-split `Ref`s), resolves each to a canonical key via `ResolveRef`.

- [ ] **Step 1: Write failing tests for the `ExtendsList` struct and `Resolve`**

Replace `internal/profile/extends_test.go`:

```go
package profile

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestExtendsListUnmarshalString(t *testing.T) {
	var p struct {
		Extends ExtendsList `yaml:"extends"`
	}
	if err := yaml.Unmarshal([]byte("extends: opencode\n"), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Extends.Raw) != 1 || p.Extends.Raw[0] != "opencode" {
		t.Errorf("Raw = %v, want [opencode]", p.Extends.Raw)
	}
	if p.Extends.Resolved != nil {
		t.Errorf("Resolved should be nil before Resolve(), got %v", p.Extends.Resolved)
	}
}

func TestExtendsListUnmarshalList(t *testing.T) {
	var p struct {
		Extends ExtendsList `yaml:"extends"`
	}
	if err := yaml.Unmarshal([]byte("extends: [opencode, ssh]\n"), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Extends.Raw) != 2 || p.Extends.Raw[0] != "opencode" || p.Extends.Raw[1] != "ssh" {
		t.Errorf("Raw = %v, want [opencode ssh]", p.Extends.Raw)
	}
}

func TestExtendsListUnmarshalEmpty(t *testing.T) {
	var p struct {
		Extends ExtendsList `yaml:"extends"`
	}
	if err := yaml.Unmarshal([]byte("extends: []\n"), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Extends.Raw) != 0 {
		t.Errorf("Raw = %v, want empty", p.Extends.Raw)
	}
}

func TestExtendsListResolve(t *testing.T) {
	ns := map[string]bool{"": true, "core": true}
	el := ExtendsList{Raw: []string{"core/mise", "javascript"}}
	if err := el.Resolve(ns); err != nil {
		t.Fatal(err)
	}
	want := []Ref{{Namespace: "core", Name: "mise"}, {Namespace: "", Name: "javascript"}}
	if !reflect.DeepEqual(el.Resolved, want) {
		t.Errorf("Resolved = %+v, want %+v", el.Resolved, want)
	}
}

func TestExtendsListResolveRejectsUnknownNamespace(t *testing.T) {
	ns := map[string]bool{"": true, "core": true}
	el := ExtendsList{Raw: []string{"corexy/foo"}}
	if err := el.Resolve(ns); err == nil {
		t.Fatal("expected unknown-namespace error")
	}
}

func TestExtendsListMarshalResolved(t *testing.T) {
	el := ExtendsList{Resolved: []Ref{{Namespace: "core", Name: "mise"}, {Namespace: "", Name: "javascript"}}}
	out, err := yaml.Marshal(el)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "- core/mise\n- javascript\n" {
		t.Errorf("marshaled = %q, want YAML list of canonical names", string(out))
	}
}

func TestExtendsListMarshalRawFallback(t *testing.T) {
	el := ExtendsList{Raw: []string{"opencode", "ssh"}}
	out, err := yaml.Marshal(el)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "- opencode\n- ssh\n" {
		t.Errorf("marshaled = %q, want raw fallback", string(out))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/profile/ -run 'TestExtendsList' -v`
Expected: FAIL — `ExtendsList` is still `[]string`.

- [ ] **Step 3: Replace `ExtendsList` with the struct form**

In `internal/profile/types.go`, replace the `ExtendsList` type and its `UnmarshalYAML` (L5-26):

```go
// ExtendsList is the yaml-decoded extends field. Raw holds the strings as
// written; Resolved is filled by Resolve splitting each Raw string against the
// registered namespaces. MarshalYAML emits Resolved (canonical strings) when
// available, else Raw (for round-tripping un-resolved lists).
type ExtendsList struct {
	Raw      []string `yaml:"-"`
	Resolved []Ref    `yaml:"-"`
}

// UnmarshalYAML decodes a scalar or list of strings into Raw. No namespace
// splitting happens here (yaml.v3 gives no context). Resolved stays nil.
func (e *ExtendsList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		e.Raw = []string{s}
		return nil
	}
	var list []string
	if err := value.Decode(&list); err != nil {
		return err
	}
	e.Raw = list
	return nil
}

// MarshalYAML emits Resolved (if non-empty) as canonical strings, else Raw.
func (e ExtendsList) MarshalYAML() (interface{}, error) {
	if len(e.Resolved) > 0 {
		out := make([]string, len(e.Resolved))
		for i, r := range e.Resolved {
			out[i] = r.FullName()
		}
		return out, nil
	}
	if len(e.Raw) > 0 {
		return e.Raw, nil
	}
	return nil, nil
}

// Resolve splits each Raw string against the registered namespaces into
// Resolved. Idempotent. An unregistered prefix or empty local name is an error.
func (e *ExtendsList) Resolve(namespaces map[string]bool) error {
	if len(e.Raw) == 0 {
		e.Resolved = nil
		return nil
	}
	resolved := make([]Ref, len(e.Raw))
	for i, s := range e.Raw {
		r, err := ParseRef(s, namespaces)
		if err != nil {
			return err
		}
		resolved[i] = r
	}
	e.Resolved = resolved
	return nil
}
```

- [ ] **Step 4: Call `list.Resolve` in `parseRaw`**

`parseRaw` (catalog.go L307-327) runs before the catalog is built, so it doesn't have `namespaces` yet. The spec says `parseRaw` calls `list.Resolve(namespaces)`. The namespace set is known statically at load time (`core` and `""` are always registered). Pass a static registry into `parseRaw`:

In `internal/profile/catalog.go`, add a package-level var:

```go
// builtinNamespaces is the namespace registry available at parse time (before
// the Catalog is assembled). "core" and "" are always registered; remote
// namespaces (future) will be added by their loader.
var builtinNamespaces = map[string]bool{"": true, "core": true}
```

In `parseRaw` (L307-327), after `rc.NullKeys = collectNullKeys(&root)`, add:

```go
	if err := rc.ExtendsList.Resolve(builtinNamespaces); err != nil {
		return RawProfile{}, ProfileError{Path: path, Message: err.Error()}
	}
	return rc, nil
```

(For remote namespaces in the future, `parseRaw` will take the registry as a parameter; for now the static set suffices because only `core`/`""` exist.)

- [ ] **Step 5: Update `resolveChain` to iterate `Resolved`**

In `internal/profile/merge.go`, replace `resolveChain` (the version from Task 2 Step 4g, which iterates `rc.ExtendsList` as a `[]string`) with a version that iterates `rc.ExtendsList.Resolved` (now `[]Ref`):

```go
func resolveChain(cat Catalog, key string, seen map[string]bool) (RawProfile, error) {
	rc, ok := cat.Get(key)
	if !ok {
		return RawProfile{}, ProfileError{Message: "profile not found: " + key}
	}
	if seen[key] {
		return RawProfile{}, ProfileError{Path: rc.Path, Message: "extends cycle detected at: " + key}
	}
	if len(rc.ExtendsList.Resolved) == 0 {
		return rc, nil
	}
	// Fragments are composition-only: they may extend other fragments, but
	// must not pull in profile identity (image/command/version).
	if cat.IsFragment(key) {
		for _, ref := range rc.ExtendsList.Resolved {
			pkey, ok := cat.ResolveRef(ref)
			if !ok {
				return RawProfile{}, ProfileError{Path: rc.Path, Message: "fragment not found: " + ref.FullName()}
			}
			if !cat.IsFragment(pkey) {
				return RawProfile{}, ProfileError{Path: rc.Path, Message: "fragment " + key + " may only extend fragments, not profile " + pkey}
			}
		}
	}
	seen[key] = true
	defer delete(seen, key)

	merged := RawProfile{}
	resolved := map[string]bool{}
	for _, ref := range rc.ExtendsList.Resolved {
		pkey, ok := cat.ResolveRef(ref)
		if !ok {
			return RawProfile{}, withParentPath(ProfileError{Message: "profile not found: " + ref.FullName()}, rc)
		}
		if resolved[pkey] {
			continue
		}
		resolved[pkey] = true
		parent, err := resolveChain(cat, pkey, seen)
		if err != nil {
			return RawProfile{}, withParentPath(err, rc)
		}
		merged = MergeProfiles(merged, parent)
	}
	merged = MergeProfiles(merged, rc)
	merged.Path = rc.Path
	return merged, nil
}
```

(Task 2 already deleted `resolveBuiltinChain`, `IsUserShadow`, and `GetBuiltin`; confirm via the grep in Step 6.)

- [ ] **Step 6: Confirm `IsUserShadow`, `GetBuiltin`, and `builtins` are gone**

Task 2 already deleted `IsUserShadow`, `GetBuiltin`, the `builtins` field, and `resolveBuiltinChain`, and updated `ResolveProfile`/`ResolveFragment`/`resolveChain` to route through `ResolveRef`. Verify with `grep` that no references remain:

```bash
grep -nE 'IsUserShadow|GetBuiltin|resolveBuiltinChain|builtins' internal/profile/*.go internal/doctor/*.go cmd/tpd/*.go
```

Expected: no matches (except possibly in comments). If any remain, delete them.

- [ ] **Step 7: Update `MergeProfiles` for the `ExtendsList` struct**

In `internal/profile/merge.go`, `MergeProfiles` (L168-214) references `child.ExtendsList` as a slice (L174-175, L211). Update:

```go
	if len(child.ExtendsList.Raw) > 0 {
		out.ExtendsList = child.ExtendsList
	}
	...
	out.ExtendsList = ExtendsList{}
```

(L211 resets to empty; use the zero struct.)

- [ ] **Step 8: Update `merge_multi_test.go` for the struct form**

Every `ExtendsList: ExtendsList{"base", "ssh"}` becomes `ExtendsList: ExtendsList{Raw: []string{"base", "ssh"}}`. Read the file and update all 10+ literals (L14, L41, L62, L79, L83, L104, L120, L121, L134, L151). `NewProfileCatalogForTest` (Task 2 Step 4g) already calls `v.ExtendsList.Resolve(...)` on each entry, so `Resolved` is filled automatically from `Raw`. No additional test-side `Resolve` calls are needed.

- [ ] **Step 9: Update the self-shadow test**

In `internal/profile/merge_test.go`, `TestResolveExtendsSelfViaBuiltin` (L74-93): change the user file to extend `core/opencode` (qualified), since unqualified `extends: opencode` from `opencode.yaml` is now a self-cycle:

```go
func TestResolveExtendsSelfViaBuiltin(t *testing.T) {
	dir := t.TempDir()
	// User file shadows built-in "opencode" and extends it via the qualified
	// core/ prefix to avoid a self-cycle.
	mustWriteProfile(t, dir, "opencode.yaml", "version: 1\nextends: core/opencode\ncaches:\n  npm: ~/.npm\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	cfg, err := ResolveProfile(cat, "opencode")
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if cfg.Image != "debian:13-slim" {
		t.Errorf("Image = %q, want debian:13-slim (inherited from built-in)", cfg.Image)
	}
	if got := cfg.Caches["npm"]; len(got) != 1 || got[0] != "~/.npm" {
		t.Errorf("Caches[npm] = %v, want [~/.npm]", got)
	}
}
```

Add a new test for the now-rejected unqualified self-shadow:

```go
func TestResolveExtendsSelfUnqualifiedIsCycle(t *testing.T) {
	dir := t.TempDir()
	// Unqualified extends: mise from user mise.yaml now resolves to the user
	// entry itself (user-first fallback), so it's a self-cycle.
	mustWriteProfile(t, dir, "mise.yaml", "version: 1\nextends: mise\ncaches:\n  npm: ~/.npm\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	_, err = ResolveProfile(cat, "mise")
	if err == nil {
		t.Fatal("expected self-cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention cycle, got: %v", err)
	}
}

func TestResolveExtendsCoreSelfIsCycle(t *testing.T) {
	// core/mise extending core/mise (qualified self) is a cycle.
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"mise": {Profile: Profile{Version: 1, ExtendsList: ExtendsList{Raw: []string{"core/mise"}}, Image: "x", Command: []string{"x"}}},
	})
	_, err := ResolveProfile(cat, "mise")
	if err == nil {
		t.Fatal("expected self-cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention cycle, got: %v", err)
	}
}
```

- [ ] **Step 10: Update `TestResolveSelfExtendsNoBuiltin` (L116-130)**

It already writes `extends: foo` from `foo.yaml` with no built-in `foo`. Under the new rules, unqualified `foo` resolves to the user `foo` itself → self-cycle. The test expects a cycle error — still passes. Add a comment and an additional case for `extends: core/foo` from `core/foo` (covered by `TestResolveExtendsCoreSelfIsCycle` above). No code change needed; verify it passes.

- [ ] **Step 11: Verify `TestResolveUserShadowMergesAllBuiltinExtends` passes**

This test was rewritten in Task 2 Step 4j to use `NewProfileCatalogForTest` + a `core/`-qualified shadow (no `IsUserShadow` special-case). No further change here; verify it still passes with the `ExtendsList` struct form (the shadow's `ExtendsList.Resolved` is filled by the explicit `shadow.ExtendsList.Resolve(cat.namespaces)` call in the test).

- [ ] **Step 12: Run the profile package tests + vet**

Run: `go test ./internal/profile/ && go vet ./internal/profile/`
Expected: PASS.

- [ ] **Step 13: Run full repo tests + vet**

Run: `go test ./... && go vet ./...`
Expected: PASS. (`cmd/tpd`, `scaffold`, `doctor`, `prune` pass — `doctor`'s `GetBuiltin("mise")` was fixed in Task 2 Step 4i, and `cli.go`'s `IsUserShadow` was replaced in Task 2 Step 4i.)

- [ ] **Step 14: Commit**

```bash
git add internal/profile/types.go internal/profile/catalog.go internal/profile/merge.go internal/profile/extends_test.go internal/profile/merge_test.go internal/profile/merge_multi_test.go internal/profile/catalog_test.go internal/doctor/checks.go
git commit -m "feat(profile): ExtendsList struct with Resolved refs; resolveChain iterates Resolved"
```

---

### Task 4: Display APIs — `DisplayNames`, `ProfileDisplayNames`, `Source`, `FragmentByDisplayName`; `Names`/`ProfileNames` return canonical keys

**Files:**
- Modify: `internal/profile/catalog.go` (add `DisplayNames`, `ProfileDisplayNames`, `Source`, `FragmentByDisplayName`; confirm `Names`/`ProfileNames` return `FullName` keys)
- Test: `internal/profile/catalog_test.go`

**Interfaces:**
- Consumes: `RawProfile.FullName()`/`DisplayName()` (Task 2), `Catalog.entries`/`fragments`.
- Produces:
  - `func (c Catalog) DisplayNames() []string` — sorted `DisplayName`s, deduplicated (user shadows core: user wins; `core/go` with no user `go` shows as `go`).
  - `func (c Catalog) ProfileDisplayNames() []string` — same filtered to non-fragments.
  - `func (c Catalog) Source(displayName string) string` — `"user"`, `"core"`, or `"user shadow"`.
  - `func (c Catalog) FragmentByDisplayName(name string) (string, bool)` — returns the canonical `FullName` for a fragment display name (user fragment wins over `core/`).

- [ ] **Step 1: Write failing tests**

Append to `internal/profile/catalog_test.go`:

```go
func TestDisplayNamesDedupsUserShadow(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shell.yaml"), []byte("version: 1\nimage: my/custom:latest\ncommand: [\"bash\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := cat.DisplayNames()
	if !contains(names, "shell") {
		t.Errorf("DisplayNames missing shell; got %v", names)
	}
	// "shell" appears once (user shadows core), not twice.
	count := 0
	for _, n := range names {
		if n == "shell" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("shell appears %d times in DisplayNames, want 1", count)
	}
}

func TestDisplayNamesIncludesCoreOnly(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	names := cat.DisplayNames()
	if !contains(names, "mise") {
		t.Errorf("DisplayNames missing core-only mise; got %v", names)
	}
	if contains(names, "core/mise") {
		t.Errorf("DisplayNames should not contain qualified core/mise; got %v", names)
	}
}

func TestProfileDisplayNamesExcludesFragments(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	names := cat.ProfileDisplayNames()
	if contains(names, "javascript") {
		t.Errorf("ProfileDisplayNames should exclude fragment javascript; got %v", names)
	}
	if !contains(names, "mise") {
		t.Errorf("ProfileDisplayNames missing profile mise; got %v", names)
	}
}

func TestSourceUserShadow(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shell.yaml"), []byte("version: 1\nimage: x\ncommand: [\"bash\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cat.Source("shell"); got != "user shadow" {
		t.Errorf("Source(shell) = %q, want \"user shadow\"", got)
	}
	if got := cat.Source("mise"); got != "core" {
		t.Errorf("Source(mise) = %q, want \"core\"", got)
	}
}

func TestSourceUserOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rustdev.yaml"), []byte("version: 1\nextends: shell\ntools:\n  rust: \"1.74\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cat.Source("rustdev"); got != "user" {
		t.Errorf("Source(rustdev) = %q, want \"user\"", got)
	}
}

func TestFragmentByDisplayNameUserWins(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "fragments", "javascript.yaml"), []byte("version: 1\ntools:\n  node: \"user\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := cat.FragmentByDisplayName("javascript")
	if !ok {
		t.Fatal("expected ok")
	}
	if got != "javascript" {
		t.Errorf("FragmentByDisplayName(javascript) = %q, want \"javascript\" (user wins)", got)
	}
}

func TestFragmentByDisplayNameCoreOnly(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := cat.FragmentByDisplayName("javascript")
	if !ok {
		t.Fatal("expected ok")
	}
	if got != "core/javascript" {
		t.Errorf("FragmentByDisplayName(javascript) = %q, want \"core/javascript\"", got)
	}
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/profile/ -run 'TestDisplayNames|TestProfileDisplayNames|TestSource|TestFragmentByDisplayName' -v`
Expected: FAIL — the methods don't exist.

- [ ] **Step 3: Implement the display APIs**

In `internal/profile/catalog.go`, add:

```go
// DisplayNames returns the set of unqualified display names, deduplicated
// across namespaces. A user entry shadows a core entry of the same display
// name (user wins, shown once). Core-only entries show as the bare name.
func (c Catalog) DisplayNames() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(c.entries))
	// User namespace first so it shadows core.
	for _, name := range c.entries {
		if name.Namespace == "" {
			seen[name.DisplayName()] = true
			out = append(out, name.DisplayName())
		}
	}
	for _, name := range c.entries {
		if name.Namespace != "" {
			dn := name.DisplayName()
			if !seen[dn] {
				seen[dn] = true
				out = append(out, dn)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ProfileDisplayNames is DisplayNames filtered to non-fragments.
func (c Catalog) ProfileDisplayNames() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(c.entries))
	for _, name := range c.entries {
		if name.Namespace == "" && !c.fragments[name.FullName()] {
			seen[name.DisplayName()] = true
			out = append(out, name.DisplayName())
		}
	}
	for _, name := range c.entries {
		if name.Namespace != "" && !c.fragments[name.FullName()] {
			dn := name.DisplayName()
			if !seen[dn] {
				seen[dn] = true
				out = append(out, dn)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Source reports the provenance of a display name: "user" (user-only),
// "core" (core-only), or "user shadow" (user entry shadowing a core entry).
func (c Catalog) Source(displayName string) string {
	_, hasUser := c.entries[displayName]
	coreKey := "core/" + displayName
	_, hasCore := c.entries[coreKey]
	switch {
	case hasUser && hasCore:
		return "user shadow"
	case hasUser:
		return "user"
	case hasCore:
		return "core"
	default:
		return "core"
	}
}

// FragmentByDisplayName resolves a fragment display name to its canonical
// FullName. A user fragment wins over a core fragment of the same name.
func (c Catalog) FragmentByDisplayName(name string) (string, bool) {
	if _, ok := c.entries[name]; ok && c.fragments[name] {
		return name, true
	}
	coreKey := "core/" + name
	if _, ok := c.entries[coreKey]; ok && c.fragments[coreKey] {
		return coreKey, true
	}
	return "", false
}
```

- [ ] **Step 4: Run the new tests + vet**

Run: `go test ./internal/profile/ -run 'TestDisplayNames|TestProfileDisplayNames|TestSource|TestFragmentByDisplayName' -v && go vet ./internal/profile/`
Expected: PASS.

- [ ] **Step 5: Run full repo tests + vet**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/profile/catalog.go internal/profile/catalog_test.go
git commit -m "feat(profile): display/source APIs (DisplayNames, Source, FragmentByDisplayName)"
```

---

### Task 5: CLI resolution — `tpd show`/`edit`/`list`/launch resolve via `ParseRef`+`ResolveRef`; `tpd edit core/mise` strips prefix; `builtinEditSeed` emits `core/`

**Files:**
- Modify: `cmd/tpd/cli.go` (`ProfileShowCmd.Run`, `ProfileEditCmd.Run`, `ProfileListCmd.Run`, `LaunchCmd.Run`, `builtinEditSeed`)
- Modify: `cmd/tpd/profile_test.go` (update `TestProfileList` to bare names + `core/` edit seed)
- Test: `cmd/tpd/profile_test.go`

**Interfaces:**
- Consumes: `ParseRef`, `ResolveRef`, `DisplayNames`/`ProfileDisplayNames`/`Source` (Tasks 1, 4), `RawProfile.DisplayName()`.
- Produces: CLI commands that accept qualified (`core/mise`) and unqualified (`mise`) names and resolve them correctly; `tpd list` shows `DisplayName` + `Source`; `tpd edit core/mise` seeds `mise.yaml` with `extends: core/mise`.

- [ ] **Step 1: Write failing CLI tests**

In `cmd/tpd/profile_test.go`, add/extend:

```go
func TestProfileListShowsDisplayNameAndSource(t *testing.T) {
	// Build a catalog with a user shadow and a core-only entry; assert the
	// NAME column is bare (DisplayName) and SOURCE is user/core/user shadow.
	// (Uses the real embedded catalog via LoadProfiles.)
	// ... existing TestProfileList, updated to expect bare names + source column.
}

func TestProfileEditCoreMiseSeedsUserMiseYaml(t *testing.T) {
	// `tpd edit core/mise` with no existing user file seeds
	// ~/.config/tpd/profiles/mise.yaml with `extends: core/mise`.
	// (Use a temp HOME; run ProfileEditCmd.Run with a non-interactive editor
	// that exits without saving so the seed is removed; assert the seed
	// content before the editor exits. Or use a saved-edit path.)
}
```

Read the existing `TestProfileList` (L58-88) and `TestProfileEdit` (if present) to match the harness style (temp HOME, `ProfileEditCmd`/`ProfileListCmd` construction). The exact test scaffolding depends on existing helpers in `profile_test.go` — use them. If no `TestProfileEdit` exists, add one using the same temp-HOME pattern.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/tpd/ -run 'TestProfileListShowsDisplayNameAndSource|TestProfileEditCoreMiseSeedsUserMiseYaml' -v`
Expected: FAIL.

- [ ] **Step 3: Update `ProfileListCmd.Run`**

In `cmd/tpd/cli.go` (L352-374), replace the iteration:

```go
func (c *ProfileListCmd) Run() error {
	cat, err := profile.LoadProfiles(profile.DefaultProfileDir())
	if err != nil {
		return fmt.Errorf("loading profiles: %w", err)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKIND\tSOURCE")
	for _, dn := range cat.DisplayNames() {
		kind := "profile"
		if _, ok := cat.Get("core/" + dn); ok && cat.IsFragment("core/"+dn) {
			kind = "fragment"
		} else if _, ok := cat.Get(dn); ok && cat.IsFragment(dn) {
			kind = "fragment"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", dn, kind, cat.Source(dn))
	}
	w.Flush()
	return nil
}
```

(Kind detection: a display name maps to either `core/<dn>` or bare `<dn>`; check both for `IsFragment`. The `Source` helper handles user/core/shadow.)

- [ ] **Step 4: Update `ProfileShowCmd.Run` to resolve via `ParseRef`+`ResolveRef`**

In `cmd/tpd/cli.go` (L205-235), resolve `c.Name`:

```go
func (c *ProfileShowCmd) Run() error {
	cat, err := profile.LoadProfiles(profile.DefaultProfileDir())
	if err != nil {
		return fmt.Errorf("loading profiles: %w", err)
	}
	ref, perr := profile.ParseRef(c.Name, cat.Namespaces())
	if perr != nil {
		return fmt.Errorf("%s", perr)
	}
	key, ok := cat.ResolveRef(ref)
	if !ok {
		return fmt.Errorf("profile not found: %s", c.Name)
	}
	if c.Resolved {
		if cat.IsFragment(key) {
			resolved, err := profile.ResolveFragment(cat, c.Name)
			if err != nil {
				return err
			}
			out, err := yaml.Marshal(resolved)
			if err != nil {
				return err
			}
			fmt.Print(string(out))
			return nil
		}
		resolved, err := profile.ResolveProfile(cat, c.Name)
		if err != nil {
			return err
		}
		out, err := yaml.Marshal(resolved)
		if err != nil {
			return err
		}
		fmt.Print(string(out))
		return nil
	}
	rc, ok := cat.Get(key)
	if !ok {
		return fmt.Errorf("profile not found: %s", c.Name)
	}
	out, err := yaml.Marshal(rc.Profile)
	if err != nil {
		return err
	}
	fmt.Print(string(out))
	return nil
}
```

(Expose `Catalog.Namespaces() map[string]bool` — a simple getter — in `catalog.go`.)

- [ ] **Step 5: Update `ProfileEditCmd.Run` to strip `core/` for the file path and resolve via `ResolveRef`**

In `cmd/tpd/cli.go` (L237-308):

```go
func (c *ProfileEditCmd) Run() error {
	userDir := profile.DefaultProfileDir()
	if userDir == "" {
		return fmt.Errorf("cannot determine profile directory (set XDG_CONFIG_HOME)")
	}
	cat, err := profile.LoadProfiles(userDir)
	if err != nil {
		return fmt.Errorf("loading profiles: %w", err)
	}
	ref, perr := profile.ParseRef(c.Name, cat.Namespaces())
	if perr != nil {
		return fmt.Errorf("%s", perr)
	}
	key, ok := cat.ResolveRef(ref)
	if !ok {
		return fmt.Errorf("profile not found: %s", c.Name)
	}
	// The file/display name is the local segment (no namespace prefix).
	displayName := key
	if i := strings.LastIndex(key, "/"); i >= 0 {
		displayName = key[i+1:]
	}
	targetPath := filepath.Join(userDir, displayName+".yaml")
	if cat.IsFragment(key) {
		targetPath = filepath.Join(profile.DefaultFragmentDir(), displayName+".yaml")
	}
	if _, err := os.Stat(targetPath); err == nil {
		return openEditor(targetPath)
	}
	// Seed the user shadow file. The built-in is read by its canonical key.
	fsys, root := catalog.Profiles, "profiles"
	kind := "profile"
	if cat.IsFragment(key) {
		fsys, root = catalog.Fragments, "fragments"
		kind = "fragment"
	}
	if _, err := fsys.ReadFile(root + "/" + displayName + ".yaml"); err != nil {
		return fmt.Errorf("reading built-in %s: %w", displayName, err)
	}
	var resolved profile.Profile
	var resolveErr error
	if kind == "fragment" {
		resolved, resolveErr = profile.ResolveFragment(cat, key)
	} else {
		resolved, resolveErr = profile.ResolveProfile(cat, key)
	}
	if resolveErr != nil {
		return fmt.Errorf("resolving %s: %w", displayName, resolveErr)
	}
	resolvedYAML, err := yaml.Marshal(resolved)
	if err != nil {
		return fmt.Errorf("marshaling resolved %s: %w", displayName, err)
	}
	data := builtinEditSeed(kind, key, resolvedYAML)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
		return fmt.Errorf("creating profile directory: %w", err)
	}
	if err := os.WriteFile(targetPath, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", targetPath, err)
	}
	before, err := os.Stat(targetPath)
	if err != nil {
		return err
	}
	if err := openEditor(targetPath); err != nil {
		return err
	}
	after, err := os.Stat(targetPath)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(targetPath)
	if err != nil {
		return err
	}
	if !savedEdit(before, after, data, content) {
		os.Remove(targetPath)
	}
	return nil
}
```

- [ ] **Step 6: Update `builtinEditSeed` to emit `extends: <canonical key>`**

In `cmd/tpd/cli.go` (L313-331), change the seed to take the canonical key and emit it:

```go
func builtinEditSeed(kind, canonicalKey string, resolved []byte) []byte {
	const rule = "# ──────────────────────────────────────────────────────────────────\n"
	var b bytes.Buffer
	fmt.Fprintf(&b, "# This file shadows the built-in %q %s. Settings here are merged on\n", canonicalKey, kind)
	b.WriteString("# top of the built-in, so only change what you need.\n\n")
	fmt.Fprintf(&b, "version: 1\nextends: %s\n\n", canonicalKey)
	b.WriteString(rule)
	fmt.Fprintf(&b, "# Resolved %s (reference) — snapshot from when this file was created;\n", kind)
	fmt.Fprintf(&b, "# the built-in may have changed since. Run `tpd show --resolved %s`\n", canonicalKey)
	fmt.Fprintf(&b, "# for the current resolved %s.\n", kind)
	b.WriteString(rule)
	b.WriteString("\n")
	for _, line := range strings.Split(strings.TrimRight(string(resolved), "\n"), "\n") {
		b.WriteString("# ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.Bytes()
}
```

- [ ] **Step 7: Verify `LaunchCmd` resolves qualified names (no change needed)**

`ParseRefForCatalog` and `ResolveProfile` already parse the string via `ParseRef`+`ResolveRef` (Task 2 Step 4g). `LaunchCmd.Run` (L102-128) passes the raw `profileName` to `tpd.Launch` -> `profile.ResolveProfile`, so a qualified `tpd core/mise` works end-to-end with no CLI change. Verify by adding a resolution-layer test in `cmd/tpd/profile_test.go`:

```go
func TestResolveQualifiedCoreMise(t *testing.T) {
	cat, err := profile.LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := profile.ResolveProfile(cat, "core/mise")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Image != "debian:13-slim" {
		t.Errorf("ResolveProfile(core/mise).Image = %q, want debian:13-slim", cfg.Image)
	}
}
```

No `mustParseRef` helper is introduced anywhere in the plan; `ParseRefForCatalog` (Task 2) is the single API for parsing a user-supplied string against the catalog.

- [ ] **Step 8: Add `Namespaces()` getter**

In `internal/profile/catalog.go`:

```go
// Namespaces returns the registered namespace set (for CLI ref parsing).
func (c Catalog) Namespaces() map[string]bool {
	return c.namespaces
}
```

- [ ] **Step 9: Run the CLI tests + vet**

Run: `go test ./cmd/tpd/ && go vet ./cmd/tpd/`
Expected: PASS.

- [ ] **Step 10: Run full repo tests + vet**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 11: Commit**

```bash
git add cmd/tpd/cli.go cmd/tpd/profile_test.go internal/profile/ref.go internal/profile/merge.go
git commit -m "feat(cli): resolve qualified/unqualified names; tpd list uses DisplayName/Source; edit seeds core/"
```

---

### Task 6: Scaffold — emit `core/`-qualified bases and fragment refs; `AddRaw("", ...)` signature

**Files:**
- Modify: `internal/scaffold/scaffold.go` (`Run`, `generate`, `resolveGeneratedProfile`)
- Modify: `internal/scaffold/fragments.go` (no signature change; mapping happens in `scaffold.go`)
- Test: `internal/scaffold/scaffold_test.go`, `internal/scaffold/new_profile_test.go`

**Interfaces:**
- Consumes: `Catalog.FragmentByDisplayName` (Task 4), `Catalog.Get`/`IsFragment` (canonical keys), `AddRaw(ns, name, rc)`.
- Produces: generated YAML with `extends: core/<name>` for built-in bases and `extends: core/<fragment>` for built-in fragments; `AddRaw("", profileName, rc)`.

- [ ] **Step 1: Write failing scaffold tests**

In `internal/scaffold/new_profile_test.go` (or `scaffold_test.go`), add:

```go
func TestGenerateEmitsCoreQualifiedBuiltinBase(t *testing.T) {
	cat, err := profile.LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	content, err := generate("myagent", []string{"core/shell"}, cat)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "extends: core/shell") {
		t.Errorf("generated content should contain `extends: core/shell`, got:\n%s", content)
	}
}

func TestGenerateEmitsCoreQualifiedFragment(t *testing.T) {
	cat, err := profile.LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	// Fragment picked by display name "javascript"; emitted as core/javascript.
	content, err := generate("myagent", []string{"core/mise", "core/javascript"}, cat)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "extends: core/mise") || !strings.Contains(content, "core/javascript") {
		t.Errorf("generated content missing core/-qualified extends, got:\n%s", content)
	}
}

func TestGenerateEmitsUserFragmentUnqualified(t *testing.T) {
	dir := t.TempDir()
	fragDir := filepath.Join(filepath.Dir(dir), "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "myfrag.yaml"), []byte("version: 1\ntools:\n  x: \"1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := profile.LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	content, err := generate("myagent", []string{"myfrag"}, cat)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "extends: myfrag") {
		t.Errorf("user fragment should be emitted unqualified, got:\n%s", content)
	}
}
```

Read the existing `scaffold_test.go`/`new_profile_test.go` for the `generate` call pattern (it's package-internal) and the `Run` test harness (temp HOME, `Options`). Update the existing `TestScaffold*` tests that assert `extends: mise` to expect `extends: core/mise` where the base is a built-in.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/scaffold/ -run 'TestGenerate' -v`
Expected: FAIL (current `generate` emits unqualified names).

- [ ] **Step 3: Update the scaffold's base-fallback to emit `core/`**

In `internal/scaffold/scaffold.go` (L138-144), the "fall back to built-in of same name" path. Only a built-in of the same name qualifies as a base; a user-only profile of the same name is **not** added (it would be a self-cycle). The default fallback is `core/mise`:

```go
	if !hasProfile {
		if _, ok := cat.Get("core/" + profileName); ok {
			bases = append([]string{"core/" + profileName}, bases...)
		} else {
			bases = append([]string{"core/mise"}, bases...)
		}
	}
```

(The `GetBuiltin` call becomes `Get("core/"+profileName)` — direct canonical lookup. The `mise` default is also `core/mise`. The previous `else if _, ok := cat.Get(profileName)` branch is removed: it would prepend the user entry itself, producing `extends: <itself>` for a user-only profile being re-scaffolded — a self-cycle.)

- [ ] **Step 4: Update fragment-picker mapping to canonical via `FragmentByDisplayName`**

In `internal/scaffold/scaffold.go`, the picker returns display names (L150, L156). Map each to canonical before appending to `bases`:

```go
	if interactive && len(opts.Extends) == 0 {
		var picked []string
		if tty {
			p, err := promptFragmentsHuh(FragmentNames(), stdin, stdout)
			if err != nil {
				return err
			}
			picked = p
		} else {
			picked = promptFragments(FragmentNames(), reader, stderr)
		}
		for _, dn := range picked {
			full, ok := cat.FragmentByDisplayName(dn)
			if !ok {
				return fmt.Errorf("unknown fragment: %s", dn)
			}
			bases = append(bases, full)
		}
		wizardUsed = true
	}
```

- [ ] **Step 5: Update the unknown-extends validation to resolve via `ResolveRef`**

In `internal/scaffold/scaffold.go` (L162-166):

```go
	bases = dedup(bases)
	for _, b := range bases {
		ref, err := profile.ParseRef(b, cat.Namespaces())
		if err != nil {
			return fmt.Errorf("invalid extends target %q: %w", b, err)
		}
		if _, ok := cat.ResolveRef(ref); !ok {
			return fmt.Errorf("unknown extends target: %s", b)
		}
	}
```

- [ ] **Step 6: Update `hasProfile` check to resolve via `ResolveRef`**

In `internal/scaffold/scaffold.go` (L131-137):

```go
	hasProfile := false
	for _, b := range bases {
		ref, err := profile.ParseRef(b, cat.Namespaces())
		if err != nil {
			// Defer the error to the validation loop below.
			continue
		}
		key, ok := cat.ResolveRef(ref)
		if ok && !cat.IsFragment(key) {
			hasProfile = true
			break
		}
	}
```

- [ ] **Step 7: Update `AddRaw` call site**

In `internal/scaffold/scaffold.go` (L488), `cat.AddRaw(profileName, rc)` → `cat.AddRaw("", profileName, rc)` (signature changed in Task 4/2 — confirm `AddRaw` now takes `(ns, name string, rc RawProfile)`).

Update `AddRaw` in `internal/profile/catalog.go` (Task 2 should have already changed the signature; if not, do it here):

```go
func (c *Catalog) AddRaw(ns, name string, rc RawProfile) {
	rc.Namespace = ns
	rc.Name = name
	c.entries[rc.FullName()] = rc
	delete(c.fragments, rc.FullName())
}
```

- [ ] **Step 8: Update the profile picker to use display names**

In `internal/scaffold/scaffold.go` (L74, L81), `builtinCat.ProfileNames()` and `cat.ProfileNames()` return canonical keys (`core/...`). The picker should show display names. Switch to `builtinCat.ProfileDisplayNames()` and `cat.ProfileDisplayNames()`. The selected display name must be resolved to a canonical key when used as a base:

When `profileName = selection` (L101, L111) from the "shadow a built-in" picker, `selection` is a display name. `ValidateName(profileName)` still applies (display name is single-segment). The base-fallback (Step 3) looks up `core/<profileName>` via `cat.Get("core/"+profileName)` — works with a display name.

When the user picks "extend a base profile" in `promptNewProfileHuh`/`promptNewProfile` (L96, L106), `baseNames` should be `ProfileDisplayNames()` (display names), and the selected base is a display name. Resolve it to canonical via `ResolveRef` before adding to `bases`. Update L81:

```go
	baseNames := dedup(append([]string{"mise"}, cat.ProfileDisplayNames()...))
```

And after the wizard returns `bases` (display names), map them to canonical:

```go
	// bases from the wizard are display names; resolve to canonical.
	canonicalBases := make([]string, 0, len(bases))
	for _, dn := range bases {
		ref, err := profile.ParseRef(dn, cat.Namespaces())
		if err != nil {
			return err
		}
		key, ok := cat.ResolveRef(ref)
		if !ok {
			return fmt.Errorf("unknown base: %s", dn)
		}
		canonicalBases = append(canonicalBases, key)
	}
	bases = canonicalBases
```

Place this right after the wizard branches (after L114). The `opts.Extends` path (L83) is user-supplied and may already be qualified — don't resolve those (they go through the validation loop in Step 5).

- [ ] **Step 9: Update `generate` to emit canonical strings**

`generate(name string, extends []string, cat profile.Catalog)` (L250-265) takes `extends` already in canonical form (from the steps above). The `ExtendsList` struct's `MarshalYAML` (Task 3) emits `Resolved` if set, else `Raw`. `generate` constructs `ExtendsList: profile.ExtendsList(extends)` — but `ExtendsList` is now a struct, not a slice. Update:

```go
func generate(name string, extends []string, cat profile.Catalog) (string, error) {
	el := profile.ExtendsList{Raw: extends}
	if err := el.Resolve(cat.Namespaces()); err != nil {
		return "", err
	}
	p := profile.Profile{
		Version:     1,
		ExtendsList: el,
	}
	if !basesProvideCommand(cat, extends) {
		p.Command = []string{"bash"}
	}
	data, err := yaml.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
```

`MarshalYAML` emits `Resolved` (canonical) → `extends: [core/mise, core/javascript]`. 

`basesProvideCommand` (L272) calls `profile.ResolveProfile(cat, b)` — `b` is now canonical; `ResolveProfile` parses it via `ParseRef` and resolves. Works.

- [ ] **Step 10: Update `resolveGeneratedProfile`**

In `internal/scaffold/scaffold.go` (L483-490):

```go
func resolveGeneratedProfile(content, profileName string, cat profile.Catalog) (profile.Profile, error) {
	rc, err := profile.ParseRaw([]byte(content), "generated:"+profileName)
	if err != nil {
		return profile.Profile{}, err
	}
	cat.AddRaw("", profileName, rc)
	return profile.ResolveProfile(cat, profileName)
}
```

- [ ] **Step 11: Update existing scaffold tests for `core/` output**

Read `scaffold_test.go` and `new_profile_test.go` and update any assertion that the generated YAML contains `extends: mise` (or similar unqualified built-in) to `extends: core/mise`. User-supplied `--extends` names in tests that reference built-ins by bare name should still resolve via `ResolveRef`, but the emitted YAML will be canonical (`core/mise`) — update those expectations.

- [ ] **Step 12: Run the scaffold tests + vet**

Run: `go test ./internal/scaffold/ && go vet ./internal/scaffold/`
Expected: PASS.

- [ ] **Step 13: Run full repo tests + vet**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 14: Commit**

```bash
git add internal/scaffold/scaffold.go internal/scaffold/scaffold_test.go internal/scaffold/new_profile_test.go internal/profile/catalog.go
git commit -m "feat(scaffold): emit core/-qualified extends for built-ins; FragmentByDisplayName mapping"
```

---

### Task 7: Update built-in fragment `typescript.yaml` to `extends: core/javascript`

**Files:**
- Modify: `internal/catalog/fragments/typescript.yaml`
- Test: `internal/profile/catalog_test.go` (assert `core/typescript` extends `core/javascript` and is unaffected by a user profile named `javascript`)

**Interfaces:** None (data change).

- [ ] **Step 1: Write the failing test**

Append to `internal/profile/catalog_test.go`:

```go
func TestBuiltinTypescriptExtendsCoreJavascript(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	rc, ok := cat.Get("core/typescript")
	if !ok {
		t.Fatal("core/typescript missing")
	}
	if len(rc.ExtendsList.Resolved) != 1 || rc.ExtendsList.Resolved[0] != (Ref{Namespace: "core", Name: "javascript"}) {
		t.Errorf("core/typescript extends = %+v, want [core/javascript]", rc.ExtendsList.Resolved)
	}
}

func TestTypescriptUnaffectedByUserProfileNamedJavascript(t *testing.T) {
	dir := t.TempDir()
	// A user *profile* named javascript would win unqualified fallback, but
	// core/typescript extends core/javascript (qualified), so the fragment
	// chain is unaffected.
	if err := os.WriteFile(filepath.Join(dir, "javascript.yaml"), []byte("version: 1\nimage: user/js:latest\ncommand: [\"node\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	// core/typescript should resolve and inherit the core/javascript fragment,
	// not the user javascript profile.
	merged, err := ResolveFragment(cat, "typescript")
	if err != nil {
		t.Fatalf("ResolveFragment: %v", err)
	}
	if _, ok := merged.Tools["node"]; !ok {
		t.Error("core/typescript should inherit node from core/javascript fragment, not the user profile")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/profile/ -run 'TestBuiltinTypescriptExtendsCoreJavascript|TestTypescriptUnaffectedByUserProfileNamedJavascript' -v`
Expected: FAIL (current `extends: javascript` resolves to `Ref{Namespace: "", Name: "javascript"}`).

- [ ] **Step 3: Update `typescript.yaml`**

In `internal/catalog/fragments/typescript.yaml`, change line 2:

```yaml
version: 1
extends: core/javascript
tools:
  biome: latest
  npm:ts-node: latest
  npm:tsx: latest
  npm:typescript: latest
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/profile/ -run 'TestBuiltinTypescriptExtendsCoreJavascript|TestTypescriptUnaffectedByUserProfileNamedJavascript' -v`
Expected: PASS.

- [ ] **Step 5: Run full repo tests + vet**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/catalog/fragments/typescript.yaml internal/profile/catalog_test.go
git commit -m "feat(catalog): core/typescript extends core/javascript (qualified)"
```

---

### Task 8: Doctor — replace `GetBuiltin`/`IsUserShadow`/path-prefix with canonical lookups and `Source`

**Files:**
- Modify: `internal/doctor/checks.go` (`checkMiseBaseImage`, `checkProfileValidity`, `checkUserOverrides`)
- Test: `internal/doctor` (if a test file exists; otherwise add one or extend an existing end-to-end test)

**Interfaces:**
- Consumes: `Catalog.Get`/`Source`/`ProfileNames`/`Names` (canonical keys), `ResolveProfile`.
- Produces: doctor checks that work with `FullName`-keyed entries.

- [ ] **Step 1: Audit `internal/doctor/checks.go`**

Read `internal/doctor/checks.go` fully. Identify:
- L84: `cat.GetBuiltin("mise")` → `cat.Get("core/mise")` (already done in Task 3 Step 13b; confirm).
- L185-186: `cat.ProfileNames()` / `cat.Get(name)` — `ProfileNames()` returns canonical keys, `Get` takes canonical. No change needed.
- L222-227: `catMerged.Names()` / `catMerged.Get(name)` / `strings.HasPrefix(rc.Path, "built-in") || catMerged.IsFragment(name)` — the path-prefix heuristic should become `catMerged.Source(name) != "user"`. But `Source` takes a display name, and `Names()` returns canonical keys. Convert: iterate `DisplayNames()` and use `Source(dn)`, or keep `Names()` and derive source from the entry's `Namespace`. Simpler: replace the heuristic with `rc.Namespace != ""` (core) or `catMerged.IsFragment(name)`.

- [ ] **Step 2: Update `checkUserOverrides` to use `Namespace`/`Source`**

In `internal/doctor/checks.go` `checkUserOverrides` (L210-251), replace the `strings.HasPrefix(rc.Path, "built-in:")` check:

```go
	for _, name := range catMerged.Names() {
		rc, ok := catMerged.Get(name)
		if !ok {
			continue
		}
		if rc.Namespace == "core" || catMerged.IsFragment(name) {
			continue
		}
		userFileCount++
		...
	}
```

(`rc.Namespace == "core"` replaces `strings.HasPrefix(rc.Path, "built-in:")`; user entries have `Namespace == ""`.)

- [ ] **Step 3: Confirm `checkProfileValidity` and `checkMiseBaseImage` use canonical keys**

- `checkProfileValidity` (L176-208): `ProfileNames()` → canonical; `Get(name)` → canonical; `ResolveProfile(cat, name)` parses the canonical key via `ParseRef` (works, qualified). No change.
- `checkMiseBaseImage` (L79-97): `Get("core/mise")` (done in Task 3). Confirm.

- [ ] **Step 4: Run doctor tests + vet**

Run: `go test ./internal/doctor/ && go vet ./internal/doctor/`
Expected: PASS. (If no test file exists, run `go build ./internal/doctor/` and rely on the full-suite run.)

- [ ] **Step 5: Run full repo tests + vet**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/doctor/checks.go
git commit -m "refactor(doctor): use Namespace/Source instead of path-prefix/GetBuiltin"
```

---

### Task 9: Prune — confirm canonical-key iteration

**Files:**
- Modify: `internal/prune/prune.go` (only if `ProfileNames`/`Get` usage breaks)
- Test: `internal/prune` (existing tests)

**Interfaces:** Consumes `ProfileNames()` (canonical keys), `ResolveProfile(cat, name)`.

- [ ] **Step 1: Audit `internal/prune/prune.go`**

Read `internal/prune/prune.go` around L143-149 (`computeUsed`). `cat.ProfileNames()` returns canonical keys (`core/mise`); `profile.ResolveProfile(cat, name)` parses the canonical key via `ParseRef` and resolves. No change needed unless a test breaks.

- [ ] **Step 2: Run prune tests + vet**

Run: `go test ./internal/prune/ && go vet ./internal/prune/`
Expected: PASS.

- [ ] **Step 3: Run full repo tests + vet**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 4: Commit (only if changes were needed)**

If no changes, skip the commit. Otherwise:

```bash
git add internal/prune/prune.go
git commit -m "refactor(prune): canonical-key iteration"
```

---

### Task 10: End-to-end verification and migration note

**Files:**
- Modify: `docs/` release notes or `AGENTS.md` (migration note for `extends: <self>` → `extends: core/<name>`)
- Test: manual + `go test ./...` + `go vet ./...`

- [ ] **Step 1: Run the full suite**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 2: Manual smoke test**

- `tpd list` — NAME column shows bare names (`mise`, `shell`), SOURCE shows `core`/`user`/`user shadow`.
- `tpd show core/mise` — shows the built-in merged profile.
- `tpd show mise` — shows user `mise` if present, else `core/mise`.
- `tpd core/mise` (launch) — launches the built-in.
- `tpd edit core/mise` (with no user `mise.yaml`) — seeds `~/.config/tpd/profiles/mise.yaml` with `extends: core/mise`.
- `tpd init` — generated profile YAML contains `extends: core/<name>` for built-in bases.

- [ ] **Step 3: Add migration note**

Add a short note to `docs/` (or `CHANGELOG.md` if present) describing the `extends: <self>` → `extends: core/<name>` migration for existing user shadow files. Mention `tpd doctor` surfaces the broken pattern as a cycle error.

- [ ] **Step 4: Final commit**

```bash
git add docs/
git commit -m "docs: namespaces migration note (extends: core/<name>)"
```

---

## Self-Review

**Spec coverage:**

- §Namespace model (core/""/future) — Task 1 (`Ref`), Task 2 (`Namespace`/`Name`).
- §Storage (RawProfile fields, Catalog map, FullName) — Task 2.
- §Catalog API (canonical vs display) — Task 2 (`Get`/`Names`/`ProfileNames` canonical), Task 4 (`DisplayNames`/`ProfileDisplayNames`/`Source`/`FragmentByDisplayName`).
- §Loading (stamp Namespace/Name, collision by FullName, NewProfileCatalogForTest stamps core, cross-type display-name collision check) — Task 2.
- §Resolution (ResolveRef, ParseRef, qualified vs unqualified, splitting, single-segment local names) — Task 1.
- §Fragment type-awareness (qualified extends in built-in fragments) — Task 3 (resolveChain iterates Resolved), Task 7 (typescript.yaml).
- §Self-reference (cycle via seen, unqualified self resolves to self) — Task 2 (delete special-case + `resolveBuiltinChain`), Task 3 Step 9 (new tests).
- §Reference parsing (ExtendsList struct, UnmarshalYAML/MarshalYAML/Resolve, parseRaw calls Resolve) — Task 3.
- §CLI behavior for qualified names (show/edit/list/launch) — Task 5.
- §emit core/ in init and edit (scaffold bases, FragmentByDisplayName, builtinEditSeed) — Task 5 (edit), Task 6 (init).
- §Remote imports (future, not implemented) — out of scope (no task).
- §Migration — Task 10 (note); Task 2 (existing `extends: <self>` becomes a cycle error, surfaced by doctor).
- §Test surface (merge_test, catalog_test, ResolveRef, ParseRef, scaffold, profile_test) — Tasks 1, 2, 3, 4, 5, 6, 7.
- §Out of scope — respected (no remote fetching, no user-defined namespaces beyond core/"").

**Placeholder scan:** No "TBD"/"TODO"/"add validation" without code. Each step has concrete code or a concrete file:line instruction. Where a step says "read the file and update," it's because the exact edits depend on content that may have shifted; the instruction names the symbols to change.

**Type consistency:**
- `Ref{Namespace, Name}` — used consistently in Tasks 1, 2, 3.
- `ExtendsList{Raw, Resolved}` — Task 3 defines it; Tasks 6, 7 use it.
- `AddRaw(ns, name, rc)` — Task 6 Step 7 updates the signature; Task 2 Step 4g doesn't call `AddRaw` (only `resolveGeneratedProfile` does).
- `ResolveRef(ref Ref) (string, bool)` — Task 1; used in Tasks 2, 3, 5, 6.
- `ParseRef(s, namespaces) (Ref, error)` — Task 1; used in Tasks 2, 3, 5, 6.
- `ParseRefForCatalog(s) (Ref, error)` — Task 2 Step 4g; used in Task 5 Step 7 (verification only). No `mustParseRef` helper exists.
- `Source(displayName) string` — Task 4; used in Task 5.
- `FragmentByDisplayName(name) (string, bool)` — Task 4; used in Task 6.
- `DisplayNames()/ProfileDisplayNames()` — Task 4; used in Tasks 5, 6.
- `Namespaces()` — Task 5 Step 8; used in Tasks 5, 6.

**Review fixes applied:**
- (High) No transitional `builtins` map — `builtins` field, `GetBuiltin`, `IsUserShadow`, `resolveBuiltinChain`, and the `IsUserShadow` special-case are all deleted in Task 2 (Step 5b, 4h), avoiding the duplicate-key violation. Task 3 only does the `ExtendsList` struct + `resolveChain` iteration change.
- (High) Loaders stamp `Namespace`/`Name` **before** the collision check and insert (Task 2 Step 4b,c), so `rc.FullName()` is the canonical key.
- (High) Scaffold base-fallback removes the `else if _, ok := cat.Get(profileName)` branch (Task 6 Step 3) — a user-only profile of the same name is no longer prepended as a base (would self-cycle).
- (Medium) `ParseRef` rejects multi-segment local names (`core/foo/bar`) with an explicit error (Task 1 Step 5) plus a test (Task 1 Step 2).
- (Medium) `mustParseRef` is gone; `ParseRefForCatalog` (Task 2 Step 4g) is the single API with explicit error propagation; Task 5 Step 7 no longer redefines it.
- (Medium) Cross-type display-name collision check added to `LoadProfiles`/`LoadProfilesTolerant` (Task 2 Step 4h) — a fragment and a profile sharing a display name across namespaces is a hard error (strict) or a dropped entry with warning (tolerant), plus a test.

---

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-08-03-namespaces.md`. Two execution options:**

**1. Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints

**Which approach?**