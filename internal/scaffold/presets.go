package scaffold

import (
	"fmt"
	"sort"
	"sync"

	"github.com/jgillich/toolpod/internal/catalog"
	"github.com/jgillich/toolpod/internal/profile"
)

var (
	presets     map[string]profile.RawProfile
	presetsOnce sync.Once
)

// Presets returns the built-in presets loaded from embedded YAML files.
// It panics on load failure — a broken built-in preset should fail loudly.
func Presets() map[string]profile.RawProfile {
	presetsOnce.Do(func() {
		m, err := profile.LoadPresets(catalog.Presets, "presets")
		if err != nil {
			panic(fmt.Sprintf("loading built-in presets: %v", err))
		}
		presets = m
	})
	return presets
}

// PresetNames returns sorted preset names for display.
func PresetNames() []string {
	names := make([]string, 0, len(Presets()))
	for n := range Presets() {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func validatePreset(name string, p profile.RawProfile) error {
	if p.Extends != "" || p.Image != "" || p.Build != nil || len(p.Command) > 0 || p.Version != 0 {
		return fmt.Errorf("preset %q must not set extends/image/build/command/version", name)
	}
	return nil
}
