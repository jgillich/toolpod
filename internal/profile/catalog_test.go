package profile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
	cat, err := LoadProfiles("")
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
	if err := os.WriteFile(filepath.Join(dir, "rustdev.yaml"), []byte("version: 1\nextends: shell\ntools:\n  rust: \"1.74\"\n"), 0o644); err != nil {
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
	if err := os.WriteFile(filepath.Join(dir, "shell.yaml"), []byte("version: 1\nimage: my/custom:latest\ncommand: [\"bash\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Both must coexist under distinct FullNames.
	if rc, ok := cat.Get("shell"); !ok || rc.Namespace != "" {
		t.Errorf("user shell = {%q, %q}, want {\"\", shell}", rc.Namespace, rc.Name)
	}
	if rc, ok := cat.Get("core/shell"); !ok || rc.Namespace != "core" {
		t.Errorf("core/shell = {%q, %q}, want {core, shell}", rc.Namespace, rc.Name)
	}
}

func TestLoadProfilesRejectsCrossTypeDisplayNameCollision(t *testing.T) {
	// A user fragment named "shell" and core/shell (profile) share the display
	// name "shell"; unqualified resolution and ProfileDisplayNames can't
	// disambiguate. This must be a hard error.
	dir := t.TempDir()
	fragDir := filepath.Join(filepath.Dir(dir), "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "shell.yaml"), []byte("version: 1\ntools:\n  x: \"1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProfiles(dir)
	if err == nil {
		t.Fatal("expected cross-type display-name collision error, got nil")
	}
	if !strings.Contains(err.Error(), "shell") || !strings.Contains(err.Error(), "fragment") {
		t.Errorf("error should name shell and fragment, got: %v", err)
	}
}

func TestLoadProfilesBuiltinsOnly(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatalf("LoadProfiles(\"\"): %v", err)
	}
	for _, name := range []string{"core/opencode", "core/codex", "core/shell"} {
		if _, ok := cat.Get(name); !ok {
			t.Errorf("built-in %q missing from catalog", name)
		}
	}
}

func TestLoadProfilesUserShadowsBuiltin(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "shell.yaml"), []byte("version: 1\nimage: my/custom:latest\ncommand: [\"bash\"]\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatalf("LoadProfiles(%q): %v", dir, err)
	}
	rc, ok := cat.Get("shell")
	if !ok {
		t.Fatal("user shadow for shell not found under bare FullName")
	}
	if rc.Image != "my/custom:latest" {
		t.Errorf("shadow image = %q, want my/custom:latest", rc.Image)
	}
	if rc.Path == "" {
		t.Error("shadow RawProfile has empty Path (should point to user file)")
	}
	if rc, ok := cat.Get("core/shell"); !ok || rc.Namespace != "core" {
		t.Errorf("built-in core/shell = {%q, %q}, want {core, shell}", rc.Namespace, rc.Name)
	}
}

func TestResolveUserShadowMergesAllBuiltinExtends(t *testing.T) {
	// A shadow extending the builtin of the same name must inherit all of its
	// parents; the qualified core/ prefix reaches the built-in without a cycle.
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"t3":    {Profile: Profile{Version: 1, Image: "img", Command: []string{"t3"}, ExtendsList: ExtendsList{Raw: []string{"a", "gui", "b", "c"}}}},
		"a":     {Profile: Profile{Env: map[string]string{"XDG_RUNTIME_DIR": "{{ .Env.XDG_RUNTIME_DIR }}"}}},
		"b":     {Profile: Profile{Mounts: map[string]Mount{"/b": {Source: "~/.b"}}}},
		"c":     {Profile: Profile{Tools: map[string]string{"c": "latest"}}},
		"extra": {Profile: Profile{Tools: map[string]string{"extra": "1"}}},
		"gui":   {Profile: Profile{Env: map[string]string{"WAYLAND_DISPLAY": "{{ .Env.WAYLAND_DISPLAY }}"}}},
	})
	cat.fragments["core/gui"] = true
	// Overlay the user shadow under the bare "t3" key, extending core/t3 + extra.
	shadow := RawProfile{
		Profile:     Profile{Version: 1, ExtendsList: ExtendsList{Raw: []string{"core/t3", "extra"}}},
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
	if merged.Tools["c"] != "latest" {
		t.Error("missing tool from builtin parent 'c'")
	}
	if merged.Tools["extra"] != "1" {
		t.Error("missing tool from second extends entry 'extra'")
	}
}

func TestLoadProfilesUserAddsProfile(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "rustdev.yaml"), []byte("version: 1\nextends: shell\ntools:\n  rust: \"1.74\"\n"), 0o644)
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

func TestBuiltinsDoNotMountUserDirs(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range cat.Names() {
		if cat.IsFragment(name) {
			continue
		}
		rc, ok := cat.Get(name)
		if !ok {
			continue
		}
		for _, sensitive := range []string{"~/.ssh", "~/.gnupg", "~/.netrc"} {
			if _, has := rc.Mounts[sensitive]; has {
				t.Errorf("built-in %q should not mount %s", name, sensitive)
			}
		}
	}
}

