# Inline fragments

Date: 2026-08-04

## Motivation

Profiles and fragments are split across two directories today even when they
describe one conceptual unit. `bash.yaml` (profile) and `bashrc.yaml`
(fragment) ship the same shell session but live in separate files and drift
apart: `tpd bash` pulls in no bashrc mounts unless the user manually composes
them via `tpd init mybash --extends bash,bashrc`. The same applies to every
agent profile's config mounts — `opencode.yaml`'s `~/.config/opencode`,
`~/.cache/opencode`, `~/.local/share/opencode` are an undifferentiated part
of the profile body, so there is no clean way for a user to run `tpd opencode`
with those mounts disabled (a hermetic / sandboxed run).

Inline fragments solve both problems. A profile carries its toggleable
fragments in the same file, behind an `enabled: true|false` field. Default
profiles stay useful; a one-line edit produces a hermetic variant. Discovery
is automatic — `tpd show` lists the fragments a profile exposes.

Fragments in `extends:` today mix two distinct roles: base profiles that
carry identity (`mise`) and add-on bundles that carry mounts/caches/tools
(`ssh`, `gui`). Splitting those roles is long overdue. Under this design:

- **Top-level `extends:`** is profiles-only. It carries base/identity
  composition (`extends: mise`, or `extends: [mise, codex, claude]` for
  profiles that compose other profiles like `buzz` and `t3code`).
- **`fragments:`** is the only way to opt into add-on bundles, whether the
  bundle is defined inline next to the profile or pulled from a standalone
  fragment file.

There is no backward-compatibility path: fragments-in-`extends` becomes a
hard schema error. Built-ins are rewritten; user profiles extending
fragments must be updated. The tool is early enough that a hard break is
cheap and the end state (one way to do each thing) is worth it.

## Design

### New profile field: `fragments:`

A new top-level field on `Profile` and `RawProfile`:

```yaml
version: 1
extends: mise
command: ["bash", "-l"]
fragments:
  bashrc:
    enabled: true
    mounts:
      ~/.bashrc: { source: ~/.bashrc, optional: true }
      ~/.bash_profile: { source: ~/.bash_profile, optional: true }
      ~/.bash_aliases: { source: ~/.bash_aliases, optional: true }
      ~/.profile: { source: ~/.profile, optional: true }
      ~/.inputrc: { source: ~/.inputrc, optional: true }
    tools:
      shellcheck: latest
```

Each entry is an `InlineFragment`:

```go
type InlineFragment struct {
    Enabled bool      `yaml:"enabled"`
    ExtendsList       // fragment-only extends; resolves to fragments
    Profile           // the fragment body: mounts/caches/tools/env/packages/repos/files/labels/ports/devices/dbus — NOT image/command/version
}
```

An inline fragment is **local to its defining profile**. It is not a catalog
entry, it carries no namespace, and it is not referenceable by other
profiles. There is no global uniqueness constraint on inline fragment names
across profiles — `claude` and `opencode` may both define a local `config`
fragment. To avoid clashes *within* a profile, two inline fragments in the
same `fragments:` block MUST NOT share a name (schema error).

### Two shapes of `fragments:` entry

A `fragments:` entry is either a **reference** or a **definition**, decided
by whether a body is present beyond `enabled:`/`extends:`:

- **Reference** (body absent): `fragments: ssh: { enabled: true }` enables
  the standalone fragment of that name. The standalone is lazy-loaded from
  disk/embed at resolution time, not at catalog load. Validation of the
  standalone is deferred to when it is actually enabled.
- **Definition** (body present): `fragments: bashrc: { enabled: true,
  mounts: ... }` is a local fragment. The body is the fragment content.

**If a definition's name matches a standalone fragment, the definition's
body is merged onto the standalone (k8s-style, same semantics as all other
fields: scalars child-wins, maps key-by-key with null-to-delete, packages
append+dedup, `command`/`image`/`version` rejected).** A definition never
*replaces* a standalone — it only adds to or overrides it. This keeps the
merge uniform across the whole profile (one merge rule for every field,
which the codebase already enforces elsewhere).

A definition with `extends:` additionally pulls in the listed fragments
first, then merges the standalone (if name matches), then merges the inline
body on top. Merge order for an inline fragment named `X` with
`extends: [A, B]` (this is the fragment's *internal* resolution, before it
is merged into the profile):

```
merge(merge(merge(A, B), standalone_X), inline_body)
```

i.e. within the fragment, the inline body wins over the standalone, which
wins over the fragment's own extends chain — body-wins-last, same as
profile resolution. The fully resolved fragment is then merged into the
profile body as described in the resolution algorithm below (where the
profile body still wins last over all its fragments).

### `extends:` is profiles-only

`extends` on a profile accepts only profiles. Listing a fragment is a schema
error at validation time:

