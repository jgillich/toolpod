package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
	for _, c := range []string{"Usage:", "run", "show", "profile"} {
		if !strings.Contains(string(out), c) {
			t.Errorf("expected bare tpd help to mention %q, got:\n%s", c, out)
		}
	}
	if !strings.Contains(string(out), "tpd <profile>") {
		t.Errorf("expected bare tpd help to show the tpd <profile> launch form, got:\n%s", out)
	}
}

func TestBareHelpShowsAllCommands(t *testing.T) {
	bin := buildTpd(t)
	cmd := exec.Command(bin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bare tpd should show help and exit 0, got err: %v\n%s", err, out)
	}
	for _, c := range []string{"run", "init", "profile", "doctor", "prune"} {
		if !strings.Contains(string(out), c) {
			t.Errorf("expected bare tpd help to mention %q, got:\n%s", c, out)
		}
	}
}

func TestLaunchFlagsBind(t *testing.T) {
	o := &launchFlags{}
	cmd := newRunCommand(o)
	if err := cmd.Flags().Parse([]string{"--pull", "--dry-run", "--verbose", "--command", "ls", "--workspace", "/tmp"}); err != nil {
		t.Fatalf("parse launch flags: %v", err)
	}
	if !o.Pull || !o.DryRun || !o.Verbose || o.Command != "ls" || o.Workspace != "/tmp" {
		t.Errorf("launch flags not bound: %+v", o)
	}
}

func TestLaunchPassthroughAfterProfile(t *testing.T) {
	// kong's passthrough:partial contract: everything from the profile name
	// onward reaches the profile verbatim, even tokens that look like flags.
	o := &launchFlags{}
	cmd := newRunCommand(o)
	if err := cmd.Flags().Parse([]string{"bash", "--model", "foo", "-c", "ls"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := cmd.Flags().Args(); fmt.Sprint(got) != "[bash --model foo -c ls]" {
		t.Errorf("passthrough args = %v, want [bash --model foo -c ls]", got)
	}
	if o.Command != "" {
		t.Errorf("-c after profile must not bind to the launch flag, got %q", o.Command)
	}
}

func TestRootLaunchPassthrough(t *testing.T) {
	root := newRootCommand()
	target, args, err := root.Find([]string{"--dry-run", "bash", "--model", "foo"})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if target != root {
		t.Fatalf("expected root as dispatch target, got %q", target.Name())
	}
	if err := target.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if got := target.Flags().Args(); fmt.Sprint(got) != "[bash --model foo]" {
		t.Errorf("passthrough args = %v, want [bash --model foo]", got)
	}
	if !target.Flags().Changed("dry-run") {
		t.Error("--dry-run before the profile name must bind to the root launch flags")
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
	out, err := runTpd(t, "show", "services/docker-host")
	if err != nil {
		t.Fatalf("show services/docker-host: %v\n%s", err, out)
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
	out, err := runTpdEnv(t, env, "edit", "services/docker-host")
	if err != nil {
		t.Fatalf("edit services/docker-host: %v\n%s", err, out)
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
