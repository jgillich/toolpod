package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateConfig points XDG_CONFIG_HOME at a fresh temp dir so only embedded
// built-ins contribute completions.
func isolateConfig(t *testing.T) string {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	return cfg
}

// seedFixture writes a user profile and a user fragment into the temp config
// dir so completion assertions target test-owned entries, never the live
// embedded catalog.
func seedFixture(t *testing.T, cfg string) {
	t.Helper()
	profilesDir := filepath.Join(cfg, "tpd", "profiles")
	fragmentsDir := filepath.Join(cfg, "tpd", "fragments", "misc")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fragmentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profilesDir, "myapp.yaml"), []byte("version: 1\nimage: x\ncommand: [sh]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragmentsDir, "util.yaml"), []byte("version: 1\ntools:\n  util: latest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runCompletion invokes cobra's hidden __complete command and returns the
// candidate names (directive lines stripped).
func runCompletion(t *testing.T, args ...string) []string {
	t.Helper()
	root := newRootCommand()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"__complete"}, args...))
	if err := root.Execute(); err != nil {
		t.Fatalf("__complete %v: %v\n%s", args, err, errOut.String())
	}
	var names []string
	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		names = append(names, strings.SplitN(line, "\t", 2)[0])
	}
	return names
}

func containsAll(t *testing.T, got []string, want ...string) {
	t.Helper()
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("completion candidates %v missing %q", got, w)
		}
	}
}

func TestCompletionTopLevel(t *testing.T) {
	cfg := isolateConfig(t)
	seedFixture(t, cfg)
	names := runCompletion(t, "")
	containsAll(t, names, "myapp", "run", "show") // user profile + commands
}

func TestCompletionProfilePrefix(t *testing.T) {
	cfg := isolateConfig(t)
	seedFixture(t, cfg)
	names := runCompletion(t, "my")
	if len(names) != 1 || names[0] != "myapp" {
		t.Errorf("expected only 'myapp', got %v", names)
	}
}

func TestCompletionRun(t *testing.T) {
	cfg := isolateConfig(t)
	seedFixture(t, cfg)
	containsAll(t, runCompletion(t, "run", ""), "myapp")
}

func TestCompletionShow(t *testing.T) {
	cfg := isolateConfig(t)
	seedFixture(t, cfg)
	// show accepts profiles and fragments.
	containsAll(t, runCompletion(t, "show", ""), "myapp", "misc/util")
}

func TestCompletionShowPrefix(t *testing.T) {
	cfg := isolateConfig(t)
	seedFixture(t, cfg)
	names := runCompletion(t, "show", "misc/")
	containsAll(t, names, "misc/util")
}

func TestCompletionShowHierarchicalFragment(t *testing.T) {
	cfg := isolateConfig(t)
	seedFixture(t, cfg)
	names := runCompletion(t, "show", "misc/")
	containsAll(t, names, "misc/util")
}

func TestCompletionPassthroughAfterProfile(t *testing.T) {
	cfg := isolateConfig(t)
	seedFixture(t, cfg)
	// Everything after the profile name is passthrough: nothing to complete.
	if names := runCompletion(t, "myapp", ""); len(names) != 0 {
		t.Errorf("expected no completions after profile name, got %v", names)
	}
}

func TestCompletionInitExtends(t *testing.T) {
	cfg := isolateConfig(t)
	seedFixture(t, cfg)
	containsAll(t, runCompletion(t, "init", "--extends", ""), "myapp", "misc/util")
}

func TestCompletionTolerantLoad(t *testing.T) {
	cfg := isolateConfig(t)
	profilesDir := filepath.Join(cfg, "tpd", "profiles")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profilesDir, "broken.yaml"), []byte("version: [not valid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedFixture(t, cfg)
	containsAll(t, runCompletion(t, ""), "myapp")
}

func TestCompletionShowAfterPositional(t *testing.T) {
	cfg := isolateConfig(t)
	seedFixture(t, cfg)
	if names := runCompletion(t, "show", "myapp", ""); len(names) != 0 {
		t.Errorf("expected no completions after show's name is given, got %v", names)
	}
}

func TestCompletionRunPassthrough(t *testing.T) {
	cfg := isolateConfig(t)
	seedFixture(t, cfg)
	if names := runCompletion(t, "run", "myapp", ""); len(names) != 0 {
		t.Errorf("expected no completions after run profile name, got %v", names)
	}
}
