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

### `@name` qualified reference syntax

A new uniform syntax for explicitly referencing a core-namespace entry,
usable in both profile `extends:` and fragment `extends:`. `@bash` is
shorthand for `core/bash`: a direct core lookup with no user-first
fallback. This replaces the `core/<name>` form in built-in files, which
is verbose and inconsistent with the bare unqualified names used
elsewhere.

```yaml
# in a user profile bash.yaml that shadows core/bash:
extends: @bash        # explicitly extend core/bash, not itself

# in a fragment:
fragments:
  bashrc:
    enabled: true
    extends: @bashrc   # inherit the core/bashrc standalone, then merge body
    mounts: { ... }
```

The bare unqualified name (user-first-then-core fallback) remains valid
for profile `extends:`. For fragments, the bare name in a fragment
`extends:` is also valid (resolves user-first-then-core). `@name` is the
explicit core-qualified form for both.

The one existing qualified built-in ref (`typescript extends
core/javascript`) migrates to `extends: @javascript`.

### New profile field: `fragments:`

A new top-level field on `Profile` and `RawProfile`:

```yaml
version: 1
extends: mise
command: ["bash", "-l"]
fragments:
  bashrc:
    enabled: true
    extends: @bashrc
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
    Enabled bool        `yaml:"enabled"`
    ExtendsList         // fragment-only extends; resolves to fragments
    Profile             // the fragment body: mounts/caches/tools/env/packages/repos/files/labels/ports/devices/dbus — NOT image/command/version
}
```

An inline fragment is **local to its defining profile**. It is not a
catalog entry, it carries no namespace, and it is not referenceable by
other profiles. There is no global uniqueness constraint on inline
fragment names across profiles — `claude` and `opencode` may both define
a local `config` fragment. Two inline fragments in the same `fragments:`
block MUST NOT share a name (schema error).

### `fragments:` entry shapes

A `fragments:` entry always has a body (the fragment content), plus an
optional `extends:` that inherits a standalone before merging the body on
top. There is **no boolean-reference form** (`bashrc: true` is a schema
error — it would do nothing useful; the user must write `bashrc: { enabled:
true, extends: @bashrc }` or inline the content directly).

The shapes:

- **`{ enabled: true, extends: @bashrc }`** — inherit the core `bashrc`
  standalone, no local additions. (A user file `~/.config/tpd/fragments/
  bashrc.yaml` shadows core `bashrc` via the user-first-then-core
  fallback; `extends: bashrc` (no `@`) would pick up the user file if
  present.)
- **`{ enabled: true, extends: @bashrc, mounts: { ... } }`** — inherit
  the core `bashrc` standalone, merge the inline body on top (body wins
  on conflicts). This is "customize the shared bundle locally."
- **`{ enabled: true, mounts: { ... } }`** (no `extends`) — **local
  definition; shadows any same-named standalone entirely.** The
  standalone of that name is not loaded for this profile. Replace
  semantics, mirroring how a user profile shadows a core profile of the
  same name. This is "co-locate a profile-specific bundle."
- **`{ enabled: true, extends: ssh, mounts: { ... } }`** — local
  fragment named e.g. `my-ssh`, inheriting the `ssh` standalone and
  adding local content. (Name doesn't match `ssh`, so no shadowing of
  `ssh`.)
