# Hierarchical Names Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make profile/fragment names hierarchical paths (`lang/go`, `services/podman`, `lang/js/node`) derived from their directory location, with `core` reserved as the only namespace prefix for built-ins.

**Architecture:** The relative path of a YAML file under the profiles/fragments root becomes its `Name` (multi-segment). User entries keep `Namespace: ""` (FullName = the path); built-ins get `Namespace: "core"` (FullName = `core/` + path). `ParseRef` treats any slash-string that doesn't match a registered namespace prefix as an unqualified hierarchical name; `ResolveRef` keeps its single rule (try FullName, else `core/` + FullName). The embedded built-in fragment catalog is restructured into subfolders.

**Tech Stack:** Go 1.25, `embed.FS`, `gopkg.in/yaml.v3`, cobra.

## Global Constraints

- Build: `go build ./...`; lint: `go vet ./...`; tests: `go test ./...`.
- Per-segment name charset (unchanged): `^[a-zA-Z0-9][a-zA-Z0-9._-]*$`, and no segment may be `..` (`profileNameRe` in `internal/profile/catalog.go:657`).
- `core` is the only registered namespace prefix today; a user file whose *first* path segment is `core` is rejected (strict) / warned-and-skipped (tolerant).
- A bare single-segment name equal to a subcommand (config, doctor, help, version, completion, prune, init) stays reserved (`reservedNames` in `validate.go:21`).
- No new comments unless the code doesn't make something apparent. Conventional commit format.
- `tpd` subcommands resolve via `internal/profile/ref.go` `ParseRef`/`ResolveRef`; do not add fallback paths there beyond the existing `core/`+FullName rule.

---

### Task 1: ParseRef accepts multi-segment local names and hierarchical fallback

**Files:**
- Modify: `internal/profile/ref.go:20-52`
- Test: `internal/profile/ref_test.go`

**Interfaces:**
- Consumes: nothing (self-contained).
- Produces: `ParseRef(s string, namespaces map[string]bool) (Ref, error)` — `core/lang/go` → `Ref{Namespace: "core", Name: "lang/go"}`; `lang/go` (no registered prefix) → `Ref{Namespace: "", Name: "lang/go"}`; `core/` (empty local) still errors; empty string still errors.

- [ ] **Step 1: Replace the failing tests**

In `internal/profile/ref_test.go`, replace `TestParseRefRejectsMultiSegmentLocalName` and `TestParseRefRejectsUnknownNamespace`:```go
func TestParseRefMultiSegmentLocalName(t *testing.T) {
	r, err := ParseRef("core/lang/go", map[string]bool{"": true, "core": true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Namespace != "core" || r.Name != "lang/go" {
		t.Errorf("got %+v, want {Namespace: \"core\", Name: \"lang/go\"}", r)
	}
}

func TestParseRefHierarchicalFallback(t *testing.T) {
	// A slash string that matches no registered prefix is an unqualified
	// hierarchical name, not an error.
	for _, s := range []string{"lang/go", "services/podman", "corexy/foo"} {
		r, err := ParseRef(s, map[string]bool{"": true, "core": true})
		if err != nil {
			t.Fatalf("ParseRef(%q): %v", s, err)
		}
		if r.Namespace != "" || r.Name != s {
			t.Errorf("ParseRef(%q) = %+v, want {Namespace: \"\", Name: %q}", s, r, s)
		}
	}
}

func TestParseRefLongestPrefixMultiSegmentLocal(t *testing.T) {
	// Longest registered prefix wins; remainder may be multi-segment.
	ns := map[string]bool{"": true, "core": true, "github.com/user/project": true}
	r, err := ParseRef("github.com/user/project/lang/ruby", ns)
	if err != nil {
		t.Fatal(err)
	}
	if r.Namespace != "github.com/user/project" || r.Name != "lang/ruby" {
		t.Errorf("got %+v, want {Namespace: \"github.com/user/project\", Name: \"lang/ruby\"}", r)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/profile/ -run 'TestParseRef' -v`
Expected: FAIL — `TestParseRefMultiSegmentLocalName` gets the "must be a single segment" error; `TestParseRefHierarchicalFallback` gets "unknown namespace in extends".

Also update `TestExtendsListResolveRejectsUnknownNamespace` in `internal/profile/extends_test.go:61-67` (it breaks under the new fallback). Replace it with:

```go
func TestExtendsListResolveHierarchicalFallback(t *testing.T) {
	ns := map[string]bool{"": true, "core": true}
	el := ExtendsList{Raw: []string{"corexy/foo"}}
	if err := el.Resolve(ns); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := []Ref{{Namespace: "", Name: "corexy/foo"}}
	if !reflect.DeepEqual(el.Resolved, want) {
		t.Errorf("Resolved = %+v, want %+v", el.Resolved, want)
	}
}
```

