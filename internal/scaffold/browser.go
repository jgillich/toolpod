package scaffold

import (
	"io"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
)

// Action values for the fragment-browser loop.
const (
	browserUp    = "__up__"
	browserFiles = "__files__"
)

// fragTree lists the subdirectories and leaf fragments directly under path in
// the fragment display-name tree. Top-level fragments (no "/") appear at the
// root alongside the top-level folders. fragNames must already be deduped —
// Catalog.FragmentDisplayNames does that upstream.
func fragTree(fragNames []string, path []string) (dirs, frags []string) {
	prefix := strings.Join(path, "/")
	seenDirs := map[string]bool{}
	seenFrags := map[string]bool{}
	for _, name := range fragNames {
		var rest string
		if prefix == "" {
			rest = name
		} else {
			if !strings.HasPrefix(name, prefix+"/") {
				continue
			}
			rest = name[len(prefix)+1:]
		}
		if i := strings.Index(rest, "/"); i >= 0 {
			seg := rest[:i]
			if !seenDirs[seg] {
				seenDirs[seg] = true
				dirs = append(dirs, seg)
			}
		} else if !seenFrags[rest] {
			seenFrags[rest] = true
			frags = append(frags, rest)
		}
	}
	sort.Strings(dirs)
	sort.Strings(frags)
	return dirs, frags
}

// promptFragmentsBrowserHuh runs the folder-structured fragment picker. Levels
// with subfolders show a navigation form whose single select fires immediately
// on Enter (descend, ascend, or open the level's own fragments); levels with
// no subfolders show a fragments form (multi-select + Done/Back buttons).
// Selections accumulate across levels and are returned sorted by display name.
func promptFragmentsBrowserHuh(fragNames []string, stdin io.Reader, stdout io.Writer) ([]string, error) {
	picked := map[string]bool{}
	var path []string
	for {
		dirs, frags := fragTree(fragNames, path)

		if len(dirs) > 0 {
			choice, err := promptFolderHuh(path, dirs, len(frags) > 0, stdin, stdout)
			if err != nil {
				return nil, err
			}
			switch {
			case strings.HasPrefix(choice, "dir:"):
				path = append(path, strings.TrimPrefix(choice, "dir:"))
				continue
			case choice == browserUp:
				path = path[:len(path)-1]
				continue
			case choice == browserFiles:
				// Fall through to the fragments form for this level.
			}
		}

		done, err := promptFragmentsLevelHuh(path, frags, picked, stdin, stdout)
		if err != nil {
			return nil, err
		}
		if done {
			out := make([]string, 0, len(picked))
			for dn := range picked {
				out = append(out, dn)
			}
			sort.Strings(out)
			return out, nil
		}
		path = path[:len(path)-1]
	}
}

// promptFolderHuh renders the folder-navigation form. Its single select fires
// immediately on Enter, so choosing a folder descends without an extra confirm.
func promptFolderHuh(path, dirs []string, hasFrags bool, stdin io.Reader, stdout io.Writer) (string, error) {
	opts := make([]huh.Option[string], 0, len(dirs)+2)
	for _, d := range dirs {
		opts = append(opts, huh.NewOption("▸ "+d, "dir:"+d))
	}
	if hasFrags {
		opts = append(opts, huh.NewOption("✓ fragments here", browserFiles))
	}
	if len(path) > 0 {
		opts = append(opts, huh.NewOption("← up", browserUp))
	}

	title := "Folder"
	if len(path) > 0 {
		title += " — /" + strings.Join(path, "/")
	}

	var choice string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title(title).
				Options(opts...).
				Value(&choice),
		),
	).WithInput(stdin).WithOutput(stdout)
	if err := form.Run(); err != nil {
		return "", err
	}
	return choice, nil
}

// promptFragmentsLevelHuh renders the fragments form for one level: a
// multi-select (space toggles) seeded with the fragments of this level already
// picked, with the Done/Back buttons in the same group so they stay visible at
// the bottom (Back hidden at the root). The picked map is updated to the final
// multi-select state for this level's fragments; the returned bool is true
// when the user confirmed with Done.
func promptFragmentsLevelHuh(path, frags []string, picked map[string]bool, stdin io.Reader, stdout io.Writer) (bool, error) {
	selected := make([]string, 0, len(frags))
	opts := make([]huh.Option[string], 0, len(frags))
	for _, f := range frags {
		opts = append(opts, huh.NewOption(f, f))
		if picked[fragDisplayName(path, f)] {
			selected = append(selected, f)
		}
	}

	var fields []huh.Field
	if len(frags) > 0 {
		title := "Fragments"
		if len(path) > 0 {
			title += " — /" + strings.Join(path, "/")
		}
		fields = append(fields,
			huh.NewMultiSelect[string]().
				Title(title).
				Options(opts...).
				Value(&selected),
		)
	}

	negative := "Back"
	if len(path) == 0 {
		negative = ""
	}
	var confirm bool
	fields = append(fields,
		huh.NewConfirm().
			Affirmative("Done").
			Negative(negative).
			Value(&confirm),
	)

	form := huh.NewForm(huh.NewGroup(fields...)).WithInput(stdin).WithOutput(stdout)
	if err := form.Run(); err != nil {
		return false, err
	}

	inSelected := make(map[string]bool, len(selected))
	for _, f := range selected {
		inSelected[f] = true
	}
	for _, f := range frags {
		picked[fragDisplayName(path, f)] = inSelected[f]
	}
	return confirm, nil
}

// fragDisplayName joins the current path and a leaf fragment into the display
// name used to key the picked set (an empty path yields the bare leaf).
func fragDisplayName(path []string, leaf string) string {
	if len(path) == 0 {
		return leaf
	}
	return strings.Join(append(append([]string{}, path...), leaf), "/")
}
