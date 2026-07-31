package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildToolpod(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "toolpod")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func TestLaunchBareShowsHelp(t *testing.T) {
	bin := buildToolpod(t)
	cmd := exec.Command(bin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bare toolpod should show help and exit 0, got err: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Usage: toolpod") {
		t.Errorf("expected help text, got:\n%s", out)
	}
	if !strings.Contains(string(out), "profile-and-args") {
		t.Errorf("expected help to mention profile-and-args, got:\n%s", out)
	}
}
