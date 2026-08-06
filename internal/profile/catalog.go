package profile

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jgillich/tpd/internal/catalog"
	"gopkg.in/yaml.v3"
)

// Catalog is the merged set of built-in + user raw profiles and fragments,
// keyed by FullName (canonical "ns/name").
type Catalog struct {
	entries    map[string]RawProfile // keyed by FullName
	namespaces map[string]bool       // registered prefixes: "core", "", future remotes
	fragments  map[string]bool       // FullNames that are fragments (not profiles)
}

func (c Catalog) IsFragment(name string) bool {
	return c.fragments[name]
}

// Namespaces returns the registered namespace set (for CLI ref parsing).
func (c Catalog) Namespaces() map[string]bool {
	return c.namespaces
}

func (c Catalog) Get(name string) (RawProfile, bool) {
	rc, ok := c.entries[name]
	return rc, ok
}

func (c Catalog) Names() []string {
	names := make([]string, 0, len(c.entries))
	for n := range c.entries {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (c Catalog) ProfileNames() []string {
	names := make([]string, 0, len(c.entries))
	for n := range c.entries {
		if c.fragments[n] {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// DisplayNames returns the set of unqualified display names, deduplicated
// across namespaces. A user entry shadows a core entry of the same display
// name (user wins, shown once). Core-only entries show as the bare name.
func (c Catalog) DisplayNames() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(c.entries))
	// User namespace first so it shadows core.
	for _, name := range c.entries {
		if name.Namespace == "" {
			seen[name.DisplayName()] = true
			out = append(out, name.DisplayName())
		}
	}
	for _, name := range c.entries {
		if name.Namespace != "" {
			dn := name.DisplayName()
			if !seen[dn] {
				seen[dn] = true
				out = append(out, dn)
			}
		}
	}
	sort.Strings(out)
	return out
}

// FragmentDisplayNames is DisplayNames filtered to fragments only.
func (c Catalog) FragmentDisplayNames() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(c.entries))
	for _, name := range c.entries {
		if name.Namespace == "" && c.fragments[name.FullName()] {
			seen[name.DisplayName()] = true
			out = append(out, name.DisplayName())
		}
	}
	for _, name := range c.entries {
		if name.Namespace != "" && c.fragments[name.FullName()] {
			dn := name.DisplayName()
			if !seen[dn] {
				seen[dn] = true
				out = append(out, dn)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ProfileDisplayNames is DisplayNames filtered to non-fragments.
func (c Catalog) ProfileDisplayNames() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(c.entries))
	for _, name := range c.entries {
		if name.Namespace == "" && !c.fragments[name.FullName()] {
			seen[name.DisplayName()] = true
			out = append(out, name.DisplayName())
		}
	}
	for _, name := range c.entries {
		if name.Namespace != "" && !c.fragments[name.FullName()] {
			dn := name.DisplayName()
			if !seen[dn] {
				seen[dn] = true
				out = append(out, dn)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Source reports the provenance of a display name: "user" (user-only),
// "core" (core-only), or "user shadow" (user entry shadowing a core entry).
func (c Catalog) Source(displayName string) string {
	_, hasUser := c.entries[displayName]
	coreKey := "core/" + displayName
	_, hasCore := c.entries[coreKey]
	switch {
	case hasUser && hasCore:
		return "user shadow"
	case hasUser:
		return "user"
	case hasCore:
		return "core"
	default:
		return "core"
	}
}

// FragmentByDisplayName resolves a fragment display name to its canonical
// FullName. A user fragment wins over a core fragment of the same name.
func (c Catalog) FragmentByDisplayName(name string) (string, bool) {
	if _, ok := c.entries[name]; ok && c.fragments[name] {
		return name, true
	}
	coreKey := "core/" + name
	if _, ok := c.entries[coreKey]; ok && c.fragments[coreKey] {
		return coreKey, true
	}
	return "", false
}

// AddRaw inserts a raw profile into the catalog under its FullName, shadowing
// any existing entry of the same name. Used by init to overlay generated
// content for validation.
func (c *Catalog) AddRaw(ns, name string, rc RawProfile) {
	rc.Namespace = ns
	rc.Name = name
	c.entries[rc.FullName()] = rc
	delete(c.fragments, rc.FullName())
}

// LoadProfiles loads embedded built-ins, then user profiles from userDir (if non-empty),
// with user entries shadowing built-ins of the same name.
func LoadProfiles(userDir string) (Catalog, error) {
	return loadCatalog(catalog.Profiles, catalog.Fragments, userDir)
}

// LoadCatalog loads a catalog from explicit built-in sources plus a user
// profile directory, mirroring LoadProfiles with the built-in filesystems
// injected. Intended for loading stable test fixtures; production code uses
// LoadProfiles.
func LoadCatalog(pfs, ffs fs.ReadFileFS, userDir string) (Catalog, error) {
	return loadCatalog(pfs, ffs, userDir)
}

// loadCatalog is LoadProfiles with the built-in sources injected, so tests can
// run the same loading pipeline against a stable fixture instead of the live
// embedded catalog.
func loadCatalog(pfs, ffs fs.ReadFileFS, userDir string) (Catalog, error) {
	entries := map[string]RawProfile{}
	fragmentNames := map[string]bool{}

	if err := loadBuiltinsFrom(pfs, entries); err != nil {
		return Catalog{}, err
	}

	if err := loadBuiltinFragmentsFrom(ffs, entries, fragmentNames); err != nil {
		return Catalog{}, err
	}

	if userDir != "" {
		if err := loadUserDir(userDir, entries, fragmentNames); err != nil {
			return Catalog{}, err
		}
		// User fragments live in a directory parallel to profiles
		// ($XDG_CONFIG_HOME/tpd/fragments/), not inside the profiles dir.
		userFragDir := filepath.Join(filepath.Dir(userDir), "fragments")
		if err := loadUserFragments(userFragDir, entries, fragmentNames); err != nil {
			return Catalog{}, err
		}
	}

	// A display name may not be both a fragment (in any namespace) and a
	// profile (in any namespace); unqualified resolution and the display APIs
	// can't disambiguate that. The intra-namespace collision checks above
	// already reject fragment/profile clashes within the same FullName.
	if collisions := crossTypeDisplayNameCollisions(entries, fragmentNames); len(collisions) > 0 {
		return Catalog{}, fmt.Errorf("display name %q is both a fragment and a profile across namespaces", collisions[0])
	}

	namespaces := map[string]bool{"": true, "core": true}
	return Catalog{entries: entries, namespaces: namespaces, fragments: fragmentNames}, nil
}

// LoadProfilesTolerant is like LoadProfiles but skips a malformed user
// profile/fragment file (logging it via warn) instead of aborting the whole
// load. Built-ins always load strictly. Used by `tpd prune`, where one
// broken user file must not prevent computing liveness for the rest — a
// strict abort there is a regression from the old prune (which never read
// profiles) and risks pruning live resources. Also used by shell completion
// (cmd/tpd/completion.go) so a malformed user file never breaks tab
// completion.
func LoadProfilesTolerant(userDir string, warn func(string)) (Catalog, error) {
	return loadCatalogTolerant(catalog.Profiles, catalog.Fragments, userDir, warn)
}

func loadCatalogTolerant(pfs, ffs fs.ReadFileFS, userDir string, warn func(string)) (Catalog, error) {
	entries := map[string]RawProfile{}
	fragmentNames := map[string]bool{}

	if err := loadBuiltinsFrom(pfs, entries); err != nil {
		return Catalog{}, err
	}
	if err := loadBuiltinFragmentsFrom(ffs, entries, fragmentNames); err != nil {
		return Catalog{}, err
	}

	if userDir != "" {
		loadUserDirTolerant(userDir, entries, fragmentNames, warn)
		userFragDir := filepath.Join(filepath.Dir(userDir), "fragments")
		loadUserFragmentsTolerant(userFragDir, entries, fragmentNames, warn)
	}

	// Cross-type display-name collisions drop the fragment (kept profiles
	// stay launchable) instead of aborting, mirroring the tolerant pattern.
	for _, dn := range crossTypeDisplayNameCollisions(entries, fragmentNames) {
		warn(fmt.Sprintf("display name %q is both a fragment and a profile across namespaces; dropping the fragment", dn))
		for key := range fragmentNames {
			if entries[key].DisplayName() == dn {
				delete(entries, key)
				delete(fragmentNames, key)
			}
		}
	}

	namespaces := map[string]bool{"": true, "core": true}
	return Catalog{entries: entries, namespaces: namespaces, fragments: fragmentNames}, nil
}

// crossTypeDisplayNameCollisions returns the display names that appear as both
// a fragment (in any namespace) and a profile (in any namespace).
func crossTypeDisplayNameCollisions(entries map[string]RawProfile, fragmentNames map[string]bool) []string {
	dnIsFragment := map[string]bool{}
	dnIsProfile := map[string]bool{}
	for key, rc := range entries {
		dn := rc.DisplayName()
		if fragmentNames[key] {
			dnIsFragment[dn] = true
		} else {
			dnIsProfile[dn] = true
		}
	}
	var out []string
	for dn := range dnIsFragment {
		if dnIsProfile[dn] {
			out = append(out, dn)
		}
	}
	sort.Strings(out)
	return out
}

func loadUserDirTolerant(dir string, entries map[string]RawProfile, fragmentNames map[string]bool, warn func(string)) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return
	}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			warn(fmt.Sprintf("%s: %v", path, err))
			return nil
		}
		if d.IsDir() {
			if d.Name() == "fragments" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			warn(fmt.Sprintf("%s: %v", path, err))
			return nil
		}
		name := nameFromPath(dir, path)
		if err := validateHierarchicalName(name, path, builtinNamespaces); err != nil {
			warn(path + ": " + err.Error())
			return nil
		}
		rc, err := parseRaw(data, path)
		if err != nil {
			warn(path + ": " + err.Error())
			return nil
		}
		if err := validateReservedName(rc, name); err != nil {
			warn(path + ": " + err.Error())
			return nil
		}
		rc.Namespace = ""
		rc.Name = name
		if fragmentNames[rc.FullName()] {
			warn(path + ": name collision: profile and fragment share name " + rc.FullName())
			return nil
		}
		entries[rc.FullName()] = rc
		return nil
	})
}

func loadUserFragmentsTolerant(dir string, entries map[string]RawProfile, fragmentNames map[string]bool, warn func(string)) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return
	}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			warn(fmt.Sprintf("%s: %v", path, err))
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			warn(fmt.Sprintf("%s: %v", path, err))
			return nil
		}
		name := nameFromPath(dir, path)
		if err := validateHierarchicalName(name, path, builtinNamespaces); err != nil {
			warn(path + ": " + err.Error())
			return nil
		}
		rc, err := parseRaw(data, path)
		if err != nil {
			warn(path + ": " + err.Error())
			return nil
		}
		if err := validateFragmentName(name, rc); err != nil {
			warn(path + ": " + err.Error())
			return nil
		}
		rc.Namespace = ""
		rc.Name = name
		if _, exists := entries[rc.FullName()]; exists {
			warn(path + ": name collision: fragment and profile share name " + rc.FullName())
			return nil
		}
		entries[rc.FullName()] = rc
		fragmentNames[rc.FullName()] = true
		return nil
	})
}