- **`{ enabled: false, ... }`** — disabled. The entry is not loaded, not
  merged, not shown. The body and `extends:` are still parsed for
  validation. This lets a child profile override a parent's `enabled:
  true` without re-declaring the body: the child just sets `fragments:
  bashrc: { enabled: false }`.

The merge rule is uniform: `extends:` resolves first (depth-first,
left-to-right), then the inline body merges on top (body-wins-last) —
identical to profile resolution. A body without `extends:` is just its
own content (replace). No implicit name-based coupling between an inline
definition and a same-named standalone; the only way to pull a
standalone is an explicit `extends:` (with `@name` for core or a bare
name for user-first-then-core).

This eliminates the two silent failure modes that implicit coupling
would introduce:

1. *Accidental override:* a user writes `fragments: ssh: { enabled:
   true, packages: [foo] }` intending to add `foo` to ssh. Without
   `extends: @ssh`, this **replaces** ssh entirely (loses
   `openssh-client` and the `~/.ssh` mounts). The user must write
   `extends: @ssh` to inherit — the intent is explicit, not silent.
2. *Silent upstream coupling:* tpd ships a core `foo-config` standalone
   and a user's local inline `foo-config` (no `extends`) silently grows
   new content it never asked for. Under this design the inline
   replaces; no coupling. If the user wants core's content, they write
   `extends: @foo-config`.

### `extends:` is profiles-only

`extends` on a profile accepts only profiles. Listing a fragment is a
schema error at validation time:

```yaml
extends: [mise, ssh]   # ERROR: "ssh" is a fragment, not a profile
```

Resolution: `resolveChain` already checks `cat.IsFragment(pkey)` for the
fragment-extends-fragment case; the profile-extends-fragment case
becomes a hard error there. Built-in profiles currently extending
fragments (`buzz`, `t3code`, `opencode-desktop`) are rewritten to use
`fragments: { gui: { enabled: true, extends: @gui }, ... }`.

### Fragment-fragment `extends` is unchanged

Standalone fragments may still extend other standalone fragments. The
one existing case (`typescript extends @javascript`, migrated from
`core/javascript`) keeps working. The rule is uniform and simple:

- `extends:` on a **profile** → profiles only.
- `extends:` on a **fragment** (standalone or inline) → fragments only.

An inline fragment's `extends:` resolves against the fragment catalog
and follows the same depth-first, left-to-right, body-wins-last merge as
today. `@name` in a fragment `extends:` is a direct core lookup; a bare
name is user-first-then-core.

### Standalone fragments are lazy-loaded

Today `LoadProfiles` parses every standalone fragment up front so they
are available for `extends`. Under the new model fragments are only
reachable through a `fragments:` entry's `extends:`, so they are parsed
on demand:

- **Name index (cheap, built up front):** the set of fragment *names*
  is derived from filenames in `internal/catalog/fragments/` (embedded)
  and `~/.config/tpd/fragments/` (user). This is all `tpd` needs for
  shell completion of `tpd init` prompts and for validating that an
  `extends:` reference points at a known fragment.
- **Content (parsed on demand):** a standalone fragment file is read
  and parsed only when some profile's enabled inline fragment `extends:`
  it (directly, or transitively via another fragment's `extends:`).
  Disabled fragments and fragments whose inline definitions don't
  `extends:` them are never parsed, never validated, never shown.

Shadowing works as today: a user file
`~/.config/tpd/fragments/bashrc.yaml` shadows core `bashrc` — but only
evaluated when some enabled fragment `extends: bashrc` (or
`extends: @bashrc` is bypassed in favor of the bare form). The
global-unique-name rule (a display name is either a profile or a
fragment across namespaces, never both) is unchanged; it is enforced at
load time against the name index, before any content is parsed.

A malformed user fragment therefore only fails when it is `extends`-ed,
not at catalog load. `tpd doctor` grows a "validate all known fragments"
check so users can spot a broken one before they need it.

### Resolution algorithm

For a profile `P` with top-level `extends: [B1, B2]` (profiles) and
`fragments: { f1: {enabled: true, ...}, f2: {enabled: false, ...},
f3: {enabled: true, ...} }`:

1. Resolve `P`'s top-level `extends` chain as today (profiles only),
   merging left-to-right, body-wins-last. Produces `merged_body`.
2. For each entry in `P.fragments` in declaration order:
   - If `enabled: false`, skip entirely. The entry is not loaded, not
     merged, not shown.
   - If `enabled: true`:
     - Resolve the entry's `extends:` chain (fragments only,
       lazy-loading standalones, depth-first, left-to-right).
     - Merge the inline body on top of the extends result (body-wins-last).
       If `extends:` is absent, the fragment is just its inline body
       (a local definition that shadows any same-named standalone — the
       standalone is not loaded).
3. Merge each enabled fragment into `merged_body` in declaration order,
   as if they were additional `extends` entries appended after the
   profile's declared extends. The profile's own body is merged *last*,
   winning over both its extends and its enabled fragments — exactly as
   today, where `merge(extends..., body)` makes the body win. So an
   enabled fragment can add keys the body doesn't touch, but a key the
   body declares explicitly is never overridden by a fragment. The
   sandbox use case works because the migration *moves* the config
   mounts out of the body and into the fragment (not duplicated): the
   body no longer declares them, so the fragment is the sole source,
   and disabling it removes them.

The result is then validated as today.

### `tpd init` simplification

`tpd init` drops its non-interactive path. `tpd init` without args is
the only form: interactive prompts (existing wizard in
`internal/scaffold/`). The `--extends`, `--force`, and `--dry-run` flags
are removed. The wizard's fragment-selection step writes `fragments:
<name>: { enabled: true, extends: @<name> }` entries into the generated
file for any standalone fragments the user selects. A user who wants a
sandboxed variant edits the file and flips `enabled: false`.

### Built-in migrations

**Move inline (per-profile, co-located + toggleable):**

- `bashrc.yaml` → inline in `bash.yaml` as a `bashrc` fragment. Delete
  standalone `bashrc.yaml`. The inline fragment `extends: @bashrc` is
  *not* used (the standalone is gone); the content is inlined directly
  as a local definition (no `extends`):

  ```yaml
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

  Nothing extends `bashrc` today (grep confirms). A user profile
  extending `bash` inherits the bashrc mounts as part of bash's
  resolved body (fragments merge into the body; `extends: bash` gets
  everything bash resolves to). A user who wants to customize bashrc
  for their own profile writes `fragments: bashrc: { enabled: true,
  extends: @bashrc, mounts: { ... } }` — but since `core/bashrc` is
  deleted, `@bashrc` won't resolve; the user instead inlines the
  content themselves or keeps a user standalone `bashrc.yaml`. (This
  is the co-location trade-off: bash owns its bashrc content; users
  wanting a different bashrc write their own.)

