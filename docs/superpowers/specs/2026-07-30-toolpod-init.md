# `toolpod init` — Spec

**Status:** Draft for review
**Date:** 2026-07-30
**Validated against:** toolpod working tree at validation time: `internal/profile`, `internal/catalog`, `cmd/toolpod`, `pkg/toolpod`, `internal/prune`, `internal/doctor`. Revised per code review.

## Summary

Add a `toolpod init` command that generates one user profile override at a time
for a selected built-in profile, with the user's chosen presets. Built-in
profiles are stripped to a minimal, safe baseline (no default caches, no
`~/.gitconfig`); `init` is the convenient opt-in for the rest.

`init [<profile>]` runs an interactive wizard when stdin is a TTY: pick the
profile (if not given positionally), then select presets. Non-interactive use
takes the same selections as flags. Exactly one file is written per
invocation, into the user profile directory, using the existing `extends:` +
`caches:` + `mounts:` schema. No new schema fields.

Presets are `profile.RawProfile` fragments — mergeable profile snippets for a
specific tool or purpose. The generator builds a base (`extends: <built-in>`)
and merges selected presets into it using the existing `mergeProfiles`
function. No custom types, no custom template renderer; the merge machinery
and YAML marshaling do all the work.

The generated file `extends:` the built-in of the same name. Because the
current catalog loader shadows a built-in when a user file of the same name
exists, a generated `opencode.yaml` containing `extends: opencode` would
self-reference and cycle. This spec therefore requires a small resolver fix
(see §"Critical Implementation Constraint") as a prerequisite.

---

## Goals

1. Keep built-in profiles minimal and safe by default (no caches, no
   `~/.gitconfig`).
2. Make opting into recommended presets a one-command, guided action.
3. Generate one small, editable user profile per run that `extends:` the
   built-in — so future built-in improvements keep applying.
4. Use only existing schema fields and existing merge machinery.
5. Single profile per invocation (simpler wizard, clearer output, no batch
   surprises).
6. Presets are `RawProfile` fragments merged via `mergeProfiles` — no new
   types, no custom template logic. Adding a preset that sets env vars or
   tools requires no code change to the generator.

---

## Non-Goals

1. No batch generation of multiple profiles in one run.
2. No removing/toggling inherited fields (no `null`-to-delete in generated
   files). `init` only *adds*; removing or changing an inherited value is
   hand-edit territory.
3. No new schema fields.
4. No automatic migration of existing user configs.
5. No dependency on the (not-yet-implemented) `config list/show/check`
   subcommands.
6. No `--update` mode in v1 (recognizing and updating previously-generated
   files). The generated header marker is defined now so v2 can detect it.
7. No custom preset targets in v1 (only the preset catalog; arbitrary mounts
   are hand-edit territory).
8. No `all` value for `--presets` — every preset is explicitly named.

---

## Critical Implementation Constraint: `extends:` a same-name built-in

The current catalog loader (`internal/profile/catalog.go`) **replaces** a
built-in entry with a user entry of the same name (shadow). `ResolveProfile`
(`internal/profile/merge.go`) resolves the `extends:` parent via
`cat.Get(name)`, which returns the user shadow when one exists. A naive
generated file:

```yaml
# ~/.config/toolpod/profiles/opencode.yaml  (WRONG — self-cycle today)
version: 1
extends: opencode
caches:
  npm: ~/.npm
```

**does not work today.** `resolveChain` resolves `extends: opencode` to the
user file itself, producing `extends cycle detected at: opencode`. Verified
during spec validation by a throwaway test.

### Exact semantics (required behavior)

The rule, precisely:

> When resolving a user profile named `P` whose `extends:` value is also `P`,
> and a built-in profile named `P` exists, the parent resolves to the built-in
> `P`, not the user shadow.
> In all other cases, `extends:` resolves against the merged catalog.
> A profile named `P` with no built-in counterpart that extends itself is a
> cycle.
> Built-in profiles that extend themselves are cycles.
> Multi-profile cycles remain cycles.

Built-in profiles in v1 do not extend anything. The case "built-in `x`
extends `x`" and "built-in `opencode` extends `shell` then user `opencode`
extends `opencode`" are documented as future behavior and not required for
v1, but the rule above covers them coherently if built-in extends are added
later.

### Catalog shape

```go
type Catalog struct {
    entries  map[string]RawProfile // merged view: user shadows built-in
    builtins map[string]RawProfile // built-ins only, for extends-self
}

func (c Catalog) Get(name string) (RawProfile, bool)        // merged view
func (c Catalog) GetBuiltin(name string) (RawProfile, bool) // built-in only
func (c Catalog) IsUserShadow(name string) bool             // true if a user file shadows a built-in of this name
```

In `resolveChain` (sketch):

```go
rc, ok := cat.Get(name)
if !ok {
    return RawProfile{}, ProfileError{Message: "profile not found: " + name}
}
if seen[name] {
    return RawProfile{}, ProfileError{Path: rc.Path, Message: "extends cycle detected at: " + name}
}
if rc.Extends == "" {
    return rc, nil
}

// Special case: a user shadow that extends the built-in of the same name.
if rc.Extends == name && cat.IsUserShadow(name) {
    parent, ok := cat.GetBuiltin(name)
    if !ok {
        // Defensive guard: IsUserShadow(name) implies a built-in exists,
        // so this branch is unreachable in practice. Treat as a cycle.
        return RawProfile{}, ProfileError{Path: rc.Path, Message: "extends cycle detected at: " + name}
    }
    merged := mergeProfiles(parent, rc)
    merged.Path = rc.Path
    return merged, nil
}

// Normal case: resolve parent against the merged catalog.
seen[name] = true
defer delete(seen, name)
parent, err := resolveChain(cat, rc.Extends, seen)
if err != nil {
    return RawProfile{}, err
}
merged := mergeProfiles(parent, rc)
merged.Path = rc.Path
return merged, nil
```

The special case is **narrow**: it only fires when `rc.Extends == rc's own
name` and the entry is a user shadow of a built-in. It must not weaken normal
cycle detection.

### Required test matrix

| Case | Expected |
|---|---|
| user `opencode.yaml` extends `opencode`, built-in `opencode` exists | resolves built-in + user overlay (no cycle) |
| user `foo.yaml` extends `foo`, no built-in `foo` | cycle error |
| user `a.yaml` extends `b`, user `b.yaml` extends `a` | cycle error |
| user `opencode.yaml` extends `shell` | normal resolution (no special case) |
| user `shell.yaml` extends `opencode`, user `opencode.yaml` extends `shell` | cycle error |
| built-in `x.yaml` extends `x` (future) | cycle error |

A new test `TestResolveExtendsSelfViaBuiltin` covers case 1 explicitly.
Existing tests (`TestResolveScalarOverride`, `TestResolveMapMergeAndNullDelete`,
`TestResolveCycle`, `TestLoadProfilesUserShadowsBuiltin`,
`TestLoadProfilesUserAddsProfile`) continue to pass.

