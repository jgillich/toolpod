package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProfilesIncludesFragments(t *testing.T) {
	cat, err := fixtureCatalog(t, "")
	if err != nil {
		t.Fatal(err)
	}
	// "lang/javascript" is a built-in fixture fragment, not a profile.
	// It should be resolvable via Get under its core/lang/ FullName.
	rc, ok := cat.Get("core/lang/javascript")
	if !ok {
		t.Fatal("fragment 'lang/javascript' not found in catalog")
	}
	if !cat.IsFragment("core/lang/javascript") {
		t.Fatal("lang/javascript should be registered as a fragment")
	}
	if rc.Path == "" {
		t.Error("fragment should have a path")
	}
}

func TestFragmentProfileNameCollisionRejected(t *testing.T) {
	dir := t.TempDir()
	// Create a user profile named "creds/ssh" that collides with the built-in
	// core/creds/ssh fixture fragment.
	if err := os.MkdirAll(filepath.Join(dir, "creds"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "creds", "ssh.yaml"), []byte("version: 1\nimage: x\ncommand: [sh]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := fixtureCatalog(t, dir)
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
	if err := os.WriteFile(filepath.Join(fragDir, "foo.yaml"), []byte("version: 1\nmounts:\n  /t:\n    source: /tmp\n"), 0o644); err != nil {
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

func TestFragmentVersionEnforced(t *testing.T) {
	dir := t.TempDir()
	fragDir := filepath.Join(filepath.Dir(dir), "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "v2frag.yaml"), []byte("version: 2\nmounts:\n  /t:\n    source: /tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfiles(dir); err == nil {
		t.Fatal("expected error for fragment with version 2, got nil")
	}
}

func TestFragmentVersionOneAccepted(t *testing.T) {
	dir := t.TempDir()
	fragDir := filepath.Join(filepath.Dir(dir), "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "goodfrag.yaml"), []byte("version: 1\nmounts:\n  /t:\n    source: /tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatalf("LoadProfiles with a version-1 fragment: %v", err)
	}
	if !cat.IsFragment("goodfrag") {
		t.Error("version-1 fragment should load into the catalog")
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
