package scaffold

import (
	"errors"
	"io"
	"sort"
	"strings"
)

// Action values for the fragment-browser loop.
const (
	browserDone   = "__done__"
	browserCancel = "__cancel__"
)

// errNavCancelled reports that the user cancelled the folder navigation.
var errNavCancelled = errors.New("fragment selection cancelled")

// fragmentNav is the folder → fragments structure backing the picker. Fragments
// are grouped by their first path segment; names without a "/" sit at the root.
type fragmentNav struct {
	folders  []string                   // sorted top-level folder names
	byFolder map[string][]folderNavItem // each folder's fragment rows
	topFrags []folderNavItem            // top-level fragment rows
}

// promptFragmentsBrowserHuh runs the folder-structured fragment picker: one
// screen of folders whose fragments expand inline below them (Enter toggles a
// folder, Space toggles a fragment). The picked display names are returned
// sorted; cancelling returns errNavCancelled.
func promptFragmentsBrowserHuh(fragNames []string, descs map[string]string, stdin io.Reader, stdout io.Writer) ([]string, error) {
	nav := buildFragmentNav(fragNames, descs)
	if len(nav.folders) == 0 && len(nav.topFrags) == 0 {
		return nil, nil
	}
	return runFolderNav(nav, stdin, stdout)
}

// buildFragmentNav groups the fragment display names into the flat two-level
// structure the picker renders: sorted folders, each folder's sorted fragment
// rows, and root-level fragments. Names deeper than two segments flatten — the
// whole remainder after the folder becomes the row's leaf label, so nothing is
// dropped.
func buildFragmentNav(fragNames []string, descs map[string]string) fragmentNav {
	nav := fragmentNav{byFolder: map[string][]folderNavItem{}}
	seenFolders := map[string]bool{}
	seenTop := map[string]bool{}
	for _, name := range fragNames {
		folder, leaf := splitFirst(name)
		if folder == "" {
			if !seenTop[name] {
				seenTop[name] = true
				nav.topFrags = append(nav.topFrags, fragmentRow(name, leaf, descs))
			}
			continue
		}
		if !seenFolders[folder] {
			seenFolders[folder] = true
			nav.folders = append(nav.folders, folder)
		}
		nav.byFolder[folder] = append(nav.byFolder[folder], fragmentRow(name, leaf, descs))
	}
	sort.Strings(nav.folders)
	for folder := range nav.byFolder {
		sort.Slice(nav.byFolder[folder], func(i, j int) bool {
			return nav.byFolder[folder][i].display < nav.byFolder[folder][j].display
		})
	}
	sort.Slice(nav.topFrags, func(i, j int) bool {
		return nav.topFrags[i].display < nav.topFrags[j].display
	})
	return nav
}

// splitFirst returns the first path segment of a display name and the rest; a
// name with no "/" yields ("", name).
func splitFirst(name string) (string, string) {
	if i := strings.Index(name, "/"); i >= 0 {
		return name[:i], name[i+1:]
	}
	return "", name
}

// fragmentRow builds a fragment list row. The label shows the leaf (its
// remainder after the folder prefix) plus the description when one exists.
func fragmentRow(display, leaf string, descs map[string]string) folderNavItem {
	label := leaf
	if d, ok := descs[display]; ok && d != "" {
		label = leaf + " — " + d
	}
	return folderNavItem{kind: itemFragment, label: label, display: display}
}

// finishPicked returns the accumulated selection, sorted by display name.
func finishPicked(picked map[string]bool) []string {
	out := make([]string, 0, len(picked))
	for dn := range picked {
		out = append(out, dn)
	}
	sort.Strings(out)
	return out
}
