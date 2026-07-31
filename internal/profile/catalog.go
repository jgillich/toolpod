package profile

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jgillich/toolpod/internal/catalog"
	"gopkg.in/yaml.v3"
)

// Catalog is the merged set of built-in + user raw profiles, keyed by profile name.
type Catalog struct {
	entries  map[string]RawProfile // merged view: user shadows built-in
	builtins map[string]RawProfile // built-ins only, for extends-self
}

// Get returns the raw profile for a profile name, plus whether it was found.
func (c Catalog) Get(name string) (RawProfile, bool) {
	rc, ok := c.entries[name]
	return rc, ok
}

// GetBuiltin returns the built-in profile for a name, plus whether it was found.
func (c Catalog) GetBuiltin(name string) (RawProfile, bool) {
	rc, ok := c.builtins[name]
	return rc, ok
}

// IsUserShadow returns true if a user file shadows a built-in of this name.
func (c Catalog) IsUserShadow(name string) bool {
	_, hasBuiltin := c.builtins[name]
	_, hasEntry := c.entries[name]
	if !hasBuiltin || !hasEntry {
		return false
	}
	// The entry is a shadow if its Path is not a built-in path.
	return c.entries[name].Path != c.builtins[name].Path
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

// LoadProfiles loads embedded built-ins, then user profiles from userDir (if non-empty),
// with user entries shadowing built-ins of the same name.
func LoadProfiles(userDir string) (Catalog, error) {
	builtins := map[string]RawProfile{}

	if err := loadBuiltins(builtins); err != nil {
		return Catalog{}, err
	}

	entries := make(map[string]RawProfile, len(builtins))
	for k, v := range builtins {
		entries[k] = v
	}

	if userDir != "" {
		if err := loadUserDir(userDir, entries); err != nil {
			return Catalog{}, err
		}
	}

	return Catalog{entries: entries, builtins: builtins}, nil
}

func loadBuiltins(entries map[string]RawProfile) error {
	root := "profiles"
	return fs.WalkDir(catalog.Profiles, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		data, err := catalog.Profiles.ReadFile(path)
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		rc, err := parseRaw(data, "built-in:"+path)
		if err != nil {
			return err
		}
		if err := validateReservedName(rc, name); err != nil {
			return err
		}
		entries[name] = rc
		return nil
	})
}

func loadUserDir(dir string, entries map[string]RawProfile) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
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
		if err := validateReservedName(rc, name); err != nil {
			return err
		}
		entries[name] = rc // shadow
		return nil
	})
}

// parseRaw parses YAML bytes into a RawProfile with the given source path.
// It also captures explicit-null keys (for null-to-delete in merge) via
// a parallel yaml.Node parse of the map fields.
func parseRaw(data []byte, path string) (RawProfile, error) {
	var rc RawProfile
	rc.Path = path
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return RawProfile{}, ProfileError{
			Path:    path,
			Line:    lineFromError(err),
			Message: fmt.Sprintf("YAML parse error: %v", err),
		}
	}
	if err := yaml.Unmarshal(data, &rc.Profile); err != nil {
		return RawProfile{}, ProfileError{
			Path:    path,
			Line:    lineFromError(err),
			Message: fmt.Sprintf("YAML parse error: %v", err),
		}
	}
	rc.NullKeys = collectNullKeys(&root)
	return rc, nil
}

func lineFromError(err error) int {
	if te, ok := err.(*yaml.TypeError); ok {
		for _, e := range te.Errors {
			if line := extractLine(e); line > 0 {
				return line
			}
		}
	}
	return 0
}

func extractLine(s string) int {
	idx := strings.Index(s, "line ")
	if idx < 0 {
		return 0
	}
	rest := s[idx+5:]
	end := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	if end < 0 {
		end = len(rest)
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0
	}
	return n
}

// collectNullKeys returns the set of map keys whose value is explicitly null
// in the top-level or nested-map fields that support null-to-delete.
// Returns a map of field-name → null-key info. A map containing the "*"
// sentinel means the entire field is null (delete the whole field). Otherwise
// the listed keys are deleted within that field's nested map.
func collectNullKeys(root *yaml.Node) map[string]map[string]bool {
	nulls := map[string]map[string]bool{
		"mounts":      {},
		"environment": {},
		"tools":       {},
		"caches":      {},
		"labels":      {},
	}
	if root == nil || root.Kind != yaml.DocumentNode {
		return nulls
	}
	body := root.Content[0]
	if body == nil || body.Kind != yaml.MappingNode {
		return nulls
	}
	for i := 0; i+1 < len(body.Content); i += 2 {
		keyNode := body.Content[i]
		valNode := body.Content[i+1]
		tracked, ok := nulls[keyNode.Value]
		if !ok {
			continue
		}
		if valNode.Tag == "!!null" {
			nulls[keyNode.Value] = map[string]bool{"*": true}
			continue
		}
		if valNode.Kind == yaml.MappingNode {
			keys := map[string]bool{}
			for j := 0; j+1 < len(valNode.Content); j += 2 {
				if valNode.Content[j+1].Tag == "!!null" {
					keys[valNode.Content[j].Value] = true
				}
			}
			if len(keys) > 0 {
				nulls[keyNode.Value] = keys
			} else {
				nulls[keyNode.Value] = tracked
			}
		}
	}
	return nulls
}

// NewCatalogForTest creates a Catalog from a raw map. For test use only;
// production code uses LoadCatalog.
func NewProfileCatalogForTest(entries map[string]RawProfile) Catalog {
	builtins := make(map[string]RawProfile, len(entries))
	for k, v := range entries {
		builtins[k] = v
	}
	return Catalog{entries: entries, builtins: builtins}
}

// LoadFragments loads YAML fragment files from an embedded filesystem (e.g.
// catalog.Fragments) and returns them keyed by fragment name. Each file must
// be a bare profile fragment (caches/mounts/tools/labels/env) without
// extends/image/build/command/version — validateFragmentName enforces this.
func LoadFragments(fsys fs.ReadFileFS, root string) (map[string]RawProfile, error) {
	fragments := map[string]RawProfile{}
	err := fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		data, err := fsys.ReadFile(path)
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		rc, err := parseRaw(data, "fragment:"+name)
		if err != nil {
			return err
		}
		fragments[name] = rc
		return nil
	})
	if err != nil {
		return nil, err
	}
	return fragments, nil
}

// DefaultProfileDir returns the default user profile directory for the current OS.
// Honors XDG_CONFIG_HOME on Linux via os.UserConfigDir. Used by the CLI when
// --profile-dir is not set.
func DefaultProfileDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return ""
	}
	return filepath.Join(base, "toolpod", "profiles")
}
