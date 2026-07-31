# Multi-extends, fragments, and profile introspection

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace single-string `extends` and init-time preset inlining with multi-extends lists, rename presets to fragments, add a `profile` subcommand for introspection, and redesign `init` as a bootstrapper with a summary and editor review flow.

**Architecture:** `extends` becomes a list type that unmarshals both string and `[]string`. `resolveChain` walks the list depth-first, merging left-to-right with the body last. Fragments are loaded alongside profiles with globally unique names. `init` writes `extends: [...]` instead of inlining, prints a summary, and prompts an editor review when the profile grants host access. A new `profile` subcommand provides `show`/`edit`/`list`.

**Tech Stack:** Go 1.25+, kong CLI, gopkg.in/yaml.v3, embed.FS, charmbracelet/huh

## Global Constraints

- Go 1.25+ (go.mod)
- YAML tags use `yaml:"name,omitempty"` conventions from `internal/profile/types.go`
- All tests must pass: `go test ./...`
- Lint must pass: `go vet ./...`
- Conventional commit format
- No comments unless code is non-obvious
- kong for CLI parsing (`github.com/alecthomas/kong`)

---

## Task 1: Rename presets → fragments (directory + embed)

**Files:**
- Rename: `internal/catalog/presets/` → `internal/catalog/fragments/`
- Modify: `internal/catalog/embed.go:8-9`
- Test: `go build ./...` (compilation check)

**Interfaces:**
- Produces: `catalog.Fragments` (embed.FS) replacing `catalog.Presets`

- [ ] **Step 1: Rename the directory**

```bash
git mv internal/catalog/presets internal/catalog/fragments
```

- [ ] **Step 2: Update embed.go**

```go
//go:embed fragments/*.yaml
var Fragments embed.FS
```

Replace the `Presets` var and its embed directive.

- [ ] **Step 3: Verify build fails (expected — callers still reference catalog.Presets)**

Run: `go build ./... 2>&1 | head -5`
Expected: build errors referencing `catalog.Presets` — these are fixed in Task 2.

- [ ] **Step 4: Commit**

```bash
git add internal/catalog/fragments/ internal/catalog/embed.go
git commit -m "refactor: rename presets directory to fragments"
```

---

## Task 2: Rename scaffold preset functions and fix all references

**Files:**
- Modify: `internal/scaffold/presets.go` (full rewrite — rename to `fragments.go`)
- Modify: `internal/scaffold/scaffold.go` (all `Presets`/`PresetNames`/`preset` references)
- Modify: `cmd/toolpod/cli.go:35,53,65,109`
- Modify: `internal/doctor/checks.go:163,168,199,205,208`
- Modify: `internal/profile/catalog.go:243-272` (rename `LoadPresets` → `LoadFragments`)
- Test: `internal/scaffold/scaffold_test.go` (update references)

**Interfaces:**
- Produces: `scaffold.Fragments() map[string]profile.RawProfile`, `scaffold.FragmentNames() []string`, `profile.LoadFragments(fsys, root) map[string]RawProfile`
- Consumes: `catalog.Fragments` from Task 1

- [ ] **Step 1: Rename internal/scaffold/presets.go to fragments.go**

```bash
git mv internal/scaffold/presets.go internal/scaffold/fragments.go
```

- [ ] **Step 2: Rewrite fragments.go with renamed identifiers**

```go
package scaffold

import (
	"fmt"
	"sort"
	"sync"

	"github.com/jgillich/toolpod/internal/catalog"
	"github.com/jgillich/toolpod/internal/profile"
)

var (
	fragments     map[string]profile.RawProfile
	fragmentsOnce sync.Once
)

func Fragments() map[string]profile.RawProfile {
	fragmentsOnce.Do(func() {
		m, err := profile.LoadFragments(catalog.Fragments, "fragments")
		if err != nil {
			panic(fmt.Sprintf("loading built-in fragments: %v", err))
		}
		fragments = m
	})
	return fragments
}

func FragmentNames() []string {
	names := make([]string, 0, len(Fragments()))
	for n := range Fragments() {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func BuiltInProfiles() []string {
	cat, err := profile.LoadProfiles("")
	if err != nil {
		return nil
	}
	return cat.Names()
}

func validateFragment(name string, p profile.RawProfile) error {
	if p.Extends != "" || len(p.ExtendsList) != 0 || p.Image != "" || p.Build != nil || len(p.Command) > 0 || p.Version != 0 {
		return fmt.Errorf("fragment %q must not set extends/image/build/command/version", name)
	}
	return nil
}
```

Note: `p.ExtendsList` is added in Task 3. For now, keep the old `p.Extends != ""` check only — update it in Task 3.

- [ ] **Step 3: Rename LoadPresets → LoadFragments in catalog.go**

In `internal/profile/catalog.go`, rename the function and its local variables:

```go
func LoadFragments(fsys fs.ReadFileFS, root string) (map[string]RawProfile, error) {
	fragments := map[string]RawProfile{}
	err := fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
		// ... same body, rename `presets` → `fragments`
	})
	// ...
}
```

- [ ] **Step 4: Update scaffold.go references**

Replace all occurrences in `internal/scaffold/scaffold.go`:
- `opts.Presets` → `opts.Fragments`
- `PresetNames()` → `FragmentNames()`
- `Presets()` → `Fragments()`
- `selectedPresets` → `selectedFragments`
- `presetNamesList` → `fragmentNamesList`
- `checkPresetFiles` → `checkFragmentFiles`
- `promptPresets` → `promptFragments`
- `promptPresetsHuh` → `promptFragmentsHuh`
- All error messages and prompt text: "preset" → "fragment"
- The `Options` struct field: `Presets []string` → `Fragments []string`

