# `tpd init` merge option

Date: 2026-08-06

## Goal

When `tpd init` targets a profile whose user file already exists, add a **merge** action alongside the existing overwrite/abort flow. Merge combines the new selection (bases/fragments picked via the wizard or `--extends`) into the existing file while preserving the user's comments and formatting — so re-running init to add fragments never clobbers a customized profile.

## Merge semantics

The existing file is the base; the new selection is merged in (`internal/scaffold/scaffold.go`):

- `extends`: **union + dedup** — existing entries keep their position, new bases/fragments appended (canonical names).
- `command`: **kept from the existing file**; only added as the `[bash]` fallback when the existing file has no `command` *and* the merged extends provide none. A plain "new wins" would clobber the user's command with the fallback.
- `version`: `1` either way; keep the existing key.
- Every other key in the existing file (mounts, caches, tools, comments) is untouched — the merge only adds what the new selection contributes.

## Formatting / comment preservation

Implemented as a **`yaml.Node` deep-merge + re-emit** (new `mergeYAML(existing, generated []byte) ([]byte, error)` in `internal/scaffold/`):

- Parse the existing file into a `yaml.Node` tree; merge the generated content node-wise; re-encode.
- **Comments, key order, and value styles (literal/flow/quote) are preserved** — yaml.v3 round-trips `HeadComment`/`LineComment`/`FootComment`.
- The file is re-emitted, so whitespace/blank-line layout may normalize; nothing is deleted or reformatted in substance.

Merge rules node-wise: for each key of the generated mapping, add it if missing; if present in both, `extends` (both sequences) unions+dedups existing-first; everything else keeps the existing node (generated only fills gaps).

Struct-level merge + re-marshal was rejected: it wipes comments and reformats. A byte-level text-splice was rejected as over-engineered.

## Invocation

- **Wizard:** the `Overwrite? [y/N]` prompt becomes a three-way `Overwrite / Merge / Abort` (huh Select on a TTY, with **Abort selected by default** so a stray Enter never touches the existing file; `o`/`m`/`a` text input otherwise).
- **`--merge` flag** on `tpd init`: non-interactive merge into an existing file; with no existing file it behaves as a normal generate. `--merge` and `--force` are mutually exclusive (error if both).
- Dry-run and the "not runnable yet" warning apply to the merged result.

## Testing

- Unit tests for `mergeYAML`: extends union + dedup + existing order, command kept, missing keys added, comments preserved across the round-trip.
- Wizard tests: three-way prompt (abort skips, overwrite replaces, merge unions).
- CLI tests: `--merge` merges, `--force --merge` errors, `--merge` with no existing file generates normally.
- Existing overwrite-prompt tests updated for the new three-way input (`o`/`a` instead of `y`/`n`).
