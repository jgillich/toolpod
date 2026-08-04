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

### `@name` reference syntax

A new uniform syntax for explicitly referencing another entry by its
display name, usable in both profile `extends:` and fragment `extends:`.
`@bash` means "the entry named `bash` that isn't this file" — it resolves
through the same user-first-then-core fallback as a bare unqualified
name, but **excludes self-reference**: a user profile `bash.yaml` writing
`extends: @bash` resolves to `core/bash` (not itself). If user-first
lands on the referencing file itself and no other namespace has the
name, it is a self-reference error.

```yaml
# in a user profile bash.yaml that shadows core/bash:
extends: @bash        # resolves to core/bash (skips this file itself)

# in a fragment:
fragments:
  bashrc:
    enabled: true
    extends: @bashrc   # resolves user bashrc if present, else core/bashrc
    mounts: { ... }
```

`@name` and a bare unqualified name share the same resolution rule
(user-first-then-core); the difference is that `@` explicitly signals
"reference another entry of this name" and excludes the referencing
file itself, while a bare name is the same fallback without the
self-exclusion. In a user profile `bash.yaml`, `extends: @bash` skips
the user file and resolves to `core/bash`; `extends: bash` (bare)
resolves to the user file itself → extends-cycle error. So `@` is the
only way for a user profile to extend its core shadow. In a core
profile, `extends: @bash` is a self-reference error if no user `bash`
exists, and resolves to the user `bash` if one does.

Future namespaces (remote catalogs) plug into the same fallback: `@foo`
resolves user-first, then core, then any registered remote namespace
that has a `foo`. The `core/<name>` qualified form remains valid for
direct core-only lookup (no fallback) where that's needed; `@` is the
ergonomic general form.

**Self-exclusion is applied at resolution time, not parse time.** The
`@` flag is carried on `Ref` (a boolean `SelfExcluding` field, set by
`ParseRef` when the input starts with `@`). `ParseRef` is a pure string
parser with no source identity; `ExtendsList.Resolve` runs inside
`parseRaw` (catalog.go:441) before loaders stamp `RawProfile.Namespace`/
`Name` (catalog.go:364, 404), so `ParseRef` cannot know the referencing
file. The exclusion is applied in `ResolveRef`/`resolveChain`: when
resolving an `@`-flagged ref, the resolver skips the user-first result
if it equals the current entry's key, falls through to core (and
further namespaces), and errors if all candidates are self. `@` combined
with an explicit namespace (`@core/foo`) is a parse error — `@` is the
unqualified form only.

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
    Profile             // the fragment body: mounts/caches/tools/env/packages/repos/files/labels/ports/devices/dbus — NOT image/command/version/fragments
}
```

An inline fragment is **local to its defining profile**. It is not a
catalog entry, it carries no namespace, and it is not referenceable by
other profiles. There is no global uniqueness constraint on inline
fragment names across profiles — `claude` and `opencode` may both define
a local `config` fragment. Two inline fragments in the same `fragments:`
block MUST NOT share a name (schema error).

**`enabled:` is required.** A `fragments:` entry without an `enabled:`
key is a schema error. (The Go `bool` zero-value decodes a missing key
to `false`, silently deadening the fragment; the required-key check
must run in the custom ordered-map decoder by inspecting the YAML node
for an `enabled:` key, not by checking the decoded `bool` value.)

**Nested `fragments:` inside an inline fragment body are rejected.** The
`InlineFragment` embeds `Profile`, which will carry the `Fragments` field
for top-level profiles; a custom `UnmarshalYAML` (or a validation pass)
must reject any `fragments:` key inside an inline fragment body. The
allowed body fields are exactly the fragment-legal set
(mounts/caches/tools/env/packages/repos/files/labels/ports/devices/dbus)
plus `enabled:` and `extends:`. Profile-identity and launch fields
(`image`/`command`/`version`/`network`/`tty`/`resources`) are **all
rejected** in an inline fragment body.

**The standalone fragment gate is tightened to match.** Today
`validateFragmentName` (catalog.go:667) only rejects `image`/`command`;
a standalone fragment carrying `network: host`, `tty:`, or `resources:`
silently passes. That is a pre-existing bug: these are profile/launch
concerns, not composition concerns, and a fragment setting `network:
host` would silently affect any profile that extends it. The design
fixes this by extending the rejected set in `validateFragmentName` to
include `network`/`tty`/`resources` (and `version` for consistency with
the inline gate). This also means the "confirm the rejected set matches
the standalone set exactly" open question is resolved by definition:
both gates reject the same field set.

### `fragments:` entry shapes

A `fragments:` entry is a map with an `enabled:` key (required) plus an
optional body (the fragment content) and an optional `extends:` that
inherits a standalone before merging the body on top. There is **no
boolean-reference form** (`bashrc: true` is a schema error — it would
do nothing useful; the user must write `bashrc: { enabled: true,
extends: @bashrc }` or inline the content directly). **An enabled
entry with no body and no `extends:` is also a schema error** —
`{ enabled: true }` alone does nothing (no content to merge, nothing to
inherit); it must carry a body, an `extends:`, or both. An entry with
`enabled: false` and no body/extends is the legal disable shape (it
toggles off a parent's entry without re-declaring content).

The shapes:

- **`{ enabled: true, extends: @bashrc }`** — inherit the `bashrc`
  standalone (user file if present, else core), no local additions.
  `@` resolves user-first-then-core but excludes self-reference, so this
  always picks up an external `bashrc`, not the inline entry itself.
- **`{ enabled: true, extends: @bashrc, mounts: { ... } }`** — inherit
  the `bashrc` standalone (user-first-then-core), merge the inline body
  on top (body wins on conflicts). This is "customize the shared bundle
  locally."
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
  bashrc: { enabled: false }`. (See Resolution algorithm for how this
  works — `fragments` is a mergeable field carried through the extends
  chain.) To re-enable, a grandchild must re-declare the body or
  `extends:` (an enabled entry with neither is a schema error); this
  is consistent with the no-empty-enabled rule, not a special
  limitation.