func TestBuiltinsDoNotMountGitconfig(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range cat.Names() {
		if cat.IsFragment(name) {
			continue
		}
		rc, ok := cat.Get(name)
		if !ok {
			continue
		}
		if _, has := rc.Mounts["~/.gitconfig"]; has {
			t.Errorf("built-in %q should not mount ~/.gitconfig", name)
		}
	}
}

func TestResolveBuzzProfile(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "buzz")
	if err != nil {
		t.Fatalf("ResolveProfile(buzz): %v", err)
	}
	if len(cfg.Command) != 1 || cfg.Command[0] != "buzz" {
		t.Errorf("command = %v, want [buzz]", cfg.Command)
	}
	if cfg.Tools["appimage:block/buzz"] != "latest" {
		t.Errorf("tools = %v, want appimage:block/buzz=latest", cfg.Tools)
	}
	if _, ok := cfg.Mounts["~/.local/share/xyz.block.buzz.app"]; !ok {
		t.Error("missing app data mount ~/.local/share/xyz.block.buzz.app")
	}
	if cfg.Env["WAYLAND_DISPLAY"] == "" {
		t.Error("missing gui fragment env WAYLAND_DISPLAY")
	}
}

func TestResolveBuzzDbusAllowlist(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "buzz")
	if err != nil {
		t.Fatalf("ResolveProfile(buzz): %v", err)
	}
	if cfg.Dbus == nil {
		t.Fatal("buzz should resolve a dbus allowlist (via gui)")
	}
	for _, name := range []string{"org.freedesktop.portal.Desktop", "org.freedesktop.Notifications"} {
		if cfg.Dbus.Talk[name] == nil {
			t.Errorf("dbus.talk missing %q", name)
		}
	}
	if cfg.Dbus.Own["xyz.block.buzz.app"] == nil {
		t.Error("dbus.own missing xyz.block.buzz.app")
	}
	if cfg.Env["DBUS_SESSION_BUS_ADDRESS"] != "" {
		t.Errorf("dbus env should be unset in resolved profile, got %q", cfg.Env["DBUS_SESSION_BUS_ADDRESS"])
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

func TestBuiltinProfilesResolvePackages(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	// Every profile extends mise, so every profile inherits the general C
	// toolchain.
	miseCfg, err := ResolveProfile(cat, "mise")
	if err != nil {
		t.Fatalf("resolve mise: %v", err)
	}
	if len(miseCfg.Packages) == 0 {
		t.Fatal("mise profile must declare a packages list")
	}
	if !containsPkg(miseCfg.Packages, "build-essential") {
		t.Errorf("mise packages must include build-essential, got %v", miseCfg.Packages)
	}
	if !containsPkg(miseCfg.Packages, "libssl-dev") {
		t.Errorf("mise packages must include libssl-dev, got %v", miseCfg.Packages)
	}
	if !containsPkg(miseCfg.Packages, "mise") {
		t.Errorf("mise packages must include mise itself, got %v", miseCfg.Packages)
	}
	if !containsPkg(miseCfg.Packages, "curl") {
		t.Errorf("mise packages must include curl (moved from the base image), got %v", miseCfg.Packages)
	}
}

func TestMiseProfileResolvesMiseRepo(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	cfg, err := ResolveProfile(cat, "mise")
	if err != nil {
		t.Fatalf("resolve mise: %v", err)
	}
	if cfg.Repos == nil {
		t.Fatal("mise profile must declare a repos map")
	}
	repo, ok := cfg.Repos["mise"]
	if !ok {
		t.Fatalf("mise repos must contain the \"mise\" repo, got %v", cfg.Repos)
	}
	if repo.ExtRepo != "mise" {
		t.Errorf("repos[mise].ExtRepo = %q, want mise", repo.ExtRepo)
	}
}

func TestGuiFragmentCarriesXdgOpenWrapper(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	cfg, err := ResolveProfile(cat, "buzz")
	if err != nil {
		t.Fatalf("resolve buzz: %v", err)
	}
	f, ok := cfg.Files["/usr/local/bin/xdg-open"]
	if !ok {
		t.Fatal("buzz should carry the xdg-open wrapper via the gui fragment")
	}
	if f.Mode != 0o755 {
		t.Errorf("wrapper mode = %o, want 755", f.Mode)
	}
	if !strings.Contains(f.Content, "org.freedesktop.portal.Desktop") {
		t.Error("wrapper should forward URLs to the host portal")
	}
	if !strings.Contains(f.Content, "xdg-open.real") {
		t.Error("wrapper should fall back to the real xdg-open")
	}
}

func TestBuiltinFragmentsDeclarePackages(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	for _, name := range []string{"core/php", "core/gui"} {
		rc, ok := cat.Get(name)
		if !ok {
			t.Fatalf("fragment %q missing from catalog", name)
		}
		if !cat.IsFragment(name) {
			t.Fatalf("%q should be a fragment", name)
		}
		if len(rc.Packages) == 0 {
			t.Errorf("fragment %q must declare packages, got empty", name)
		}
	}
}

func containsPkg(pkgs []string, want string) bool {
	for _, p := range pkgs {
		if p == want {
			return true
		}
	}
	return false
}
