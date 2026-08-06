# `meta` field + generated catalog documentation

Date: 2026-08-06

## Goal

Add an optional `meta:` block to profiles and fragments carrying a human-readable `description` (and, for future use, `tags`). Meta describes the entry itself and is **not inherited through `extends:`**. Surface descriptions in `tpd list`, the `tpd init` wizard, the README built-in profiles table, and a new generated `docs/catalog.md` that embeds each entry's full source in a spoiler.

## Schema

`Profile` (`internal/profile/types.go`) gains:

```go
type Meta struct {
    Description string   `yaml:"description,omitempty"`
    Tags        []string `yaml:"tags,omitempty"`
}

// on Profile:
Meta *Meta `yaml:"meta,omitempty"`
```

- Pointer so an absent `meta` is omitted from `tpd show` and resolved YAML (same pattern as `Resources`, `Dbus`).
- `Service` (inline companion containers) gets **no** `meta`: services aren't named catalog entries, and yaml.v3 silently ignores an unknown `meta:` key there.
- Validation: `meta.description` and each `meta.tags` entry reject control characters. No other constraints — tags stay deliberately unconstrained until a consumer needs structure.

## Merge: meta is identity, not content

- `MergeProfiles` never touches `Meta`.
- `resolveChain` stamps the leaf entry's own meta onto the resolved result, right beside the existing `merged.Path = rc.Path` (`internal/profile/merge.go:135`). Resolved profiles and fragments therefore carry only their own meta; a child never inherits a base's.
- No null-to-delete handling — there is no inherited meta to delete.

## Surfacing

- **`tpd show`** (raw and `--resolved`): automatic, via `Profile.Meta`.
- **`tpd list`**: new `DESCRIPTION` column, truncated to ~60 chars with `…` (tabwriter doesn't wrap). New `Catalog.Description(displayName)` resolves the winning entry (user shadow wins) and returns its meta description.
- **`tpd init` wizard** (huh TTY pickers): option labels become `name — desc` (the value stays the display name). Applied to the built-in profile select (`promptProfileHuh`), the base-profile picker (`promptNewProfileHuh`), and the fragment multi-select (`promptFragmentsLevelHuh`). huh v1.0.0 — and the latest upstream — has no per-option descriptions, so embedding in the label is the only option; the `/` filter then matches description text too, which is a bonus. Folder-navigation options carry no description.
- **Text-mode prompts** unchanged: they are the non-TTY fallback and their exact format is asserted by tests.

## Generated documentation

- `internal/catalog/gendocs.go`: pure functions over the embedded built-in catalog:
  - `ProfilesTable()` → markdown rows for the README profiles table (profiles only): `| [<name>](internal/catalog/profiles/<name>.yaml) | <description> |`.
  - `CatalogDoc()` → `docs/catalog.md`: a profiles table (same rows) plus fragments grouped by top-level folder (`cloud`, `gui`, `infra`, `lang`, `service`, `vcs`), each entry rendered as the name (code), the description, and the full source inside a `<details><summary>Source</summary>` / yaml code fence / `</details>` spoiler. No link — the source is inline.
- `cmd/gen-catalog/main.go`: patches the README profiles table in place and writes `docs/catalog.md`. A `make docs` target runs it.
- **Stale-check test** in `internal/catalog`: `go:embed` the repo `README.md` and `docs/catalog.md`, regenerate both, fail if the committed output differs.
- **Coverage:** every built-in profile and fragment gets a `meta.description` (they are the docs' content); a test enforces it. README stays profile-only.

## Built-in catalog changes

- Add `meta:` blocks to all built-in profiles (~20) and fragments (~40) with one-line descriptions.

## Testing

- **types:** YAML round-trip of `meta` (present/absent).
- **merge:** a child extending a meta-carrying parent resolves with the child's own meta (or none), never the parent's.
- **catalog:** `Catalog.Description` resolution, including the user-shadow case.
- **docs:** staleness test; coverage test (every built-in has a description); unit tests for `gendocs` grouping/spoiler/sorting.
- **cli e2e:** `tpd list` renders the DESCRIPTION column.

## Out of scope

- Displaying `tags` anywhere (stored only; a future consumer may filter or annotate with them).
- Shell completion descriptions.
- Including user profiles/fragments in generated docs (machine-local; only built-ins are embedded).
