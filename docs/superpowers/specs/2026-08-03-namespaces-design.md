# Profile/fragment namespaces

Date: 2026-08-03
Builds on: `2026-07-30-toolpod-design.md` (catalog, extends), `2026-07-31-multi-extends-fragments-design.md` (extends/fragments), and the edit-seed shadow mechanism (`9b633d0`).

## Goal

Give profiles and fragments explicit namespaces so `core/mise` always refers to the embedded built-in and an unqualified `mise` resolves to either the user profile or the built-in. Replace the current "extends the built-in of the same name" special-case with an explicit `extends: core/foo`. Self-referencing a file (extending a name that resolves back to itself) becomes a hard error caught by cycle detection. Lay the namespace plumbing so remote imports (`extends: github.com/user/project/foo`, Go-import-style) can register a namespace at runtime later.

## Why namespaces, why now

Today the catalog is flat: built-ins and user entries share one global namespace, and a user file shadows the built-in of the same name. Extending the built-in you're shadowing is handled by a special-case in `resolveChain` (`IsUserShadow` + `resolveBuiltinChain`): the first `extends` entry may equal the profile's own name and is quietly resolved as the built-in. That special-case is the only thing today that lets a user shadow customize a built-in.

This works for two namespaces but does not generalize. Remote imports need a name to carry a namespace (`github.com/user/project/foo`), and the shadow trick can't scale to that. Introducing `core/` now makes the shadow explicit (`extends: core/mise`) and replaces string-keyed special-casing with a structured identity that remote imports can reuse.

## Namespace model

Every catalog entry carries a structured identity `(Namespace, Name)`:

- `core` — embedded built-in profiles and fragments. Stored as `core/mise`, `core/go`, etc.
- `""` (empty) — user profiles/fragments in `~/.config/tpd/{profiles,fragments}/`. Stored as the bare name (`mise`, `go`).
- Future: remote imports register a namespace at runtime, e.g. `github.com/user/project`, with entries stored as `github.com/user/project/foo`. The local name is always a single path segment (file basename); the namespace is the full prefix. See Remote imports.

The empty namespace is the only one that participates in unqualified fallback (see Resolution). All other namespaces require their prefix.

## Storage

`internal/profile/types.go` — `RawProfile` gains a structured identity:

