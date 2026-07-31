# Multi-extends, fragments, and profile introspection

Date: 2026-07-31

## Motivation

Profiles currently use a single `extends` string and inline preset contents at
`init` time. When presets are updated, existing user profiles go stale — the
user must re-run `init` or manually sync. The current `init` also inlines
preset contents, which means the generated file is a snapshot, not a live
composition.

Multi-extends makes presets (renamed "fragments") first-class entries in
`extends:`, so profiles stay live-linked to fragment updates. A new `profile`
subcommand provides introspection (`show --resolved`). `init` is rethought as
a profile bootstrapper with a summary and opt-in resolved view.

Fragments are intended to be small, composable building blocks. They should
represent a single concern (for example `ssh`, `go`, `node`, `docker`) rather
than complete development environments.

## Changes

### 1. Rename presets → fragments

Rename throughout: `internal/catalog/presets/` → `internal/catalog/fragments/`,
`scaffold.Presets()` → `scaffold.Fragments()`, `PresetNames()` →
`FragmentNames()`, `--presets` flag → `--fragments` (with `--presets` kept as a
hidden alias for backward compat). README and help text updated. No behavior
change — fragments are still mergeable profile fragments that forbid
`extends`/`image`/`command`/`version`.

### 2. Multi-extends

`extends` accepts a string (back-compat) or a list:

```yaml
extends: opencode          # old form, still works
extends: [opencode, ssh]   # new form
```

**Merge order:** left-to-right, profile body wins last.
`extends: [A, B]` → `merge(merge(A, B), body)`. Later extends override earlier
on conflicting keys; the body overrides all.

**Nested graphs are resolved depth-first.** If A extends C and B extends D,
the merge order is `C, A, D, B, body` — each parent's own extends are fully
resolved before the next sibling is merged.

**Duplicate references:** every referenced profile or fragment is merged
exactly once. If the same name appears twice (directly or via nested extends),
the first resolution wins and subsequent references are ignored.

**Cycle detection:** `resolveChain` tracks all seen names across the full
multi-extends traversal. Cycles are rejected with the same error format as
today.

**Type:** `Extends` becomes a custom type (`ExtendsList`) that implements
`yaml.Unmarshaler` to accept both `string` and `[]string`. Internally stored
as `[]string`. A single string is normalized to a one-element slice.

### 3. Fragment name resolution

Name resolution searches both profiles and fragments. Names must be globally
unique — if a profile and fragment share the same name (across built-in + user
files), the catalog load rejects with an error. One name = one thing.

User fragments live in `~/.config/toolpod/fragments/` (parallel to
`~/.config/toolpod/profiles/`). Built-in fragments are embedded in
`internal/catalog/fragments/`.

Fragments are still validated to forbid `extends`/`image`/`command`/`version`.
They're mergeable fragments, not mini-profiles.

### 4. `profile` subcommand

New top-level subcommand:

- **`toolpod profile show <name> [--resolved]`** — prints the profile YAML.
  Without `--resolved`, shows the raw file as written (for built-in profiles,
  shows the embedded YAML exactly as shipped). With `--resolved`, inlines all
  extends and prints the fully merged profile (same as what the runtime sees).

- **`toolpod profile edit <name>`** — opens the user profile in `$EDITOR`.
  If no user profile exists, errors: "This is a built-in profile. Run
  `toolpod init <name>` to create a user override." Trivial implementation
  (~10 lines).

- **`toolpod profile list`** — lists all profiles (built-in + user). Each
  entry is marked as `user` (has a user override file) or `built-in`:
  ```
  opencode    user
  claude      built-in
  shell       built-in
  ```

Future: `toolpod profile graph <name>` — prints the extends tree. Noted but
not implemented in this round.

### 5. init redesign (profile bootstrapper)

**What init writes:**
```yaml
version: 1
extends: [opencode, ssh, npm, go]
```
No inlined fragment contents. The file is clean and live-linked.

**The init flow:**

1. User runs `toolpod init [profile] --fragments ssh,npm,go`.
2. init resolves the full extends chain and generates a summary:

   ```
   Profile: opencode
   Fragments: ssh, npm, go

   Container access:
     • mounts ~/.ssh
     • mounts ~/.gitconfig
     • installs node
     • installs go
     • caches ~/.npm
   ```

3. **If the resolved profile grants access to host resources (mounts or
   environment variables):** prompt: "Review the resolved profile? [y/N]".
   If yes, write the resolved YAML to a temp file and open it in `$EDITOR`.
   After the editor exits, ask: "Proceed with generating this profile?
   [y/N]". If no, abort without writing.

4. **If not (only caches and tools):** write the file directly, print the
   summary.

5. Write `~/.config/toolpod/profiles/<name>.yaml` with `extends: [...]`.

### 6. Backward compatibility

- `extends: opencode` (string) still works — the parser accepts both forms.
- Existing inlined profiles from old `init` runs still work — the fields are
  literal body values that merge last and win.
- `--presets` flag kept as hidden alias for `--fragments`.
- No auto-migration of existing files. Users who want live-linked fragments
  re-run `init` or edit manually.

## Files touched

- `internal/profile/types.go` — `Extends` → `ExtendsList` (custom unmarshaler)
- `internal/profile/merge.go` — `resolveChain` walks a list; cycle detection
  across all entries
- `internal/profile/catalog.go` — load fragments alongside profiles;
  add `~/.config/toolpod/fragments/` loading; reject name collisions
- `internal/scaffold/scaffold.go` — `generate()` writes `extends: [...]`;
  add summary generation; add editor-prompt flow; rename functions
- `internal/catalog/` — rename `presets/` → `fragments/`
- `cmd/toolpod/cli.go` — add `Profile` command struct with `show`/`edit`/`list`
  subcommands; rename `--presets` → `--fragments`
- Tests across all packages
- README updates

## Testing

- `ExtendsList` unmarshal: string, list, empty, invalid types
- `resolveChain` with multi-extends: order, overrides, nested depth-first,
  duplicates ignored, cycles, mixed string/list
- Catalog: fragment+profile name collision rejection
- `profile show --resolved`: correct merged output for multi-extends;
  built-in profile shows embedded YAML
- `profile edit`: built-in profile errors with hint
- `profile list`: user/built-in marking
- `init` summary: host-resource detection, container-access listing
- `init` editor prompt: review → approve, review → decline, no-mounts skip
- Existing inlined profiles still resolve correctly
- Single-string `extends` still works