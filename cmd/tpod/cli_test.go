package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildTpod(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "tpod")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func TestLaunchBareShowsHelp(t *testing.T) {
	bin := buildTpod(t)
	cmd := exec.Command(bin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bare tpod should show help and exit 0, got err: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Usage: tpod") {
		t.Errorf("expected help text, got:\n%s", out)
	}
	if !strings.Contains(string(out), "profile-and-args") {
		t.Errorf("expected help to mention profile-and-args, got:\n%s", out)
	}
}

func TestBareHelpShowsAllCommands(t *testing.T) {
	bin := buildTpod(t)
	cmd := exec.Command(bin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bare tpod should show help and exit 0, got err: %v\n%s", err, out)
	}
	for _, c := range []string{"launch", "init", "profile", "doctor", "prune"} {
		if !strings.Contains(string(out), c) {
			t.Errorf("expected bare tpod help to mention %q, got:\n%s", c, out)
		}
	}
}

func TestInitHelpMentionsExtends(t *testing.T) {
	bin := buildTpod(t)
	cmd := exec.Command(bin, "init", "--help")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("init --help: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--extends") {
		t.Errorf("expected --extends in init help, got:\n%s", out)
	}
}