The merge rule is uniform: `extends:` resolves first (depth-first,
left-to-right), then the inline body merges on top (body-wins-last) —
identical to profile resolution. A body without `extends:` is just its
own content (replace). No implicit name-based coupling between an inline
definition and a same-named standalone; the only way to pull a
standalone is an explicit `extends:` (with `@name` for a self-excluding
reference or a bare name for the standard user-first-then-core fallback).

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
extends: [mise, ssh]   # ERROR: "ssh" is a fragment, not a profile — use fragments: ssh: {enabled: true, extends: @ssh}"
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
today. `@name` and a bare name both resolve user-first-then-core; `@`
additionally excludes self-reference (the file containing the
`extends:`). Note: for an inline fragment, "self" is the *profile file*
containing the `fragments:` block, not the inline fragment's name — so
`extends: @bashrc` inside an inline `bashrc:` entry behaves identically
to bare `bashrc` unless the profile itself is named `bashrc`. `@` is
meaningful for profile-level `extends:` (a user profile shadowing a core
profile) and for standalone fragments (a user fragment shadowing a core
fragment); for inline fragments it's harmless but usually a no-op.

### Standalone fragments: built-ins eager, user files lazy

Today `LoadProfiles` parses every standalone fragment up front so they
are available for `extends`. Under the new model fragments are only
reachable through a `fragments:` entry's `extends:`, so user fragment
files are parsed on demand. **Built-in (embedded) fragments remain
eager:** they're in the binary (no I/O), deterministic, and eager
loading preserves the "built-ins are always valid" invariant that
`internal/catalog/catalog_test.go` relies on (it asserts that all
built-in profiles and fragments load and validate cleanly). User
fragment files (`~/.config/tpd/fragments/*.yaml`) are lazy-loaded.

- **Name index (built up front):** the set of fragment *names* is
  derived from filenames in `internal/catalog/fragments/` (embedded)
  and `~/.config/tpd/fragments/` (user). This is all `tpd` needs for
  shell completion of `tpd init` prompts and for validating that an
  `extends:` reference points at a known fragment.
- **Built-in content (eager):** embedded fragment files are parsed and
  validated at `LoadProfiles`, as today. The existing
  `loadBuiltinFragments` path is retained.
