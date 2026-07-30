package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCatalogBuiltinsOnly(t *testing.T) {
	cat, err := LoadCatalog("")
	if err != nil {
		t.Fatalf("LoadCatalog(\"\"): %v", err)
	}
	for _, name := range []string{"opencode", "codex", "shell"} {
		if _, ok := cat.Get(name); !ok {
			t.Errorf("built-in %q missing from catalog", name)
		}
	}
}

func TestLoadCatalogUserShadowsBuiltin(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "shell.yaml"), []byte("version: 1\nimage: my/custom:latest\ncommand: [\"bash\"]\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := LoadCatalog(dir)
	if err != nil {
		t.Fatalf("LoadCatalog(%q): %v", dir, err)
	}
	rc, ok := cat.Get("shell")
	if !ok {
		t.Fatal("user shadow for shell not found")
	}
	if rc.Image != "my/custom:latest" {
		t.Errorf("shadow image = %q, want my/custom:latest", rc.Image)
	}
	if rc.Path == "" {
		t.Error("shadow RawConfig has empty Path (should point to user file)")
	}
}

func TestLoadCatalogUserAddsProfile(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "rustdev.yaml"), []byte("version: 1\nextends: shell\ntools:\n  rust: \"1.74\"\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := LoadCatalog(dir)
	if err != nil {
		t.Fatalf("LoadCatalog(%q): %v", dir, err)
	}
	if _, ok := cat.Get("rustdev"); !ok {
		t.Error("user profile rustdev not in catalog")
	}
}

func TestLoadCatalogRejectsReservedName(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "doctor.yaml"), []byte("version: 1\nimage: x\ncommand: [\"sh\"]\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LoadCatalog(dir)
	if err == nil {
		t.Fatal("expected reserved-name rejection, got nil")
	}
}
