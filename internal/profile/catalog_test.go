package profile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadProfilesBuiltinsOnly(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatalf("LoadProfiles(\"\"): %v", err)
	}
	for _, name := range []string{"opencode", "codex", "shell"} {
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
		t.Fatal("user shadow for shell not found")
	}
	if rc.Image != "my/custom:latest" {
		t.Errorf("shadow image = %q, want my/custom:latest", rc.Image)
	}
	if rc.Path == "" {
		t.Error("shadow RawProfile has empty Path (should point to user file)")
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

func TestBuiltinsHaveNoDefaultCaches(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range cat.Names() {
		rc, ok := cat.Get(name)
		if !ok {
			continue
		}
		if len(rc.Caches) != 0 {
			t.Errorf("built-in %q should not declare caches; got %v", name, rc.Caches)
		}
	}
}

func TestBuiltinsDoNotMountUserDirs(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range cat.Names() {
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
		rc, ok := cat.Get(name)
		if !ok {
			continue
		}
		if _, has := rc.Mounts["~/.gitconfig"]; has {
			t.Errorf("built-in %q should not mount ~/.gitconfig", name)
		}
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
	want := "/tmp/custom-config/toolpod/profiles"
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
	want := "/tmp/fake-home/.config/toolpod/profiles"
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