- Each agent profile's config mounts become an inline fragment in that
  profile, named `<agent>-config` to avoid cross-profile name clashes
  within any future composed profile:
  - `claude`: `fragments: claude-config: { enabled: true, mounts:
    {~/.claude, ~/.cache/claude-code} }`
  - `opencode`: `fragments: opencode-config: { enabled: true, mounts:
    {~/.config/opencode, ~/.cache/opencode, ~/.local/share/opencode} }`
  - `gemini`, `codex`, `copilot`, `amp`, `crush`, `qwen`, `pi`: each
    gets a `<agent>-config` inline fragment for its config dir.
  - `powershell`: `fragments: powershell-config: { enabled: true,
    mounts: {~/.config/powershell} }`
  - `bash`: its `files:` (`/etc/profile.d/mise.sh`) stays in the body
    (not a toggleable concern; the bashrc mounts are the toggleable
    part).

  The `<agent>-config` naming avoids collision when a profile composes
  multiple agents (e.g. `buzz` extends `codex`+`claude`); without the
  prefix, two `config` fragments would clash once composed.

**Rewrite `extends` (fragments move out):**

- `buzz`: `extends: [mise, gui, gui-runtime, codex, claude]` →
  `extends: [mise, codex, claude]` + `fragments: { gui: { enabled:
  true, extends: @gui }, gui-runtime: { enabled: true, extends:
  @gui-runtime } }`.
- `t3code`: `extends: [mise, gui, gui-runtime, opencode, claude]` →
  `extends: [mise, opencode, claude]` + `fragments: { gui: { enabled:
  true, extends: @gui }, gui-runtime: { enabled: true, extends:
  @gui-runtime } }`.
- `opencode-desktop`: `extends: [mise, gui]` → `extends: mise` +
  `fragments: { gui: { enabled: true, extends: @gui } }`.

**Qualified ref migration:** `internal/catalog/fragments/typescript.yaml`
`extends: core/javascript` → `extends: @javascript`.

