package config

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jgillich/toolpod/internal/catalog"
	"gopkg.in/yaml.v3"
)

// Catalog is the merged set of built-in + user raw configs, keyed by profile name.
type Catalog struct {
	entries map[string]RawConfig
}

// Get returns the raw config for a profile name, plus whether it was found.
func (c Catalog) Get(name string) (RawConfig, bool) {
	rc, ok := c.entries[name]
	return rc, ok
}

// Names returns all profile names in the catalog, sorted.
func (c Catalog) Names() []string {
	names := make([]string, 0, len(c.entries))
	for n := range c.entries {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// LoadCatalog loads embedded built-ins, then user configs from userDir (if non-empty),
// with user entries shadowing built-ins of the same name.
func LoadCatalog(userDir string) (Catalog, error) {
	entries := map[string]RawConfig{}

	if err := loadBuiltins(entries); err != nil {
		return Catalog{}, err
	}

	if userDir != "" {
		if err := loadUserDir(userDir, entries); err != nil {
			return Catalog{}, err
		}
	}

	return Catalog{entries: entries}, nil
}

func loadBuiltins(entries map[string]RawConfig) error {
	root := "configs"
	return fs.WalkDir(catalog.Configs, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		data, err := catalog.Configs.ReadFile(path)
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		rc, err := parseRaw(data, "built-in:"+path)
		if err != nil {
			return err
		}
		entries[name] = rc
		return nil
	})
}

func loadUserDir(dir string, entries map[string]RawConfig) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		rc, err := parseRaw(data, path)
		if err != nil {
			return err
		}
		entries[name] = rc // shadow
		return nil
	})
}

// parseRaw parses YAML bytes into a RawConfig with the given source path.
func parseRaw(data []byte, path string) (RawConfig, error) {
	var rc RawConfig
	rc.Path = path
	if err := yaml.Unmarshal(data, &rc.Config); err != nil {
		return RawConfig{}, ConfigError{
			Path:    path,
			Message: fmt.Sprintf("YAML parse error: %v", err),
		}
	}
	return rc, nil
}

// DefaultUserConfigDir returns the default user config dir for the current OS.
// Used by the CLI when --config-dir is not set.
func DefaultUserConfigDir() string {
	if dir := os.Getenv("TOOLPOD_CONFIG_DIR"); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "toolpod")
	}
	return ""
}