- [ ] **Step 5: Update cli.go references**

In `cmd/toolpod/cli.go`:
- `Presets []string` → `Fragments []string` in `InitCmd`
- Help text: "preset" → "fragment"
- `scaffold.PresetNames()` → `scaffold.FragmentNames()`
- `opts.Presets` → `opts.Fragments`
- kong Vars key: `"presets"` → `"fragments"`
- The `--presets` kong tag gets `aliases:"presets"` for backward compat: `` `sep:"," help:"Comma-separated fragment names (${fragments})." aliases:"presets"` ``

- [ ] **Step 6: Update doctor/checks.go references**

Replace "presets" → "fragments" in check names, messages, and `--presets` references in hint strings.

- [ ] **Step 7: Update scaffold_test.go references**

Replace all `Presets` → `Fragments`, `PresetNames` → `FragmentNames`, `opts.Presets` → `opts.Fragments`, and any "preset" in test strings.

- [ ] **Step 8: Verify build and tests pass**

Run: `go build ./... && go test ./...`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "refactor: rename presets to fragments throughout codebase"
```

---

## Task 3: ExtendsList type with custom YAML unmarshaler

**Files:**
- Modify: `internal/profile/types.go` (add `ExtendsList` type, change `Profile.Extends`)
- Create: `internal/profile/extends_test.go`
- Modify: `internal/profile/merge.go` (use `ExtendsList` instead of `Extends`)
- Modify: `internal/profile/catalog.go` (parseRaw — Extends is now ExtendsList)

**Interfaces:**
- Produces: `ExtendsList` type (`[]string`) with `UnmarshalYAML` accepting string or list; `Profile.Extends` field removed, replaced by `Profile.ExtendsList ExtendsList`
- Consumes: nothing new

- [ ] **Step 1: Write the failing test**

Create `internal/profile/extends_test.go`:

```go
package profile

import (
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
	if len(p.Extends) != 1 || p.Extends[0] != "opencode" {
		t.Errorf("got %v, want [opencode]", p.Extends)
	}
}

func TestExtendsListUnmarshalList(t *testing.T) {
	var p struct {
		Extends ExtendsList `yaml:"extends"`
	}
	if err := yaml.Unmarshal([]byte("extends: [opencode, ssh]\n"), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Extends) != 2 || p.Extends[0] != "opencode" || p.Extends[1] != "ssh" {
		t.Errorf("got %v, want [opencode ssh]", p.Extends)
	}
}

func TestExtendsListUnmarshalEmpty(t *testing.T) {
	var p struct {
		Extends ExtendsList `yaml:"extends"`
	}
	if err := yaml.Unmarshal([]byte("extends: []\n"), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Extends) != 0 {
		t.Errorf("got %v, want empty", p.Extends)
	}
}

func TestExtendsListMarshalList(t *testing.T) {
	e := ExtendsList{"opencode", "ssh"}
	out, err := yaml.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	// Should marshal as a YAML list
	if string(out) != "- opencode\n- ssh\n" {
		t.Errorf("marshaled = %q, want YAML list", string(out))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/profile/ -run TestExtendsList -v`
Expected: FAIL — `ExtendsList` not defined.

- [ ] **Step 3: Implement ExtendsList in types.go**

Replace the `Extends string` field in `Profile` with `ExtendsList ExtendsList`. Add the type:

```go
// ExtendsList is a list of profile/fragment names to extend.
// It unmarshals from both a string ("extends: foo") and a list
// ("extends: [foo, bar]"). A single string is normalized to a
// one-element slice.
type ExtendsList []string

func (e *ExtendsList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		*e = ExtendsList{s}
		return nil
	}
	var list []string
	if err := value.Decode(&list); err != nil {
		return err
	}
	*e = ExtendsList(list)
	return nil
}
```

In `Profile`, replace `Extends string \`yaml:"extends,omitempty"\`` with:
```go
	ExtendsList ExtendsList `yaml:"extends,omitempty"`
```

Update the yaml import in types.go.

- [ ] **Step 4: Update merge.go to use ExtendsList**

In `internal/profile/merge.go`, replace all `rc.Extends` / `out.Extends` with `rc.ExtendsList` / `out.ExtendsList`. Specifically:

- `resolveChain`: check `len(rc.ExtendsList) == 0` instead of `rc.Extends == ""`
- The shadow check: `rc.ExtendsList` equal to name — check `len(rc.ExtendsList) == 1 && rc.ExtendsList[0] == name`
- `MergeProfiles`: `if len(child.ExtendsList) > 0 { out.ExtendsList = child.ExtendsList }`
- The final clearing: `out.ExtendsList = nil`

- [ ] **Step 5: Update catalog.go references to Extends**

Search for `.Extends` in catalog.go and update to `.ExtendsList` with appropriate length checks.

- [ ] **Step 6: Update scaffold/fragments.go validateFragment**

Update the check to use `ExtendsList`:
```go
func validateFragment(name string, p profile.RawProfile) error {
	if len(p.ExtendsList) != 0 || p.Image != "" || p.Build != nil || len(p.Command) > 0 || p.Version != 0 {
		return fmt.Errorf("fragment %q must not set extends/image/build/command/version", name)
	}
	return nil
}
```

- [ ] **Step 7: Update all other references to .Extends across the codebase**

Run: `grep -rn "\.Extends\b" --include="*.go" | grep -v _test`
Update each to `.ExtendsList` with appropriate checks (length vs empty string).

- [ ] **Step 8: Verify build and tests pass**

Run: `go build ./... && go test ./...`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add -A
git commit -m "feat: add ExtendsList type supporting string and list YAML forms"
```

---

## Task 4: Multi-extends resolution in resolveChain

**Files:**
- Modify: `internal/profile/merge.go` (resolveChain walks a list)
- Create: `internal/profile/merge_multi_test.go`

**Interfaces:**
- Produces: updated `resolveChain` that walks `ExtendsList` depth-first, deduplicates, detects cycles across all entries
- Consumes: `ExtendsList` from Task 3

- [ ] **Step 1: Write the failing tests**

Create `internal/profile/merge_multi_test.go`:

```go
package profile

import (
	"testing"
)

func TestMultiExtendsLeftToRight(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"base": {Profile: Profile{Version: 1, Image: "base:latest", Command: []string{"sh"}}},
		"ssh":  {Profile: Profile{Mounts: map[string]Mount{"~/.ssh": {Source: "~/.ssh"}}}},
		"npm":  {Profile: Profile{Caches: map[string]string{"npm": "~/.npm"}, Tools: map[string]string{"node": "latest"}}},
		"myprofile": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{"base", "ssh", "npm"},
		}},
	})
	resolved, err := ResolveProfile(cat, "myprofile")
	if err != nil {
		t.Fatal(err)
	}
	// base provides image+command
	if resolved.Image != "base:latest" {
		t.Errorf("Image = %q, want base:latest", resolved.Image)
	}
	// ssh provides mount
	if _, ok := resolved.Mounts["~/.ssh"]; !ok {
		t.Error("missing ~/.ssh mount from ssh fragment")
	}
	// npm provides cache+tool
	if _, ok := resolved.Caches["npm"]; !ok {
		t.Error("missing npm cache from npm fragment")
	}
	if resolved.Tools["node"] != "latest" {
		t.Error("missing node tool from npm fragment")
	}
}