> Note: this resolver fix is independently valuable — it delivers the override
> pattern the design doc §4.4 already promises ("The shadowing user config may
> still `extends: <built-in-name>` to amend the built-in"). `init` is one
> consumer; every user who amends a built-in while keeping its name benefits.

---

## Naming and paths (reconciled with the current code)

The current implementation uses **profile** terminology:

- Flag: `--profile-dir` (`cmd/toolpod/cli.go:49`, `cli.go:76`).
- Default dir: `profile.DefaultProfileDir()` → `~/.config/toolpod/profiles/`
  (`internal/profile/catalog.go:218`).
- Env var `TOOLPOD_CONFIG_DIR` is referenced by the design doc §4.4 and the
  (not-yet-implemented) `config` subcommand plan, but is **not wired up** in
  shipped code.

### Decision for v1: XDG compliance, no custom env var

The previous draft introduced `TOOLPOD_CONFIG_DIR`. That is dropped. Instead,
`DefaultProfileDir()` is made XDG-compliant by using `os.UserConfigDir()`
(which honors `XDG_CONFIG_HOME` on Linux and falls back to `~/.config`), and
no toolpod-specific env var is added:

```go
func DefaultProfileDir() string {
    base, err := os.UserConfigDir() // XDG_CONFIG_HOME on Linux, else ~/.config
    if err != nil || base == "" {
        return "" // caller must handle empty (see §Behavior Rules)
    }
    return filepath.Join(base, "toolpod", "profiles")
}
```

- Flag: `--profile-dir <path>` (matches `doctor` and launch; takes absolute
  precedence over the default).
- Env: `XDG_CONFIG_HOME` (honored by `os.UserConfigDir`; standard, not
  toolpod-specific).
- Default: `~/.config/toolpod/profiles/` (Linux), or the platform equivalent
  from `os.UserConfigDir()`.

This avoids inventing a `TOOLPOD_*` env var whose semantics (`+ /profiles`) are
surprising, and brings the implementation in line with the design doc §4.4
("via XDG"). `init`, `launch`, and `doctor` all agree because they all call
`DefaultProfileDir()` (verified: `pkg/toolpod/launch.go:20`,
`internal/doctor/checks.go:31`, and the new `init` command).

> The design doc §4.4 mentions `TOOLPOD_CONFIG_DIR`; that line should be
> updated to say `XDG_CONFIG_HOME` instead (see §Documentation Updates).

### Permissions

```
directory: 0700   (profiles dir, and ~/.config/toolpod/ parents if needed)
files:     0644
```

---

## Built-in profile changes (strip to minimal baseline)

Remove `caches:` and the `~/.gitconfig` mount from **all** built-ins. The
built-ins become minimal launchers; everything optional is added via `init`.

> **Decision on `~/.gitconfig`:** removed from built-ins by explicit product
> decision — not all users want git identity exposed to every agent/container.
> The `gitconfig` preset (§Presets) lets users add it back via `init`, and the
> interactive wizard lists it. A `doctor` info line (§Interaction With
> Existing Commands) can suggest running `init` when a profile has no
> `gitconfig` mount. This trades a slightly worse first-run git experience for
> a safer default and explicit consent.

### `internal/catalog/profiles/opencode.yaml`

Current on disk:

```yaml
version: 1
image: ghcr.io/jdx/mise:latest
command: ["opencode"]
tools:
  opencode: latest
mounts:
  ~/.config/opencode:
    source: ~/.config/opencode
    read_only: true
  ~/.cache/opencode:
    source: ~/.cache/opencode
    read_only: false
  ~/.local/share/opencode:
    source:  ~/.local/share/opencode
    read_only: false
  ~/.gitconfig:
    source: ~/.gitconfig
    read_only: true
caches:
  npm: ~/.npm
  cargo: ~/.cargo
  pip: ~/.cache/pip
  go: ~/go
labels:
  profile: opencode
network: bridge
tty: auto
```

After (drop `caches:` and the `~/.gitconfig` mount; keep the opencode-specific
state mounts, which are required for the agent to function):

```yaml
version: 1
image: ghcr.io/jdx/mise:latest
command: ["opencode"]
tools:
  opencode: latest
mounts:
  ~/.config/opencode:
    source: ~/.config/opencode
    read_only: true
  ~/.cache/opencode:
    source: ~/.cache/opencode
    read_only: false
  ~/.local/share/opencode:
    source:  ~/.local/share/opencode
    read_only: false
labels:
  profile: opencode
network: bridge
tty: auto
```

### `internal/catalog/profiles/codex.yaml`

Current:

```yaml
version: 1
image: ghcr.io/jdx/mise:latest
command: ["codex"]
tools:
  codex: latest
mounts:
  ~/.config/codex:
    source: ~/.config/codex
    read_only: true
  ~/.gitconfig:
    source: ~/.gitconfig
    read_only: true
caches:
  npm: ~/.npm
  cargo: ~/.cargo
  pip: ~/.cache/pip
  go: ~/go
labels:
  profile: codex
network: bridge
tty: auto
```

After (drop `caches:` and `~/.gitconfig`):

```yaml
version: 1
image: ghcr.io/jdx/mise:latest
command: ["codex"]
tools:
  codex: latest
mounts:
  ~/.config/codex:
    source: ~/.config/codex
    read_only: true
labels:
  profile: codex
network: bridge
tty: auto
```

### `internal/catalog/profiles/shell.yaml`

Current:

```yaml
version: 1
image: ghcr.io/jdx/mise:latest
command: ["bash"]
tty: auto
caches:
  npm: ~/.npm
  cargo: ~/.cargo
  pip: ~/.cache/pip
  go: ~/go
```

After (drop `caches:`; no mounts to remove; keep `bash` as the actual
on-disk command — the design doc says `sh` but the implementation uses
`bash`; this spec does not change that):

```yaml
version: 1
image: ghcr.io/jdx/mise:latest
command: ["bash"]
tty: auto
```

> After this change `shell` has only `version`, `image`, `command`, `tty`.
> `validate()` (`internal/profile/validate.go`) only requires `version`,
> `command`, and exactly one of `image`/`build` — this is valid.

---

## Reserved names

Add `init` to `reservedNames` in `internal/profile/validate.go`.

Current (exact, `validate.go:5`):

```go
var reservedNames = map[string]bool{
    "config":     true,
    "doctor":     true,
    "help":       true,
    "version":    true,
    "completion": true,
    "prune":      true,
}
```

After:

```go
var reservedNames = map[string]bool{
    "config":     true,
    "doctor":     true,
    "help":       true,
    "version":    true,
    "completion": true,
    "prune":      true,
    "init":       true,
}
```

Extend `TestValidateReservedName` (`internal/profile/validate_test.go:41`) to
include `"init"`. A user file named `init.yaml` is rejected at load time with
the existing `profile name ... is reserved` error (exit code 2).

---

## Presets

Presets are `profile.RawProfile` fragments — mergeable profile snippets for a
specific tool or purpose. There is no distinction between "cache presets" and
"mount presets" in the type system or the UX; they are all just presets. The
user selects presets by name; the generator merges them into a base profile
using the existing `mergeProfiles` function.

### Definition

Presets are `profile.RawProfile` values. No new types are introduced.

```go
var presets = map[string]profile.RawProfile{
    // package caches
    "npm":   {Profile: profile.Profile{Caches: map[string]string{"npm": "~/.npm"}}},
    "cargo": {Profile: profile.Profile{Caches: map[string]string{"cargo": "~/.cargo"}}},
    "pip":   {Profile: profile.Profile{Caches: map[string]string{"pip": "~/.cache/pip"}}},
    "go":    {Profile: profile.Profile{Caches: map[string]string{"go": "~/go"}}},

    // host file mounts
    "gitconfig": {Profile: profile.Profile{Mounts: map[string]profile.Mount{
        "~/.gitconfig": {Source: "~/.gitconfig", ReadOnly: true},
    }}},
    "ssh": {Profile: profile.Profile{Mounts: map[string]profile.Mount{
        "~/.ssh":             {Source: "~/.ssh", ReadOnly: true},
        "~/.ssh/known_hosts": {Source: "~/.ssh/known_hosts", ReadOnly: false},
    }}},
    "netrc": {Profile: profile.Profile{Mounts: map[string]profile.Mount{
        "~/.netrc": {Source: "~/.netrc", ReadOnly: true},
    }}},
}
```

`gnupg` is **not** a v1 preset: GPG signing requires the GPG agent socket at
`/run/user/<uid>/gnupg/S.gpg-agent`, which is not mounted. Making `~/.gnupg`
read-write does not fix this — the preset would be broken. It can be
revisited if/when there's a working story for GPG agent sockets; not in v1.
When it comes back, it's just another entry in the map — no struct change, no
generator change. The merge handles any field (mounts, env, tools, etc.).

The `ssh` preset mounts `~/.ssh` read-only (keys and config protected) plus
`~/.ssh/known_hosts` read-write as a separate entry, so SSH can update
`known_hosts` on first connection to a new host.

> Note: `go: ~/go` mounts the whole GOPATH (`~/go/bin`, `~/go/pkg/mod`,
> `~/go/src`). This is broader than a pure module cache, but matches the
> existing design doc convention. A more precise future preset could split
> `go-mod: ~/go/pkg/mod` and `go-build: ~/.cache/go-build`; not in v1.

### Validation constraint

Presets must not set identity/non-mergeable fields (`extends`, `image`,
`build`, `command`, `version`). Since the preset map is hardcoded, this is a
development-time check, not a runtime check. It is enforced by a test:

```go
func TestPresetsAreValid(t *testing.T) {
    for name, p := range presets {
        if err := validatePreset(name, p); err != nil {
            t.Errorf("preset %q: %v", name, err)
        }
    }
}

func validatePreset(name string, p profile.RawProfile) error {
    if p.Extends != "" || p.Image != "" || p.Build != nil || len(p.Command) > 0 || p.Version != 0 {
        return fmt.Errorf("preset %q must not set extends/image/build/command/version", name)
    }
    return nil
}
```

This catches bad presets during `go test` without adding a runtime check to
every `toolpod init` invocation.

### Generation

The generator builds a base, merges selected presets into it, then sets
`extends` **after** the merge loop (because `mergeProfiles` clears `Extends`
as part of its output normalization — `merge.go:85`):

```go
base := profile.RawProfile{Profile: profile.Profile{
    Version: 1,
}}
for _, name := range selectedPresets {
    base = mergeProfiles(base, presets[name])
}
base.Extends = profileName // set after merge; mergeProfiles clears Extends
// marshal base as YAML
```

This works because `mergeProfiles` (`internal/profile/merge.go:47`) already
handles map merging (caches, mounts, env, tools, labels), scalar replacement,
and null-to-delete. The generated file is the merge result rendered as YAML
via `yaml.Marshal`. No custom template logic for mounts vs caches vs env —
walk the merged `Profile`, emit non-empty fields.

Because generated files `extends:` the built-in and `mergeMounts`
(`internal/profile/merge.go:90`) merges maps key-by-key, a preset's mounts are
**added** to whatever the built-in already mounts. There is no need to
re-declare the built-in's own mounts.

The exact preset list is editable over time; the v1 set above is the starting
point. New presets can be added without a schema change or generator change.

---

## User Experience

### Interactive mode (stdin is a TTY)

`toolpod init` with no arguments runs the full wizard (profile → presets).
Empty input at the presets prompt means **none**; the visible default is
`[none]`:

```text
$ toolpod init

Available built-in profiles: opencode, codex, shell
Profile: opencode

Presets (npm,cargo,pip,go,gitconfig,ssh,netrc) [none]: npm,go,gitconfig,ssh

created ~/.config/toolpod/profiles/opencode.yaml

This profile extends the built-in "opencode" and adds the selected presets.
Edit the file to change behavior, or delete it to restore the built-in default.
```

`toolpod init opencode` skips the profile prompt and goes straight to presets:

```text
$ toolpod init opencode

Presets (npm,cargo,pip,go,gitconfig,ssh,netrc) [none]: gitconfig

created ~/.config/toolpod/profiles/opencode.yaml
...
```

> The examples above show combined output for readability. In reality,
> prompts and warnings are on **stderr**; generated YAML / success messages
> are on **stdout**, so `toolpod init opencode --dry-run > opencode.yaml`
> produces clean YAML without prompt text.

### Flag + TTY interaction

> If `--presets` is provided, `init` does not prompt for presets. If
> `--presets` is not provided and stdin is a TTY, `init` prompts for presets.
> If stdin is not a TTY, missing presets default to none.

| Invocation | Profile | Presets |
|---|---|---|
| `toolpod init` (TTY) | prompt | prompt |
| `toolpod init opencode` (TTY) | opencode | prompt |
| `toolpod init opencode --presets npm,ssh` (TTY) | opencode | npm,ssh (no prompt) |
| `toolpod init opencode` (non-TTY) | opencode | none |
| `toolpod init` (non-TTY) | **error: profile required** | — |

### Non-interactive mode

```sh
toolpod init opencode --presets npm,go,gitconfig,ssh
toolpod init shell
toolpod init opencode --presets gitconfig,netrc --dry-run
toolpod init opencode --presets npm --force
```

`<profile>` is required when stdin is not a TTY. `--presets` defaults to none
when omitted.

---

## Command Surface

```
toolpod init [<profile>] [flags]
```

### Flags

| Flag | Description |
|---|---|
| `<profile>` | Built-in profile name to generate an override for. Optional in interactive mode (prompted); required in non-interactive mode. |
| `--presets <names>` | Comma-separated preset names. Default: none. Suppresses the presets prompt. |
| `--force` | Overwrite an existing user profile file. |
| `--dry-run` | Print the generated file without writing it. |
| `--profile-dir <path>` | Override the user profile directory. |

Env: `XDG_CONFIG_HOME` is honored via `os.UserConfigDir()` (standard, not
toolpod-specific).

### Profile resolution for `<profile>`

`<profile>` must name a **built-in** profile. Available names come from the
embedded built-in catalog only:

```go
cat, err := profile.LoadProfiles("") // built-ins only
// cat.Names() → [codex opencode shell]
```

User-defined profile names are rejected:

```text
error: unknown built-in profile: foo
available built-in profiles: codex, opencode, shell
```

---

## Behavior Rules

### 1. One file per invocation

`init` writes exactly one file: `<profile-dir>/<profile>.yaml`. No batch
mode, no multiple files.

### 2. Generated file extends the built-in

```yaml
extends: opencode
```

Future built-in changes are inherited automatically — provided the resolver
fix in §"Critical Implementation Constraint" lands.

### 3. Skip existing files unless `--force`

```text
skipped ~/.config/toolpod/profiles/opencode.yaml (already exists; use --force to overwrite)
```

No merging into existing files in v1.

### 4. Non-interactive defaults

If stdin is not a TTY:

- `<profile>` is required.
- `--presets` defaults to none.
- No prompts.

Missing `<profile>` in non-interactive mode:

```text
error: profile name is required
usage: toolpod init <profile> [--presets ...]
```

If the profile directory cannot be determined (e.g. `XDG_CONFIG_HOME` and
`$HOME` are both unset so `os.UserConfigDir()` returns `""`) and
`--profile-dir` is not provided, error out rather than writing to the CWD:

```text
error: cannot determine profile directory (set --profile-dir or XDG_CONFIG_HOME)
```

### 5. Unknown names rejected

```text
error: unknown preset: yarn
available presets: npm, cargo, pip, go, gitconfig, ssh, netrc

error: unknown preset: all
available presets: npm, cargo, pip, go, gitconfig, ssh, netrc
note: there is no "all" shorthand; specify presets explicitly
```

**Any unknown name rejects the entire flag** — no partial application.
`--presets npm,yarn` errors on `yarn` and applies nothing.

### 6. Validate after writing

After writing, `init` validates the generated file:

1. `profile.LoadProfiles(userDir)`
2. `profile.ResolveProfile(cat, <profile>)`
3. On error, **remove the written file** and report the error. Do not leave
   an invalid file on disk that would break every subsequent launch.

Because the merge is done via the existing `mergeProfiles` and the resolver
fix is a prerequisite, this can only fail on a resolver/merge bug — it's a
sanity check, not a user-facing error path. Removing the file on failure
keeps the user's state clean; the error message names the removed path:

```text
error: generated config failed validation: ~/.config/toolpod/profiles/opencode.yaml: ...
note: removed invalid file ~/.config/toolpod/profiles/opencode.yaml
```

In `--dry-run`, the rendered YAML is validated by writing it to a temp
directory (`os.MkdirTemp`), loading it via `LoadProfiles(tempDir)`, resolving,
then removing the temp dir. This is "dry" from the user's perspective
(nothing written to their profile dir) while using the real
`LoadProfiles`/`ResolveProfile` code path (not a test helper like
`NewProfileCatalogForTest`).

```text
created ~/.config/toolpod/profiles/opencode.yaml
generated config is valid
```

### 7. Deterministic output

Generated `caches:` and `mounts:` blocks are emitted in **alphabetical order
by key**. This keeps tests stable and diffs minimal. YAML map marshaling in
Go (`gopkg.in/yaml.v3`) sorts map keys alphabetically by default, so this is
free.

```yaml
caches:
  cargo: ~/.cargo
  go: ~/go
  npm: ~/.npm
  pip: ~/.cache/pip
mounts:
  ~/.gitconfig:
    source: ~/.gitconfig
    read_only: true
  ~/.ssh:
    source: ~/.ssh
    read_only: true
  ~/.ssh/known_hosts:
    source: ~/.ssh/known_hosts
    read_only: false
```

### 8. Dry-run wording

`--dry-run` does **not** print "created". It prints where it *would* write,
then the YAML:

```text
# dry-run: would write ~/.config/toolpod/profiles/opencode.yaml
<generated YAML>
```

`--dry-run` takes precedence over `--force` — nothing is written, so there is
nothing to overwrite. Under `--dry-run`, `--force` is entirely ignored: the
marker check and overwrite confirmation prompt are **skipped** (nothing is
being overwritten). The presets selection prompt still appears in interactive
mode (the user is configuring the preview). `--dry-run` still validates the
rendered YAML via a temp dir.

### 9. Selection parsing rules

`--presets` accepts comma-separated values. Parsing rules:

- Trailing/leading commas and whitespace are ignored.
- Empty string means none (e.g. `--presets ""` is none, not an error).
- `--presets ""` is equivalent to omitting the flag.
- Duplicate names are silently deduplicated (`npm,npm` → `npm`).
- There is **no `all`** value — every preset must be explicitly named.
- **Any unknown name rejects the entire flag** — no partial application.
  `--presets npm,yarn` errors on `yarn` and applies nothing.

### 10. Directory creation

`init` creates the profile directory (and parents) with `os.MkdirAll(dir, 0o700)`
if it does not exist, before writing any file.

### 10b. File-vs-directory bind mount caveat

Some presets (e.g. `gitconfig`, `netrc`, and `ssh`'s `known_hosts`) target
**files**, not directories. Docker bind-mounting a nonexistent host path
creates an empty **directory**, not a file, which will confuse tools
expecting a file (`git config --global`, `curl --netrc`). `init` checks
host-side existence at generation time and warns (to stderr) if a file
preset's source does not exist:

```text
warning: ~/.netrc does not exist; the mount will create an empty directory.
         Create the file first or remove this preset.
```

The file is still generated (the user may create the file later); the warning
is informational. For the `ssh` preset, the file existence check applies to
`~/.ssh/known_hosts` (the writable file sub-mount) but not to `~/.ssh` (the
read-only directory mount).

### 11. `--force` with hand-edited files

If the target file already exists and `--force` is used, `init` overwrites
it. To protect hand-edits:

- In **interactive mode**, if the existing file does **not** contain the
  `# Generated by toolpod init.` marker line, `init` prompts for explicit
  confirmation before overwriting, even with `--force`:

  ```text
  ~/.config/toolpod/profiles/opencode.yaml exists and was not generated by toolpod.
  Overwrite? [y/N]: n
  skipped ~/.config/toolpod/profiles/opencode.yaml
  ```

  Declining the prompt exits **0** (the user made an explicit choice to skip;
  that is not an error). The error-table exit code 2 is for "file exists and
  `--force` was not provided at all" — a different case.

  Files containing the marker (previously toolpod-generated) are overwritten
  without an extra prompt under `--force`.

> The interactive `--force` prompt deviates from the usual "`--force` means
> don't ask" Unix convention. This is deliberate: it protects hand-edited
> files from accidental destruction by `--force`'s blunt overwrite. A
> pseudo-TTY script that wants no prompt can run in non-interactive mode
> (`</dev/null` or `--presets` flags), where `--force` overwrites
> unconditionally.

- In **non-interactive mode**, `--force` overwrites unconditionally (the
  marker is still checked so that a future `--update` mode can distinguish;
  no confirmation is possible without a TTY).

---

## Generated File Format

The header marker line `# Generated by toolpod init.` is **defined and stable
from v1** so a future `--update` mode can recognize toolpod-owned files. The
marker format is exactly that line; do not vary it.

The generated file is the merged `RawProfile` marshaled as YAML, with the
header comment prepended. `yaml.Marshal` produces the body; the comment is
prepended as a string. The `extends:` and `version:` fields are set by the
base; all other fields come from the merged presets.

### With presets (caches + mounts)

```yaml
# Generated by toolpod init.
# This user profile overrides the built-in "opencode" profile.
# Remove this file to restore the built-in default.

version: 1
extends: opencode

caches:
  go: ~/go
  npm: ~/.npm

mounts:
  ~/.gitconfig:
    source: ~/.gitconfig
    read_only: true
  ~/.ssh:
    source: ~/.ssh
    read_only: true
  ~/.ssh/known_hosts:
    source: ~/.ssh/known_hosts
    read_only: false
```

### Mounts only, no caches

```yaml
# Generated by toolpod init.
# This user profile overrides the built-in "opencode" profile.
# Remove this file to restore the built-in default.

version: 1
extends: opencode

mounts:
  ~/.gitconfig:
    source: ~/.gitconfig
    read_only: true
```

### No presets

```yaml
# Generated by toolpod init.
# This user profile overrides the built-in "opencode" profile.
# Remove this file to restore the built-in default.

version: 1
extends: opencode
```

Empty fields are omitted by `yaml.Marshal` (the `omitempty` tags on
`Profile` handle this). Comments are not part of the machine schema.

> **Schema fix required:** the `Command` field in `profile.Profile`
> (`internal/profile/types.go:10`) currently has `yaml:"command"` without
> `omitempty`. A nil `Command` marshals as `command: []`, which would appear
> spuriously in generated files. Add `omitempty`:
>
> ```go
> Command []string `yaml:"command,omitempty"`
> ```
>
> This is safe: it only affects marshaling, not unmarshaling or validation.
> A resolved config with no command still fails validation; a generated
> override correctly omits the field. This change is part of Step 1 (it's a
> prerequisite for clean generated output).
>
> **Field order:** `yaml.Marshal` outputs struct fields in declaration order.
> The current `Profile` struct declares `Mounts` before `Caches`
> (`types.go:12` vs `types.go:14`), so generated files will have `mounts:`
> before `caches:`. The examples in this spec show `caches:` before `mounts:`
> for readability — the actual output order is `mounts:` then `caches:`.
> Either reorder the struct fields or accept the order; this is cosmetic and
> does not affect functionality. The examples should be updated to match
> reality during implementation.

---

## Interaction With Existing Commands

### Launch (`toolpod <profile>`, `toolpod shell`)

After `init`, the user shadow adds caches/mounts from the selected presets.
`LaunchWithWriter` (`pkg/toolpod/launch.go`) already calls
`profile.LoadProfiles(userDir)` then `profile.ResolveProfile`. Only the
resolver fix is required; no launch changes. `DefaultProfileDir` now uses
`os.UserConfigDir()` (XDG-compliant), so launch and `init` agree on the
directory.

### `toolpod doctor`

`checkProfileValidity` (`internal/doctor/checks.go:128`) already runs
`LoadProfiles` + `ResolveProfile` for every profile, so generated shadows are
covered once the resolver fix lands. No `doctor` code changes required beyond
the `DefaultProfileDir` XDG change (which doctor picks up automatically —
confirmed: `checks.go:31` calls `profile.DefaultProfileDir()`).

A doctor informational line is **required for v1** (not optional), to make the
gitconfig-removed default discoverable on first run. The check fires **only
for profiles that have a user override** (i.e. the user has run `init` or
hand-written a file in the profiles dir). It does **not** fire for unmodified
built-ins — the user hasn't opted in yet, so there's nothing to inform them
about. This keeps doctor output quiet for users who deliberately want the
minimal built-ins.

**Detection mechanism:** the check compares the built-in-only catalog against
the merged catalog to find user files:

1. Load built-ins only: `catBuiltins := LoadProfiles("")`
2. Load built-ins + user: `catMerged := LoadProfiles(userDir)`
3. A profile is "user-overridden" if its entry in `catMerged` has a `Path`
   pointing into `userDir` (i.e. it came from a user file, whether it shadows
   a built-in or adds a new name). The `RawProfile.Path` field already
   distinguishes built-in (`built-in:profiles/...`) from user
   (`<userDir>/...`) entries.
4. For each user-overridden profile, resolve it and check whether the
   resolved `Caches` is empty and/or whether `Mounts` contains `~/.gitconfig`.

For each user-overridden profile `P`:

```text
[info] caches: none configured (run `toolpod init <P>` to enable persistent package caches)
[info] gitconfig: not mounted (run `toolpod init <P> --presets gitconfig` to mount ~/.gitconfig)
```

Only the line(s) applicable to the resolved profile are printed (e.g. a user
override that adds caches but not gitconfig prints only the gitconfig line).
These are `Info` status (not failures). This requires a new check function in
`internal/doctor` (e.g. `checkUserOverrides(userDir)`); it is a concrete task
in Step 3 of Implementation Order. Plan 3 (`toolpod-operations.md`) must be
amended to include it.

### `toolpod prune`

No changes. Cache volumes are still named `toolpod-cache-<name>`
(`pkg/toolpod/spec.go:26`) and `prune` removes any `toolpod-`-prefixed volume
(`internal/prune/prune.go:113`). Generated cache configs produce the same
volume names:

```
toolpod-cache-npm
toolpod-cache-cargo
toolpod-cache-pip
toolpod-cache-go
```

### `config` subcommands

Planned by design doc §7.2, **not yet implemented**. `init` must not depend
on them. The validation step in §Behavior Rules #6 calls
`profile.LoadProfiles`/`ResolveProfile` directly. When `config list/show/check`
land, they pick up `init`-generated files for free via the same loader.

---

## Error Handling

Exit code `2` for all `init` errors (consistent with `ProfileError.ExitCode()`
in `internal/profile/errors.go:21` and the launch path in
`pkg/toolpod/launch.go:24`). `init` does not talk to Docker, so the `prune`
command's `3` for runtime errors does not apply.

```text
error: profile name is required
error: unknown built-in profile: foo
error: unknown preset: yarn
error: ~/.config/toolpod/profiles/opencode.yaml already exists (use --force to overwrite)
error: generated config failed validation: ...
```

| Condition | Exit code |
|---|---:|
| Success | 0 |
| Missing/invalid flags or input | 2 |
| Unknown profile or preset | 2 |
| Existing file without `--force` | 2 |
| Existing file, `--force` given, user declined interactive prompt | 0 |
| Generated config invalid / resolver error | 2 |
| Unexpected I/O error | 2 |

---

## Implementation Notes

### Package layout

```
internal/
  scaffold/
    scaffold.go      # Run(), Options, selection parsing, preset merging, validation
    presets.go       # preset map + validatePreset
    scaffold_test.go
```

Package name `scaffold` (not `init`) to avoid aliasing the special `init`
function and the `import initpkg "..."` ugliness:

```go
import "github.com/jgillich/toolpod/internal/scaffold"
// scaffold.Run(...)
```

No `template.go` — there is no custom template renderer. The generated file is
`yaml.Marshal(mergedProfile)` with the header comment prepended.

### Core function

```go
type Options struct {
    Profile    string   // required in non-interactive mode; optional in interactive
    Presets    []string // preset names
    Force      bool
    DryRun     bool
    ProfileDir string
}

func Run(ctx context.Context, opts Options, stdin io.Reader, stdout, stderr io.Writer) error
```

`Run` takes separate `stdout` and `stderr` writers so prompts and warnings go
to stderr and generated YAML / success messages go to stdout.

`Run` accepts `context.Context` for codebase consistency (other `Run`
functions like `doctor.Run` and `prune.Run` take one). `init` has no network
or long-running operations, so the context is not currently used for
cancellation; it's there so the signature matches the convention and a
future cancellation hook is non-breaking. Tests should pass
`context.Background()` and not rely on context cancellation behavior.

### Preset merging

```go
base := profile.RawProfile{Profile: profile.Profile{
    Version: 1,
}}
for _, name := range opts.Presets {
    base = profile.MergeProfiles(base, presets[name])
}
base.Extends = profileName // set after merge; MergeProfiles clears Extends
// marshal base as YAML, prepend header comment
```

`profile.MergeProfiles` is the existing merge function
(`internal/profile/merge.go:47`). It must be exported (or a wrapper provided)
for `scaffold` to call it. Currently it is unexported (`mergeProfiles`); the
implementation should export it or expose a `MergeProfiles` wrapper.

### Built-in profile discovery

```go
cat, err := profile.LoadProfiles("") // built-ins only
```

### Generated file path

```go
filepath.Join(userDir, opts.Profile+".yaml")
```

### CLI wiring

Add a `case "init":` branch in `cmd/toolpod/cli.go` `main()` (alongside
`doctor`, `prune`, `shell`). Parse flags with a `pflag.NewFlagSet("init", ...)`
matching the style of `runDoctor`/`runPrune`. The positional profile is
`fs.Arg(0)`. Call `scaffold.Run`. Update `usage()` (`cli.go:146`) to list
`init`.

### Help text

`toolpod --help` gains a line:

```text
  init [<profile>] [flags]       Generate a user profile override with presets
```

`toolpod init --help`:

```text
Generate a user profile override for a built-in profile.

The generated file extends the built-in profile and adds selected presets
(caches, mounts, and other profile fragments). It is written to the user
profile directory (~/.config/toolpod/profiles/<name>.yaml by default;
honors XDG_CONFIG_HOME).

Usage:
  toolpod init [<profile>] [flags]

Flags:
  --presets <names>    Comma-separated preset names (default: none)
  --force              Overwrite an existing user profile file
  --dry-run            Print the generated file without writing it
  --profile-dir <path> Override the user profile directory

Presets: npm, cargo, pip, go (caches); gitconfig, ssh, netrc (mounts)

Examples:
  toolpod init opencode
  toolpod init opencode --presets npm,go,gitconfig,ssh
  toolpod init shell --dry-run
  toolpod init opencode --presets gitconfig,netrc --force
```

---

## Implementation Order

Three small, independently-reviewable steps:

### Step 1 — Resolver fix

- Add `builtins` map to `Catalog`; add `GetBuiltin`, `IsUserShadow`.
- Special-case self-extends for user shadows in `resolveChain`.
- Export `MergeProfiles` (or add a wrapper) so `scaffold` can call it.
- Add `omitempty` to `Command` in `profile.Profile` (`types.go:10`) so
  generated files don't emit `command: []`.
- Add `TestResolveExtendsSelfViaBuiltin` and the test matrix from
  §"Critical Implementation Constraint".
- Ensure existing cycle/merge/shadow tests still pass.

This is independently valuable and can land first.

### Step 2 — Strip default caches (and `~/.gitconfig`) from built-ins

- Remove `caches:` from all three built-in profiles.
- Remove the `~/.gitconfig` mount from `opencode` and `codex`.
- Make `DefaultProfileDir()` XDG-compliant (`os.UserConfigDir()`), replacing
  the hardcoded `~/.config` path.
- Add `TestBuiltinsHaveNoDefaultCaches` and
  `TestBuiltinsDoNotMountUserDirs` (assert no `~/.ssh`, `~/.gnupg`,
  `~/.netrc`; `~/.gitconfig` is also absent per the product decision).
- Update design doc / README.

This can also land independently.

### Step 3 — Add `toolpod init`

- Add `internal/scaffold` (`Run`, `Options`, preset map, `validatePreset`,
  selection parsing, preset merging via `MergeProfiles`, deterministic YAML
  output, dry-run temp-dir validation, `--force` marker check, dir
  creation/guard against empty `DefaultProfileDir`).
- Add the `init` CLI subcommand and `init` reserved name.
- Add interactive/non-interactive behavior with the flag+TTY matrix.
- Add the `doctor` `checkUserOverrides` check (Info lines for user-overridden
  profiles lacking caches/gitconfig); amend Plan 3 accordingly.
- Add tests (unit, integration, reserved-name).
- Update `usage()` / `--help` with the drafted help text.

---

## Testing Plan

### Unit tests (`internal/scaffold`)

1. Profile name required in non-interactive mode; optional in interactive.
2. Unknown profile → error.
3. Preset selection parsing: `npm,go`; none; `""` (none); `npm,npm` (dedup);
   trailing comma.
4. Unknown preset → error (entire flag rejected, nothing applied).
5. Generated file correctness:
   - caches + mounts (alphabetical order)
   - mounts only
   - neither (just `extends:`)
   - header marker exact
   - `ssh` emits `~/.ssh` read-only + `~/.ssh/known_hosts` read-write
   - `gitconfig` and `netrc` emit `read_only: true`
6. Skip existing file without `--force`.
7. Overwrite existing file with `--force`.
8. Dry run prints "would write …", writes nothing, validates via temp dir.
9. `--dry-run` + `--force` with an existing file → nothing written, no error.
10. `--dry-run` + interactive (simulated TTY) → prompts on stderr, YAML on
    stdout, nothing written.
11. Non-interactive mode does not prompt; missing profile → error.
12. Prompts go to stderr; generated YAML / success to stdout.
13. Flag+TTY matrix: `--presets` suppresses presets prompt.
14. Post-write validation failure removes the written file.
15. `--force` interactive: prompts before overwriting a file lacking the
    generated marker; overwrites without prompt if marker present; declining
    the prompt exits 0.
16. `--force` non-interactive: overwrites unconditionally.
17. Empty `DefaultProfileDir` (no `XDG_CONFIG_HOME`, no `$HOME`) + no
    `--profile-dir` → error, no file written.
18. Directory created with `os.MkdirAll(dir, 0o700)` if absent.
19. Empty built-in catalog → "no built-in profiles available" error.
20. Interactive wizard: simulated stdin lines (profile → presets) produce a
    correct file end-to-end.
21. File-preset source does not exist on host (e.g. `~/.netrc` missing) →
    warning to stderr; file still generated.
22. `TestPresetsAreValid` rejects any preset that sets `extends`/`image`/
    `build`/`command`/`version`.
23. Preset merge produces correct result: `npm` + `ssh` → profile with both
    `caches.npm` and `mounts.~/.ssh` + `mounts.~/.ssh/known_hosts`.

### Resolver tests (`internal/profile`)

24. **`TestResolveExtendsSelfViaBuiltin`** — user `opencode.yaml` with
    `extends: opencode` + `caches:` resolves to (built-in opencode) + (user
    caches), no cycle. **Prerequisite.**
25. `TestResolveSelfExtendsNoBuiltin` — user `foo.yaml` `extends: foo`, no
    built-in `foo` → cycle error.
26. `TestResolveUserCrossCycle` — user `a`↔`b` → cycle error.
27. Existing `TestResolveCycle` still passes.
28. Existing `TestLoadProfilesUserShadowsBuiltin` /
    `TestLoadProfilesUserAddsProfile` still pass.

### Built-in regression tests (`internal/profile` or `internal/catalog`)

29. `TestBuiltinsHaveNoDefaultCaches` — `LoadProfiles("")` → no built-in has
    `caches:`. Must run against built-ins only.
30. `TestBuiltinsDoNotMountUserDirs` — no built-in mounts `~/.ssh`,
    `~/.gnupg`, or `~/.netrc`. (These are user-opt-in mounts added via `init`,
    not built-in defaults.)
31. `TestBuiltinsDoNotMountGitconfig` — no built-in mounts `~/.gitconfig`.
    (Separate product decision; kept in its own test so a future reversal
    doesn't muddy test 30.)
32. `TestDefaultProfileDirHonorsXDG` — setting `XDG_CONFIG_HOME` changes
    `DefaultProfileDir()`; unsetting it falls back to `~/.config/toolpod/profiles/`.
33. `TestDefaultProfileDirEmpty` — if both `XDG_CONFIG_HOME` and `$HOME` are
    unset, `DefaultProfileDir()` returns `""` (caller must guard).

### Reserved-name test

34. Extend `TestValidateReservedName` (`internal/profile/validate_test.go:41`)
    to include `"init"`.

### Integration test

```go
dir := t.TempDir()

err := scaffold.Run(ctx, scaffold.Options{
    Profile:    "opencode",
    Presets:    []string{"npm", "go", "gitconfig", "ssh"},
    ProfileDir: dir,
}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

cat, _ := profile.LoadProfiles(dir)
cfg, _ := profile.ResolveProfile(cat, "opencode")
// cfg.Caches["npm"] == "~/.npm"  (pre-ResolveTildes)
// cfg.Caches["go"]  == "~/go"
// cfg.Mounts["~/.gitconfig"].Source == "~/.gitconfig"
// cfg.Mounts["~/.gitconfig"].ReadOnly == true
// cfg.Mounts["~/.ssh"].ReadOnly == true
// cfg.Mounts["~/.ssh/known_hosts"].ReadOnly == false
// cfg.Image == "ghcr.io/jdx/mise:latest"  (inherited from built-in)
// cfg.Command == ["opencode"]             (inherited)
```

> This test requires the resolver fix (Step 1) to be in place: the generated
> shadow `extends: opencode`, which only resolves correctly after
> `TestResolveExtendsSelfViaBuiltin` passes.

---

## Documentation Updates

- Update `docs/superpowers/specs/2026-07-30-toolpod-design.md` and the README:
  built-in profiles are minimal; run `toolpod init <profile>` to add
  recommended presets (caches, mounts, and other profile fragments).
- Remove mention of default caches and `~/.gitconfig` from built-in profile
  documentation.
- Add `init` to the CLI surface list in design doc §7.
- **Update design doc §4.4** to replace `TOOLPOD_CONFIG_DIR` with
  `XDG_CONFIG_HOME` (honored via `os.UserConfigDir`), and note that user
  profiles live at `<config>/toolpod/profiles/*.yaml` (the `profiles/` subdir
  is part of the implementation, not directly under `toolpod/`). Also remove
  the `--config-dir` flag reference and the `TOOLPOD_CONFIG_DIR` env var
  reference (neither exists in shipped code; both are docs-only and should
  not be implemented).
- **Update Plans 1–3** (`docs/superpowers/plans/2026-07-30-toolpod-*.md`) to
  use the working-tree terminology: `profile`/`LoadProfiles`/`ResolveProfile`/
  `DefaultProfileDir` instead of `config`/`LoadCatalog`/`Resolve`/
  `DefaultUserConfigDir`. Remove any `--config-dir`/`TOOLPOD_CONFIG_DIR`
  references from the plans. **No code change is required for the flag
  rename** — the working tree already uses `--profile-dir`/`DefaultProfileDir`;
  the plans are stale docs relative to the working tree. Plans 1–3 **MUST**
  be updated before any implementation work begins. An implementer following
  the current plan text will build `--config-dir`/`TOOLPOD_CONFIG_DIR`/
  `DefaultUserConfigDir`, none of which match the working tree.
- Reconcile terminology: the working tree uses `profile`/`--profile-dir`/
  `profiles/`. Update the design doc and any plans that still say
  `config`/`--config-dir`/`~/.config/toolpod/*.yaml` so docs and code do not
  drift. (This spec uses the working-tree terminology throughout.)
- **Reconcile `shell` profile command:** design doc §9 says `sh`, the
  implementation uses `bash` (`internal/catalog/profiles/shell.yaml:3`).
  Update the design doc to match the implementation (`bash`). The mise base
  image (`ghcr.io/jdx/mise:latest`) is Debian-based and ships `bash`, so
  `bash` is safe. (If the base image ever changes to an alpine variant
  without `bash`, switch to `sh`.)
- **Windows path:** design doc §4.4 mentions `%APPDATA%\toolpod\` for
  Windows. With `os.UserConfigDir()`, Windows resolves to
  `%AppData%\toolpod\profiles\`. Update the design doc to note the `profiles/`
  subdir applies on all platforms.

---

## Future Enhancements (explicitly not v1)

1. **`--update` mode.** Recognize files by the `# Generated by toolpod init.`
   marker and update only those.
2. **Custom preset targets.** `--preset yarn=~/.cache/yarn`. Not in v1.
3. **Removing/toggling inherited fields.** Requires `null`-to-delete in
   generated files. Not in v1; hand-edit territory.
4. **Per-profile preset sets.** Offer different default presets for
   `opencode` vs `shell`. Not in v1.
5. **Finer `go` cache presets.** `go-mod: ~/go/pkg/mod`,
   `go-build: ~/.cache/go-build`. Not in v1.
6. **Multi-profile batch.** Dropped from v1; if needed, add a separate
   flag later.
7. **`gnupg` preset.** Revisit when there's a working story for GPG agent
   sockets. Adding it is just another entry in the preset map — no code change
   to the generator.

---

## Recommended Acceptance Criteria

1. The resolver handles a shadowing user file that `extends:` the built-in of
   the same name (§"Critical Implementation Constraint"), covered by
   `TestResolveExtendsSelfViaBuiltin` and the full test matrix.
2. Built-in profiles have no `caches:` (covered by
   `TestBuiltinsHaveNoDefaultCaches`), no `~/.gitconfig` mount
   (`TestBuiltinsDoNotMountGitconfig`), and no user-dir mounts
   (`TestBuiltinsDoNotMountUserDirs`).
3. `toolpod init <profile>` writes one user override into
   `~/.config/toolpod/profiles/<profile>.yaml`.
4. Generated file `extends:` the built-in and adds selected presets via
   `mergeProfiles`.
5. Existing files are skipped unless `--force`.
6. Non-interactive use works with `<profile>` + `--presets`; missing profile
   errors.
7. Interactive wizard prompts for presets (empty = none; `[none]` visible).
   `<profile>` is optional in interactive mode.
8. Generated config resolves via `profile.ResolveProfile` (validated
   post-write; on failure the file is removed). Dry-run validates via a temp
   dir without touching the user's profile dir.
9. `init` is a reserved profile name.
10. Flags use `--profile-dir`; `XDG_CONFIG_HOME` is honored globally by
    `DefaultProfileDir` (via `os.UserConfigDir`), so `init`, `launch`, and
    `doctor` agree on the directory. If the dir cannot be determined and
    `--profile-dir` is unset, `init` errors rather than writing to CWD.
11. Tests cover selection parsing, generation, skip/force, dry-run,
    validation, the self-extends resolver fix, and the minimal-built-ins
    regression.
12. `toolpod --help` and `toolpod init --help` document the new command.
13. Interactive prompts go to stderr; generated YAML / success messages go
    to stdout.
14. `--dry-run` prints the generated file and writes nothing; `--force` is
    ignored under `--dry-run`; dry-run still prompts in interactive mode.
15. Presets are `RawProfile` fragments merged via `mergeProfiles` — no
    `MountPreset` type, no custom template renderer. `ssh` is read-only +
    writable `known_hosts`; `netrc` and `gitconfig` are read-only; `gnupg`
    is not offered in v1.
16. Generated caches and mounts are emitted in deterministic (alphabetical)
    order.
17. Selection parsing deduplicates, treats empty as none, and rejects the
    entire flag on any unknown name (no partial application). There is no
    `all` value.
18. `--force` in interactive mode prompts before overwriting a file that
    lacks the `# Generated by toolpod init.` marker; declining the prompt
    exits 0.
19. `doctor` prints a mandatory `Info` line for user-overridden profiles that
    lack caches or a gitconfig mount (not for unmodified built-ins). Detection
    uses `RawProfile.Path` to identify user files.
20. `XDG_CONFIG_HOME` is honored (not a toolpod-specific env var); the
    design doc §4.4 is updated, and `TOOLPOD_CONFIG_DIR` / `--config-dir`
    references in the design doc and Plans 1–3 are removed (plans are stale
    relative to the working tree; no code change required for the rename).
21. File-preset sources that don't exist on the host (e.g. `~/.netrc`)
    produce a stderr warning at generation time; the file is still generated.
22. `TestPresetsAreValid` rejects presets that set identity fields
    (`extends`/`image`/`build`/`command`/`version`). This is a test-time
    check, not a runtime check.
23. `MergeProfiles` is exported (or wrapped) so `scaffold` can call it
    without duplicating merge logic.
24. `Command` field in `profile.Profile` has `omitempty` so generated files
    don't emit `command: []`.
25. `extends:` is set **after** the merge loop (not in the base), because
    `MergeProfiles` clears `Extends` as output normalization.

---

## Verification (performed during spec validation)

- **Built-in profiles** `opencode.yaml`, `codex.yaml`, `shell.yaml` confirmed
  to currently contain `caches:` (npm/cargo/pip/go) and `opencode`/`codex` to
  contain a `~/.gitconfig` mount. Confirmed by reading
  `internal/catalog/profiles/*.yaml`.
- **Self-extends cycle reproduced.** A throwaway test in `internal/profile`
  wrote a user `opencode.yaml` with `extends: opencode` and called
  `ResolveProfile`. Result: `extends cycle detected at: opencode`. Confirms
  the naive generated form does not work with the current resolver.
- **Reserved names** exact set confirmed in `internal/profile/validate.go:5`
  and exercised by `validate_test.go:41`. `init` is the only addition needed.
- **Flag/dir naming** confirmed: `--profile-dir`, `ProfileDir`,
  `DefaultProfileDir()` → `~/.config/toolpod/profiles/`. The current
  `DefaultProfileDir` hardcodes `os.UserHomeDir() + .config/...` and does
  **not** honor `XDG_CONFIG_HOME` (hence the `os.UserConfigDir()` change in
  this spec). `TOOLPOD_CONFIG_DIR` is referenced only in docs/plans, not in
  shipped code, and is dropped in favor of `XDG_CONFIG_HOME`.
- **`config` subcommands** (`list`/`show`/`check`) are **not implemented** in
  `cmd/toolpod/cli.go`; `init` must not depend on them.
- **Cache volume naming** confirmed in `pkg/toolpod/spec.go:26`
  (`toolpod-cache-<name>`) and `internal/prune/prune.go:113` (prefix match on
  `toolpod-`). No changes needed.
- **Map-merge for mounts** confirmed in `internal/profile/merge.go:90`
  (`mergeMounts`): parent + child merged key-by-key, so a generated `mounts:`
  block adds to the built-in's mounts without re-declaring them.
- **`mergeProfiles`** confirmed in `internal/profile/merge.go:47` — it handles
  all map fields (caches, mounts, env, tools, labels) with key-by-key merge.
  Currently unexported; needs exporting or a wrapper for `scaffold` to use.
  **It clears `Extends` at the end** (`merge.go:85`: `out.Extends = ""`), so
  the generator must set `extends` **after** the merge loop, not in the base.
- **`Command` field** in `profile.Profile` (`types.go:10`) has
  `yaml:"command"` without `omitempty` — a nil slice marshals as
  `command: []`. Needs `omitempty` added so generated files are clean.
- **Struct field order** in `profile.Profile` (`types.go:12-14`): `Mounts`
  is declared before `Caches`, so `yaml.Marshal` outputs `mounts:` before
  `caches:`. The spec examples show `caches:` first for readability; actual
  output order is `mounts:` then `caches:`. Cosmetic; update examples during
  implementation or reorder struct fields.
- **`doctor` profile check** confirmed in `internal/doctor/checks.go:128`;
  it already exercises `LoadProfiles` + `ResolveProfile` for all profiles, so
  generated shadows are covered once the resolver fix lands.
- **`shell` command** on disk is `bash` (`shell.yaml:3`); the design doc says
  `sh`. This spec keeps the on-disk `bash` and does not change it.
- **`os.UserConfigDir()`** returns `(string, error)`, not `string`. The
  `DefaultProfileDir` sketch handles the error.