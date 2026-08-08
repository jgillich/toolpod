package profile

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jgillich/tpd/internal/catalog"
)

// fixtureCatalog loads the stable testdata/catalog fixture (never the live
// embedded catalog) plus optional user files, running the real loading
// pipeline against it.
func fixtureCatalog(t *testing.T, userDir string) (Catalog, error) {
	t.Helper()
	return fixtureCatalogWith(t, loadCatalog, userDir)
}

func fixtureCatalogTolerant(t *testing.T, userDir string, warn func(string)) (Catalog, error) {
	t.Helper()
	return fixtureCatalogWith(t, func(pfs, ffs fs.ReadFileFS, userDir string) (Catalog, error) {
		return loadCatalogTolerant(pfs, ffs, userDir, warn)
	}, userDir)
}

func fixtureCatalogWith(t *testing.T, load func(pfs, ffs fs.ReadFileFS, userDir string) (Catalog, error), userDir string) (Catalog, error) {
	t.Helper()
	fsys, ok := os.DirFS(filepath.Join("testdata", "catalog")).(fs.ReadFileFS)
	if !ok {
		t.Fatal("testdata/catalog must be a fs.ReadFileFS")
	}
	return load(fsys, fsys, userDir)
}

func TestBuiltinCatalogResolves(t *testing.T) {
	cat, err := LoadCatalog(catalog.Profiles, catalog.Fragments, "")
	if err != nil {
		t.Fatalf("LoadCatalog(embedded): %v", err)
	}
	for _, name := range cat.Names() {
		var err error
		if cat.IsFragment(name) {
			_, err = ResolveFragmentWithProv(cat, name)
		} else {
			_, err = ResolveProfileWithProv(cat, name)
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestBuiltinMiseProfilesImportDefaultsFirst asserts the catalog rule that any
// profile importing mise lists defaults first and therefore inherits the
// defaults fragment's templated memory cap.
func TestBuiltinMiseProfilesImportDefaultsFirst(t *testing.T) {
	cat, err := LoadCatalog(catalog.Profiles, catalog.Fragments, "")
	if err != nil {
		t.Fatalf("LoadCatalog(embedded): %v", err)
	}
	for _, name := range cat.Names() {
		if cat.IsFragment(name) {
			continue
		}
		rc, ok := cat.Get(name)
		if !ok {
			continue
		}
		for _, ref := range rc.ExtendsList.Resolved {
			if ref.Name != "mise" {
				continue
			}
			if len(rc.ExtendsList.Raw) == 0 || rc.ExtendsList.Raw[0] != "defaults" {
				t.Errorf("%s imports mise; extends must start with defaults, got %v", name, rc.ExtendsList.Raw)
			}
			resolved, err := ResolveProfileWithProv(cat, name)
			if err != nil {
				t.Errorf("%s: %v", name, err)
				break
			}
			if resolved.Resources == nil || !strings.Contains(resolved.Resources.Memory, "{{") {
				t.Errorf("%s imports mise but does not inherit the defaults memory cap", name)
			}
			break
		}
	}
}

// TestBuiltinExtendsOmitCoreNamespace asserts that built-in profiles and
// fragments reference other built-ins by unqualified name (toolchain/go, not
// core/toolchain/go), so user shadowing of a base fragment flows through to
// derived built-ins.
func TestBuiltinExtendsOmitCoreNamespace(t *testing.T) {
	cat, err := LoadCatalog(catalog.Profiles, catalog.Fragments, "")
	if err != nil {
		t.Fatalf("LoadCatalog(embedded): %v", err)
	}
	for _, name := range cat.Names() {
		rc, _ := cat.Get(name)
		for _, ref := range rc.ExtendsList.Resolved {
			if ref.Namespace == "core" {
				t.Errorf("%s extends %q: built-ins must reference other built-ins without the core/ prefix", name, ref.FullName())
			}
		}
	}
}

func TestRawProfileFullName(t *testing.T) {
	if got := (RawProfile{Namespace: "core", Name: "mise"}).FullName(); got != "core/mise" {
		t.Errorf("core/mise FullName = %q", got)
	}
	if got := (RawProfile{Namespace: "", Name: "mise"}).FullName(); got != "mise" {
		t.Errorf("user mise FullName = %q, want \"mise\"", got)
	}
}

func TestRawProfileDisplayName(t *testing.T) {
	if got := (RawProfile{Namespace: "core", Name: "mise"}).DisplayName(); got != "mise" {
		t.Errorf("DisplayName = %q, want mise", got)
	}
}

func TestLoadProfilesStampsCoreNamespace(t *testing.T) {
	cat, err := fixtureCatalog(t, "")
	if err != nil {
		t.Fatal(err)
	}
	rc, ok := cat.Get("core/mise")
	if !ok {
		t.Fatal("core/mise not keyed under FullName")
	}
	if rc.Namespace != "core" || rc.Name != "mise" {
		t.Errorf("core/mise identity = {%q, %q}, want {core, mise}", rc.Namespace, rc.Name)
	}
	// Bare "mise" (user namespace) must not exist when there's no user file.
	if _, ok := cat.Get("mise"); ok {
		t.Error("bare \"mise\" should not exist without a user file")
	}
}

func TestLoadProfilesUserEntryStampsEmptyNamespace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rustdev.yaml"), []byte("version: 1\nextends: bash\ntools:\n  rust: \"1.74\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	rc, ok := cat.Get("rustdev")
	if !ok {
		t.Fatal("user rustdev not found under bare FullName")
	}
	if rc.Namespace != "" || rc.Name != "rustdev" {
		t.Errorf("user identity = {%q, %q}, want {\"\", rustdev}", rc.Namespace, rc.Name)
	}
	if rc.Path == "" {
		t.Error("user entry has empty Path")
	}
}

func TestLoadProfilesUserShadowsCoreCoexist(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bash.yaml"), []byte("version: 1\nimage: my/custom:latest\ncommand: [\"bash\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := fixtureCatalog(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	// Both must coexist under distinct FullNames.
	if rc, ok := cat.Get("bash"); !ok || rc.Namespace != "" {
		t.Errorf("user bash = {%q, %q}, want {\"\", bash}", rc.Namespace, rc.Name)
	}
	if rc, ok := cat.Get("core/bash"); !ok || rc.Namespace != "core" {
		t.Errorf("core/bash = {%q, %q}, want {core, bash}", rc.Namespace, rc.Name)
	}
}

func TestLoadProfilesRejectsCrossTypeDisplayNameCollision(t *testing.T) {
	// A user fragment named "bash" and core/bash (profile) share the display
	// name "bash"; unqualified resolution and ProfileDisplayNames can't
	// disambiguate. This must be a hard error.
	dir := t.TempDir()
	fragDir := filepath.Join(filepath.Dir(dir), "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "bash.yaml"), []byte("version: 1\ntools:\n  x: \"1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := fixtureCatalog(t, dir)
	if err == nil {
		t.Fatal("expected cross-type display-name collision error, got nil")
	}
	if !strings.Contains(err.Error(), "bash") || !strings.Contains(err.Error(), "fragment") {
		t.Errorf("error should name bash and fragment, got: %v", err)
	}
}

func TestLoadProfilesTolerantDropsFragmentOnCrossTypeDisplayNameCollision(t *testing.T) {
	// A user profile named creds/ssh collides with the built-in core/creds/ssh
	// fragment. Tolerant mode keeps the profile (launchable) and drops the fragment.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "creds"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "creds", "ssh.yaml"), []byte("version: 1\nimage: my/custom:latest\ncommand: [\"bash\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var warnings []string
	cat, err := fixtureCatalogTolerant(t, dir, func(w string) { warnings = append(warnings, w) })
	if err != nil {
		t.Fatalf("LoadProfilesTolerant: %v", err)
	}
	if rc, ok := cat.Get("creds/ssh"); !ok || rc.Namespace != "" {
		t.Errorf("user profile creds/ssh = {%q, %q}, want {\"\", creds/ssh}", rc.Namespace, rc.Name)
	}
	if cat.IsFragment("creds/ssh") {
		t.Error("user profile creds/ssh should not be a fragment")
	}
	if _, ok := cat.Get("core/creds/ssh"); ok {
		t.Error("core/creds/ssh fragment should be dropped on cross-type collision")
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "creds/ssh") || !strings.Contains(warnings[0], "fragment") {
		t.Errorf("warn should name creds/ssh and fragment, got %v", warnings)
	}
}

func TestLoadProfilesUserShadowsBuiltin(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "bash.yaml"), []byte("version: 1\nimage: my/custom:latest\ncommand: [\"bash\"]\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := fixtureCatalog(t, dir)
	if err != nil {
		t.Fatalf("LoadProfiles(%q): %v", dir, err)
	}
	rc, ok := cat.Get("bash")
	if !ok {
		t.Fatal("user shadow for bash not found under bare FullName")
	}
	if rc.Image != "my/custom:latest" {
		t.Errorf("shadow image = %q, want my/custom:latest", rc.Image)
	}
	if rc.Path == "" {
		t.Error("shadow RawProfile has empty Path (should point to user file)")
	}
	if rc, ok := cat.Get("core/bash"); !ok || rc.Namespace != "core" {
		t.Errorf("built-in core/bash = {%q, %q}, want {core, bash}", rc.Namespace, rc.Name)
	}
}

func TestResolveUserShadowMergesAllBuiltinExtends(t *testing.T) {
	// A shadow extending the builtin of the same name must inherit all of its
	// parents; the qualified core/ prefix reaches the built-in without a cycle.
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"t3":    {Profile: Profile{Version: 1, Image: "img", Command: []string{"t3"}, ExtendsList: ExtendsList{Raw: []string{"a", "gui", "b", "c"}}}},
		"a":     {Profile: Profile{Env: map[string]string{"XDG_RUNTIME_DIR": "{{ .Env.XDG_RUNTIME_DIR }}"}}},
		"b":     {Profile: Profile{Mounts: map[string]Mount{"/b": {Source: "~/.b"}}}},
		"c":     {Profile: Profile{Tools: map[string]Tool{"c": {Version: "latest"}}}},
		"extra": {Profile: Profile{Tools: map[string]Tool{"extra": {Version: "1"}}}},
		"gui":   {Profile: Profile{Env: map[string]string{"WAYLAND_DISPLAY": "{{ .Env.WAYLAND_DISPLAY }}"}}},
	})
	cat.fragments["core/gui"] = true
	// Overlay the user shadow under the bare "t3" key, extending core/t3 + extra.
	shadow := RawProfile{
		Profile:   Profile{Version: 1, ExtendsList: ExtendsList{Raw: []string{"core/t3", "extra"}}},
		Namespace: "", Name: "t3", Path: "user:/home/u/t3.yaml",
	}
	if err := shadow.ExtendsList.Resolve(cat.namespaces); err != nil {
		t.Fatal(err)
	}
	cat.entries["t3"] = shadow

	merged, err := ResolveProfile(cat, "t3")
	if err != nil {
		t.Fatal(err)
	}
	if merged.Env["XDG_RUNTIME_DIR"] == "" {
		t.Error("missing env from builtin parent 'a'")
	}
	if merged.Env["WAYLAND_DISPLAY"] == "" {
		t.Error("missing env from builtin fragment parent 'gui'")
	}
	if _, ok := merged.Mounts["/b"]; !ok {
		t.Error("missing mount from builtin parent 'b'")
	}
	if merged.Tools["c"].Version != "latest" {
		t.Error("missing tool from builtin parent 'c'")
	}
	if merged.Tools["extra"].Version != "1" {
		t.Error("missing tool from second extends entry 'extra'")
	}
}

func TestLoadProfilesUserAddsProfile(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "rustdev.yaml"), []byte("version: 1\nextends: bash\ntools:\n  rust: \"1.74\"\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatalf("LoadProfiles(%q): %v", dir, err)
	}
	if _, ok := cat.Get("rustdev"); !ok {
		t.Error("user profile rustdev not in catalog")
	}
}

func TestLoadProfilesRejectsReservedName(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "doctor.yaml"), []byte("version: 1\nimage: x\ncommand: [\"sh\"]\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LoadProfiles(dir)
	if err == nil {
		t.Fatal("expected reserved-name rejection, got nil")
	}
}

func TestLoadProfilesRejectsBadFilename(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "foo bar.yaml"), []byte("version: 1\nimage: x\ncommand: [\"sh\"]\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LoadProfiles(dir)
	if err == nil {
		t.Fatal("expected rejection of invalid filename, got nil")
	}
	if !strings.Contains(err.Error(), "invalid profile name derived from filename") {
		t.Fatalf("error = %v, want 'invalid profile name derived from filename'", err)
	}
}

func TestDefaultProfileDirHonorsXDG(t *testing.T) {
	// os.UserConfigDir honors XDG_CONFIG_HOME only on Linux; macOS uses
	// ~/Library/Application Support and Windows uses %AppData%.
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME only honored on Linux")
	}
	t.Setenv("XDG_CONFIG_HOME", "/tmp/custom-config")
	t.Setenv("HOME", "/tmp/fake-home")
	got := DefaultProfileDir()
	want := "/tmp/custom-config/tpd/profiles"
	if got != want {
		t.Errorf("DefaultProfileDir() = %q, want %q", got, want)
	}
}