func TestMultiExtendsLaterOverridesEarlier(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"a": {Profile: Profile{Image: "a:latest", Network: "none"}},
		"b": {Profile: Profile{Image: "b:latest"}},
		"myprofile": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{"a", "b"},
			Command:     []string{"sh"},
		}},
	})
	resolved, err := ResolveProfile(cat, "myprofile")
	if err != nil {
		t.Fatal(err)
	}
	// b comes after a, so b's image wins
	if resolved.Image != "b:latest" {
		t.Errorf("Image = %q, want b:latest (later extends wins)", resolved.Image)
	}
	// a's network is not overridden by b, so it survives
	if resolved.Network != "none" {
		t.Errorf("Network = %q, want none", resolved.Network)
	}
}

func TestMultiExtendsBodyWinsLast(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"a": {Profile: Profile{Image: "a:latest"}},
		"myprofile": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{"a"},
			Image:       "myimage:latest",
			Command:     []string{"sh"},
		}},
	})
	resolved, err := ResolveProfile(cat, "myprofile")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Image != "myimage:latest" {
		t.Errorf("Image = %q, want myimage:latest (body wins)", resolved.Image)
	}
}

func TestMultiExtendsWithNestedDepthFirst(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"c": {Profile: Profile{Image: "c:latest", Network: "none"}},
		"a": {Profile: Profile{ExtendsList: ExtendsList{"c"}, Network: "bridge"}},
		"b": {Profile: Profile{Image: "b:latest"}},
		"myprofile": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{"a", "b"},
			Command:     []string{"sh"},
		}},
	})
	resolved, err := ResolveProfile(cat, "myprofile")
	if err != nil {
		t.Fatal(err)
	}
	// Depth-first: c is resolved before a, so a's Network=bridge overrides c's Network=none.
	// Then b is merged. b has Image=b:latest, which overrides a's inherited c:latest.
	// But a didn't set image, so image comes from c → then b overrides.
	if resolved.Network != "bridge" {
		t.Errorf("Network = %q, want bridge (a overrides c)", resolved.Network)
	}
	if resolved.Image != "b:latest" {
		t.Errorf("Image = %q, want b:latest", resolved.Image)
	}
}

func TestMultiExtendsDuplicateIgnored(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"ssh": {Profile: Profile{Mounts: map[string]Mount{"~/.ssh": {Source: "~/.ssh"}}}},
		"myprofile": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{"ssh", "ssh"},
			Command:     []string{"sh"},
			Image:       "base:latest",
		}},
	})
	resolved, err := ResolveProfile(cat, "myprofile")
	if err != nil {
		t.Fatal(err)
	}
	// ssh is merged once, not twice — mount map has one entry
	if len(resolved.Mounts) != 1 {
		t.Errorf("Mounts = %d entries, want 1 (duplicate ignored)", len(resolved.Mounts))
	}
}

func TestMultiExtendsCycleRejected(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"a": {Profile: Profile{ExtendsList: ExtendsList{"b"}, Image: "a:latest", Command: []string{"sh"}}},
		"b": {Profile: Profile{ExtendsList: ExtendsList{"a"}}},
	})
	_, err := ResolveProfile(cat, "a")
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}

