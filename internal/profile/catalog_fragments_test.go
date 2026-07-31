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

func TestUserFragmentsLoaded(t *testing.T) {
	dir := t.TempDir()
	fragDir := filepath.Join(dir, "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a user fragment
	if err := os.WriteFile(filepath.Join(fragDir, "myfrag.yaml"), []byte("mounts:\n  /data:\n    source: /data\n"), 0o644); err != nil {
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
