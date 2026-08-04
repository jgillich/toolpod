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
	isolateConfig(t)
	names := runCompletion(t, "")
	containsAll(t, names, "bash", "run", "show") // built-in profile + commands
}

func TestCompletionProfilePrefix(t *testing.T) {
	isolateConfig(t)
	names := runCompletion(t, "ba")
	if len(names) != 1 || names[0] != "bash" {
		t.Errorf("expected only 'bash', got %v", names)
	}
}

func TestCompletionRun(t *testing.T) {
	isolateConfig(t)
	containsAll(t, runCompletion(t, "run", ""), "bash")
}

func TestCompletionShow(t *testing.T) {
	isolateConfig(t)
	// show accepts profiles and fragments.
	containsAll(t, runCompletion(t, "show", ""), "bash", "docker")
}

func TestCompletionShowPrefix(t *testing.T) {
	isolateConfig(t)
	names := runCompletion(t, "show", "doc")
	containsAll(t, names, "docker")
}

func TestCompletionPassthroughAfterProfile(t *testing.T) {
	isolateConfig(t)
	// Everything after the profile name is passthrough: nothing to complete.
	if names := runCompletion(t, "bash", ""); len(names) != 0 {
		t.Errorf("expected no completions after profile name, got %v", names)
	}
}

func TestCompletionInitExtends(t *testing.T) {
	isolateConfig(t)
	containsAll(t, runCompletion(t, "init", "--extends", ""), "bash", "docker")
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
	containsAll(t, runCompletion(t, ""), "bash")
}

func TestCompletionShowAfterPositional(t *testing.T) {
	isolateConfig(t)
	if names := runCompletion(t, "show", "bash", ""); len(names) != 0 {
		t.Errorf("expected no completions after show's name is given, got %v", names)
	}
}

func TestCompletionRunPassthrough(t *testing.T) {
	isolateConfig(t)
	if names := runCompletion(t, "run", "bash", ""); len(names) != 0 {
		t.Errorf("expected no completions after run profile name, got %v", names)
	}
}