```yaml
extends: [mise, ssh]   # ERROR: "ssh" is a fragment, not a profile
```

Resolution: `resolveChain` already checks `cat.IsFragment(pkey)` for the
fragment-extends-fragment case; the profile-extends-fragment case becomes a
hard error there. Built-in profiles currently extending fragments (`buzz`,
`t3code`, `opencode-desktop`) are rewritten to use `fragments: { gui:
{enabled: true}, gui-runtime: {enabled: true} }`.

### Fragment-fragment `extends` is unchanged

Standalone fragments may still extend other standalone fragments. The one
existing case (`typescript extends core/javascript`) keeps working. The
rule is uniform and simple:

- `extends:` on a **profile** → profiles only.
- `extends:` on a **fragment** (standalone or inline) → fragments only.

An inline fragment's `extends:` resolves against the fragment catalog and
follows the same depth-first, left-to-right, body-wins-last merge as today.

### Standalone fragments are lazy-loaded

Today `LoadProfiles` parses every standalone fragment up front so they are
available for `extends`. Under the new model fragments are only reachable
through `fragments:`, so they are parsed on demand:

- **Name index (cheap, built up front):** the set of fragment *names* is
  derived from filenames in `internal/catalog/fragments/` (embedded) and
  `~/.config/tpd/fragments/` (user). This is all `tpd` needs for shell
  completion of `tpd init` prompts and for validating that an enabled
  reference points at a known fragment.
- **Content (parsed on demand):** a standalone fragment file is read and
  parsed only when some profile enables it (directly, or transitively via
  another fragment's `extends:`). Disabled standalones are never parsed,
  never validated, never shown in `tpd show`.

Shadowing works as today: a user file `~/.config/tpd/fragments/bashrc.yaml`
shadows core `bashrc` — but only evaluated when `bashrc` is enabled
somewhere. The global-unique-name rule (a display name is either a profile
or a fragment across namespaces, never both) is unchanged; it is enforced
at load time against the name index, before any content is parsed.

A malformed user fragment therefore only fails when it is enabled, not at
catalog load. `tpd doctor` grows a "validate all known fragments" check so
users can spot a broken one before they need it.

### Resolution algorithm

For a profile `P` with top-level `extends: [B1, B2]` (profiles) and
`fragments: { f1: {enabled: true, ...}, f2: {enabled: false, ...},
f3: {enabled: true, ...} }`:

1. Resolve `P`'s top-level `extends` chain as today (profiles only), merging
   left-to-right, body-wins-last. Produces `merged_body`.
2. For each entry in `P.fragments` in declaration order:
   - If `enabled: false`, skip entirely. The entry is not loaded, not
     merged, not shown.
   - If `enabled: true`:
     - If it is a **reference** (no body): resolve the standalone fragment
       of that name (lazy-load, resolve *its* extends chain, merge).
     - If it is a **definition** (body present): if the name matches a
       standalone, resolve the standalone first; then merge the inline body
       on top (and the inline's own `extends:` first, before the
       standalone).
3. Merge each enabled fragment into `merged_body` in declaration order,
   as if they were additional `extends` entries appended after the
   profile's declared extends. The profile's own body is merged *last*,
   winning over both its extends and its enabled fragments — exactly as
   today, where `merge(extends..., body)` makes the body win. So an
   enabled fragment can add keys the body doesn't touch, but a key the
   body declares explicitly is never overridden by a fragment. The
   sandbox use case works because the migration *moves* the config mounts
   out of the body and into the fragment (not duplicated): the body no
   longer declares them, so the fragment is the sole source, and
   disabling it removes them.

The result is then validated as today.

### `tpd init` simplification

`tpd init` drops its non-interactive path. `tpd init` without args is the
only form: interactive prompts (existing wizard in `internal/scaffold/`).
The `--extends` and `--force`/`--dry-run` flags are removed. The wizard's
fragment-selection step writes `fragments: <name>: { enabled: true }`
entries into the generated file for any standalone fragments the user
selects. A user who wants a sandboxed variant edits the file and flips
`enabled: false`.

### Built-in migrations

**Move inline (per-profile, co-located + toggleable):**

- `bashrc.yaml` → inline in `bash.yaml` as `fragments: bashrc: { enabled:
  true, ... }`. Delete standalone `bashrc.yaml`. Nothing extends it today
  (grep confirms), and a user profile extending `bash` reuses bash's
  inline `bashrc` via the merge: bash's `bashrc` fragment merges into bash's
  body, so a profile `extends: bash` inherits the bashrc mounts as part of
  bash's resolved body. (Inlining does not lose reuse — `extends: bash`
  still gets everything bash resolves to.)
