# Hierarchical names for profiles and fragments

Date: 2026-08-05
Supersedes: the namespace-prefix parts of `2026-08-03-namespaces-design.md` (the `core/` and empty-namespace model stays; the "single-segment local name" invariant is removed).

## Goal

Let user and built-in content organize into subfolders and be addressed by their path (`lang/go`, `services/podman`, `lang/js/node`). Rather than registering directories as namespace prefixes, make the relative path of a file under the profiles/fragments root *be its name*. `core` remains the only built-in namespace prefix, plus future remote namespaces. A user file may not use a registered namespace prefix as its first path segment.

Motivation: the directory-is-namespace approach forces a namespace registry derived from directory structure, multi-segment namespaces like `core/lang`, and fallback tables. Hierarchical names eliminate all of that — a name is just a path.

## Name model

- User `~/.config/tpd/profiles/lang/go.yaml` → `Namespace: ""`, `Name: "lang/go"` → FullName `lang/go`.
- Built-in `internal/catalog/fragments/lang/go.yaml` → `Namespace: "core"`, `Name: "lang/go"` → FullName `core/lang/go`.
- User `~/.config/tpd/profiles/lang/js/node.yaml` → `Namespace: ""`, `Name: "lang/js/node"` → FullName `lang/js/node` (arbitrary depth).
- `Name` is the path relative to the profiles/fragments root minus the `.yaml` extension. It may contain `/`; each segment must match `profileNameRe` and must not be `..`. `DisplayName()` returns `Name` unchanged, so it equals the addressable form for both user and built-in entries.

## Resolution

`ParseRef` (`internal/profile/ref.go`):
- No `/` → `Ref{"", s}` (unchanged).
- `/` present → longest registered prefix match at a segment boundary; the remainder is the local name and may now be multi-segment. `core/` with an empty remainder stays an error.
- No registered prefix matches → the whole string is an unqualified hierarchical name: `Ref{"", s}`. (Previously this was an "unknown namespace" error.)

`ResolveRef` keeps the single uniform rule: try `FullName`, else `core/` + `FullName`, skipped when the namespace is already `core`. So `lang/go` → user `lang/go`, else built-in `core/lang/go`; `core/lang/go` → built-in directly; `mise` → user `mise`, else `core/mise`. Unqualified names never match a namespaced entry (qualified-only).

## Reserved namespace prefixes

A user file whose first path segment is a registered namespace prefix is rejected at load (strict) / warned-and-skipped (tolerant). `core` is always registered; a future remote prefix (e.g. `github.com/foo/bar`) is registered when fetched. Error text: "`core` is a reserved namespace prefix". User shadowing of built-ins is unaffected — create `fragments/lang/go.yaml` to shadow `core/lang/go`; unqualified resolution prefers the user entry.

A bare user file named `core.yaml` (name `core`, no slash) is fine; the reservation applies to the first *segment* of a multi-segment name only.

## Catalog API

- `DisplayName()` stays `rc.Name` (now multi-segment), so `DisplayNames()`/`ProfileDisplayNames()` dedup by addressable name, user wins over core (existing logic unchanged).
- `Source(name)` checks `entries[name]` and `entries["core/"+name]` (unchanged).
- `Get`, `IsFragment`, `fragments` map continue to use canonical FullName keys.
- The cross-type display-name collision rule stays, keyed on the addressable Name: a Name may not be both a fragment and a profile across namespaces (e.g. user profile `lang/go` vs built-in fragment `core/lang/go`).

## Loading

- `loadUserDir`/`loadUserFragments` (`internal/profile/catalog.go`): derive `Name` from the path relative to the root (trim `.yaml`), stamp `Namespace: ""`, validate each segment, reject a reserved first segment.
- `loadBuiltins`/`loadBuiltinFragments`: same derivation, stamp `Namespace: "core"`.
- `builtinNamespaces` stays `{"": true, "core": true}`; no directory scan to build a registry.
- `internal/catalog/embed.go`: the fragments embed becomes recursive (`//go:embed fragments`); profiles stays `profiles/*.yaml` (flat). `fs.WalkDir` already recurses in the loaders.
- The `fragments` subfolder skip inside the profiles dir is retained (unchanged behavior).

## CLI

- `displayName(key)` (`cmd/tpd/cli.go`) now strips a leading `core/` (returns the addressable path) instead of taking the last segment. Used for file-path derivation and built-in seed reads. `tpd edit core/lang/go` seeds/edits `~/.config/tpd/fragments/lang/go.yaml` with `extends: core/lang/go`.
- Advisory lookups pass the leaf segment (`filepath.Base` of the addressable name); advisory keys stay leaf-based and remain unique in the restructured catalog.
- `runList` kind detection resolves each display name via user-then-core (was `Get("core/"+dn)` / `Get(dn)`).
- Container name and hostname (`internal/runtime/docker_run.go`): sanitize `spec.ProfileName` by replacing `/` with `-` (still Docker-charset-valid). Labels and `--dry-run` output keep the qualified name.

## Scaffold/init

- `FragmentNames()` returns multi-segment display names for the picker (`lang/go`, `services/podman`).
- `FragmentByDisplayName(name)` resolves user-then-core by the full name.
- Emitted `extends:` keep the current canonical form: built-in fragments become `core/lang/go`; built-in profiles stay `core/mise`.

## Built-in restructure

Move fragment YAMLs into subfolders (profiles stay flat):

- `lang/`: dotnet, elixir, go, haskell, java, javascript, julia, kotlin, ocaml, perl, php, python, ruby, rust, scala, typescript, zig
- `services/`: docker-host, podman-host, podman, kubernetes, helm, terraform
- `gui/`: gui, gui-runtime
- `cloud/`: aws, azure, gcloud
- `vcs/`: github, gitlab
- `creds/`: ssh, netrc, gitconfig, bashrc, vault

Update the one fragment→fragment extends (`fragments/lang/typescript.yaml` → `extends: core/lang/javascript`) and the built-in profiles that extend moved fragments (`gui`/`gui-runtime` become `gui/gui`/`gui/gui-runtime` in `buzz.yaml`, `t3code.yaml`, `opencode-desktop.yaml`). The taxonomy above is a proposal; adjust names while implementing.

## Migration

- Existing unqualified `extends: <moved-fragment>` in user files (e.g. `extends: javascript`) stops resolving; `tpd doctor`'s profile-validity check reports it. Fix: `extends: lang/javascript`.
- No user-dir layout change required unless the user moves files.
- No back-compat aliases for moved built-in fragments (deliberate; names are globally unique).

## Test surface

- `catalog_test.go`: built-in fragment lookups/resolution move to `lang/...` (e.g. `core/lang/javascript`, `ResolveFragment(cat, "lang/javascript")`); `core/lang/typescript` extends `core/lang/javascript`.
- `ref_test.go`: `ParseRef` accepts multi-segment local names (`core/lang/go`), treats unmatched slash strings as unqualified hierarchical names, still rejects `core/` (empty local), and rejects `corex/...` reserved-prefix misuse at load.
- New: user subfolder load (`profiles/lang/go.yaml` → name `lang/go`), nested (`lang/js/node`), reserved `core` first segment rejected, user `lang/go` shadows `core/lang/go`.
- CLI tests: `tpd edit core/lang/go` seeds `fragments/lang/go.yaml`; list shows `lang/go`; container name sanitization for a slashed profile name.
- Scaffold tests: picker/emitted extends for a namespaced built-in fragment.

## Out of scope

- Remote namespace fetching (the split mechanism only).
- Removing the `fragments` subfolder skip.
- Back-compat aliases for moved built-in fragments.
