# Folder-structured fragment picker for `tpd init`

Date: 2026-08-06

## Goal

The `tpd init` wizard's fragment picker currently lists every fragment in one flat list (`promptFragmentsHuh(FragmentNames(), …)` in `internal/scaffold/scaffold.go:176`). Rework it into a folder browser: the root screen shows the top-level directories (`cloud`, `gui`, `infra`, `lang`, `service`, `vcs`, plus any user directories), and entering a directory shows the fragments inside it with an option to go back. Fragments from multiple folders accumulate into the final selection.

Decisions confirmed with the user:
- **Interaction model:** two-form drill-down. Levels with subfolders show a folder-navigation form whose single select fires immediately on Enter (descend, ascend, or open the level's own fragments); levels with no subfolders show a fragments form — a multi-select (space toggles) plus `Done`/`Back` buttons.
- **User fragments:** the picker offers user fragments from `~/.config/tpd/fragments/` in addition to built-ins (currently built-ins only).
- **Non-TTY prompt:** the text-mode comma-list prompt is unchanged in shape (only its source list widens to include user fragments).

## Fragment listing source

Add `Catalog.FragmentDisplayNames()` in `internal/profile/catalog.go`, mirroring `ProfileDisplayNames()` but filtered to `c.fragments`. Returns sorted, deduped display names (e.g. `lang/go`, `lang/javascript`, user `creds/ssh`). User entries shadow core entries of the same display name, so each name appears once — dedup/shadowing happens here, not in the tree builder. This becomes the single source for both the browser and the text prompt, replacing `FragmentNames()` (built-ins only).

Note: a cross-type collision (user profile `lang/go.yaml` vs built-in fragment `core/lang/go`) is a hard catalog-load error (`catalog.go:195`), so the wizard never reaches the picker in that state. Pre-existing, out of scope.

## Tree navigation

New file `internal/scaffold/browser.go` with the loop plus two form builders:

- `fragTree(fragNames []string, path []string) (dirs, frags []string)` — pure function. For the current path (e.g. `["lang"]`) it walks the display names and returns the sorted subdirectory segments and the sorted leaf fragment display names directly under it. The root path is empty; a top-level fragment (display name with no `/`) appears at root alongside folders. Works at any nesting depth because it operates on `/`-separated display-name segments. This is the testable core of the browser.

- `promptFolderHuh(path, dirs, hasFrags, stdin, stdout)` — the navigation form. A single-field `huh.Select`, so **Enter fires** (the form is the submit): options are subfolders (`▸ name`), `✓ fragments here` (only when the level has fragments), and `← up` (only when not at the root). Returns the chosen action; the caller descends/ascends/opens the fragments form. No trailing `/` in labels — huh v1.0.0 Select binds `/` to filter mode.

- `promptFragmentsLevelHuh(path, frags, picked, stdin, stdout)` — the fragments form. A single group holding the `huh.MultiSelect` (space toggles) seeded with the current level's already-picked fragments followed by a `huh.Confirm` with `Done`/`Back` buttons (`Back` hidden at the root via an empty negative label, which huh renders as a single button). Being in one group, the group view stacks every field, so the buttons stay visible at the bottom while fragments are toggled. The picked map is updated to the final multi-select state for this level's fragments; the returned bool is true on `Done`.

- `promptFragmentsBrowserHuh(fragNames, stdin, stdout)` — the loop. Each iteration: if the level has subfolders, run `promptFolderHuh` and act (descend → `continue` back to the top, so the deeper level re-runs the loop; `← up` → pop path; `✓ fragments here` → fall through). Otherwise run `promptFragmentsLevelHuh`; `Done` → return sorted `picked` (deterministic `extends:`; no test asserts order), `Back` → pop path and loop. `form.Run()` errors (interrupt/Esc) propagate like the other huh prompts.

Edge case: a fragment and a subfolder sharing a name under one directory (e.g. user `lang/bash.yaml` plus `lang/bash/x.yaml`) — the navigation form lists the subfolder and the fragments form lists the fragment, so they never collide. No special handling required.

## Wizard wiring

In `Run` (`internal/scaffold/scaffold.go`):

- Build `fragNames := cat.FragmentDisplayNames()` once.
- TTY path → `picked = promptFragmentsBrowserHuh(fragNames, stdin, stdout)`.
- Text path → `picked = promptFragments(fragNames, reader, stderr)` (unchanged shape).
- Returned display names still resolve via the existing `cat.FragmentByDisplayName` loop and append to `bases` unchanged.
- Remove the now-unused `FragmentNames()` from `internal/scaffold/fragments.go` (dropping its `sort` import, which only `FragmentNames` used) and the now-dead `promptFragmentsHuh` from `internal/scaffold/scaffold.go`; keep `Fragments()` (used by `TestFragmentsAreValid` and `TestValidateFragmentRejectsIdentityFields`).

## Testing

- Unit tests for `fragTree`: root folder listing, nested depth, top-level fragments, empty directory, folder-vs-leaf separation. Ordering/leaf-vs-dir only — dedup is tested upstream.
- New test for `Catalog.FragmentDisplayNames()` in `internal/profile/catalog_test.go` (next to `TestProfileDisplayNamesExcludesFragments`): fragment-only filtering, user shadow dedup, sorting.
- Existing text-mode wizard tests keep passing: the prompt shape is unchanged and `TestDryRunInteractivePrompts` only asserts the `Fragments` substring. The text-mode prompt now lists user fragments, which the fixture-based tests already cover since `fixtureLoader` includes user dir fragments.
- The huh browser loop is not driven in tests (huh requires a real terminal); `fragTree` carries the logic coverage and the loop stays thin.
- `TestFragmentsAreValid` / `TestValidateFragmentRejectsIdentityFields` continue to use `Fragments()` and are unaffected.

## Out of scope

- The "Extend a base profile" screen in the New-profile flow (lists profiles only, not fragments).
- Shell completion for `--extends` (`completeNames`).
- Changing the text-mode prompt format.
