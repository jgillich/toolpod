# Profile/fragment namespaces

Date: 2026-08-03
Builds on: `2026-07-30-toolpod-design.md` (catalog, extends), `2026-07-31-multi-extends-fragments-design.md` (extends/fragments), and the edit-seed shadow mechanism (`9b633d0`).

## Goal

Give profiles and fragments explicit namespaces so `core/mise` always refers to the embedded built-in and an unqualified `mise` resolves to either the user profile or the built-in. Replace the current "extends the built-in of the same name" special-case with an explicit `extends: core/foo`. Self-referencing a file (extending a name that resolves back to itself) becomes a hard error caught by cycle detection. Lay the namespace plumbing so remote imports (`extends: github.com/user/project/profiles/foo`, Go-import-style) can register a namespace at runtime later.

## Why namespaces, why now

Today the catalog is flat: built-ins and user entries share one global namespace, and a user file shadows the built-in of the same name. Extending the built-in you're shadowing is handled by a special-case in `resolveChain` (`IsUserShadow` + `resolveBuiltinChain`): the first `extends` entry may equal the profile's own name and is quietly resolved as the built-in. That special-case is the only thing today that lets a user shadow customize a built-in.

This works for two namespaces but does not generalize. Remote imports need a name to carry a namespace (`github.com/user/project/foo`), and the shadow trick can't scale to that. Introducing `core/` now makes the shadow explicit (`extends: core/mise`) and replaces string-keyed special-casing with a structured identity that remote imports can reuse.

## Namespace model

Every catalog entry carries a structured identity `(Namespace, Name)`:

- `core` — embedded built-in profiles and fragments. Stored as `core/mise`, `core/go`, etc.
- `""` (empty) — user profiles/fragments in `~/.config/tpd/{profiles,fragments}/`. Stored as the bare name (`mise`, `go`).
- Future: remote imports register a namespace at runtime, e.g. `github.com/user/project`, with entries stored as `github.com/user/project/foo`.

The empty namespace is the only one that participates in unqualified fallback (see Resolution). All other namespaces require their prefix.

## Storage

`internal/profile/types.go` — `RawProfile` gains a structured identity:

```go
type RawProfile struct {
	Profile
	Namespace string                  `yaml:"-"` // "core", "", or future "github.com/..."
	Name      string                  `yaml:"-"` // local name, e.g. "mise"
	Path      string                  `yaml:"-"`
	NullKeys  map[string]map[string]bool `yaml:"-"`
}

func (rc RawProfile) FullName() string {
	if rc.Namespace == "" {
		return rc.Name
	}
	return rc.Namespace + "/" + rc.Name
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

`Get`, `IsFragment`, `Names`, `ProfileNames`, `AddRaw` operate on `FullName`. `GetBuiltin` and `IsUserShadow` are removed: a built-in is just `Get("core/"+name)`, and the shadow special-case is gone.

Loading (`loadBuiltins`, `loadBuiltinFragments`) stamps `Namespace: "core"`, `Name: <file basename>` before inserting. User loaders stamp `Namespace: ""`. Collision check compares `FullName`, so `core/mise` and `mise` coexist (no collision); a user `mise` and a user fragment `mise` still collide (both `mise`).

`NewProfileCatalogForTest` stamps `Namespace: "core"` to preserve current behavior (tests that today treat entries as built-ins).

## Resolution

A reference in `extends:` (or a CLI profile name) is parsed into `(ns, name)` once at load:

- **Qualified** (`core/foo`, future `github.com/user/project/foo`): split at the *registered* namespace prefix. The resolver consults the namespace registry to find the longest registered prefix matching the string; the remainder is the local name. Direct lookup: `entries[ns+"/"+name]`. No fallback.
- **Unqualified** (`foo`): look up `entries[""+"/"+name]` (user); if absent, look up `entries["core/"+name]`. User wins, else core. This is the only fallback path.

The namespace registry is populated at catalog load: `core` and `""` are always registered. Remote imports (future) register their own. The registry is consulted at parse time to split qualified names, so the structured `(ns, name)` is carried from that point on.

Unqualified fallback applies everywhere: at the CLI (`tpd mise`), in `extends:` (`extends: mise`), and in `tpd init`'s default base. Only an explicit `core/` prefix escapes the user shadow.

### Self-reference

Self-reference is a cycle of length 1, caught by the existing `seen` check in `resolveChain` once the special-case is removed. Because unqualified resolution is **user-first**, an unqualified self-name resolves to the entry itself, not to the built-in.

- `extends: mise` from the user `mise.yaml`: `mise` resolves via fallback to the user `mise` (user wins) → the entry being resolved → cycle, rejected. To extend the built-in, the user must write `extends: core/mise` (qualified → direct lookup of `core/mise`, skips the user entry).
- `extends: core/mise` from the core `mise.yaml`: `FullName` of the extends ref equals the entry's `FullName` (`core/mise`) → cycle, rejected.
- `extends: mise` from `core/opencode.yaml` (a *different* core file): resolves to user `mise` if it exists, else `core/mise`. Not a self-reference (opencode ≠ mise). This is intentional — built-in profiles pick up user customizations to `mise` automatically.

The embedded catalog files keep unqualified `extends: mise` (e.g. `opencode.yaml`, `shell.yaml`); only newly-generated user shadow files use `extends: core/mise`.

No new code path. The `IsUserShadow` branch in `merge.go` and the entire `resolveBuiltinChain` function are deleted.

### Fragments

Fragments may only extend fragments. The check compares `(ns, name)` pairs (via `FullName`), unchanged in spirit.

## Extends parsing

`ExtendsList` becomes structured. `internal/profile/types.go`:

```go
type ExtendsList []ExtendsRef

