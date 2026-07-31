package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustWriteProfile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveScalarOverride(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\nnetwork: bridge\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\nnetwork: host\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Network != "host" {
		t.Errorf("Network = %q, want host", cfg.Network)
	}
	if cfg.Image != "base:1" {
		t.Errorf("Image = %q, want base:1 (inherited)", cfg.Image)
	}
}

func TestResolveMapMergeAndNullDelete(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\ntools:\n  node: \"20\"\n  rust: \"1.74\"\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\ntools:\n  node: \"22\"\n  rust: null\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Tools["node"] != "22" {
		t.Errorf("node = %q, want 22 (overridden)", cfg.Tools["node"])
	}
	if _, exists := cfg.Tools["rust"]; exists {
		t.Error("rust should be deleted by null-to-delete rule")
	}
}

func TestResolveListReplaced(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"a\"]\nargs_if_none: [\"--x\"]\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\nargs_if_none: [\"--y\"]\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.ArgsIfNone) != 1 || cfg.ArgsIfNone[0] != "--y" {
		t.Errorf("args_if_none = %v, want [--y] (replaced not concatenated)", cfg.ArgsIfNone)
	}
}

func TestResolveExtendsSelfViaBuiltin(t *testing.T) {
	dir := t.TempDir()
	// User file shadows built-in "opencode" and extends "opencode" (the built-in).
	mustWriteProfile(t, dir, "opencode.yaml", "version: 1\nextends: opencode\ncaches:\n  npm: ~/.npm\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	cfg, err := ResolveProfile(cat, "opencode")
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	// Should inherit image/command from the built-in opencode, plus the user caches.
	if cfg.Image != "ghcr.io/jdx/mise:latest" {
		t.Errorf("Image = %q, want ghcr.io/jdx/mise:latest (inherited from built-in)", cfg.Image)
	}
	if cfg.Caches["npm"] != "~/.npm" {
		t.Errorf("Caches[npm] = %q, want ~/.npm", cfg.Caches["npm"])
	}
}

func TestResolveCycle(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "a.yaml", "version: 1\nimage: x\ncommand: [\"x\"]\nextends: b\n")
	mustWriteProfile(t, dir, "b.yaml", "version: 1\nimage: y\ncommand: [\"y\"]\nextends: a\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResolveProfile(cat, "a")
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	ce, ok := err.(ProfileError)
	if !ok {
		t.Fatalf("expected ProfileError, got %T", err)
	}
	if ce.Message == "" || !strings.Contains(ce.Message, "cycle") {
		t.Errorf("error message %q should mention cycle", ce.Message)
	}
}

func TestResolveSelfExtendsNoBuiltin(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "foo.yaml", "version: 1\nimage: x\ncommand: [\"x\"]\nextends: foo\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResolveProfile(cat, "foo")
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention cycle, got: %v", err)
	}
}

func TestResolveUserCrossCycle(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "a.yaml", "version: 1\nimage: x\ncommand: [\"x\"]\nextends: b\n")
	mustWriteProfile(t, dir, "b.yaml", "version: 1\nimage: y\ncommand: [\"y\"]\nextends: a\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResolveProfile(cat, "a")
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention cycle, got: %v", err)
	}
}