- [ ] **Step 3: Implement**

In `internal/profile/ref.go`, modify `ParseRef` (the loop over `prefixes` and the trailing error):

```go
	for _, ns := range prefixes {
		if strings.HasPrefix(s, ns+"/") {
			local := s[len(ns)+1:]
			if local == "" {
				return Ref{}, fmt.Errorf("empty local name in extends: %s", s)
			}
			// The local name may be multi-segment (lang/go, lang/js/node);
			// the namespace is a registered prefix, everything after it is name.
			return Ref{Namespace: ns, Name: local}, nil
		}
	}
	// No registered prefix matches, so the whole string is an unqualified
	// hierarchical name. This is how user namespaces (lang/go,
	// services/podman) parse: their directory is not a registered prefix.
	return Ref{Namespace: "", Name: s}, nil
```

Update the doc comment on `ParseRef` to describe the hierarchical fallback.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/profile/ -run 'TestParseRef' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/profile/ref.go internal/profile/ref_test.go internal/profile/extends_test.go
git commit -m "feat(profile): allow multi-segment and hierarchical names in ParseRef"
```

---

### Task 2: Loaders derive hierarchical names; reserve `core` prefix

**Files:**
- Modify: `internal/profile/catalog.go` (loaders at :252-341, :343-412, :551-576, :578-645)
- Test: `internal/profile/catalog_test.go`

**Interfaces:**
- Consumes: Task 1 (not strictly, but same name model).
- Produces:
  - `nameFromPath(root, path string) string` — `("fragments", "fragments/lang/go.yaml")` → `"lang/go"`.
  - `validateHierarchicalName(name, path string, namespaces map[string]bool) error` — rejects a segment failing `profileNameRe`/`..`, and a first segment equal to a registered namespace (`core`).
  - `loadUserDir`, `loadUserFragments`, `loadBuiltins`, `loadBuiltinFragments` (and `Tolerant` variants) stamp `Name` as the hierarchical path; user loaders reject a reserved first segment.

- [ ] **Step 1: Write the failing tests**

Append to `internal/profile/catalog_test.go`:

```go
func TestLoadProfilesUserSubfolderNamespace(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "lang"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lang", "go.yaml"),
		[]byte("version: 1\ncommand: [\"go\"]\nimage: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	rc, ok := cat.Get("lang/go")
	if !ok {
		t.Fatal("user lang/go not keyed under hierarchical FullName")
	}
	if rc.Namespace != "" || rc.Name != "lang/go" {
		t.Errorf("identity = {%q, %q}, want {\"\", \"lang/go\"}", rc.Namespace, rc.Name)
	}
}

func TestLoadProfilesUserNestedSubfolder(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lang", "js", "node.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("version: 1\ncommand: [\"node\"]\nimage: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rc, ok := cat.Get("lang/js/node"); !ok || rc.Name != "lang/js/node" {
		t.Errorf("lang/js/node = %+v, want present with Name lang/js/node", rc)
	}
}

func TestLoadProfilesRejectsReservedNamespacePrefix(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "core"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "core", "go.yaml"),
		[]byte("version: 1\ncommand: [\"go\"]\nimage: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProfiles(dir)
	if err == nil {
		t.Fatal("expected reserved-namespace error, got nil")
	}
	if !strings.Contains(err.Error(), "core is a reserved namespace prefix") {
		t.Fatalf("error = %v, want 'core is a reserved namespace prefix'", err)
	}
}

func TestLoadProfilesTolerantSkipsReservedNamespacePrefix(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "core"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "core", "go.yaml"),
		[]byte("version: 1\ncommand: [\"go\"]\nimage: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var warns []string
	cat, err := LoadProfilesTolerant(dir, func(w string) { warns = append(warns, w) })
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cat.Get("core/go"); ok {
		t.Error("reserved-namespace file must be skipped in tolerant mode")
	}
	if len(warns) == 0 || !strings.Contains(warns[0], "reserved namespace") {
		t.Errorf("expected a reserved-namespace warning, got %v", warns)
	}
}
```

(`TestLoadProfilesUserShadowsCoreHierarchical` — user `fragments/lang/go.yaml` shadowing the built-in `core/lang/go` — is added in Task 4 Step 5, because the built-in `core/lang/go` only exists after the catalog is restructured.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/profile/ -run 'TestLoadProfilesUserSubfolderNamespace|TestLoadProfilesUserNestedSubfolder|TestLoadProfilesRejectsReservedNamespacePrefix|TestLoadProfilesTolerantSkipsReservedNamespacePrefix' -v`
Expected: FAIL — `lang/go` resolves to key `go` (flat basename) or the reserved file loads without error.

- [ ] **Step 3: Implement**

In `internal/profile/catalog.go`:

Add helpers next to `validateFilenameName` (near :661):

```go
// nameFromPath derives the hierarchical name for a YAML file from its path
// relative to the catalog root: root/lang/go.yaml -> "lang/go".
func nameFromPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = filepath.Base(path)
	}
	name := strings.TrimSuffix(filepath.ToSlash(rel), ".yaml")
	return name
}

// validateHierarchicalName checks every segment of a file-derived name and
// rejects a first segment that names a registered namespace (core), which
// would collide with built-in entries.
func validateHierarchicalName(name, path string, namespaces map[string]bool) error {
	for _, seg := range strings.Split(name, "/") {
		if !profileNameRe.MatchString(seg) || strings.Contains(seg, "..") {
			return ProfileError{Path: path, Message: "invalid profile name derived from filename: " + name}
		}
	}
	if strings.Contains(name, "/") {
		first := strings.SplitN(name, "/", 2)[0]
		if namespaces[first] {
			return ProfileError{Path: path, Message: first + " is a reserved namespace prefix"}
		}
	}
	return nil
}
```

In `loadUserDir` (:371), `loadUserDirTolerant` (:252), `loadUserFragments` (:610), `loadUserFragmentsTolerant` (:300): replace

```go
name := strings.TrimSuffix(filepath.Base(path), ".yaml")
if err := validateFilenameName(name, path); err != nil { ... }
```

with

```go
name := nameFromPath(dir, path)
if err := validateHierarchicalName(name, path, builtinNamespaces); err != nil { ... }
```

where the strict variants `return err` and the tolerant variants `warn(path+": "+err.Error()); return nil`. In the tolerant variants also use `nameFromPath(dir, path)`.

In `loadBuiltins` (:343) and `loadBuiltinFragments` (:578): replace

```go
name := strings.TrimSuffix(filepath.Base(path), ".yaml")
```

with

```go
name := nameFromPath(root, path)
```

and keep stamping `rc.Namespace = "core"`.

In `LoadFragments` (:551): replace

```go
name := strings.TrimSuffix(filepath.Base(path), ".yaml")
```

with

```go
name := nameFromPath(root, path)
```

so the scaffold fragment picker keys by hierarchical name (`lang/go`).

Delete `validateFilenameName` (:661-666) if no references remain (run `go build ./...` to confirm; `ValidateName` has its own inline checks).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/profile/ -run 'TestLoadProfilesUserSubfolderNamespace|TestLoadProfilesUserNestedSubfolder|TestLoadProfilesRejectsReservedNamespacePrefix|TestLoadProfilesTolerantSkipsReservedNamespacePrefix' -v`
Expected: PASS.

Run: `go test ./internal/profile/ ./internal/catalog/`
Expected: PASS (existing tests still green — built-ins not yet moved).

- [ ] **Step 5: Commit**

```bash
git add internal/profile/catalog.go internal/profile/catalog_test.go
git commit -m "feat(profile): derive hierarchical names from file paths, reserve core prefix"
```

---

### Task 3: Catalog display/source APIs work with hierarchical names

**Files:**
- Modify: `internal/profile/catalog.go` (only if a test fails)
- Test: `internal/profile/catalog_test.go`, `internal/profile/ref_test.go`

**Interfaces:**
- Consumes: Tasks 1-2.
- Produces: no new signatures; asserts `DisplayNames()`, `Source()`, `FragmentByDisplayName()`, `ResolveRef` already key on the addressable hierarchical name.

- [ ] **Step 1: Write the failing tests**

Append to `internal/profile/ref_test.go`:

```go
func TestResolveRefHierarchicalUserThenCore(t *testing.T) {
	cat := Catalog{
		entries: map[string]RawProfile{
			"core/lang/go": {Profile: Profile{Image: "builtin"}, Namespace: "core", Name: "lang/go"},
			"lang/go":      {Profile: Profile{Image: "user"}, Namespace: "", Name: "lang/go"},
		},
		namespaces: map[string]bool{"": true, "core": true},
	}
	if got, _ := cat.ResolveRef(Ref{Name: "lang/go"}); got != "lang/go" {
		t.Errorf("ResolveRef(lang/go) = %q, want user lang/go", got)
	}
	if got, _ := cat.ResolveRef(Ref{Namespace: "core", Name: "lang/go"}); got != "core/lang/go" {
		t.Errorf("ResolveRef(core/lang/go) = %q, want core/lang/go", got)
	}
}

func TestResolveRefHierarchicalFallsBackToCore(t *testing.T) {
	cat := Catalog{
		entries: map[string]RawProfile{
			"core/lang/go": {Profile: Profile{Image: "builtin"}, Namespace: "core", Name: "lang/go"},
		},
		namespaces: map[string]bool{"": true, "core": true},
	}
	if got, _ := cat.ResolveRef(Ref{Name: "lang/go"}); got != "core/lang/go" {
		t.Errorf("ResolveRef(lang/go) = %q, want core/lang/go", got)
	}
}
```

Append to `internal/profile/catalog_test.go`:

```go
func TestFragmentByDisplayNameHierarchicalCoreOnly(t *testing.T) {
	cat := Catalog{
		entries: map[string]RawProfile{
			"core/lang/go": {Profile: Profile{Version: 1}, Namespace: "core", Name: "lang/go"},
		},
		namespaces: map[string]bool{"": true, "core": true},
		fragments:  map[string]bool{"core/lang/go": true},
	}
	got, ok := cat.FragmentByDisplayName("lang/go")
	if !ok {
		t.Fatal("expected ok")
	}
	if got != "core/lang/go" {
		t.Errorf("FragmentByDisplayName(lang/go) = %q, want core/lang/go", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test ./internal/profile/ -run 'TestResolveRefHierarchical|TestFragmentByDisplayNameHierarchical' -v`
Expected: PASS (the APIs were already keyed on the Name, which is now hierarchical). If `FragmentByDisplayName` or `Source` fail, fix them — they must check `entries[name]` / `entries["core/"+name]` by the full name, which the current implementation already does (`catalog.go:129-138`, `:111-125`).

- [ ] **Step 3: Commit**

```bash
git add internal/profile/ref_test.go internal/profile/catalog_test.go
git commit -m "test(profile): hierarchical-name resolution and display APIs"
```

---

### Task 4: Restructure built-in fragments into subfolders

**Files:**
- Modify: `internal/catalog/embed.go:8`
- Move: all files in `internal/catalog/fragments/` into subfolders
- Modify: `internal/catalog/fragments/lang/typescript.yaml`, `internal/catalog/profiles/{buzz,t3code,opencode-desktop}.yaml`
- Test: `internal/profile/catalog_test.go`, `internal/scaffold/*_test.go`, `cmd/tpd/profile_test.go`

**Interfaces:**
- Consumes: Tasks 1-2 (hierarchical built-in naming).
- Produces: built-in fragment FullNames now `core/lang/go`, `core/services/podman`, `core/gui/gui`, `core/gui/gui-runtime`, `core/cloud/aws`, `core/vcs/github`, `core/creds/ssh`, etc. `core/lang/typescript` extends `core/lang/javascript`.

- [ ] **Step 1: Move the fragment files**

Move the YAML files under `internal/catalog/fragments/` (creating the directories) per this taxonomy:

- `lang/`: dotnet, elixir, go, haskell, java, javascript, julia, kotlin, ocaml, perl, php, python, ruby, rust, scala, typescript, zig
- `services/`: docker-host, podman-host, podman, kubernetes, helm, terraform
- `gui/`: gui, gui-runtime
- `cloud/`: aws, azure, gcloud
- `vcs/`: github, gitlab
- `creds/`: ssh, netrc, gitconfig, bashrc, vault

Use `git mv` for each file.

- [ ] **Step 2: Update extends references**

In `internal/catalog/fragments/lang/typescript.yaml`, change:

```yaml
extends: core/javascript
```
to:
```yaml
extends: core/lang/javascript
```

In `internal/catalog/profiles/buzz.yaml`, `t3code.yaml`, `opencode-desktop.yaml`, change `- gui` → `- gui/gui` and `- gui-runtime` → `- gui/gui-runtime` in their `extends:` lists.

- [ ] **Step 3: Make the embed recursive**

In `internal/catalog/embed.go`:

```go
//go:embed fragments
var Fragments embed.FS
```

(`fs.WalkDir` in `loadBuiltinFragments` already recurses.)

- [ ] **Step 4: Run tests to find broken references**

Run: `go test ./internal/profile/ ./internal/catalog/ ./internal/scaffold/ ./cmd/tpd/ 2>&1 | head -80`
Expected: FAILs naming old flat keys (`core/javascript`, `core/go`, `core/docker-host`, …) and the CLI edit paths.

- [ ] **Step 5: Update `internal/profile/catalog_test.go`**

- `TestBuiltinTypescriptExtendsCoreJavascript`: `cat.Get("core/typescript")` → `cat.Get("core/lang/typescript")`; expect `Ref{Namespace: "core", Name: "lang/javascript"}`.
- `TestProfileDisplayNamesExcludesFragments`: `contains(names, "javascript")` → `contains(names, "lang/javascript")`.
- `TestBuiltinFragmentsDeclarePackages`: `"core/php"` → `"core/lang/php"`, `"core/gui"` → `"core/gui/gui"`.
- `TestFragmentByDisplayNameCoreOnly`: expect `"core/lang/javascript"`; write the user fragment as `fragments/lang/javascript.yaml` in `TestFragmentByDisplayNameUserWins` and expect `"lang/javascript"`.
- `TestTypescriptUnaffectedByUserFragmentNamedJavascript`: write the user fragment at `fragments/lang/javascript.yaml`, resolve via `ResolveFragment(cat, "lang/typescript")`, and assert it still inherits `node` from the built-in `core/lang/javascript` (drop the `userjs` assertion or expect it absent).
- Add `TestLoadProfilesUserShadowsCoreHierarchical` (deferred from Task 2): user fragment at `fragments/lang/go.yaml`; assert `ResolveRef(Ref{Name: "lang/go"})` returns `"lang/go"` (user wins) and `cat.Source("lang/go") == "user shadow"`.

- [ ] **Step 6: Update `internal/scaffold` tests (inputs AND outputs)**

Built-in fragment names no longer resolve as bare words, so tests that pass them as `extends:` inputs (or type them into the picker) fail with `unknown extends target`. Update both the inputs and the expected output strings. The mapping is:

| old (input & output) | new |
|-----|-----|
| `javascript` | `lang/javascript` |
| `go` | `lang/go` |
| `ruby` | `lang/ruby` |
| `podman` | `services/podman` |
| `gitconfig` | `creds/gitconfig` |
| `ssh` | `creds/ssh` |
| `netrc` | `creds/netrc` |
| `docker-host` | `services/docker-host` |

In `internal/scaffold/scaffold_test.go` and `new_profile_test.go`:
- Output assertions (`- core/javascript` → `- core/lang/javascript`, etc.): `scaffold_test.go` :93/:97/:100/:102, :215/:282/:324, :484, :539/:540/:543; `new_profile_test.go` :226, :273.
- Input `Extends:` slices (rename the bare names): `scaffold_test.go` :41, :76, :130, :189, :207, :225, :251, :270, :336, :382, :443, :468, :508, :556, :582, :598, :612, :631; `new_profile_test.go` :70 (`opencode,podman,ruby` → `opencode,services/podman,lang/ruby`), :238, :269 (`core/javascript` → `core/lang/javascript`).
- `new_profile_test.go:151` `TestNewProfileNameCollidesWithFragment`: the colliding fragment name must become `lang/javascript` (built-in `core/lang/javascript`).
- Picker inputs: `scaffold_test.go` interactive tests (`TestInteractiveOverwritePromptDecline` :295, `TestInteractiveOverwritePromptAccept` :316, `TestDryRunInteractivePrompts` :350, `TestInteractiveWizard` :522) that type `javascript`/`gitconfig` into the fragment picker must type the new names.

Keep `core/mise`, `core/bash`, `core/opencode`, `base`, `nocmd`, `doesnotexist` unchanged (profiles stay flat; `doesnotexist` stays a genuine unknown).

- [ ] **Step 7: Update `cmd/tpd` and remaining test references**

`internal/profile/catalog_fragments_test.go:16`: `cat.Get("core/ssh")` → `cat.Get("core/creds/ssh")`.

`cmd/tpd/cli_test.go`:
- `TestShowDockerPrintsSensitiveAdvisory` (:139) and `TestEditDockerPrintsSensitiveAdvisory` (:157): run `show`/`edit` on `services/docker-host` instead of `docker-host`.

`cmd/tpd/completion_test.go` (exists; plan Task 7 wrongly assumes it doesn't):
- `TestCompletionShow` (:80), `TestCompletionInitExtends` (:99): `docker-host` → `services/docker-host`.
- `TestCompletionShowPrefix` (:83-87): the `doc` prefix no longer matches any fragment; change the prefix to `services/` and expect `services/docker-host`, or drop the prefix case.

`cmd/tpd/profile_test.go`:
- `TestProfileEditBuiltInFragmentNoSaveRemovesSeed` (:191) and `TestProfileEditBuiltInFragmentSaveCreatesOverride` (:206): run `tpd edit services/docker-host` instead of `tpd edit docker-host`; expect the seeded/checked path `fragments/services/docker-host.yaml`; expect `extends: core/services/docker-host`.
- `TestProfileListShowsDisplayNameAndSource` (:125): the `docker-host` case expects `services/docker-host`.
- `TestProfileShowResolvedFragment` (:292): `show --resolved ssh` → `show --resolved creds/ssh`.

- [ ] **Step 8: Run the full suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: PASS. If the taxonomy in Step 1 moves a fragment to a different folder than this step assumes, update the strings in this step accordingly.

- [ ] **Step 9: Commit**

```bash
git add internal/catalog internal/profile/catalog_test.go internal/profile/catalog_fragments_test.go internal/scaffold cmd/tpd
git commit -m "refactor(catalog): organize built-in fragments into lang/services/gui/cloud/vcs/creds"
```

---

### Task 5: CLI addressable-name plumbing (edit, list, advisory, container naming)

**Files:**
- Modify: `cmd/tpd/cli.go` (`displayName` :525, `runEdit` :203-280, `runList` :335-353, advisory call sites :160/:174/:185/:218)
- Modify: `internal/runtime/docker_run.go:81,101`
- Modify: `pkg/tpd/launch.go:64-71`
- Test: `cmd/tpd/profile_test.go`, `internal/runtime/docker_test.go`

**Interfaces:**
- Consumes: Task 4 (namespaced built-in fragments).
- Produces: `displayName(key)` strips a leading `core/` (returns `services/docker-host` for `core/services/docker-host`); advisory lookups use the leaf segment; `runList` resolves via `ParseRefForCatalog`+`ResolveRef`; `containerNameFor(profile string) string` returns `tpd-<profile with "/"->"-">`; the launch error hint strips only the `core/` prefix.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/tpd/profile_test.go`:

```go
func TestProfileEditQualifiedBuiltInFragmentSeedsNamespacedPath(t *testing.T) {
	cfg := t.TempDir()
	env := []string{
		"XDG_CONFIG_HOME=" + cfg,
		"EDITOR=" + writeEditor(t, cfg, "editor", "#!/bin/sh\nprintf '\\n# saved by test\\n' >> \"$1\"\n"),
	}
	out, err := runTpdEnv(t, env, "edit", "core/services/docker-host")
	if err != nil {
		t.Fatalf("edit core/services/docker-host: %v\n%s", err, out)
	}
	target := filepath.Join(cfg, "tpd", "fragments", "services", "docker-host.yaml")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected seeded override at %s: %v", target, err)
	}
	if !strings.Contains(string(data), "extends: core/services/docker-host") {
		t.Errorf("seed must extend core/services/docker-host, got:\n%s", string(data))
	}
}
```

Append to `internal/runtime/docker_test.go` (package `runtime`):

```go
func TestContainerNameSanitizesProfile(t *testing.T) {
	for in, wantPrefix := range map[string]string{
		"lang/go":                     "tpd-lang-go-",
		"core/services/docker-host":   "tpd-core-services-docker-host-",
		"mise":                        "tpd-mise-",
	} {
		if got := containerNameFor(in); !strings.HasPrefix(got, wantPrefix) {
			t.Errorf("containerNameFor(%q) = %q, want prefix %q", in, got, wantPrefix)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/tpd/ -run TestProfileEditQualifiedBuiltInFragmentSeedsNamespacedPath -v && go test ./internal/runtime/ -run TestContainerNameSanitizesProfile -v`
Expected: FAIL — the qualified edit seeds the flat path (or fails to find the built-in file), and `containerNameFor` does not exist.

- [ ] **Step 3: Implement**

In `cmd/tpd/cli.go`:

Change `displayName` (:525-529) to:

```go
// displayName is the addressable name of a canonical catalog key: the key
// with a leading "core/" namespace stripped ("services/docker-host" for both
// "core/services/docker-host" and "services/docker-host"). Used for file
// paths (edit/seed) and list output.
func displayName(key string) string {
	return strings.TrimPrefix(key, "core/")
}
```

Add a helper for advisory lookups (keys are leaf names, e.g. `docker-host`):

```go
// advisoryName is the leaf segment of an addressable name, the key the
// advisory table uses (docker-host, gui, ssh, ...).
func advisoryName(key string) string {
	return filepath.Base(strings.TrimPrefix(key, "core/"))
}
```

Replace every `catalog.Advisory(displayName(key))` call with `catalog.Advisory(advisoryName(key))` (:160, :174, :185). In `runEdit` (:218), use `advisoryName(key)` too. Confirm `runEdit`'s `targetPath := filepath.Join(userDir, display+".yaml")` and the built-in read `root + "/" + display + ".yaml"` now use the full addressable path (`display`), so `tpd edit core/services/docker-host` seeds `fragments/services/docker-host.yaml` and reads `fragments/services/docker-host.yaml`. `os.MkdirAll(filepath.Dir(targetPath), 0o700)` at :255 already creates subfolders.

In `runList` (:342-349), replace the kind detection:

```go
	for _, dn := range cat.DisplayNames() {
		kind := "profile"
		if ref, err := cat.ParseRefForCatalog(dn); err == nil {
			if key, ok := cat.ResolveRef(ref); ok && cat.IsFragment(key) {
				kind = "fragment"
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", dn, kind, cat.Source(dn))
	}
```

In `internal/runtime/docker_run.go`, add a helper and use it:

```go
// containerNameFor builds the Docker container-name prefix from a profile
// name. Profile names may be hierarchical (lang/go); '/' is not valid in a
// container name, so it becomes '-'.
func containerNameFor(profileName string) string {
	return "tpd-" + strings.ReplaceAll(profileName, "/", "-") + "-"
}
```

Then at :81:

```go
containerName := containerNameFor(spec.ProfileName) + randomID(8)
```

and at :101:

```go
Hostname: strings.ReplaceAll(spec.ProfileName, "/", "-"),
```

Add `"strings"` to the imports in `docker_run.go` if not already present.

In `pkg/tpd/launch.go:64-71` (fragment-can't-be-launched error), the suggestion strips the name with `strings.LastIndex` and yields the wrong `--extends` hint for hierarchical names. Replace:

```go
			name := key
			if i := strings.LastIndex(name, "/"); i >= 0 {
				name = name[i+1:]
			}
```

with:

```go
			name := strings.TrimPrefix(key, "core/")
```

so `tpd core/lang/go` suggests `tpd init myprofile --extends lang/go`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/tpd/ ./internal/runtime/ -run 'TestProfileEditQualified|TestContainerNameSanitizes|TestProfileEditBuiltInFragment' -v`
Expected: PASS.

Run: `go build ./... && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/tpd/cli.go cmd/tpd/profile_test.go internal/runtime/docker_run.go internal/runtime/docker_test.go pkg/tpd/launch.go
git commit -m "feat(cli,runtime): address namespaced profiles in edit/list and sanitize container names"
```

---

### Task 6: Scaffold supports hierarchical profile names

**Files:**
- Modify: `internal/profile/validate.go:441-456`
- Modify: `internal/scaffold/scaffold.go:215,289`
- Test: `internal/profile/validate_test.go`, `internal/scaffold/scaffold_test.go`

**Interfaces:**
- Consumes: Tasks 1-5.
- Produces: `ValidateName` accepts multi-segment names (validating each segment) and rejects a `core` first segment; `tpd init lang/go` writes `profiles/lang/go.yaml` (MkdirAll on the parent).

- [ ] **Step 1: Write the failing tests**

In `internal/profile/validate_test.go`, extend `TestValidateName` (:73-86): move `"a/b"` from the `invalid` list to the `valid` list (it is now a legal hierarchical name), and add:

```go
	if err := ValidateName("lang/go"); err != nil {
		t.Errorf("ValidateName(lang/go) = %v, want nil", err)
	}
	if err := ValidateName("core/go"); err == nil {
		t.Error("ValidateName(core/go) = nil, want reserved-namespace error")
	}
	if err := ValidateName("core"); err != nil {
		t.Errorf("ValidateName(core) = %v, want nil (a bare core profile name is allowed)", err)
	}
	if err := ValidateName("lang/foo bar"); err == nil {
		t.Error("ValidateName(lang/foo bar) = nil, want invalid-segment error")
	}
	if err := ValidateName("lang/.."); err == nil {
		t.Error("ValidateName(lang/..) = nil, want '..' error")
	}
```

Append to `internal/scaffold/scaffold_test.go` (mirrors the `Run` usage in `TestGenerateYAMLWithCachesAndMounts` at :71-87). Use a name that does not collide with any built-in (a built-in fragment `core/lang/go` exists after Task 4, which would make `lang/go` fail at `scaffold.go:139` with "collides with an existing fragment"):

```go
func TestInitNamespacedProfileWritesSubfolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tpd", "profiles")
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "acme/go",
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}
	target := filepath.Join(dir, "acme", "go.yaml")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected generated file at %s: %v", target, err)
	}
	if !strings.Contains(string(data), "extends:") {
		t.Errorf("generated file should extend a base, got:\n%s", string(data))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/profile/ -run TestValidateName -v && go test ./internal/scaffold/ -run TestInitNamespacedProfileWritesSubfolder -v`
Expected: FAIL — `ValidateName("lang/go")` returns the "must not contain slashes" error, `"a/b"` in `invalid` fails, and the init file fails to write into the `acme` subdir (MkdirAll only creates `profiles`).

- [ ] **Step 3: Implement**

In `internal/profile/validate.go`, replace `ValidateName` (:445-456):

```go
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("profile name is required")
	}
	segs := strings.Split(name, "/")
	for _, seg := range segs {
		if !profileNameRe.MatchString(seg) || strings.Contains(seg, "..") {
			return fmt.Errorf("invalid profile name %q: each segment must match %s and must not contain '..'", name, profileNameRe)
		}
	}
	// The reserved-namespace check applies to the first segment of a
	// hierarchical name only; a bare profile named "core" is allowed (it
	// doesn't collide with built-in "core/..." keys).
	if len(segs) > 1 && segs[0] == "core" {
		return fmt.Errorf("invalid profile name %q: %s is a reserved namespace prefix", name, "core")
	}
	if !strings.Contains(name, "/") && reservedNames[name] {
		return fmt.Errorf("profile name %q is reserved (collides with a subcommand)", name)
	}
	return nil
}
```

In `internal/scaffold/scaffold.go`, change :289 (`os.MkdirAll(userDir, 0o700)`) to:

```go
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
```

Keep the `targetPath := filepath.Join(userDir, profileName+".yaml")` at :215 — it already produces `profiles/lang/go.yaml`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/profile/ -run TestValidateName -v && go test ./internal/scaffold/`
Expected: PASS.

Run: `go build ./... && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/profile/validate.go internal/profile/validate_test.go internal/scaffold/scaffold.go internal/scaffold/scaffold_test.go
git commit -m "feat(scaffold): support hierarchical profile names in init"
```

---

### Task 7: Completion smoke + final verification

**Files:**
- Test: `cmd/tpd/completion_test.go` (optional, only if a completion helper needs coverage)

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: Verify completion surfaces namespaced fragment names**

The existing completion tests (`cmd/tpd/completion_test.go` `TestCompletionShow`, `TestCompletionShowPrefix`, `TestCompletionInitExtends`) were updated in Task 4 Step 7 to expect namespaced names (`services/docker-host`). Verify and add a hierarchical-fragment completion case if none exists:

```go
func TestCompletionShowHierarchicalFragment(t *testing.T) {
	names := runCompletion(t, "show", "services/")
	if !containsAll(t, names, "services/docker-host", "services/podman") {
		t.Errorf("expected namespaced fragment completions, got %v", names)
	}
}
```

(Mirror the `runCompletion`/`containsAll` helpers already in `cmd/tpd/completion_test.go`.)

- [ ] **Step 2: Full verification**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all PASS.

- [ ] **Step 3: Manual smoke (optional, rootless podman)**

Create `$XDG_CONFIG_HOME/tpd/fragments/lang/smoke.yaml` with `version: 1` and a tool, then verify `tpd show lang/smoke` resolves and `tpd list` shows `lang/smoke`. Clean up after.

- [ ] **Step 4: Commit any test additions**

```bash
git add cmd/tpd/completion_test.go
git commit -m "test(completion): namespaced fragment name completion"
```

---

## Self-Review Notes

Spec coverage:
- Name model + arbitrary depth → Task 2 (`nameFromPath`, nested test).
- Resolution rules → Tasks 1, 3 (`ParseRef`, `ResolveRef`).
- Reserved `core` prefix → Tasks 2, 6.
- Catalog API → Task 3.
- CLI (edit/list/advisory/container naming, launch hint) → Task 5.
- Scaffold/init → Task 6.
- Built-in restructure + extends updates + embed → Task 4.
- Migration (doctor flags broken extends) → no code change needed; the existing profile-validity check reports unresolvable extends as errors (verified: `ResolveProfile` returns "profile not found" for unresolvable refs). Documented in spec, no task required.
- Advisory leaf keying → Task 5.

Review amendments (subagent review of v1, all incorporated):
- Task 1: `extends_test.go:61` `TestExtendsListResolveRejectsUnknownNamespace` → hierarchical-fallback test; included in the commit.
- Task 4: Steps 6-7 expanded into a full test sweep covering input args AND output assertions across `internal/scaffold/*_test.go`, `internal/profile/catalog_fragments_test.go`, `cmd/tpd/{cli,completion,profile}_test.go` (including the picker inputs and the `doc` completion prefix).
- Task 5: `pkg/tpd/launch.go:64-71` fragment-launch hint now strips only `core/` (`--extends lang/go`); file/commit updated.
- Task 6: `validate_test.go` moves `"a/b"` to valid; `ValidateName("core")` allowed (reserved check gated on `len(segs) > 1`); scaffold test uses `acme/go` to avoid colliding with the built-in `core/lang/go`; task commit updated.
