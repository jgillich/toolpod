package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestProfileShowBuiltIn(t *testing.T) {
	out, err := runTpod(t, "show", "shell")
	if err != nil {
		t.Fatalf("profile show shell: %v\n%s", err, out)
	}
	if !strings.Contains(out, "command:") {
		t.Errorf("expected raw shell profile to declare command, got:\n%s", out)
	}
}

func TestProfileShowResolved(t *testing.T) {
	out, err := runTpod(t, "show", "--resolved", "shell")
	if err != nil {
		t.Fatalf("profile show --resolved shell: %v\n%s", err, out)
	}
	if !strings.Contains(out, "image:") {
		t.Errorf("expected resolved shell to inherit image from mise, got:\n%s", out)
	}
}

func TestProfileShowNonexistent(t *testing.T) {
	out, _ := runTpod(t, "show", "nope")
	if !strings.Contains(out, "not found") {
		t.Errorf("expected 'not found' error for missing profile, got:\n%s", out)
	}
}

func TestProfileList(t *testing.T) {
	bin := buildTpod(t)
	out, err := exec.Command(bin, "list").CombinedOutput()
	if err != nil {
		t.Fatalf("profile list: %v\n%s", err, out)
	}
	s := string(out)
	if !strings.Contains(s, "shell") {
		t.Errorf("expected profile list to contain 'shell', got:\n%s", s)
	}
	if !strings.Contains(s, "built-in") {
		t.Errorf("expected profile list to label built-in entries, got:\n%s", s)
	}
	if !strings.Contains(s, "fragment") {
		t.Errorf("expected profile list to label fragments, got:\n%s", s)
	}
	// Regression: built-in fragments must be marked as built-in (not just
	// "fragment" with no origin).
	dockerMarked := false
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "docker") {
			if !strings.Contains(line, "fragment") || !strings.Contains(line, "built-in") {
				t.Errorf("docker fragment row should be marked 'fragment' 'built-in', got: %q", line)
			}
			dockerMarked = true
		}
	}
	if !dockerMarked {
		t.Errorf("expected profile list to contain the docker fragment")
	}
}

func TestProfileEditBuiltInErrors(t *testing.T) {
	out, _ := runTpod(t, "edit", "shell")
	if !strings.Contains(out, "built-in") || !strings.Contains(out, "init") {
		t.Errorf("expected built-in + init hint for editing a built-in profile, got:\n%s", out)
	}
}

func TestProfileShowResolvedFragmentRefused(t *testing.T) {
	out, _ := runTpod(t, "show", "--resolved", "ssh")
	if !strings.Contains(out, "fragment") {
		t.Errorf("expected --resolved on a fragment to be refused with a 'fragment' message, got:\n%s", out)
	}
}
