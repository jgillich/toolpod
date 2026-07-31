# `init` new profiles design

Date: 2026-07-31

## Summary

Today `toolpod init` can only generate a *user profile override* that extends a
built-in profile. This design lets `init` also create brand-new profiles: the
positional arg becomes the profile name to create, and a new `--extends` flag
selects what to extend (`mise`, built-in profiles, user profiles, or
fragments). New profiles are first-class: other profiles can `extends:` them,
and `profile show/edit/list` work on them like any other user profile.

## Current behavior

- `toolpod init <profile>` where `<profile>` must be a built-in.
- Generates `~/.config/toolpod/profiles/<profile>.yaml` with
  `extends: [<profile>, ...fragments]`.
- User profiles shadow built-ins of the same name.
- No way to create a brand-new profile except hand-writing YAML.

## CLI surface

```
$ toolpod init foo --extends opencode,podman,ruby
$ toolpod init foo                    # new profile, no bases (default: mise)
$ toolpod init opencode               # backward compatible: shadows built-in
```

- Positional arg = **profile name to create** (was: built-in to extend).
- New `--extends` flag: comma-separated list of bases to extend — `mise`,
  built-in profiles, user profiles, or fragments — in merge order
  (left-to-right, body wins last).
- `--fragments` stays as a compat alias; fragments are appended to the extends
  list (fragments win over earlier entries).
- **Default base**: if the name matches a built-in, default `--extends` to that
  built-in (preserves the shadow workflow). Otherwise default to `mise`.
- Generated file contains only `version`, `extends`, and nothing else. No
  command/tools/mounts are captured — the user edits the file afterward.

## Wizard

The profile picker shows **"New"** as the first option. Selecting it prompts
for:

1. Profile name (text input).
2. Base selection (multi-select over `mise` + built-in profiles + user
   profiles; defaults to `mise` if nothing selected).
3. Then the existing fragments multi-select.

Selecting an existing built-in keeps today's flow (extends that built-in, then
fragments). Non-TTY (test) path mirrors the same flow with text prompts.

## Validation and edge cases

- New profile names validated: reject slashes, `..`, whitespace (prevents path
  traversal in `<name>.yaml`), reserved subcommand names (`config`, `doctor`,
  `help`, `version`, `completion`, `prune`, `init`), and fragment-name
  collisions.
- Extends targets are validated to exist in the catalog (built-in, user, or
  fragment) at generation time.
- **Missing command**: a scratch profile (`extends: mise`, no command) cannot
  resolve to a valid profile. `init` still writes the file but warns
  "no `command` set — edit the file before launching" instead of failing.
- User profiles are loaded as bases: the wizard and validation must use the
  full catalog (built-ins + user profiles), not just built-ins.

## Backward compatibility

`toolpod init opencode --fragments npm,go` produces the identical file as
today. Existing user overrides are unaffected.

## Files to change

- `cmd/toolpod/cli.go` — `InitCmd`: positional `Profile` → `Name`,
  add `--extends`.
- `internal/scaffold/scaffold.go` — `Run`/`generate`: resolve default base,
  validate name + extends targets, generate full extends list, warn on missing
  command; wizard New flow (huh + text fallback).
- `internal/profile/validate.go` (or scaffold) — profile-name validation
  helper.
- `README.md` — document `init` new-profile usage.
- Tests: `cmd/toolpod/cli_test.go`, `internal/scaffold/scaffold_test.go`,
  `internal/scaffold/summary_test.go` — name validation, extends-target
  resolution, default-base logic, backward-compat generation, wizard New flow.

## Testing

- Unit: default-base logic (built-in name → shadow, else mise), extends-target
  validation, name validation, backward-compat generation output.
- Wizard: New-flow prompts via the existing non-TTY prompt path.
- Integration: `toolpod init foo --extends opencode` generates a file that
  resolves; `toolpod profile show foo` works.