```go
type RawProfile struct {
	Profile
	Namespace string                  `yaml:"-"` // "core", "", or future "github.com/..."
	Name      string                  `yaml:"-"` // local name, a single segment (file basename)
	Path      string                  `yaml:"-"`
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

`internal/profile/catalog.go` — `Catalog` stores entries keyed by `FullName()`:

```go
type Catalog struct {
	entries    map[string]RawProfile // keyed by FullName: "core/mise", "mise", "github.com/u/p/mise"
	namespaces map[string]bool       // registered prefixes: "core", "", future remotes
	fragments  map[string]bool       // FullNames that are fragments
}
```

### Catalog API (canonical vs display)

Two distinct APIs replace the old single-name API:

- **Canonical** (catalog-internal, used by `Get`, `IsFragment`, `AddRaw`, `entries` map, cycle detection): `FullName` strings. `Get(name)` looks up by `FullName` (the caller is expected to pass the canonical form; `Get` does **not** do fallback — see Resolution for the helper that does).
- **Display** (user-facing list, wizard pickers, summary): `DisplayName`. `DisplayNames()` returns the set of `DisplayName`s, deduplicated across namespaces (user shadows core: user wins; core-only `mise` shows as `mise`; `core/go` with no user `go` shows as `go`). `ProfileDisplayNames()` is the same filtered to non-fragments. `Source(name)` returns `"user"`, `"core"`, or `"user shadow"` for a display name, used by `tpd list`'s SOURCE column.

`GetBuiltin` and `IsUserShadow` are removed: a built-in is `Get("core/"+name)`, and a shadow is `Source(name) == "user shadow"`.

`AddRaw(ns, name, rc)` sets `rc.Namespace`/`rc.Name` and inserts under `FullName`. The scaffold flow calls `AddRaw("", profileName, rc)` for the generated profile.

### Loading

`loadBuiltins` and `loadBuiltinFragments` stamp `Namespace: "core"`, `Name: <file basename>`. User loaders stamp `Namespace: ""`. Collision check compares `FullName`, so `core/mise` and `mise` coexist (no collision); a user `mise` and a user fragment `mise` still collide (both `mise`).

`NewProfileCatalogForTest` stamps `Namespace: "core"` to preserve current behavior (tests that today treat entries as built-ins).

## Resolution

Resolution converts a reference (from `extends:` or the CLI) into a **canonical identity** `(Namespace, Name)` and then looks it up in `entries`. There are two cases:

- **Qualified** (`core/foo`, future `github.com/user/project/foo`): split at the *registered* namespace prefix (longest match, see Splitting), remainder is the local name. Canonical identity is `(ns, name)`, key is `FullName` (`ns + "/" + name`). Direct lookup. No fallback.
- **Unqualified** (`foo`): canonical key is `foo` (the bare name — `FullName` for the empty namespace) if a user entry exists, else `core/foo` if a built-in exists. Resolution picks one canonical key and uses it for all downstream work (lookup, cycle detection, fragment checks). If neither exists, resolution fails.

`entries` is always keyed by `FullName`, which for the empty namespace is the bare name (`mise`, not `/mise`). There is no `""+"/"+name` form anywhere; the empty namespace is represented as the bare local name.

The `Catalog` exposes a `ResolveRef(ref Ref) (string, bool)` helper that returns the canonical `FullName` for a reference:

```go
// Ref is a parsed-but-not-yet-resolved reference. Namespace == "" means
// unqualified (resolve via fallback); any other value means qualified
// (direct lookup, no fallback).
type Ref struct {
	Namespace string // "" for unqualified; "core" or a future remote ns otherwise
	Name      string  // local name; for unqualified this is the bare string
}

