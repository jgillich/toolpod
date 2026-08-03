package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/jgillich/tpd/internal/profile"
)

func buildTpd(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "tpd")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func TestLaunchBareShowsHelp(t *testing.T) {
	bin := buildTpd(t)
	cmd := exec.Command(bin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bare tpd should show help and exit 0, got err: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Usage: tpd") {
		t.Errorf("expected help text, got:\n%s", out)
	}
	if !strings.Contains(string(out), "profile-and-args") {
		t.Errorf("expected help to mention profile-and-args, got:\n%s", out)
	}
}

func TestBareHelpShowsAllCommands(t *testing.T) {
	bin := buildTpd(t)
	cmd := exec.Command(bin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bare tpd should show help and exit 0, got err: %v\n%s", err, out)
	}
	for _, c := range []string{"launch", "init", "profile", "doctor", "prune"} {
		if !strings.Contains(string(out), c) {
			t.Errorf("expected bare tpd help to mention %q, got:\n%s", c, out)
		}
	}
}

func TestLaunchPullFlagBinds(t *testing.T) {
	// --pull must bind to LaunchCmd.Pull so Run can pass it to LaunchOpts.
	var cli CLI
	parser := kong.Must(&cli, kong.Name("tpd"))
	if _, err := parser.Parse([]string{"launch", "--pull", "shell"}); err != nil {
		t.Fatalf("parse launch --pull: %v", err)
	}
	if !cli.Launch.Pull {
		t.Error("launch --pull did not set LaunchCmd.Pull")
	}
}

func TestInitHelpMentionsExtends(t *testing.T) {
	bin := buildTpd(t)
	cmd := exec.Command(bin, "init", "--help")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("init --help: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--extends") {
		t.Errorf("expected --extends in init help, got:\n%s", out)
	}
}

func TestExitCodeFor(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"exitError code 0", &exitError{code: 0}, 0},
		{"exitError code 1", &exitError{code: 1}, 1},
		{"exitError code 2", &exitError{code: 2}, 2},
		{"exitError code 3", &exitError{code: 3}, 3},
		{"exitError code 5 (container exit)", &exitError{code: 5}, 5},
		{"exitError carries message", &exitError{code: 2, err: fmt.Errorf("boom")}, 2},
		{"plain error", fmt.Errorf("boom"), 1},
		{"profile error", profile.ProfileError{Message: "bad"}, 2},
		{"wrapped profile error", fmt.Errorf("loading profiles: %w", profile.ProfileError{Message: "bad"}), 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCodeFor(tt.err); got != tt.want {
				t.Errorf("exitCodeFor(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestShowDockerPrintsSensitiveAdvisory(t *testing.T) {
	out, err := runTpd(t, "show", "docker")
	if err != nil {
		t.Fatalf("show docker: %v\n%s", err, out)
	}
	if !strings.Contains(out, "warning:") {
		t.Errorf("expected advisory warning on stderr, got:\n%s", out)
	}
	if !strings.Contains(out, "Docker socket") {
		t.Errorf("expected Docker socket advisory, got:\n%s", out)
	}
}

func TestEditDockerPrintsSensitiveAdvisory(t *testing.T) {
	cfg := t.TempDir()
	env := []string{
		"XDG_CONFIG_HOME=" + cfg,
		"EDITOR=" + writeEditor(t, cfg, "editor", "#!/bin/sh\nexit 0\n"),
	}
	out, err := runTpdEnv(t, env, "edit", "docker")
	if err != nil {
		t.Fatalf("edit docker: %v\n%s", err, out)
	}
	if !strings.Contains(out, "warning:") {
		t.Errorf("expected advisory warning on stderr, got:\n%s", out)
	}
	if !strings.Contains(out, "Docker socket") {
		t.Errorf("expected Docker socket advisory, got:\n%s", out)
	}
}

func TestProfileShowNonexistentExitCode(t *testing.T) {
	out, err := runTpd(t, "show", "nope")
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("show nope should fail; got err: %v\n%s", err, out)
	}
	if got := exitErr.ExitCode(); got != 2 {
		t.Errorf("show nope exit code = %d, want 2 (profile not found)\n%s", got, out)
	}
	if !strings.Contains(out, "not found") {
		t.Errorf("expected 'not found' error, got:\n%s", out)
	}
}