type ExtendsRef struct {
	Namespace string // "core", "", future "github.com/user/project"
	Name      string  // "mise"
}

func (e ExtendsRef) String() string {
	if e.Namespace == "" {
		return e.Name
	}
	return e.Namespace + "/" + e.Name
}
```

`UnmarshalYAML` splits each string entry against the catalog's registered namespaces at parse time. `parseRaw` takes the namespace set as a parameter, threaded from `LoadProfiles`. Longest-prefix match: `github.com/user/project/mise` splits as `("github.com/user/project", "mise")` once that namespace is registered; `core/mise` splits as `("core", "mise")`; `mise` is `("", "mise")`. An unregistered prefix is a parse error: "unknown namespace in extends: X".

`MarshalYAML` emits the canonical string form (`core/mise`, `mise`). This is what `tpd init` and `tpd edit` seeds write.

## emit core/ in init and edit

`tpd init` and `tpd edit` currently emit `extends: <name>` where `<name>` is the built-in being shadowed. Under namespaces this would be a self-reference (user `mise.yaml` extending `mise`, which resolves to `core/mise` — not a self-ref, but ambiguous and not the intent). Both commands emit `extends: core/<name>` instead.

- `internal/scaffold/scaffold.go`: the default base for a new profile shadowing a built-in becomes `core/<name>`; the `bases` list written into the YAML uses `core/`-qualified refs for built-ins. The "fall back to built-in of the same name" path (`if _, ok := cat.GetBuiltin(profileName); ok { bases = append([]string{profileName}, bases...) }`) becomes `bases = append([]string{"core/"+profileName}, bases...)`.
- `cmd/tpd/cli.go` `builtinEditSeed`: the seed shadow line becomes `extends: core/<name>`.

The wizard pickers and `tpd list` display unqualified names for user-facing ergonomics (`mise`, not `core/mise`); the `core/` prefix appears only in generated `extends:` lines and when a user explicitly wants the built-in.

## Remote imports (future, not implemented here)

Rough sketch so the namespace design accommodates it:

```yaml
extends: github.com/user/project/profiles/foo
```

A remote namespace is registered at runtime (e.g. `tpd` fetches and caches the repo, registers `github.com/user/project` as a namespace, stamps its entries' `Namespace` accordingly). Resolution is uniform: `github.com/user/project/profiles/foo` → longest-prefix split → `("github.com/user/project", "profiles/foo")` → direct lookup. No fallback for remote namespaces.

The namespace registry and the longest-prefix split in `ExtendsList.UnmarshalYAML` are designed so that adding a remote namespace requires only registering it and loading its entries — no changes to resolution, merge, or validation. This spec implements the registry and split; it does not implement fetching.

## Migration

Existing user files fall into two groups:

- Hand-written profiles extending a built-in by an unqualified name that is *not* their own (e.g. `extends: mise` from `myagent.yaml`) keep working: `mise` resolves to user `mise` if present, else `core/mise`. No change.
- User shadows that today extend the built-in of the same name via the `extends: <self>` special-case (e.g. a user `mise.yaml` with `extends: mise`). Under the new rules that is a self-reference and becomes an error. The fix is mechanical: change `extends: mise` to `extends: core/mise`. Existing `tpd init`/`tpd edit` seed files are the primary source of these; re-running `tpd init` regenerates them with the correct `core/` form.

The change in `init`/`edit` output (emitting `extends: core/mise` instead of `extends: mise`) only affects newly generated files. The `tpd doctor` command (or a one-line migration note in release notes) can flag the broken pattern for existing files.

## Test surface

- `merge_test.go`: the `extends: opencode` self-shadow test switches to `extends: core/opencode` and expects the same merge; the `extends: foo` self-cycle test now also covers `extends: core/foo` from `core/foo.yaml`.
- `catalog_test.go`: `ProfileNames`/`Names` return `FullName`s; built-in entries are `core/...`.
- New tests: unqualified fallback (user `mise` shadows `core/mise`); qualified `core/mise` bypasses user; self-reference rejected for both `extends: mise` (from `core/mise.yaml`) and `extends: core/mise` (from `core/mise.yaml`); longest-prefix split with a synthetic multi-segment namespace.
- `scaffold_test.go` / `new_profile_test.go`: generated YAML contains `extends: core/...`.
- `profile_test.go` (cli): edit seed contains `extends: core/...`.

## Out of scope

- Remote import fetching (`github.com/...` resolution, caching, verification).
- Changing the user-facing display in `tpd list` (still shows unqualified names; a future change may add a SOURCE column encoding the namespace).
- Allowing user-defined namespace prefixes beyond `""` and `core`.