func TestDefaultProfileDirFallback(t *testing.T) {
	// The ~/.config fallback path is Linux-specific.
	if runtime.GOOS != "linux" {
		t.Skip("~/.config fallback only applies on Linux")
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/tmp/fake-home")
	got := DefaultProfileDir()
	want := "/tmp/fake-home/.config/tpd/profiles"
	if got != want {
		t.Errorf("DefaultProfileDir() = %q, want %q", got, want)
	}
}

func TestDefaultProfileDirEmpty(t *testing.T) {
	// os.UserConfigDir uses %AppData% on Windows, not $HOME; gating to Linux.
	if runtime.GOOS != "linux" {
		t.Skip("os.UserConfigDir behavior is platform-specific")
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	got := DefaultProfileDir()
	if got != "" {
		t.Errorf("DefaultProfileDir() = %q, want empty string", got)
	}
}

func TestDisplayNamesDedupsUserShadow(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bash.yaml"), []byte("version: 1\nimage: my/custom:latest\ncommand: [\"bash\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := fixtureCatalog(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	names := cat.DisplayNames()
	if !contains(names, "bash") {
		t.Errorf("DisplayNames missing bash; got %v", names)
	}
	// "bash" appears once (user shadows core), not twice.
	count := 0
	for _, n := range names {
		if n == "bash" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("bash appears %d times in DisplayNames, want 1", count)
	}
}

func TestDisplayNamesIncludesCoreOnly(t *testing.T) {
	cat, err := fixtureCatalog(t, "")
	if err != nil {
		t.Fatal(err)
	}
	names := cat.DisplayNames()
	if !contains(names, "mise") {
		t.Errorf("DisplayNames missing core-only mise; got %v", names)
	}
	if contains(names, "core/mise") {
		t.Errorf("DisplayNames should not contain qualified core/mise; got %v", names)
	}
}

func TestProfileDisplayNamesExcludesFragments(t *testing.T) {
	cat, err := fixtureCatalog(t, "")
	if err != nil {
		t.Fatal(err)
	}
	names := cat.ProfileDisplayNames()
	if contains(names, "toolchain/javascript") {
		t.Errorf("ProfileDisplayNames should exclude fragment toolchain/javascript; got %v", names)
	}
	if !contains(names, "mise") {
		t.Errorf("ProfileDisplayNames missing profile mise; got %v", names)
	}
}

func TestFragmentDisplayNamesExcludesProfiles(t *testing.T) {
	cat, err := fixtureCatalog(t, "")
	if err != nil {
		t.Fatal(err)
	}
	names := cat.FragmentDisplayNames()
	if !contains(names, "toolchain/javascript") {
		t.Errorf("FragmentDisplayNames missing fragment toolchain/javascript; got %v", names)
	}
	if !contains(names, "creds/ssh") {
		t.Errorf("FragmentDisplayNames missing fragment creds/ssh; got %v", names)
	}
	if contains(names, "mise") {
		t.Errorf("FragmentDisplayNames should exclude profile mise; got %v", names)
	}
	if contains(names, "core/toolchain/javascript") {
		t.Errorf("FragmentDisplayNames should not contain qualified core/toolchain/javascript; got %v", names)
	}
}

func TestFragmentDisplayNamesUserShadowDedup(t *testing.T) {
	dir := t.TempDir()
	fragDir := filepath.Join(filepath.Dir(dir), "fragments")
	if err := os.MkdirAll(filepath.Join(fragDir, "toolchain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "toolchain", "go.yaml"), []byte("version: 1\ntools:\n  go: latest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := fixtureCatalog(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	names := cat.FragmentDisplayNames()
	count := 0
	for _, n := range names {
		if n == "toolchain/go" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("toolchain/go appears %d times in FragmentDisplayNames, want 1; got %v", count, names)
	}
}

func TestSourceUserShadow(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bash.yaml"), []byte("version: 1\nimage: x\ncommand: [\"bash\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := fixtureCatalog(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cat.Source("bash"); got != "user shadow" {
		t.Errorf("Source(bash) = %q, want \"user shadow\"", got)
	}
	if got := cat.Source("mise"); got != "core" {
		t.Errorf("Source(mise) = %q, want \"core\"", got)
	}
}

func TestSourceUserOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rustdev.yaml"), []byte("version: 1\nextends: bash\ntools:\n  rust: \"1.74\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cat.Source("rustdev"); got != "user" {
		t.Errorf("Source(rustdev) = %q, want \"user\"", got)
	}
}

func TestFragmentByDisplayNameUserWins(t *testing.T) {
	dir := t.TempDir()
	fragDir := filepath.Join(filepath.Dir(dir), "fragments", "toolchain")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "javascript.yaml"), []byte("version: 1\ntools:\n  node: \"user\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := fixtureCatalog(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := cat.FragmentByDisplayName("toolchain/javascript")
	if !ok {
		t.Fatal("expected ok")
	}
	if got != "toolchain/javascript" {
		t.Errorf("FragmentByDisplayName(toolchain/javascript) = %q, want \"toolchain/javascript\" (user wins)", got)
	}
}

func TestFragmentByDisplayNameCoreOnly(t *testing.T) {
	cat, err := fixtureCatalog(t, "")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := cat.FragmentByDisplayName("toolchain/javascript")
	if !ok {
		t.Fatal("expected ok")
	}
	if got != "core/toolchain/javascript" {
		t.Errorf("FragmentByDisplayName(toolchain/javascript) = %q, want \"core/toolchain/javascript\"", got)
	}
}

func TestBuiltinTypescriptExtendsCoreJavascript(t *testing.T) {
	cat, err := fixtureCatalog(t, "")
	if err != nil {
		t.Fatal(err)
	}
	rc, ok := cat.Get("core/toolchain/typescript")
	if !ok {
		t.Fatal("core/toolchain/typescript missing")
	}
	if len(rc.ExtendsList.Resolved) != 1 || rc.ExtendsList.Resolved[0] != (Ref{Namespace: "", Name: "toolchain/javascript"}) {
		t.Errorf("core/toolchain/typescript extends = %+v, want [toolchain/javascript]", rc.ExtendsList.Resolved)
	}
}

func TestTypescriptExtendsUserFragmentNamedJavascript(t *testing.T) {
	// Built-ins reference other built-ins by unqualified name, so a user
	// *fragment* named toolchain/javascript wins the fallback: core/toolchain/typescript
	// inherits its tools. Built-ins don't pin core/ because the shadowing model
	// is the point — overriding a base fragment should flow into derived ones.
	dir := t.TempDir()
	fragDir := filepath.Join(filepath.Dir(dir), "fragments", "toolchain")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "javascript.yaml"), []byte("version: 1\ntools:\n  userjs: \"user\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := fixtureCatalog(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := ResolveFragment(cat, "toolchain/typescript")
	if err != nil {
		t.Fatalf("ResolveFragment: %v", err)
	}
	if _, ok := merged.Tools["userjs"]; !ok {
		t.Error("core/toolchain/typescript should inherit tools from the user toolchain/javascript fragment")
	}
}

func TestLoadProfilesUserSubfolderNamespace(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "lang"), 0o755); err != nil {
		t.Fatal(err)
	}
	// lang/cobol has no built-in fragment, so it must not trip the cross-type
	// display-name collision check.
	if err := os.WriteFile(filepath.Join(dir, "lang", "cobol.yaml"),
		[]byte("version: 1\ncommand: [\"cobol\"]\nimage: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	rc, ok := cat.Get("lang/cobol")
	if !ok {
		t.Fatal("user lang/cobol not keyed under hierarchical FullName")
	}
	if rc.Namespace != "" || rc.Name != "lang/cobol" {
		t.Errorf("identity = {%q, %q}, want {\"\", \"lang/cobol\"}", rc.Namespace, rc.Name)
	}
}

func TestLoadProfilesUserNestedSubfolder(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lang", "js", "node.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("version: 1\ncommand: [\"node\"]\nimage: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rc, ok := cat.Get("lang/js/node"); !ok || rc.Name != "lang/js/node" {
		t.Errorf("lang/js/node = %+v, want present with Name lang/js/node", rc)
	}
}

func TestLoadProfilesRejectsReservedNamespacePrefix(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "core"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "core", "mytool.yaml"),
		[]byte("version: 1\ncommand: [\"mytool\"]\nimage: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProfiles(dir)
	if err == nil {
		t.Fatal("expected reserved-namespace error, got nil")
	}
	if !strings.Contains(err.Error(), "core is a reserved namespace prefix") {
		t.Fatalf("error = %v, want 'core is a reserved namespace prefix'", err)
	}
}

func TestLoadProfilesTolerantSkipsReservedNamespacePrefix(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "core"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "core", "mytool.yaml"),
		[]byte("version: 1\ncommand: [\"mytool\"]\nimage: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var warns []string
	cat, err := LoadProfilesTolerant(dir, func(w string) { warns = append(warns, w) })
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cat.Get("core/mytool"); ok {
		t.Error("reserved-namespace file must be skipped in tolerant mode")
	}
	if len(warns) == 0 || !strings.Contains(warns[0], "reserved namespace") {
		t.Errorf("expected a reserved-namespace warning, got %v", warns)
	}
}

func TestFragmentByDisplayNameHierarchicalCoreOnly(t *testing.T) {
	cat := Catalog{
		entries: map[string]RawProfile{
			"core/lang/go": {Profile: Profile{Version: 1}, Namespace: "core", Name: "lang/go"},
		},
		namespaces: map[string]bool{"": true, "core": true},
		fragments:  map[string]bool{"core/lang/go": true},
	}
	got, ok := cat.FragmentByDisplayName("lang/go")
	if !ok {
		t.Fatal("expected ok")
	}
	if got != "core/lang/go" {
		t.Errorf("FragmentByDisplayName(lang/go) = %q, want core/lang/go", got)
	}
}

func TestLoadProfilesUserShadowsCoreHierarchical(t *testing.T) {
	// User fragments/toolchain/go.yaml shadows the built-in core/toolchain/go fragment.
	dir := t.TempDir()
	fragDir := filepath.Join(filepath.Dir(dir), "fragments", "toolchain")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "go.yaml"), []byte("version: 1\ntools:\n  user-go: \"1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := fixtureCatalog(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := cat.ResolveRef(Ref{Name: "toolchain/go"}); got != "toolchain/go" {
		t.Errorf("ResolveRef(toolchain/go) = %q, want user toolchain/go", got)
	}
	if got := cat.Source("toolchain/go"); got != "user shadow" {
		t.Errorf("Source(toolchain/go) = %q, want \"user shadow\"", got)
	}
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