- **User content (parsed on demand):** a user fragment file is read and
  parsed only when an enabled inline fragment `extends:` it (directly,
  or transitively via another fragment's `extends:`), or when `tpd
  show` / `tpd edit` reads it by name. Disabled user fragments and user
  fragments whose inline definitions don't `extends:` them are never
  parsed, never validated, never shown.

**Operations that trigger user-fragment parsing:** (1) resolution of
an enabled inline fragment's `extends:` chain that lands on a user
fragment; (2) `tpd show <fragment-name>` (raw display); (3) `tpd show
<profile> --resolved` (inlines enabled fragments); (4) `tpd edit
<fragment-name>` (seeds from built-in or user). Any code path that
reads a user fragment by catalog key (`cat.Get`) must go through the
lazy loader, not assume pre-parsed entries. Built-in fragments are
already in the catalog map after `LoadProfiles`.

Shadowing works as today: a user file
`~/.config/tpd/fragments/bashrc.yaml` shadows core `bashrc` in the name
index — but only evaluated when some enabled fragment `extends: bashrc`
or `extends: @bashrc` (both resolve user-first-then-core; `@` excludes
self-reference, so a user `bashrc` standalone writing `extends: @bashrc`
would resolve to `core/bashrc`, not itself). The global-unique-name rule
(a display name is either a profile or a fragment across namespaces,
never both) is unchanged; it is enforced at load time against the name
index, before any content is parsed.

A malformed user fragment therefore only fails when it is `extends`-ed
or read by `show`/`edit`, not at catalog load. **The name index wins
over core for `@name` resolution**: a user file
`~/.config/tpd/fragments/foo.yaml` shadows `core/foo` in the index even
before it is parsed, so `extends: @foo` resolves to the user entry; if
that file is malformed, the parse fails at use time with no core
fallback. This is intentional (shadowing is by name, not by content) and
matches today's behavior for standalones loaded eagerly. `tpd doctor`
grows a "validate all known user fragments" check so users can spot a
broken one before they need it.

**Eager vs. lazy validation split:** disabled inline fragments are
validated at load time (their body is in the profile file, already
parsed, so checking `enabled:`/rejecting identity fields/nested
`fragments:` is cheap and catches errors early). User standalone
fragments are validated lazily (only when `extends`-ed or read), since
parsing them eagerly would defeat the lazy-load goal. Built-in
fragments are validated eagerly (as today). This split is intentional:
inline bodies are already in memory; user standalones are not; built-in
standalones are cheap and trusted.

### Resolution algorithm

`fragments` is a **first-class mergeable field** carried through the
extends chain, not something baked into each base's body during
`resolveChain`. This is the key design decision that makes child-disable,
replace semantics, and `extends: bash` inheritance all work consistently.

**Merge semantics for `fragments`:** `MergeProfiles` treats `Fragments`
as a key-by-key map merge (child wins per key), identical to `mounts` or
`tools`. A child profile's `fragments: bashrc: { enabled: false }`
overrides a parent's `fragments: bashrc: { enabled: true, ... }` entry;
the child's entry replaces the parent's entirely for that key (the
parent's body/extends for that fragment are dropped). `null` deletes an
inherited fragment key (consistent with the existing null-to-delete
convention).

**Two-phase resolution:** `resolveChain` resolves the `extends` chain
(merging profiles and their `fragments` maps) but does **not** inline
enabled fragments into the body. Inlining happens once, after the chain
is fully merged, at `ResolveProfile` (`ResolveFragment` doesn't inline
— standalone fragments can't contain `fragments:`, so there's nothing
to inline).

For a profile `P` with top-level `extends: [B1, B2]` (profiles) and
`fragments: { f1: {enabled: true, ...}, f2: {enabled: false, ...}, ... }`:

1. **Chain resolution** (`resolveChain`, unchanged structure): resolve
   `P`'s top-level `extends` chain as today (profiles only), merging
   left-to-right, body-wins-last. `MergeProfiles` now also merges the
   `Fragments` map key-by-key (child wins per key). The result is a
   `RawProfile` with a combined `Fragments` map containing entries from
   `P` and all its bases — base entries inherited, `P`'s entries
   overriding same-named base entries. Produces `merged` (body + merged
   `Fragments` map, **enabled fragments not yet inlined**).

2. **Fragment inlining** (new, at `ResolveProfile` only): for each
   entry in `merged.Fragments` in **declaration order** (see Ordering
   below):
   - If `enabled: false`, skip entirely. The entry is not loaded, not
     merged, not shown. This is how a child disables a parent's
     fragment: the child's `fragments: f1: { enabled: false }` replaces
     the parent's `f1` entry in the merged map (step 1), and step 2
     skips it.
   - If `enabled: true`:
     - Resolve the entry's `extends:` chain (fragments only,
       lazy-loading standalones, depth-first, left-to-right).
     - Merge the inline body on top of the extends result
       (body-wins-last). If `extends:` is absent, the fragment is just
       its inline body (a local definition that shadows any same-named
       standalone — the standalone is not loaded).
     - Merge the resolved fragment into `merged.Profile` (the body),
       as if it were an additional `extends` entry. The profile's own
       body (already in `merged`) is **not** re-merged after fragments —
       **fragments merge onto the body, so a fragment can override a
       key the body declares.** This is a deliberate choice: the
       migration moves config mounts *out of* the body and *into* the
       fragment, so the fragment is the sole source and there is no
       body-vs-fragment conflict for the toggleable keys. A profile
       that declares a mount in its body *and* in an enabled fragment
       will see the fragment's value win; this is documented behavior,
       not a bug. (If a profile author wants the body to win, they
       don't duplicate the key into a fragment.)

3. The result is validated as today.

**Why fragments win over the body (the ordering decision):** the
sandbox use case is "disable the fragment → the mounts disappear." That
requires the fragment to be the source of the mounts, not the body. If
the body won over fragments, disabling a fragment would leave the body's
copy of the mounts in place — defeating the toggle. So fragments must
win. The migration ensures there is no body/fragment duplication for
toggleable keys; the rule only bites a profile author who deliberately
declares the same key in both places, and "fragment wins" is the sane
default for that case (the fragment is the toggleable unit).

**Why `fragments` must travel through the chain (not bake in):** if
`resolveChain` inlined enabled fragments into each base's body before
returning, a child's `fragments: f1: { enabled: false }` would have
nothing to act on — the base's `f1` content would already be in the
inherited body, and "skip the disabled entry" would leave it there. The
child-disable claim requires `fragments` to be a mergeable map that
travels intact through the chain, with inlining deferred to the
top-level entry point. This is why `Fragments` is a field on
`RawProfile` merged by `MergeProfiles`, and inlining is a separate
phase.

### Ordering: preserving declaration order

Go's `map[string]InlineFragment` loses YAML key order, but the design
relies on declaration order (step 2 processes fragments in order, and
`packages` append+dedup is order-sensitive). The `Fragments` field
therefore uses a **custom `UnmarshalYAML`** that decodes into an ordered
slice of `{key, InlineFragment}` pairs internally, exposed as a map-like
type that preserves insertion order. The existing `NullKeys` tracking
already does a parallel `yaml.Node` walk; the ordered-fragments decoder
follows the same pattern. This mirrors how `command` (a list, not a map)
preserves order today.

### `tpd init` simplification

`tpd init` drops the `--extends` flag. Today `--extends` mixes profiles
and fragments in one list; under the hard break that list must split
into `extends:` (profiles only) and `fragments:` entries, and the
non-interactive path would need to classify each name and emit both
forms — a special-case parser for a flow we're discouraging. The
interactive wizard already handles this cleanly (separate profile and
fragment selection steps). `--force` and `--dry-run` are orthogonal to
inline fragments and **remain**; they serve the scripting path (`tpd
init myprofile --force` to overwrite, `--dry-run` to preview), which is
unaffected by this design. README and doctor messages referencing
`--extends`/`--fragments` are updated.

**Non-TTY behavior change:** the wizard's non-interactive fallback
(`promptFragments`, scaffold.go:180) is reached today when `--extends`
is omitted on a non-TTY stdin. With `--extends` removed, the wizard
always prompts for fragments interactively; a non-TTY stdin with no
`--extends` can't pick fragments and falls back to the default base
only. This is an acknowledged behavior change: scripted init no longer
adds fragments via the wizard. A scripted user who wants fragments
edits the generated file (or a future `--enable-fragment` flag, out of
scope here). The non-TTY path is simplified, not removed — it still
produces a working default profile.

The wizard's fragment-selection step writes `fragments:
<name>: { enabled: true, extends: <name> }` entries using the bare name
(standard user-first-then-core fallback). `@<name>` would work equally
well here (same resolution, and the generated profile won't share a name
with a fragment); the bare form is chosen for simplicity in generated
output.

A user who wants a sandboxed variant edits the file and flips `enabled:
false`.

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
  for their own profile inlines the content themselves or keeps a user
  standalone `bashrc.yaml`. (This is the co-location trade-off: bash
  owns its bashrc content; users wanting a different bashrc write their
  own.)

- Each agent profile's config mounts become an inline fragment in that
  profile, named `<agent>-config` to avoid cross-profile name clashes
  within any future composed profile:
  - `claude`: `fragments: claude-config: { enabled: true, mounts:
    {~/.claude, ~/.cache/claude-code} }`
  - `opencode`: `fragments: opencode-config: { enabled: true, mounts:
    {~/.config/opencode, ~/.cache/opencode, ~/.local/share/opencode} }`
  - `opencode-desktop`: same `opencode-config` fragment as `opencode`
    (the config mounts are identical), plus a `desktop-config` fragment
    for its GUI-specific mount `~/.config/ai.opencode.desktop`. The
    desktop profile is the more likely sandbox target, so its config
    mounts must be toggleable too.
  - `gemini`, `codex`, `copilot`, `amp`, `crush`, `qwen`, `pi`: each
    gets a `<agent>-config` inline fragment for its config dir.
  - `powershell`: `fragments: powershell-config: { enabled: true,
    mounts: {~/.config/powershell, ~/.local/share/powershell} }`
    (both mounts from the current profile move into the fragment).
  - `bash`: its `files:` (`/etc/profile.d/mise.sh`) stays in the body
    (not a toggleable concern; the bashrc mounts are the toggleable
    part).

  The `<agent>-config` naming avoids collision when a profile composes
  multiple agents (e.g. `buzz` extends `codex`+`claude`); without the
  prefix, two `config` fragments would clash once composed.

  Note: `opencode-desktop` sharing the `opencode-config` fragment name
  with `opencode` is fine — inline fragments are local to their
  defining profile, so there is no cross-profile name clash. If
  `opencode-desktop` extends `opencode`, the `Fragments` map merge
  means `opencode-desktop`'s `opencode-config` entry replaces
  `opencode`'s; the desktop profile re-declares it with the same
  content (or adds the desktop-specific mount to it).

**Rewrite `extends` (fragments move out):**

- `buzz`: `extends: [mise, gui, gui-runtime, codex, claude]` →
  `extends: [mise, codex, claude]` + `fragments: { gui: { enabled:
  true, extends: @gui }, gui-runtime: { enabled: true, extends:
  @gui-runtime } }`.
- `t3code`: `extends: [mise, gui, gui-runtime, opencode, claude, codex]`
  → `extends: [mise, opencode, claude, codex]` + `fragments: { gui:
  { enabled: true, extends: @gui }, gui-runtime: { enabled: true,
  extends: @gui-runtime } }`.
- `opencode-desktop`: `extends: [mise, gui]` → `extends: mise` +
  `fragments: { gui: { enabled: true, extends: @gui } }`. (Its
  `extends: mise` — it no longer extends `opencode`; it's a standalone
  profile that happens to share the opencode config-mount shape via its
  own `opencode-config` fragment. If it should extend `opencode` to
  inherit the opencode tool/mise setup, that's a separate decision;
  today it extends `mise`+`gui` only, not `opencode`.)

**Qualified ref migration:** `internal/catalog/fragments/typescript.yaml`
`extends: core/javascript` → `extends: @javascript`. **This is a
deliberate behavior change:** today `core/javascript` pins core (no
fallback), so a user's `javascript` fragment never feeds `typescript`.
Under `@javascript`, a user `~/.config/tpd/fragments/javascript.yaml`
shadows core for `typescript`'s extends too — the user's `javascript`
now flows into any profile or fragment that enables `typescript`. This
is desirable (fragment composition is additive, and user shadowing
should work uniformly), but it is a semantic shift worth noting. If a
built-in ever needs to pin core regardless of user shadows, the
`core/<name>` form remains available for that.

**Stay standalone (cross-cutting):** `ssh`, `gitconfig`, `netrc`,
`github`, `gitlab`, `aws`, `azure`, `gcloud`, `docker`, `podman`,
`kubernetes`, `helm`, `terraform`, `vault`, `security`, `gui`,
`gui-runtime`, and all language fragments (`go`, `python`, `rust`,
`java`, `javascript`, `typescript`, `ruby`, `php`, `perl`, `elixir`,
`haskell`, `julia`, `kotlin`, `ocaml`, `scala`, `zig`, `dotnet`).

### Files

- `internal/profile/ref.go`: accept `@<name>` in `ParseRef` by stripping
  the `@` prefix and setting a `SelfExcluding bool` flag on `Ref` (the
  ref is otherwise unqualified — user-first-then-core). The exclusion is
  applied in `ResolveRef`/`resolveChain` against the current entry's
  key (skip user-first result if it equals self, then core, error if
  core is also self). `@` combined with a namespace prefix
  (`@core/foo`) is a parse error — `@` is the unqualified form only.
  `Ref.FullName()` still marshals the canonical `core/<name>` form for
  output.
- `internal/profile/types.go`: add `InlineFragment` type and an ordered
  `Fragments` field on `Profile`/`RawProfile` (custom `UnmarshalYAML`
  preserving declaration order; see Ordering). Extend `collectNullKeys`
  to track nulls inside inline fragment bodies and top-level
  `fragments:` keys.
- `internal/profile/validate.go`: forbid fragments in profile `extends`
  (error message suggests the `fragments:` form); enforce
  no-duplicate-names within a `fragments:` block; reject
  `image`/`command`/`version`/`network`/`tty`/`resources` in inline
  fragment bodies; reject nested `fragments:` inside an inline fragment
  body **and inside standalone fragments** (`validateFragmentName` can't
  do this today; a new check is needed once `Fragments` exists on the
  struct); reject a `fragments:` entry that is a bare boolean (must be
  a map); reject an enabled entry with no body and no `extends:` (would
  do nothing); require `enabled:` key in every `fragments:` entry.
- `internal/profile/merge.go`: add `Fragments` to `MergeProfiles`
  (key-by-key map merge, child wins per key, `null` deletes). Move
  enabled-fragment inlining out of `resolveChain` into
  `ResolveProfile` (new two-phase resolution; `ResolveFragment`
  unchanged — standalone fragments can't carry `fragments:`).
- `internal/profile/catalog.go`: keep `loadBuiltinFragments` eager
  (embedded files, validated at load). Split user fragment loading into
  a name index (built at `LoadProfiles`) and content parsing (on demand,
  via a lazy loader invoked by `resolveChain`'s fragment-extends
  resolution, `tpd show`, and `tpd edit`). Keep the cross-type
  display-name collision check, run against the name index.
- `internal/catalog/profiles/*.yaml`: rewrite per the migration above.
- `internal/catalog/fragments/bashrc.yaml`: delete.
- `internal/catalog/fragments/typescript.yaml`: `core/javascript` →
  `@javascript`.
- `cmd/tpd/cli.go`: remove `init`'s `--extends` flag only; keep
  `--force`/`--dry-run` (orthogonal, scripting path). Update the
  non-interactive fallback in `internal/scaffold/scaffold.go` (the
  `promptFragments` path at line 180 is no longer reachable from the
  CLI; simplify it or gate it on `opts.Extends` for future use).
- `internal/scaffold/`: fragment selection in the wizard writes
  `fragments: <name>: { enabled: true, extends: <name> }` (bare name
  form).
- `internal/doctor/`: add "validate all known fragments" check; update
  the stale `tpd init %s --fragments gitconfig` message in
  `internal/doctor/checks.go:362` to the new `tpd init` flow.
- `README.md`: update the `tpd init opencode --extends=javascript,...`
  example (line ~76) to the interactive `tpd init` flow.
- Tests across `internal/profile/` and `pkg/tpd/` for the new merge
  cases (local-definition-replaces, definition-with-extends-merges,
  transitive fragment-extends, child-disables-parent-fragment, no-duplicate-names,
  fragments-in-extends-rejected, bare-boolean-rejected,
  enabled-with-no-body-no-extends-rejected, missing-enabled-rejected,
  nested-fragments-rejected-inline,
  nested-fragments-rejected-standalone, identity-fields-rejected-inline,
  identity-fields-rejected-standalone, `@name` parsing,
  `@name`-self-exclusion-applied-at-resolve, `@core/foo`-rejected,
  ordered-fragments, user-fragment-lazy-load, built-in-fragment-eager).

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

2. **Merged fragments map ordering.** Per-file declaration order is
   preserved, but when a child overrides a parent's key or adds new
   ones, the merged map's iteration order is undefined by the
   key-by-key merge. Since `packages` append+dedup is order-sensitive
   across fragments, the merge needs an explicit rule: e.g. parent
   order first, child overrides keep the parent's position, new keys
   appended in child declaration order. Decide during implementation.

3. **`bashrc` reusability after inlining.** Once `core/bashrc.yaml` is
   deleted, another profile can't get bashrc's content via
   `fragments: bashrc: { enabled: true, extends: @bashrc }` (no
   standalone to find — it would error). A user wanting bashrc in
   another profile inlines the content or keeps a user standalone
   `bashrc.yaml`. This trade-off is accepted (nothing extends `bashrc`
   today), but revisitable if reuse becomes a real need.