// ResolveRef resolves a reference to a canonical catalog FullName (an entries key).
// For unqualified names (ref.Namespace == ""), returns the user key (bare name)
// if present, else the core key ("core/"+name). For qualified names, returns
// the qualified key directly (no fallback). Returns ok=false if no entry matches.
func (c Catalog) ResolveRef(ref Ref) (string, bool)
```

All callers of `Get` in resolution, cycle detection, and fragment checks go through `ResolveRef` first. The public resolution entry points (`ResolveProfile`, `ResolveFragment`) and the CLI launch path all accept a user-supplied string, parse it into a `Ref` via `ParseRef` (see Splitting), call `ResolveRef` to get the canonical key, and then operate on that key. They never pass a raw user string to `Get` directly. This is the only place fallback happens. Cycle detection (`seen` map), fragment checks (`IsFragment`), and type checks all operate on the resolved canonical `FullName`, never on the raw reference. So `extends: mise` from the user `mise.yaml` resolves to `mise` (user), and `seen["mise"]` catches the self-cycle; `extends: core/mise` from the core `mise.yaml` resolves to `core/mise`, and `seen["core/mise"]` catches it.

The namespace registry is populated at catalog load: `core` and `""` are always registered. Remote imports (future) register their own. Unqualified fallback applies everywhere: at the CLI (`tpd mise`), in `extends:` (`extends: mise`), and in `tpd init`'s default base. Only an explicit `core/` (or remote) prefix escapes fallback.

### Splitting (qualified vs unqualified, segment boundaries)

A string is split into a `Ref` by `ParseRef(s, namespaces) (Ref, error)`:

1. If `s` contains no `/`, it is unqualified: `Ref{Namespace: "", Name: s}`. `s` must be non-empty.
2. Otherwise, find the longest registered namespace `ns` such that `s` starts with `ns + "/"` (segment boundary — the `/` is required, so `core` does not match `corexy/foo`). The remainder after `ns+"/"` is the local name. If `ns == ""`... but `""` has no prefix, so an unqualified string never reaches this branch. So a qualified split always has `ns != ""`.
3. If no registered namespace matches, the string is unqualified after all (the `/` belongs to a future or unknown namespace): this is a parse error, "unknown namespace in extends: X". We do not fall back to treating `corefoo/bar` as unqualified `corefoo/bar`.
4. The local name (remainder) must be non-empty; `core/` is rejected with "empty local name in extends: core/".

`ParseRef` is used by `ResolveRef`, by the `extends:` parse phase (see Reference parsing), and by the CLI to interpret a profile argument.

### Fragment type-awareness

Fragment-only-extends-fragments is enforced **after** canonical resolution, on the resolved `FullName`. This handles the built-in fragment case correctly: `core/typescript.yaml` extends `javascript`. `javascript` resolves via fallback — if a user *profile* named `javascript` exists, it would win over `core/javascript` (the fragment), and the fragment check would fail because the resolved entry is a profile.

This is a real ambiguity: the built-in `core/typescript` meant to extend the `core/javascript` *fragment*, not a hypothetical user profile. The fix: **built-in fragment files emit qualified extends** (`extends: core/javascript`) so they never fall back to a user entry of the same name. The embedded catalog is updated accordingly (`core/typescript.yaml`, and any other built-in fragment extending another built-in fragment, gains `core/`-qualified refs). Built-in *profile* files (e.g. `core/opencode.yaml`) keep unqualified `extends: mise` — they intend to pick up user customizations to `mise`.

This mirrors the rule for user shadows: when you mean the built-in specifically, qualify it.

### Self-reference

Self-reference is a cycle of length 1, caught by the existing `seen` check in `resolveChain` once the special-case is removed. Because unqualified resolution is **user-first**, an unqualified self-name resolves to the entry itself, not to the built-in.

- `extends: mise` from the user `mise.yaml`: `ResolveRef` returns `mise` (user wins) → the entry being resolved → `seen["mise"]` → cycle, rejected. To extend the built-in, the user writes `extends: core/mise` (qualified → direct `core/mise`, skips the user entry).
- `extends: core/mise` from the core `mise.yaml`: `ResolveRef` returns `core/mise` → `seen["core/mise"]` → cycle, rejected.
- `extends: mise` from `core/opencode.yaml` (a *different* core file): resolves to user `mise` if it exists, else `core/mise`. Not a self-reference (`core/opencode` ≠ `mise`). Intentional — built-in profiles pick up user customizations to `mise` automatically.

No new code path. The `IsUserShadow` branch in `merge.go` and the entire `resolveBuiltinChain` function are deleted.

## Reference parsing

`internal/profile/types.go` — a reference as it appears in YAML is a string. The structured form is `Ref` (defined above under Resolution). The list is a struct, not a bare slice, so it can carry both the raw strings (phase 1) and the parsed refs (phase 2):

```go
// ExtendsList is the yaml-decoded extends field. It carries the raw strings
// until ExtendsList.Resolve splits them against the namespace registry.
type ExtendsList struct {
	Raw    []string  `yaml:"-"`
	Resolved []Ref   `yaml:"-"`
}

// UnmarshalYAML decodes a scalar or list of strings into Raw. No namespace
// splitting happens here (yaml.v3 gives no context). Resolved stays nil.
func (e *ExtendsList) UnmarshalYAML(value *yaml.Node) error

// MarshalYAML emits Resolved (if non-empty) as canonical strings, else Raw.
func (e ExtendsList) MarshalYAML() (interface{}, error)