func loadBuiltinsFrom(fsys fs.ReadFileFS, entries map[string]RawProfile) error {
	root := "profiles"
	return fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
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
		name := nameFromPath(root, path)
		rc, err := parseRaw(data, "built-in:"+path)
		if err != nil {
			return err
		}
		if err := validateReservedName(rc, name); err != nil {
			return err
		}
		rc.Namespace = "core"
		rc.Name = name
		entries[rc.FullName()] = rc
		return nil
	})
}

func loadUserDir(dir string, entries map[string]RawProfile, fragmentNames map[string]bool) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip the fragments/ subdirectory; it is loaded separately by loadUserFragments.
			if d.Name() == "fragments" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name := nameFromPath(dir, path)
		if err := validateHierarchicalName(name, path, builtinNamespaces); err != nil {
			return err
		}
		rc, err := parseRaw(data, path)
		if err != nil {
			return err
		}
		if err := validateReservedName(rc, name); err != nil {
			return err
		}
		rc.Namespace = ""
		rc.Name = name
		if fragmentNames[rc.FullName()] {
			return ProfileError{Path: rc.Path, Message: "name collision: profile and fragment share name " + rc.FullName()}
		}
		entries[rc.FullName()] = rc
		return nil
	})
}

// builtinNamespaces is the namespace registry available at parse time (before
// the Catalog is assembled). "core" and "" are always registered; remote
// namespaces (future) will be added by their loader.
var builtinNamespaces = map[string]bool{"": true, "core": true}

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
	if err := rc.ExtendsList.Resolve(builtinNamespaces); err != nil {
		return RawProfile{}, ProfileError{Path: path, Message: err.Error()}
	}
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
		"ports":       {},
		"devices":     {},
		"dbus":        {},
		"packages":    {},
		"repos":       {},
		"files":       {},
		"services":    {},
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

