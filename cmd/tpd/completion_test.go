package main

import (
	"bytes"
	"strings"
	"testing"
)

// runCompletion invokes cobra's hidden __complete command and returns the
// candidate names (directive lines stripped). XDG_CONFIG_HOME is isolated so
// only embedded built-ins contribute completions.
func runCompletion(t *testing.T, args ...string) []string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
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
	names := runCompletion(t, "")
	containsAll(t, names, "shell", "launch", "show") // built-in profile + commands
}

func TestCompletionProfilePrefix(t *testing.T) {
	names := runCompletion(t, "shel")
	if len(names) != 1 || names[0] != "shell" {
		t.Errorf("expected only 'shell', got %v", names)
	}
}

func TestCompletionLaunch(t *testing.T) {
	containsAll(t, runCompletion(t, "launch", ""), "shell")
}

func TestCompletionShow(t *testing.T) {
	// show accepts profiles and fragments.
	containsAll(t, runCompletion(t, "show", ""), "shell", "docker")
}

func TestCompletionShowPrefix(t *testing.T) {
	names := runCompletion(t, "show", "doc")
	containsAll(t, names, "docker")
}

func TestCompletionPassthroughAfterProfile(t *testing.T) {
	// Everything after the profile name is passthrough: nothing to complete.
	if names := runCompletion(t, "shell", ""); len(names) != 0 {
		t.Errorf("expected no completions after profile name, got %v", names)
	}
}

func TestCompletionInitExtends(t *testing.T) {
	containsAll(t, runCompletion(t, "init", "--extends", ""), "shell", "docker")
}
