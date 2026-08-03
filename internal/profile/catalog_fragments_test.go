package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProfilesIncludesFragments(t *testing.T) {
	cat, err := LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	// "ssh" is a built-in fragment, not a profile.
	// It should be resolvable via Get.
	rc, ok := cat.Get("ssh")
	if !ok {
		t.Fatal("fragment 'ssh' not found in catalog")
	}
	if rc.Path == "" {
		t.Error("fragment should have a path")
	}
}

func TestFragmentProfileNameCollisionRejected(t *testing.T) {
	dir := t.TempDir()
	// Create a user profile named "ssh" that collides with the built-in fragment.
	if err := os.WriteFile(filepath.Join(dir, "ssh.yaml"), []byte("version: 1\nimage: x\ncommand: [sh]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProfiles(dir)
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
}

func TestUserProfileUserFragmentCollisionRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "foo.yaml"), []byte("version: 1\nimage: x\ncommand: [sh]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fragDir := filepath.Join(filepath.Dir(dir), "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "foo.yaml"), []byte("version: 1\nmounts:\n  /t:\n    host: /tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfiles(dir); err == nil {
		t.Fatal("expected collision error for same-named user profile and fragment, got nil")
	}
}

func TestUserFragmentsLoaded(t *testing.T) {
	dir := t.TempDir()
	fragDir := filepath.Join(filepath.Dir(dir), "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a user fragment
	if err := os.WriteFile(filepath.Join(fragDir, "myfrag.yaml"), []byte("version: 1\nmounts:\n  /data:\n    source: /data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	rc, ok := cat.Get("myfrag")
	if !ok {
		t.Fatal("user fragment 'myfrag' not found")
	}
	if _, ok := rc.Mounts["/data"]; !ok {
		t.Error("user fragment mount missing")
	}
}

func TestFragmentExtendsFragment(t *testing.T) {
	dir := t.TempDir()
	fragDir := filepath.Join(filepath.Dir(dir), "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "basefrag.yaml"), []byte("version: 1\ntools:\n  base-tool: latest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "childfrag.yaml"), []byte("version: 1\nextends: basefrag\ntools:\n  child-tool: latest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "proj.yaml"), []byte("version: 1\nimage: x\ncommand: [sh]\nextends: childfrag\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveProfile(cat, "proj")
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if resolved.Tools["base-tool"].Version != "latest" {
		t.Error("missing base-tool from basefrag")
	}
	if resolved.Tools["child-tool"].Version != "latest" {
		t.Error("missing child-tool from childfrag")
	}
}

func TestFragmentWithoutVersionRejected(t *testing.T) {
	dir := t.TempDir()
	fragDir := filepath.Join(filepath.Dir(dir), "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "oldfrag.yaml"), []byte("tools:\n  old-tool: latest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfiles(dir); err == nil {
		t.Fatal("expected error for fragment without version, got nil")
	}
}

func TestFragmentExtendingProfileRejected(t *testing.T) {
	dir := t.TempDir()
	fragDir := filepath.Join(filepath.Dir(dir), "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "base.yaml"), []byte("version: 1\nimage: base:latest\ncommand: [sh]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "badfrag.yaml"), []byte("version: 1\nextends: base\ntools:\n  x: latest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "proj.yaml"), []byte("version: 1\nimage: x\ncommand: [sh]\nextends: badfrag\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveProfile(cat, "proj"); err == nil {
		t.Fatal("expected error for fragment extending a profile")
	}
}
