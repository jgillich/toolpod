package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustWriteConfig(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveScalarOverride(t *testing.T) {
	dir := t.TempDir()
	mustWriteConfig(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\nnetwork: bridge\n")
	mustWriteConfig(t, dir, "child.yaml", "version: 1\nextends: base\nnetwork: host\n")
	cat, err := LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Resolve(cat, "child")
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
	mustWriteConfig(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\ntools:\n  node: \"20\"\n  rust: \"1.74\"\n")
	mustWriteConfig(t, dir, "child.yaml", "version: 1\nextends: base\ntools:\n  node: \"22\"\n  rust: null\n")
	cat, err := LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Resolve(cat, "child")
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
	mustWriteConfig(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"a\"]\nargs_if_none: [\"--x\"]\n")
	mustWriteConfig(t, dir, "child.yaml", "version: 1\nextends: base\nargs_if_none: [\"--y\"]\n")
	cat, err := LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Resolve(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.ArgsIfNone) != 1 || cfg.ArgsIfNone[0] != "--y" {
		t.Errorf("args_if_none = %v, want [--y] (replaced not concatenated)", cfg.ArgsIfNone)
	}
}

func TestResolveCycle(t *testing.T) {
	dir := t.TempDir()
	mustWriteConfig(t, dir, "a.yaml", "version: 1\nimage: x\ncommand: [\"x\"]\nextends: b\n")
	mustWriteConfig(t, dir, "b.yaml", "version: 1\nimage: y\ncommand: [\"y\"]\nextends: a\n")
	cat, err := LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Resolve(cat, "a")
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	ce, ok := err.(ConfigError)
	if !ok {
		t.Fatalf("expected ConfigError, got %T", err)
	}
	if ce.Message == "" || !strings.Contains(ce.Message, "cycle") {
		t.Errorf("error message %q should mention cycle", ce.Message)
	}
}
