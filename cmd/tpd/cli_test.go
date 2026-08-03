package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
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