// Resolve splits each Raw string against the registered namespaces into
// Resolved. Called by parseRaw with the namespace set. Idempotent.
func (e *ExtendsList) Resolve(namespaces map[string]bool) error
```

`ExtendsRef` from the original draft is gone; the structured element is `Ref` (one type for extends entries, CLI args, and internal resolution). `Profile.ExtendsList` is the struct above. Code that iterates extends uses `list.Resolved` after `Resolve`.

Phase 1 (YAML decode): `UnmarshalYAML` fills `Raw`, leaves `Resolved` nil.
Phase 2 (contextual split): `parseRaw` calls `list.Resolve(namespaces)`, which fills `Resolved` with `Ref` values via `ParseRef` (longest-prefix match). An unregistered prefix is a parse error: "unknown namespace in extends: X". Empty local name (`core/`) is rejected.

`MarshalYAML` emits the canonical string form (`core/mise`, `mise`) from `Resolved` (or `Raw` if `Resolved` is nil, for round-tripping un-resolved lists). This is what `tpd init` and `tpd edit` seeds write.

## CLI behavior for qualified names

The CLI (`tpd show`, `tpd edit`, `tpd <profile>` launch) accepts both qualified (`core/mise`) and unqualified (`mise`) names on the command line. Resolution uses `ResolveRef`: unqualified resolves user-first then core; qualified is direct. Behavior:

- **`tpd show core/mise`**: show the built-in `core/mise` (merged). `tpd show mise`: show user `mise` if present (merged), else `core/mise`.
- **`tpd <profile>` (launch)**: resolve the CLI name via `ResolveRef` and launch that entry.
- **`tpd edit core/mise`**: editing a built-in seeds the **user** file `mise.yaml` (the shadow path), never `core/mise.yaml`. The edit-seed mechanism already computes the user target path from the *display* name; for a qualified `core/` argument, the CLI strips the `core/` prefix to get the file name (`mise`), seeds `~/.config/tpd/profiles/mise.yaml` with `extends: core/mise`, and opens it. If a user `mise.yaml` already exists, it's opened directly (the `core/` qualifier is ignored — you can't edit the built-in in place, only shadow it). `tpd edit mise` (unqualified) behaves the same as today: resolves to the user file if present, else seeds a shadow of whatever `ResolveRef` returns (user or core).
- **`tpd list`**: shows `DisplayName` (unqualified) in the NAME column and `Source` (`user` / `core` / `user shadow`) in the SOURCE column, as today.

Name validation (`ValidateName`) applies to the **file/display name**, which must remain a single segment with no `/`. Qualified names on the CLI bypass `ValidateName` for the namespace prefix; the local name segment is validated.

## emit core/ in init and edit

`tpd init` and `tpd edit` emit `extends: core/<name>` when seeding a shadow of a built-in, instead of the current `extends: <name>`.

- `internal/scaffold/scaffold.go`: the default base for a new profile shadowing a built-in becomes `core/<name>`. The "fall back to built-in of the same name" path (`if _, ok := cat.GetBuiltin(profileName); ok { bases = append([]string{profileName}, ...) }`) becomes `bases = append([]string{"core/"+profileName}, ...)`. The `bases` list written into the generated YAML uses `core/`-qualified refs for built-ins. When the user picks a fragment in the wizard, the fragment ref is also `core/`-qualified if it's a built-in fragment.
- `internal/scaffold/fragments.go`: `FragmentNames()` returns **display names** (`DisplayName`, unqualified) for the picker UI. The wizard selection result is a list of display names. To emit qualified refs in the generated YAML, the scaffold resolves each picked display name to its canonical `FullName` via a new `Catalog.FragmentByDisplayName(name) (string, bool)` helper (returns `"core/javascript"` for `javascript` when only the built-in fragment exists, or `"javascript"` when a user fragment of that name exists; if both exist, the user fragment wins by the same fallback rule). The generated `extends:` entries use the returned `FullName`. `FragmentNames()` itself is unchanged (still returns bare names for the picker); the change is that the scaffold maps the picked names through `FragmentByDisplayName` before emitting them.
- `cmd/tpd/cli.go` `builtinEditSeed`: the seed shadow line becomes `extends: core/<name>`.

The wizard pickers and `tpd list` display `DisplayName` for user-facing ergonomics (`mise`, not `core/mise`); the `core/` prefix appears only in generated `extends:` lines and when a user explicitly passes a qualified name to the CLI.

## Remote imports (future, not implemented here)

Rough sketch so the namespace design accommodates it:

```yaml
extends: github.com/user/project/foo
```

The **namespace is `github.com/user/project`** and the **local name is `foo`** (single segment). The `/profiles/` path is an internal layout detail of how the remote repo is fetched and laid out, not part of the namespace string. When `tpd` fetches the repo, it registers `github.com/user/project` as a namespace and stamps each entry's `Namespace` with it; the local name is the file basename under the repo's profiles/fragments dir, exactly as for `core` and user entries.

Resolution is uniform: `github.com/user/project/foo` → longest-prefix split → `("github.com/user/project", "foo")` → direct lookup. No fallback for remote namespaces.

This keeps local names single-segment everywhere, so `ValidateName` and file-path derivation stay simple. The namespace registry and longest-prefix split are designed so adding a remote namespace requires only registering it and loading its entries — no changes to resolution, merge, or validation. This spec implements the registry and split; it does not implement fetching.

## Migration

Existing user files fall into two groups:

- Hand-written profiles extending a built-in by an unqualified name that is *not* their own (e.g. `extends: mise` from `myagent.yaml`) keep working: `mise` resolves to user `mise` if present, else `core/mise`. No change.
- User shadows that today extend the built-in of the same name via the `extends: <self>` special-case (e.g. a user `mise.yaml` with `extends: mise`). Under the new rules that is a self-reference and becomes an error. The fix is mechanical: change `extends: mise` to `extends: core/mise`. Existing `tpd init`/`tpd edit` seed files are the primary source of these; re-running `tpd init` regenerates them with the correct `core/` form.

The change in `init`/`edit` output (emitting `extends: core/mise` instead of `extends: mise`) only affects newly generated files. `tpd doctor` (or a one-line migration note in release notes) can flag the broken `extends: <self>` pattern for existing files.

Built-in fragment files that extend other built-in fragments (`core/typescript.yaml` → `javascript`, and any others) are updated to `extends: core/javascript` in the same change. Built-in profile files keep unqualified `extends: mise`.

## Test surface

- `merge_test.go`: the `extends: opencode` self-shadow test switches to `extends: core/opencode` and expects the same merge; the `extends: foo` self-cycle test now also covers `extends: core/foo` from `core/foo.yaml`.
- `catalog_test.go`: `DisplayNames`/`ProfileDisplayNames` return unqualified names; `Names` (canonical) returns `FullName`s; built-in entries are `core/...`.
- New tests: `ResolveRef` unqualified fallback (user `mise` shadows `core/mise`); `ResolveRef` qualified `core/mise` bypasses user; self-reference rejected for `extends: mise` from user `mise.yaml` and `extends: core/mise` from `core/mise.yaml`; longest-prefix split with a synthetic multi-segment namespace; `ParseRef` rejects `core/` (empty local name) and rejects `corexy/foo` when `core` is registered but `corexy` is not (segment-boundary check); built-in fragment `core/typescript` extends `core/javascript` and is unaffected by a user profile named `javascript`.
- `scaffold_test.go` / `new_profile_test.go`: generated YAML contains `extends: core/...`.
- `profile_test.go` (cli): `tpd edit core/mise` seeds `mise.yaml` with `extends: core/mise`; edit seed contains `extends: core/...`.

## Out of scope

- Remote import fetching (`github.com/...` resolution, caching, verification).
- Allowing user-defined namespace prefixes beyond `""` and `core`.
- Multi-segment local names (the namespace carries the path prefix; local names stay single-segment file basenames).