- Each agent profile's config mounts become an inline fragment in that
  profile, named `<agent>-config` to avoid cross-profile name clashes
  within any future composed profile:
  - `claude`: `fragments: claude-config: { enabled: true, mounts:
    {~/.claude, ~/.cache/claude-code} }`
  - `opencode`: `fragments: opencode-config: { enabled: true, mounts:
    {~/.config/opencode, ~/.cache/opencode, ~/.local/share/opencode} }`
  - `gemini`, `codex`, `copilot`, `amp`, `crush`, `qwen`, `pi`: each gets a
    `<agent>-config` inline fragment for its config dir.
  - `powershell`: `fragments: powershell-config: { enabled: true, mounts:
    {~/.config/powershell} }`
  - `bash`: its `files:` (`/etc/profile.d/mise.sh`) stays in the body
    (not a toggleable concern; the bashrc mounts are the toggleable part).

  The `<agent>-config` naming avoids collision when a profile composes
  multiple agents (e.g. `buzz` extends `codex`+`claude`); without the
  prefix, two `config` fragments would clash once composed.

**Rewrite `extends` (fragments move out):**

- `buzz`: `extends: [mise, gui, gui-runtime, codex, claude]` → `extends:
  [mise, codex, claude]` + `fragments: { gui: {enabled: true}, gui-runtime:
  {enabled: true} }`.
- `t3code`: `extends: [mise, gui, gui-runtime, opencode, claude]` →
  `extends: [mise, opencode, claude]` + `fragments: { gui: {enabled: true},
  gui-runtime: {enabled: true} }`.
- `opencode-desktop`: `extends: [mise, gui]` → `extends: mise` +
  `fragments: { gui: {enabled: true} }`.

**Stay standalone (cross-cutting):** `ssh`, `gitconfig`, `netrc`, `github`,
`gitlab`, `aws`, `azure`, `gcloud`, `docker`, `podman`, `kubernetes`,
`helm`, `terraform`, `vault`, `security`, `gui`, `gui-runtime`, and all
language fragments (`go`, `python`, `rust`, `java`, `javascript`,
`typescript`, `ruby`, `php`, `perl`, `elixir`, `haskell`, `julia`,
`kotlin`, `ocaml`, `scala`, `zig`, `dotnet`).

### Files

- `internal/profile/types.go`: add `InlineFragment` type and
  `Fragments map[string]InlineFragment` field on `Profile`/`RawProfile`.
  Extend `collectNullKeys` to track nulls inside inline fragment bodies.
- `internal/profile/validate.go`: forbid fragments in profile `extends`;
  enforce no-duplicate-names within a `fragments:` block; reject
  `image`/`command`/`version` in inline fragment bodies.
- `internal/profile/merge.go`: integrate inline fragment resolution into
  `resolveChain`; lazy-load standalones on enable; merge definitions onto
  standalones.
- `internal/profile/catalog.go`: split fragment loading into a name index
  (built at `LoadProfiles`) and content parsing (on demand). Keep the
  cross-type display-name collision check, run against the name index.
- `internal/catalog/profiles/*.yaml`: rewrite per the migration above.
- `internal/catalog/fragments/bashrc.yaml`: delete.
- `cmd/tpd/cli.go`: remove `init` non-interactive flags (`--extends`,
  `--force`, `--dry-run`); keep `tpd init` interactive-only.
- `internal/scaffold/`: fragment selection in the wizard writes
  `fragments: <name>: { enabled: true }`.
- `internal/doctor/`: add "validate all known fragments" check.
- Tests across `internal/profile/` and `pkg/tpd/` for the new merge cases
  (reference, definition, definition-onto-standalone, transitive
  fragment-extends, no-duplicate-names, fragments-in-extends-rejected).

### Out of scope

- A CLI toggle (`--enable`, `--set fragments.x.enabled=false`) for
  overriding fragment enable state at launch time without editing the
  profile file. The launch command's `SetInterspersed(false)` makes any
  positional-flag toggle awkward, and the field-driven model already covers
  the common case (edit the file once). Defer to a separate design if
  needed later.
- Migrating any user-written profiles. Hard break; users update by hand.
- A `tpd fragments list` command. Discovery is via `tpd show <profile>`
  (enabled fragments for that profile) and shell completion of `tpd init`
  prompts.

## Open questions

1. **`tpd show` output shape.** A profile's `fragments:` block should be
   visible in `tpd show <name>` (raw form) and `tpd show <name> --resolved`
   (enabled fragments inlined, disabled omitted). The exact rendering
   (list vs map, enabled marker) is left to implementation review.

2. **Inline fragment body field set.** An inline fragment body uses the
   same `Profile` struct but must reject `image`/`command`/`version`
   (`validateFragmentName` already does this for standalones; inline needs
   the equivalent). Confirm the rejected set matches the standalone set
   exactly.