func TestSingleStringExtendsStillWorks(t *testing.T) {
	// This tests backward compat: a profile with a single string extends
	// should still resolve. ExtendsList normalizes string to [string].
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"base": {Profile: Profile{Version: 1, Image: "base:latest", Command: []string{"sh"}}},
		"child": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{"base"}, // normalized from string
		}},
	})
	resolved, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Image != "base:latest" {
		t.Errorf("Image = %q, want base:latest", resolved.Image)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/profile/ -run TestMultiExtends -v`
Expected: FAIL — `resolveChain` doesn't walk a list yet.

- [ ] **Step 3: Rewrite resolveChain for multi-extends**

In `internal/profile/merge.go`, rewrite `resolveChain`:

```go
func resolveChain(cat Catalog, name string, seen map[string]bool) (RawProfile, error) {
	rc, ok := cat.Get(name)
	if !ok {
		return RawProfile{}, ProfileError{Message: "profile not found: " + name}
	}
	if seen[name] {
		return RawProfile{}, ProfileError{Path: rc.Path, Message: "extends cycle detected at: " + name}
	}

	if len(rc.ExtendsList) == 0 {
		return rc, nil
	}

	// Special case: a user shadow that extends the built-in of the same name.
	if len(rc.ExtendsList) == 1 && rc.ExtendsList[0] == name && cat.IsUserShadow(name) {
		builtin, ok := cat.GetBuiltin(name)
		if !ok {
			return RawProfile{}, ProfileError{Path: rc.Path, Message: "extends cycle detected at: " + name}
		}
		parent, err := resolveBuiltinChain(cat, builtin, seen)
		if err != nil {
			return RawProfile{}, err
		}
		merged := MergeProfiles(parent, rc)
		merged.Path = rc.Path
		return merged, nil
	}

	seen[name] = true
	defer delete(seen, name)

	// Resolve each extends entry depth-first, merge left-to-right.
	// Duplicates are ignored after first resolution.
	merged := RawProfile{}
	resolved := map[string]bool{}
	for _, parentName := range rc.ExtendsList {
		if resolved[parentName] {
			continue
		}
		resolved[parentName] = true
		parent, err := resolveChain(cat, parentName, seen)
		if err != nil {
			return RawProfile{}, err
		}
		merged = MergeProfiles(merged, parent)
	}

	// Merge the profile's own body last (wins over all extends).
	merged = MergeProfiles(merged, rc)
	merged.Path = rc.Path
	return merged, nil
}
```

Update `resolveBuiltinChain` similarly to use `ExtendsList`:

```go
func resolveBuiltinChain(cat Catalog, rc RawProfile, inheritedSeen map[string]bool) (RawProfile, error) {
	if len(rc.ExtendsList) == 0 {
		return rc, nil
	}
	name := rc.ExtendsList[0]
	if inheritedSeen[name] {
		return RawProfile{}, ProfileError{Path: rc.Path, Message: "extends cycle detected at: " + name}
	}
	parent, ok := cat.GetBuiltin(name)
	if !ok {
		return RawProfile{}, ProfileError{Path: rc.Path, Message: "built-in profile not found: " + name}
	}
	seen := make(map[string]bool, len(inheritedSeen)+1)
	for k := range inheritedSeen {
		seen[k] = true
	}
	seen[name] = true
	resolved, err := resolveBuiltinChain(cat, parent, seen)
	if err != nil {
		return RawProfile{}, err
	}
	merged := MergeProfiles(resolved, rc)
	merged.Path = rc.Path
	return merged, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/profile/ -run TestMultiExtends -v`
Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `go test ./...`
Expected: PASS (existing single-extends tests should still pass)

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat: multi-extends resolution with depth-first merge and dedup"
```

---

## Task 5: Load fragments into catalog with collision detection

**Files:**
- Modify: `internal/profile/catalog.go` (LoadProfiles loads fragments too, rejects collisions)
- Modify: `internal/scaffold/fragments.go` (may be simplified — fragments come from catalog now)
- Create: `internal/profile/catalog_fragments_test.go`

**Interfaces:**
- Produces: `Catalog` now contains fragments; `LoadProfiles` loads `~/.config/toolpod/fragments/` alongside profiles; name collisions are rejected
- Consumes: `catalog.Fragments` from Task 1

- [ ] **Step 1: Write the failing test**

Create `internal/profile/catalog_fragments_test.go`:

```go
package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProfilesIncludesFragments(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	// "ssh" is a built-in fragment, not a profile.
	// It should be resolvable via Get.
	rc, ok := cat.Get("ssh")
	if !ok {
		t.Fatal("fragment 'ssh' not found in catalog")
	}
	if rc.Path == "" {
		t.Error("fragment should have a path")
	}
}

func TestFragmentProfileNameCollisionRejected(t *testing.T) {
	dir := t.TempDir()
	// Create a user profile named "ssh" that collides with the built-in fragment.
	if err := os.WriteFile(filepath.Join(dir, "ssh.yaml"), []byte("version: 1\nimage: x\ncommand: [sh]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProfiles(dir)
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
}

func TestUserFragmentsLoaded(t *testing.T) {
	dir := t.TempDir()
	fragDir := filepath.Join(dir, "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a user fragment
	if err := os.WriteFile(filepath.Join(fragDir, "myfrag.yaml"), []byte("mounts:\n  /data:\n    source: /data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	rc, ok := cat.Get("myfrag")
	if !ok {
		t.Fatal("user fragment 'myfrag' not found")
	}
	if _, ok := rc.Mounts["/data"]; !ok {
		t.Error("user fragment mount missing")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/profile/ -run "TestLoadProfilesIncludesFragments|TestFragmentProfileNameCollisionRejected|TestUserFragmentsLoaded" -v`
Expected: FAIL — fragments not loaded into catalog yet.

- [ ] **Step 3: Modify LoadProfiles to load fragments**

In `internal/profile/catalog.go`, modify `LoadProfiles`:

```go
func LoadProfiles(userDir string) (Catalog, error) {
	builtins := map[string]RawProfile{}
	if err := loadBuiltins(builtins); err != nil {
		return Catalog{}, err
	}

	entries := make(map[string]RawProfile, len(builtins))
	for k, v := range builtins {
		entries[k] = v
	}

	// Load built-in fragments into the same namespace.
	builtinFragments := map[string]RawProfile{}
	if err := loadBuiltinFragments(builtinFragments); err != nil {
		return Catalog{}, err
	}
	for name, frag := range builtinFragments {
		if _, exists := entries[name]; exists {
			return Catalog{}, ProfileError{Path: frag.Path, Message: "name collision: fragment and profile share name " + name}
		}
		entries[name] = frag
	}

	if userDir != "" {
		if err := loadUserDir(userDir, entries); err != nil {
			return Catalog{}, err
		}
		// Load user fragments from <userDir>/fragments/
		userFragDir := filepath.Join(userDir, "fragments")
		if err := loadUserFragments(userFragDir, entries); err != nil {
			return Catalog{}, err
		}
	}

	return Catalog{entries: entries, builtins: builtins}, nil
}

func loadBuiltinFragments(entries map[string]RawProfile) error {
	root := "fragments"
	return fs.WalkDir(catalog.Fragments, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		data, err := catalog.Fragments.ReadFile(path)
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		rc, err := parseRaw(data, "built-in-fragment:"+path)
		if err != nil {
			return err
		}
		if err := validateFragmentName(name, rc); err != nil {
			return err
		}
		if _, exists := entries[name]; exists {
			return ProfileError{Path: rc.Path, Message: "name collision: fragment and profile share name " + name}
		}
		entries[name] = rc
		return nil
	})
}

func loadUserFragments(dir string, entries map[string]RawProfile) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
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
		if err := validateFragmentName(name, rc); err != nil {
			return err
		}
		if _, exists := entries[name]; exists {
			return ProfileError{Path: rc.Path, Message: "name collision: fragment and profile share name " + name}
		}
		entries[name] = rc
		return nil
	})
}

func validateFragmentName(name string, rc RawProfile) error {
	if rc.Extends != "" || len(rc.ExtendsList) != 0 || rc.Image != "" || rc.Build != nil || len(rc.Command) > 0 || rc.Version != 0 {
		return ProfileError{Path: rc.Path, Message: "fragment " + name + " must not set extends/image/build/command/version"}
	}
	return nil
}
```

Note: move `validateFragment` logic from `scaffold/fragments.go` into `profile/catalog.go` as `validateFragmentName` (unexported), since the catalog now owns fragment validation. Keep `scaffold.validateFragment` as a wrapper or remove it if unused.

- [ ] **Step 4: Update Catalog.Get to find fragments**

`Catalog.Get` already searches `entries`, which now includes fragments. No change needed — verify this works.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/profile/ -run "TestLoadProfilesIncludesFragments|TestFragmentProfileNameCollisionRejected|TestUserFragmentsLoaded" -v`
Expected: PASS

- [ ] **Step 6: Run full test suite**

Run: `go test ./...`
Expected: PASS — may need to fix tests that assumed fragments weren't in the catalog.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat: load fragments into catalog with collision detection"
```

---

## Task 6: init writes extends list instead of inlining

**Files:**
- Modify: `internal/scaffold/scaffold.go` (generate function)
- Modify: `internal/scaffold/scaffold_test.go` (update expectations)

**Interfaces:**
- Produces: `generate()` writes `extends: [profile, frag1, frag2]` instead of inlined content
- Consumes: `ExtendsList` from Task 3, `Fragments()` from Task 2

- [ ] **Step 1: Write the failing test**

Add to `internal/scaffold/scaffold_test.go`:

```go
func TestGenerateWritesExtendsList(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Profile:    "opencode",
		Fragments:  []string{"npm", "go"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Read the generated file
	data, err := os.ReadFile(filepath.Join(dir, "opencode.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	// Should contain extends list, not inlined caches/tools
	if !strings.Contains(content, "extends:") {
		t.Error("generated file should contain extends:")
	}
	if !strings.Contains(content, "npm") {
		t.Error("generated file should reference npm fragment")
	}
	if !strings.Contains(content, "go") {
		t.Error("generated file should reference go fragment")
	}
	// Should NOT contain inlined cache paths from npm fragment
	if strings.Contains(content, "~/.npm") {
		t.Error("generated file should not inline ~/.npm cache (should be live-linked via extends)")
	}
	// Should NOT contain inlined tool entries from fragments
	if strings.Contains(content, "node: latest") {
		t.Error("generated file should not inline node tool (should be live-linked via extends)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/scaffold/ -run TestGenerateWritesExtendsList -v`
Expected: FAIL — generate still inlines.

- [ ] **Step 3: Rewrite generate()**

In `internal/scaffold/scaffold.go`, replace `generate`:

```go
func generate(profileName string, selectedFragments []string) (string, error) {
	extends := make([]string, 0, len(selectedFragments)+1)
	extends = append(extends, profileName)
	extends = append(extends, selectedFragments...)

	p := profile.Profile{
		Version:     1,
		ExtendsList: profile.ExtendsList(extends),
	}

	data, err := yaml.Marshal(p)
	if err != nil {
		return "", err
	}

	header := headerMarker + "\n" +
		fmt.Sprintf("# This user profile extends the built-in %q profile.\n", profileName) +
		"# Remove this file to restore the built-in default.\n\n"

	return header + string(data), nil
}
```

Note: `yaml.Marshal` on `ExtendsList` should produce a YAML list. Verify the marshaler from Task 3 produces `- opencode\n- npm\n- go\n`. If it doesn't, add a `MarshalYAML` method to `ExtendsList` that delegates to `[]string` marshaling.

- [ ] **Step 4: Remove checkFragmentFiles call (no longer relevant)**

In `scaffold.go`, remove or comment out the `checkFragmentFiles(selectedFragments, stderr)` call — fragments are no longer inlined, so there's nothing to check at init time. The optional-mount skip happens at launch.

- [ ] **Step 5: Update existing scaffold tests**

Tests that check for inlined content (mounts, caches, tools) in generated files need to be updated to check for `extends: [...]` instead. Search for tests that assert on `~/.npm`, `node: latest`, etc. in generated output and update them.

Key tests to update:
- `TestPresetMergeProducesCorrectResult` → `TestFragmentExtendsList` — assert extends list, not inlined content
- `TestNoPresetsProducesJustExtends` → `TestNoFragmentsProducesJustExtends` — still asserts just extends
- Any test checking `Caches` or `Tools` in generated output

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/scaffold/ -v`
Expected: PASS

- [ ] **Step 7: Run full test suite**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "feat: init writes extends list instead of inlining fragments"
```

---

## Task 7: init summary and editor review flow

**Files:**
- Modify: `internal/scaffold/scaffold.go` (add summary, editor prompt)
- Modify: `internal/scaffold/scaffold_test.go` (test summary and prompt)

**Interfaces:**
- Produces: summary output and optional editor review when the resolved profile has mounts or env vars
- Consumes: `profile.ResolveProfile` to get resolved profile for summary

- [ ] **Step 1: Write the failing tests**

Add to `internal/scaffold/scaffold_test.go`:

```go
func TestInitSummaryWithMounts(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	// Non-interactive, no TTY — summary prints but no editor prompt
	err := Run(context.Background(), Options{
		Profile:    "opencode",
		Fragments:  []string{"ssh"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	output := stdout.String() + stderr.String()
	if !strings.Contains(output, "mounts") {
		t.Errorf("summary should mention mounts, got: %s", output)
	}
	if !strings.Contains(output, "~/.ssh") {
		t.Errorf("summary should list ~/.ssh mount, got: %s", output)
	}
}

func TestInitNoEditorPromptWithoutMounts(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	// npm fragment has only caches+tools, no mounts
	err := Run(context.Background(), Options{
		Profile:    "shell",
		Fragments:  []string{"npm"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	output := stdout.String() + stderr.String()
	if strings.Contains(output, "Review") {
		t.Errorf("should not prompt for review without mounts, got: %s", output)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/scaffold/ -run "TestInitSummaryWithMounts|TestInitNoEditorPromptWithoutMounts" -v`
Expected: FAIL — no summary generation yet.

- [ ] **Step 3: Add summary generation**

In `internal/scaffold/scaffold.go`, add a function:

```go
func printSummary(stdout io.Writer, profileName string, fragments []string, resolved profile.Profile) {
	fmt.Fprintf(stdout, "Profile: %s\n", profileName)
	if len(fragments) > 0 {
		fmt.Fprintf(stdout, "Fragments: %s\n", strings.Join(fragments, ", "))
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Container access:")

	hasMounts := len(resolved.Mounts) > 0
	hasEnv := len(resolved.Env) > 0

	for target := range resolved.Mounts {
		fmt.Fprintf(stdout, "  • mounts %s\n", target)
	}
	for k := range resolved.Env {
		fmt.Fprintf(stdout, "  • passes %s\n", k)
	}
	for name := range resolved.Tools {
		fmt.Fprintf(stdout, "  • installs %s\n", name)
	}
	for name := range resolved.Caches {
		fmt.Fprintf(stdout, "  • caches %s\n", name)
	}

	if !hasMounts && !hasEnv {
		return
	}
	_ = hasMounts
	_ = hasEnv
}
```

- [ ] **Step 4: Add editor review flow**

In `scaffold.go`, after generating content and before writing the file, add:

```go
	// Resolve the profile to generate a summary.
	mergedCat, err := profile.LoadProfiles(userDir)
	if err != nil {
		return fmt.Errorf("loading profiles for summary: %w", err)
	}
	// Build a temp catalog with the new profile to resolve it.
	tmpCat := mergedCat
	// We need to resolve what the profile WILL look like.
	// Parse the generated content as a RawProfile and resolve it.
	tmpDir2, err := os.MkdirTemp("", "toolpod-init-summary-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir2)
	if err := os.WriteFile(filepath.Join(tmpDir2, profileName+".yaml"), []byte(content), 0o644); err != nil {
		return err
	}
	sumCat, err := profile.LoadProfiles(tmpDir2)
	if err != nil {
		return fmt.Errorf("loading temp profile for summary: %w", err)
	}
	resolved, _ := profile.ResolveProfile(sumCat, profileName)

	printSummary(stdout, profileName, selectedFragments, resolved)

	// If the profile grants host access (mounts or env), prompt for review.
	if len(resolved.Mounts) > 0 || len(resolved.Env) > 0 {
		if interactive {
			fmt.Fprintf(stdout, "\nReview the resolved profile? [y/N]: ")
			line, _ := reader.ReadString('\n')
			line = strings.TrimSpace(strings.ToLower(line))
			if line == "y" || line == "yes" {
				if err := openEditorWithResolved(resolved, stdout); err != nil {
					fmt.Fprintf(stderr, "warning: could not open editor: %v\n", err)
				} else {
					fmt.Fprintf(stdout, "Proceed with generating this profile? [y/N]: ")
					line, _ = reader.ReadString('\n')
					line = strings.TrimSpace(strings.ToLower(line))
					if line != "y" && line != "yes" {
						fmt.Fprintln(stdout, "aborted")
						return nil
					}
				}
			}
		}
	}
```

Add the editor helper:

```go
func openEditorWithResolved(resolved profile.Profile, stdout io.Writer) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	data, err := yaml.Marshal(resolved)
	if err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp("", "toolpod-resolved-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return err
	}
	tmpFile.Close()

	cmd := exec.Command(editor, tmpFile.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
```

Add `"os/exec"` to imports.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/scaffold/ -run "TestInitSummaryWithMounts|TestInitNoEditorPromptWithoutMounts" -v`
Expected: PASS

- [ ] **Step 6: Run full test suite**

Run: `go test ./...`
Expected: PASS — fix any tests that are affected by the new summary output.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat: init prints summary and prompts editor review for host access"
```

---

## Task 8: `profile show` subcommand

**Files:**
- Modify: `cmd/toolpod/cli.go` (add Profile command struct with Show subcommand)
- Create: `cmd/toolpod/profile_test.go`

**Interfaces:**
- Produces: `toolpod profile show <name> [--resolved]` command
- Consumes: `profile.LoadProfiles`, `profile.ResolveProfile` from earlier tasks

- [ ] **Step 1: Write the failing test**

Create `cmd/toolpod/profile_test.go`:

```go
package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestProfileShowBuiltIn(t *testing.T) {
	out, err := runToolpod(t, "profile", "show", "shell")
	if err != nil {
		t.Fatalf("profile show shell: %v\n%s", err, out)
	}
	if !strings.Contains(out, "command:") {
		t.Errorf("output should contain profile content, got: %s", out)
	}
}

func TestProfileShowResolved(t *testing.T) {
	out, err := runToolpod(t, "profile", "show", "--resolved", "shell")
	if err != nil {
		t.Fatalf("profile show --resolved shell: %v\n%s", err, out)
	}
	// Resolved output should contain the merged image from the extends chain
	if !strings.Contains(out, "image:") {
		t.Errorf("resolved output should contain image, got: %s", out)
	}
}

func TestProfileShowNonexistent(t *testing.T) {
	out, _ := runToolpod(t, "profile", "show", "nonexistent")
	if !strings.Contains(out, "not found") {
		t.Errorf("should error for nonexistent profile, got: %s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/toolpod/ -run TestProfileShow -v`
Expected: FAIL — `profile show` command doesn't exist yet.

- [ ] **Step 3: Add Profile command to cli.go**

In `cmd/toolpod/cli.go`, add new types:

```go
type ProfileShowCmd struct {
	Name    string `arg:"" help:"Profile name to show."`
	Resolved bool   `help:"Inline all extends and show the fully merged profile."`
}

type ProfileEditCmd struct {
	Name string `arg:"" help:"Profile name to edit."`
}

type ProfileListCmd struct {
}

type ProfileCmd struct {
	Show ProfileShowCmd `cmd:"" help:"Print a profile (use --resolved to inline extends)."`
	Edit ProfileEditCmd `cmd:"" help:"Open the user profile in $EDITOR."`
	List ProfileListCmd `cmd:"" help:"List all profiles."`
}
```

Add `Profile ProfileCmd` to the `CLI` struct.

Implement `Run` methods:

```go
func (s *ProfileShowCmd) Run() error {
	userDir := profile.DefaultProfileDir()
	cat, err := profile.LoadProfiles(userDir)
	if err != nil {
		return fmt.Errorf("loading profiles: %w", err)
	}

	if s.Resolved {
		resolved, err := profile.ResolveProfile(cat, s.Name)
		if err != nil {
			return err
		}
		data, err := yaml.Marshal(resolved)
		if err != nil {
			return err
		}
		fmt.Print(string(data))
		return nil
	}

	rc, ok := cat.Get(s.Name)
	if !ok {
		return fmt.Errorf("profile not found: %s", s.Name)
	}
	data, err := yaml.Marshal(rc.Profile)
	if err != nil {
		return err
	}
	fmt.Print(string(data))
	return nil
}
```

Add imports: `"github.com/jgillich/toolpod/internal/profile"`, `"gopkg.in/yaml.v3"`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/toolpod/ -run TestProfileShow -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat: add 'profile show' subcommand with --resolved flag"
```

---

## Task 9: `profile edit` and `profile list` subcommands

**Files:**
- Modify: `cmd/toolpod/cli.go` (add edit and list Run methods)
- Modify: `cmd/toolpod/profile_test.go` (add tests)

**Interfaces:**
- Produces: `toolpod profile edit <name>` and `toolpod profile list`

- [ ] **Step 1: Write the failing tests**

Add to `cmd/toolpod/profile_test.go`:

```go
func TestProfileList(t *testing.T) {
	out, err := runToolpod(t, "profile", "list")
	if err != nil {
		t.Fatalf("profile list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "shell") {
		t.Errorf("list should include shell, got: %s", out)
	}
	if !strings.Contains(out, "built-in") {
		t.Errorf("list should mark built-in profiles, got: %s", out)
	}
}

func TestProfileEditBuiltInErrors(t *testing.T) {
	out, _ := runToolpod(t, "profile", "edit", "shell")
	if !strings.Contains(out, "built-in") {
		t.Errorf("should refuse to edit built-in, got: %s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/toolpod/ -run "TestProfileList|TestProfileEditBuiltInErrors" -v`
Expected: FAIL — not implemented.

- [ ] **Step 3: Implement profile edit**

```go
func (e *ProfileEditCmd) Run() error {
	userDir := profile.DefaultProfileDir()
	if userDir == "" {
		return fmt.Errorf("cannot determine profile directory")
	}
	targetPath := filepath.Join(userDir, e.Name+".yaml")
	if _, err := os.Stat(targetPath); err != nil {
		// Check if it's a built-in
		cat, _ := profile.LoadProfiles("")
		if _, ok := cat.Get(e.Name); ok {
			return fmt.Errorf("this is a built-in profile. Run 'toolpod init %s' to create a user override.", e.Name)
		}
		return fmt.Errorf("profile not found: %s", e.Name)
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, targetPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
```

Add imports: `"os/exec"`, `"path/filepath"`.

- [ ] **Step 4: Implement profile list**

```go
func (l *ProfileListCmd) Run() error {
	userDir := profile.DefaultProfileDir()
	cat, err := profile.LoadProfiles(userDir)
	if err != nil {
		return fmt.Errorf("loading profiles: %w", err)
	}
	for _, name := range cat.Names() {
		label := "built-in"
		if cat.IsUserShadow(name) {
			label = "user"
		}
		fmt.Printf("%-20s %s\n", name, label)
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/toolpod/ -run "TestProfileList|TestProfileEditBuiltInErrors" -v`
Expected: PASS

- [ ] **Step 6: Run full test suite**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat: add 'profile edit' and 'profile list' subcommands"
```

---

## Task 10: Update --presets flag alias and CLI help text

**Files:**
- Modify: `cmd/toolpod/cli.go` (add `--presets` as hidden alias, update help text)
- Test: manual `--help` verification

**Interfaces:**
- Produces: `--fragments` is the primary flag, `--presets` is a hidden alias

- [ ] **Step 1: Add aliases to the Fragments field in InitCmd**

In `cmd/toolpod/cli.go`, update the `Fragments` field tag:

```go
Fragments []string `sep:"," help:"Comma-separated fragment names (${fragments})." aliases:"presets"`
```

- [ ] **Step 2: Update the Init command help text**

Change `Init InitCmd` tag from "presets" to "fragments":
```go
Init InitCmd `cmd:"" help:"Generate a user profile override with fragments."`
```

- [ ] **Step 3: Verify help output**

Run: `go build -o /tmp/toolpod ./cmd/toolpod && /tmp/toolpod init --help`
Expected: shows `--fragments` in help, `--presets` works as alias.

Run: `/tmp/toolpod init --presets npm,go --dry-run shell`
Expected: works (alias accepted).

- [ ] **Step 4: Commit**

```bash
git add cmd/toolpod/cli.go
git commit -m "feat: rename --presets to --fragments with backward-compat alias"
```

---

## Task 11: Update README and doctor messages

**Files:**
- Modify: `README.md`
- Modify: `internal/doctor/checks.go`

- [ ] **Step 1: Update README**

- Rename "preset" → "fragment" throughout
- Document multi-extends syntax (`extends: [opencode, ssh, npm]`)
- Document `profile show --resolved`, `profile edit`, `profile list` commands
- Update the `extends` field description to mention list support
- Update the init section to describe the new summary + review flow
- Add the fragment design invariant: "Fragments are small, composable building blocks representing a single concern."
- Update the "Available presets" table → "Available fragments"

- [ ] **Step 2: Update doctor check messages**

In `internal/doctor/checks.go`, update all "preset" references to "fragment" and update `--presets` to `--fragments` in hint strings.

- [ ] **Step 3: Verify build and tests**

Run: `go build ./... && go test ./...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "docs: update README and doctor messages for fragments and multi-extends"
```

---

## Task 12: Final verification and edge cases

**Files:**
- Various test files

- [ ] **Step 1: Test backward compat — single string extends**

Verify that an existing profile with `extends: opencode` (string form) still works:

```bash
go test ./internal/profile/ -run TestSingleStringExtendsStillWorks -v
```

- [ ] **Step 2: Test backward compat — inlined profile from old init**

Create a test that simulates an old-style inlined profile (mounts/caches in the body, no extends list) and verify it resolves:

```go
func TestOldInlinedProfileStillResolves(t *testing.T) {
	dir := t.TempDir()
	// Simulate an old init output: extends as string + inlined mounts
	content := "version: 1\nextends: opencode\nmounts:\n  ~/.ssh:\n    source: ~/.ssh\n    read_only: true\n"
	if err := os.WriteFile(filepath.Join(dir, "myprofile.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := profile.LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := profile.ResolveProfile(cat, "myprofile")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resolved.Mounts["~/.ssh"]; !ok {
		t.Error("inlined mount from old profile should survive")
	}
}
```

- [ ] **Step 3: Run full test suite and vet**

Run: `go vet ./... && go test ./...`
Expected: PASS

- [ ] **Step 4: Manual smoke test**

```bash
go build -o /tmp/toolpod ./cmd/toolpod
/tmp/toolpod --help
/tmp/toolpod profile list
/tmp/toolpod profile show shell
/tmp/toolpod profile show --resolved shell
/tmp/toolpod init --dry-run shell --fragments npm,ssh
```

- [ ] **Step 5: Commit any fixes**

```bash
git add -A
git commit -m "test: verify backward compat and edge cases for multi-extends"
```