**Stay standalone (cross-cutting):** `ssh`, `gitconfig`, `netrc`,
`github`, `gitlab`, `aws`, `azure`, `gcloud`, `docker`, `podman`,
`kubernetes`, `helm`, `terraform`, `vault`, `security`, `gui`,
`gui-runtime`, and all language fragments (`go`, `python`, `rust`,
`java`, `javascript`, `typescript`, `ruby`, `php`, `perl`, `elixir`,
`haskell`, `julia`, `kotlin`, `ocaml`, `scala`, `zig`, `dotnet`).

### Files

- `internal/profile/ref.go`: accept `@<name>` as a core-qualified ref
  (equivalent to `core/<name>`) in `ParseRef`. Both profile and
  fragment `extends:` use it.
- `internal/profile/types.go`: add `InlineFragment` type and
  `Fragments map[string]InlineFragment` field on
  `Profile`/`RawProfile`. Extend `collectNullKeys` to track nulls
  inside inline fragment bodies.
- `internal/profile/validate.go`: forbid fragments in profile
  `extends`; enforce no-duplicate-names within a `fragments:` block;
  reject `image`/`command`/`version` in inline fragment bodies; reject
  a `fragments:` entry that is a bare boolean (must be a map with
  `enabled:`).
- `internal/profile/merge.go`: integrate inline fragment resolution
  into `resolveChain`; lazy-load standalones on `extends:`; resolve
  inline definitions (with their own `extends:`) into merged bodies.
- `internal/profile/catalog.go`: split fragment loading into a name
  index (built at `LoadProfiles`) and content parsing (on demand).
  Keep the cross-type display-name collision check, run against the
  name index.
- `internal/catalog/profiles/*.yaml`: rewrite per the migration above.
- `internal/catalog/fragments/bashrc.yaml`: delete.
- `internal/catalog/fragments/typescript.yaml`: `core/javascript` →
  `@javascript`.
- `cmd/tpd/cli.go`: remove `init` non-interactive flags (`--extends`,
  `--force`, `--dry-run`); keep `tpd init` interactive-only.
- `internal/scaffold/`: fragment selection in the wizard writes
  `fragments: <name>: { enabled: true, extends: @<name> }`.
- `internal/doctor/`: add "validate all known fragments" check.
- Tests across `internal/profile/` and `pkg/tpd/` for the new merge
  cases (local-definition-replaces, definition-with-extends-merges,
  transitive fragment-extends, no-duplicate-names,
  fragments-in-extends-rejected, bare-boolean-rejected, `@name`
  parsing).

### Out of scope

- A CLI toggle (`--enable`, `--set fragments.x.enabled=false`) for
  overriding fragment enable state at launch time without editing the
  profile file. The launch command's `SetInterspersed(false)` makes any
  positional-flag toggle awkward, and the field-driven model already
  covers the common case (edit the file once). Defer to a separate
  design if needed later.
- An `if:` field (GitHub-workflows-style conditional inclusion). It
  would generalize `enabled` but requires an expression language and
  an options/variables system, which interacts with the deferred
  CLI-toggle feature. Defer to a later design that covers both
  together.
- Migrating any user-written profiles. Hard break; users update by
  hand.
- A `tpd fragments list` command. Discovery is via `tpd show <profile>`
  (enabled fragments for that profile) and shell completion of `tpd
  init` prompts.

## Open questions

1. **`tpd show` output shape.** A profile's `fragments:` block should
   be visible in `tpd show <name>` (raw form) and `tpd show <name>
   --resolved` (enabled fragments inlined, disabled omitted). The
   exact rendering (list vs map, enabled marker) is left to
   implementation review.

2. **Inline fragment body field set.** An inline fragment body uses
   the same `Profile` struct but must reject `image`/`command`/`version`
   (`validateFragmentName` already does this for standalones; inline
   needs the equivalent). Confirm the rejected set matches the
   standalone set exactly.

3. **`bashrc` reusability after inlining.** Once `core/bashrc.yaml` is
   deleted, another profile can't get bashrc's content via
   `fragments: bashrc: { enabled: true, extends: @bashrc }` (no
   standalone to find — it would error). A user wanting bashrc in
   another profile inlines the content or keeps a user standalone
   `bashrc.yaml`. This trade-off is accepted (nothing extends `bashrc`
   today), but revisitable if reuse becomes a real need.