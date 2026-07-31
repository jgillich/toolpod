package main

import (
	"strings"
	"testing"
)

func TestProfileShowBuiltIn(t *testing.T) {
	out, err := runToolpod(t, "profile", "show", "shell")
	if err != nil {
		t.Fatalf("profile show shell: %v\n%s", err, out)
	}
	if !strings.Contains(out, "command:") {
		t.Errorf("expected raw shell profile to declare command, got:\n%s", out)
	}
}

func TestProfileShowResolved(t *testing.T) {
	out, err := runToolpod(t, "profile", "show", "--resolved", "shell")
	if err != nil {
		t.Fatalf("profile show --resolved shell: %v\n%s", err, out)
	}
	if !strings.Contains(out, "image:") {
		t.Errorf("expected resolved shell to inherit image from mise, got:\n%s", out)
	}
}

func TestProfileShowNonexistent(t *testing.T) {
	out, _ := runToolpod(t, "profile", "show", "nope")
	if !strings.Contains(out, "not found") {
		t.Errorf("expected 'not found' error for missing profile, got:\n%s", out)
	}
}

func TestProfileList(t *testing.T) {
	out, err := runToolpod(t, "profile", "list")
	if err != nil {
		t.Fatalf("profile list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "shell") {
		t.Errorf("expected profile list to contain 'shell', got:\n%s", out)
	}
	if !strings.Contains(out, "built-in") {
		t.Errorf("expected profile list to label built-in profiles, got:\n%s", out)
	}
	if !strings.Contains(out, "fragment") {
		t.Errorf("expected profile list to label fragments, got:\n%s", out)
	}
}

func TestProfileEditBuiltInErrors(t *testing.T) {
	out, _ := runToolpod(t, "profile", "edit", "shell")
	if !strings.Contains(out, "built-in") || !strings.Contains(out, "init") {
		t.Errorf("expected built-in + init hint for editing a built-in profile, got:\n%s", out)
	}
}

func TestProfileShowResolvedFragmentRefused(t *testing.T) {
	out, _ := runToolpod(t, "profile", "show", "--resolved", "ssh")
	if !strings.Contains(out, "fragment") {
		t.Errorf("expected --resolved on a fragment to be refused with a 'fragment' message, got:\n%s", out)
	}
}