// NewProfileCatalogForTest creates a Catalog from a raw map, stamping every
// entry as a core-namespace built-in. For test use only; production code uses
// LoadProfiles.
func NewProfileCatalogForTest(entries map[string]RawProfile) Catalog {
	out := make(map[string]RawProfile, len(entries))
	for k, v := range entries {
		v.Namespace = "core"
		v.Name = k
		// Only Raw-form extends needs parsing; pre-set Resolved (from
		// hand-built entries) must survive untouched.
		if len(v.ExtendsList.Raw) > 0 {
			if err := v.ExtendsList.Resolve(map[string]bool{"": true, "core": true}); err != nil {
				panic("NewProfileCatalogForTest: bad extends in " + k + ": " + err.Error())
			}
		}
		out[v.FullName()] = v
	}
	return Catalog{entries: out, namespaces: map[string]bool{"": true, "core": true}, fragments: map[string]bool{}}
}

// LoadFragments loads YAML fragment files from an embedded filesystem (e.g.
// catalog.Fragments) and returns them keyed by fragment name. Each file must
// be a bare profile fragment (caches/mounts/tools/labels/env), optionally
// extending other fragments, without image/build/command/version —
// validateFragmentName enforces this.
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
		name := nameFromPath(root, path)
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

func loadBuiltinFragmentsFrom(fsys fs.ReadFileFS, entries map[string]RawProfile, fragmentNames map[string]bool) error {
	root := "fragments"
	return fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
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
		name := nameFromPath(root, path)
		rc, err := parseRaw(data, "built-in-fragment:"+path)
		if err != nil {
			return err
		}
		if err := validateFragmentName(name, rc); err != nil {
			return err
		}
		rc.Namespace = "core"
		rc.Name = name
		if _, exists := entries[rc.FullName()]; exists {
			return ProfileError{Path: rc.Path, Message: "name collision: fragment and profile share name " + rc.FullName()}
		}
		entries[rc.FullName()] = rc
		fragmentNames[rc.FullName()] = true
		return nil
	})
}

