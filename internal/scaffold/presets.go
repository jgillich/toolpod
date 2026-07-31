package scaffold

import (
	"fmt"
	"sort"

	"github.com/jgillich/toolpod/internal/profile"
)

var presets = map[string]profile.RawProfile{
	"npm":   {Profile: profile.Profile{Caches: map[string]string{"npm": "~/.npm"}}},
	"cargo": {Profile: profile.Profile{Caches: map[string]string{"cargo": "~/.cargo"}}},
	"pip":   {Profile: profile.Profile{Caches: map[string]string{"pip": "~/.cache/pip"}}},
	"go":    {Profile: profile.Profile{Caches: map[string]string{"go": "~/go"}}},

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

func validatePreset(name string, p profile.RawProfile) error {
	if p.Extends != "" || p.Image != "" || p.Build != nil || len(p.Command) > 0 || p.Version != 0 {
		return fmt.Errorf("preset %q must not set extends/image/build/command/version", name)
	}
	return nil
}

// PresetNames returns sorted preset names for display.
func PresetNames() []string {
	names := make([]string, 0, len(presets))
	for n := range presets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
