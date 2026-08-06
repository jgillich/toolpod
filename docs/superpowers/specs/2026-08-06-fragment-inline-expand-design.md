# Inline fragment expansion in the init picker

## Summary

Replace the folder-structured fragment picker's two-screen flow (folder nav,
then a separate fragments form) with a single screen where a folder's
fragments expand inline below it. Enter toggles a folder's expansion; Space
toggles a highlighted fragment (huh MultiSelect markers: `✓` picked, `•`
unpicked); Tab → Done finalizes.

## Background

The picker today walks the fragment display-name tree level by level:
`promptFragmentsBrowserHuh` loops over a `path`, showing the folder nav when a
level has subfolders and the separate `promptFragmentsLevelHuh` form
(MultiSelect + Done/Back) when it reaches leaf fragments. Each folder visit is
a separate screen: enter a folder, toggle its fragments, Done, back up.

The catalog is 2 levels deep (folders → fragments). Display names can be
deeper in the schema, but the UI assumes 2 levels.

## Behavior

One screen, one run of the folder nav:

- **Rows** are a flat list: folder rows plus, for each expanded folder, its
  fragment rows indented below it. Top-level fragments (no `/`) render as
  fragment rows in the root list.
- **Enter** on a folder toggles its expansion (`▸` collapsed / `▾` expanded);
  on a fragment it is a no-op.
- **Space** toggles a highlighted fragment.
- **Up/Down** navigate all visible rows; **/** filters visible rows.
- **Tab** focuses the Done button; **Enter** on it finalizes and returns the
  picked display names sorted; **esc/q/ctrl+c** cancels (ErrNavCancelled).
- Picked state persists across expand/collapse within the single screen.

## Design

### Data model (`internal/scaffold/foldernav.go`)

- `folderNavItem` gains a `kind` (`folder | fragment`). Folder items carry the
  folder name; fragment items carry the full display name and the leaf-only
  label.
- `folderNavModel` gains `expanded map[string]bool` (folder → open) and
  `picked map[string]bool` (display name → picked). The visible item list is
  rebuilt from `expanded` on change; `bubbles/list.SetItems` preserves the
  cursor, so the cursor stays on the toggled folder row.
- The delegate reads `expanded`/`picked` (by reference) at render time so a
  frame reflects toggles immediately.

### Item construction (`internal/scaffold/browser.go`)

New `buildFragmentNav(fragNames, descs)` replaces the path-walk helpers:

- Folders = sorted unique first path segments.
- Per folder: sorted leaf fragments; label = `leaf — desc` (description
  looked up by full display name, as today).
- Top-level fragments (names with no `/`) become fragment rows in the root
  list.
- Names deeper than 2 segments flatten: the full remainder becomes the leaf
  label (`aws/extra`), so nothing is dropped.

### Rendering

Keeps the previous huh-ThemeCharm styling (section border, title, cursor,
button, help). Fragment rows use huh MultiSelect colors:

- Folder row: `▸ name` / `▾ name`; cursor `> ` fuchsia; label green when
  highlighted, normal foreground otherwise.
- Fragment row: indented 2; `✓ name — desc` picked / `• name — desc` unpicked
  (SelectedPrefix `#02CF92`/`#02A877`, UnselectedPrefix `243`, picked text
  green, unpicked normal); cursor `> ` when highlighted.

### Flow

`promptFragmentsBrowserHuh` builds the initial items once and runs the folder
nav once. `runFolderNav` returns `([]string, error)` — picked display names or
the cancel error. `promptFragmentsLevelHuh`, the path loop, `browserUp`, and
the "fragments here" row are removed; the `huh` import drops out of
`browser.go`.

### Edge cases

- No folders and no fragments: return `[]` without showing an empty screen.
- Deeper-than-2 names flatten (above); the `"✓ fragments here"` affordance is
  gone since top-level fragments render directly.
- `/` filter searches only visible (expanded) rows — collapsed folders'
  fragments are not searchable until expanded. Accepted limitation.

## Testing

Update `internal/scaffold/browser_test.go`:

- `buildFragmentNav`: folder sorting, per-folder leaves, top-level rows,
  flattening of deeper names.
- `folderNavModel`: enter expands (item count grows) and collapses; space
  toggles picked; Done returns sorted picked; esc cancels; delegate renders
  `✓`/`•` markers.
- Drop tests for removed helpers (`fragTree` descent, path-qualified
  `fragmentLabel`).
