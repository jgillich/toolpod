# Folder-structured fragment picker for `tpd init`

Date: 2026-08-06

## Goal

The `tpd init` wizard's fragment picker currently lists every fragment in one flat list (`promptFragmentsHuh(FragmentNames(), …)` in `internal/scaffold/scaffold.go:176`). Rework it into a folder browser: the root screen shows the top-level directories (`cloud`, `gui`, `infra`, `lang`, `service`, `vcs`, plus any user directories), and entering a directory shows the fragments inside it with an option to go back. Fragments from multiple folders accumulate into the final selection.

Decisions confirmed with the user:
- **Interaction model:** toggle browser — each screen is a single select listing folders to descend into, fragments to toggle on/off, `Back` to ascend, `Done` to finish.
- **User fragments:** the picker offers user fragments from `~/.config/tpd/fragments/` in addition to built-ins (currently built-ins only).
- **Non-TTY prompt:** the text-mode comma-list prompt is unchanged in shape (only its source list widens to include user fragments).

## Fragment listing source

Add `Catalog.FragmentDisplayNames()` in `internal/profile/catalog.go`, mirroring `ProfileDisplayNames()` but filtered to `c.fragments`. Returns sorted, deduped display names (e.g. `lang/go`, `lang/javascript`, user `creds/ssh`). User entries shadow core entries of the same display name, so each name appears once — dedup/shadowing happens here, not in the tree builder. This becomes the single source for both the browser and the text prompt, replacing `FragmentNames()` (built-ins only).

Note: a cross-type collision (user profile `lang/go.yaml` vs built-in fragment `core/lang/go`) is a hard catalog-load error (`catalog.go:195`), so the wizard never reaches the picker in that state. Pre-existing, out of scope.

## Tree navigation

New file `internal/scaffold/browser.go` with two pieces:

- `fragTree(fragNames []string, path []string) (dirs, frags []string)` — pure function. For the current path (e.g. `["lang"]`) it walks the display names and returns the sorted subdirectory segments and the sorted leaf fragment display names directly under it. The root path is empty; a top-level fragment (display name with no `/`) appears at root alongside folders. Works at any nesting depth because it operates on `/`-separated display-name segments. This is the testable core of the browser.

- `promptFragmentsBrowserHuh(fragNames []string, stdin io.Reader, stdout io.Writer) ([]string, error)` — a loop that renders one `huh.Select` per screen:
  - Title shows the current path and picked count, e.g. `Fragments — /lang (2 selected)`.
  - Options, in order: folders as `> cloud` (no trailing `/` — huh v1.0.0 Select binds the `/` key to filter mode, so labels must not contain `/`), fragments as `✓ aws` / `  aws`, then `Back` on non-root screens, then `Done` on every screen.
  - Choice handling: folder → push segment onto path; fragment → toggle in `picked`; `Back` → pop path; `Done` → return sorted `picked` (display names). Sorted output is a deliberate change from the old `MultiSelect`'s toggle order: deterministic `extends:` in the generated file; no test asserts order.
  - Cursor retention: re-render seeds each screen's value to the option just activated — toggle → same fragment, `Back` → the folder returned from, descend → default first option — so per-toggle re-renders don't reset the cursor to the top of long folders.
  - `form.Run()` errors (interrupt/Esc) propagate like the other huh prompts.

Edge case: a fragment and a subfolder sharing a name under one directory (e.g. user `lang/bash.yaml` plus `lang/bash/x.yaml`) renders as separate folder and fragment rows; the distinct markers disambiguate. No special handling required.

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