func loadUserFragments(dir string, entries map[string]RawProfile, fragmentNames map[string]bool) error {
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
		name := nameFromPath(dir, path)
		if err := validateHierarchicalName(name, path, builtinNamespaces); err != nil {
			return err
		}
		rc, err := parseRaw(data, path)
		if err != nil {
			return err
		}
		if err := validateFragmentName(name, rc); err != nil {
			return err
		}
		rc.Namespace = ""
		rc.Name = name
		if _, exists := entries[rc.FullName()]; exists {
			return ProfileError{Path: rc.Path, Message: "name collision: fragment and profile share name " + rc.FullName()}
		}
		entries[rc.FullName()] = rc
		fragmentNames[rc.FullName()] = true
		return nil
	})
}

func ParseRaw(data []byte, path string) (RawProfile, error) {
	return parseRaw(data, path)
}

// profileNameRe is the single strict grammar for profile names. It matches
// Docker's container-name charset, so the derived container name
// (tpd-<name>-<rand> in docker_run.go) and Hostname are always valid:
// [a-zA-Z0-9][a-zA-Z0-9._-]*. Rejects ':', '\', '/', whitespace, and control
// characters. Applied uniformly to CLI input (ValidateName), names derived
// from user filenames, and the container name/hostname construction.
var profileNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// nameFromPath derives the hierarchical name for a YAML file from its path
// relative to the catalog root: root/lang/go.yaml -> "lang/go".
func nameFromPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = filepath.Base(path)
	}
	name := strings.TrimSuffix(filepath.ToSlash(rel), ".yaml")
	return name
}

// validateHierarchicalName checks every segment of a file-derived name and
// rejects a first segment that names a registered namespace (core), which
// would collide with built-in entries.
func validateHierarchicalName(name, path string, namespaces map[string]bool) error {
	for _, seg := range strings.Split(name, "/") {
		if !profileNameRe.MatchString(seg) || strings.Contains(seg, "..") {
			return ProfileError{Path: path, Message: "invalid profile name derived from filename: " + name}
		}
	}
	if strings.Contains(name, "/") {
		first := strings.SplitN(name, "/", 2)[0]
		if namespaces[first] {
			return ProfileError{Path: path, Message: first + " is a reserved namespace prefix"}
		}
	}
	return nil
}

func validateFragmentName(name string, rc RawProfile) error {
	if rc.Version != 1 {
		return ProfileError{Path: rc.Path, Message: "fragment " + name + " must set version: 1"}
	}
	if rc.Image != "" || len(rc.Command) > 0 {
		return ProfileError{Path: rc.Path, Message: "fragment " + name + " must not set image/command"}
	}
	return nil
}

// DefaultProfileDir returns the default user profile directory for the current OS.
// Honors XDG_CONFIG_HOME on Linux via os.UserConfigDir. Used by the CLI when
// --profile-dir is not set.
func DefaultProfileDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return ""
	}
	return filepath.Join(base, "tpd", "profiles")
}

// DefaultFragmentDir returns the default user fragment directory, a sibling of
// DefaultProfileDir under the tpd config root.
func DefaultFragmentDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return ""
	}
	return filepath.Join(base, "tpd", "fragments")
}
