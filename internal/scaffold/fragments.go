package scaffold

import (
	"fmt"
	"sort"
	"sync"

	"github.com/jgillich/toolpod/internal/catalog"
	"github.com/jgillich/toolpod/internal/profile"
)

var (
	fragments     map[string]profile.RawProfile
	fragmentsOnce sync.Once
)

// Fragments returns the built-in fragments loaded from embedded YAML files.
// It panics on load failure — a broken built-in fragment should fail loudly.
func Fragments() map[string]profile.RawProfile {
	fragmentsOnce.Do(func() {
		m, err := profile.LoadFragments(catalog.Fragments, "fragments")
		if err != nil {
			panic(fmt.Sprintf("loading built-in fragments: %v", err))
		}
		fragments = m
	})
	return fragments
}

// FragmentNames returns sorted fragment names for display.
func FragmentNames() []string {
	names := make([]string, 0, len(Fragments()))
	for n := range Fragments() {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// BuiltInProfiles returns sorted names of all built-in profiles for display.
func BuiltInProfiles() []string {
	cat, err := profile.LoadProfiles("")
	if err != nil {
		return nil
	}
	return cat.Names()
}

func validateFragment(name string, p profile.RawProfile) error {
	if len(p.ExtendsList) != 0 || p.Image != "" || p.Build != nil || len(p.Command) > 0 || p.Version != 0 {
		return fmt.Errorf("fragment %q must not set extends/image/build/command/version", name)
	}
	return nil
}
