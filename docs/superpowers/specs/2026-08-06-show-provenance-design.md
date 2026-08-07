# `tpd show --provenance` — per-file config attribution

## Problem

`tpd show --resolved` prints the fully merged profile with no indication of where
each key came from. When a user profile shadows a built-in via an unqualified
`extends` (e.g. the built-in `core/t3code` extends `claude`, and a user
`~/.config/tpd/profiles/claude.yaml` wins the resolution), the surprise is
invisible: `tpd show --resolved core/t3code` silently includes tools the built-in
never declared. Users need a view that breaks the resolved profile down by the
file that contributed each piece.

## Goal

Add `tpd show --provenance <name>` that prints one section per catalog entry in
the resolved chain — root first, then dependencies — where each section is that
entry's own `extends` plus only the keys it **owns** in the final merge.
Overrides hide the parent's key entirely; a child that overrides `tools.claude`
means `core/claude`'s `claude: latest` is not shown.

## Behavior

- `--provenance` implies resolution (no need for `--resolved`).
- Works for profiles and fragments; errors identically on unknown names and
  cycles (same message and exit code as `--resolved`).
- Output is a diagnostic view and is not intended to re-parse as one YAML
  document.

### Output shape

```
# core/t3code  (built-in:profiles/t3code.yaml)
extends: claude
command:
  - t3code
  - --no-sandbox
tools:
  appimage:pingdotgg/t3code: latest

# claude  (/home/jgillich/.config/tpd/profiles/claude.yaml)
extends: core/claude

# core/infra/kubernetes  (built-in-fragment:fragments/infra/kubernetes.yaml)
tools:
  kubectl: latest

# core/claude  (built-in:profiles/claude.yaml)
tools:
  claude: latest

# core/mise  (built-in:profiles/mise.yaml)
image: debian:13-slim
packages:
  - mise
  # ...remaining owned keys elided for brevity
```

- Header: `# <full-name>  (<source path>)`. The header uses the **FullName**
  (e.g. `core/t3code`, not the bare `t3code`) because that is the contributor
  identity the provenance is grouped by, and it disambiguates a user `claude`
  from the built-in `core/claude`. The source path is where the entry lives, so
  shadowing is obvious at a glance: a user entry shows its real file path, a
  built-in shows its embedded `built-in:` / `built-in-fragment:` path (the two
  prefixes are the ones the loader stores, catalog.go:414 and :653).
- Body: the entry's own `extends` as declared, plus only the keys attributed to
  it (see Attribution). Validation requires every resolved profile to have
  `image` and `command` (validate.go:42-47), so they always appear — owned by the
  entry that last set them (`command` under `core/t3code`, `image` under
  `core/mise` above).
- `kubectl` appears under `# core/infra/kubernetes`, the file that literally
  declares it — the shadowing story is told by the chain (user `claude.yaml`
  extends it) and the header path.

## Attribution rules

Per-key last writer wins, computed by the existing merge pipeline (extended to
all fields) so the breakdown can never disagree with the resolved profile:

| Field | Rule |
| --- | --- |
| `tools`, `caches`, `repos`, `files`, `labels` | last writer per key; child wins; null deletes |
| `mounts`, `env`, `ports`, `devices`, `dbus`, `services` | existing provenance (unchanged semantics) |
| `resources` | per-subfield last writer (`memory`, `cpus`), like `dbus.talk`/`dbus.own` |
| `packages` | first declarer per package; a whole-field `packages: null` resets the declarer set, so a later entry that re-declares a package owns it |
| `image`, `command`, `tty`, `network` | single owner: the last entry that set it |
| `extends` | each entry shows its own declared extends |
| `version` | never shown (noise; it is merged — child wins when non-zero — just not displayed) |
| `meta` | never shown (not merged) |

Note on package ordering: "first declarer" means first in **merge** order. The
merge runs DFS post-order (a child's chain is fully merged before its parent's
own body), so an ancestor can declare a package after its descendant and still
be its first declarer. Sections render in pre-order, so a package the root
declares may appear attributed to a deeper section that declared it first. This
is correct per append+dedup semantics, but the render/merge order mismatch is
intentional.

## Implementation

1. **`internal/profile/provenance.go`** — extend `Provenance` with:
   - `Tools`, `Caches`, `Repos`, `Files`, `Labels map[string]Contributor`
   - `Packages map[string]Contributor`
   - `Image`, `Command`, `TTY Contributor`
   - `Resources` (per-subfield `Memory`, `CPUs Contributor`), like
     `DbusProvenance`.
   - Attribution flows through the same two paths as today: `initProvenance`
     stamps a leaf entry's own keys, and `mergeProvMap`'s `childContrib`
     fallback attributes non-leaf own-keys. `initProvenance` only runs for leaf
     entries (merge.go:94); the wording above covers both.
2. **`internal/profile/merge.go`** — in `MergeProfiles`:
   - `mergeProvMap` for tools/caches/repos/files/labels (same child-wins +
     null-delete logic as today).
   - first-declarer helper for packages; a whole-field `packages: null`
     (`nullKeys["*"]`) resets the declarer set before merging the child list.
   - scalar attribution for image/command/tty (same pattern as `Network`), and
     per-subfield attribution in the `resources` merge block.
3. **`internal/profile/merge.go` `resolveChain`** — collect chain entries
   (FullName, source path, own extends) into `Resolved.Chain` in DFS pre-order.
   Dedup must use a **separate, whole-resolution `chainSeen` set** that persists
   for the duration — distinct from the cycle-detection `seen` stack (which is
   unwound per-path, merge.go:110-111) and from the per-level `resolved` map
   (which only dedups siblings). A parent shared across two sibling subtrees is
   otherwise rendered twice.
4. **`internal/profile/breakdown.go`** (new) — render each chain entry as a
   section: header + own extends + keys grouped from `Resolved.Prov` by
   contributor. Sort keys within a section (Go map iteration is random), and
   sort sections by chain order (deterministic) so output and tests are stable.
5. **`cmd/tpd/cli.go`** — add `--provenance` flag to `show`, sharing the
   `resolveCatalogName` + profile/fragment branching that `--resolved` already
   uses, so profiles and fragments behave identically.

## Edge cases

- **Deletions (`key: null`)** are omitted; we do not track who deleted.
- **Shared parents** render once, at first occurrence.
- **Fragments** render the same way (they lack `image`/`command` because those
  are profile-level identity).
- Resolution errors behave identically to `--resolved`.

## Testing

- Unit (`internal/profile`): provenance extension — tools/caches/repos/files
  last-writer, packages first-declarer including the whole-field-null reset,
  scalar ownership, resources per-subfield; breakdown rendering — shadowing
  scenario (user `claude.yaml` leaking into `core/t3code`), override-hides-
  parent, null-delete, fragment attribution, shared-parent dedup (two sibling
  subtrees sharing a base renders it once), chain ordering, deterministic output
  (sorted keys).
- CLI e2e (`cmd/tpd`): `tpd show --provenance` output shape, help text, unknown
  name, fragment names.
- Regression: existing approval/provenance tests pass (struct gains fields;
  semantics unchanged).
