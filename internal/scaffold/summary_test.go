package scaffold

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noReviewPrompt reports whether output contains a host-access review prompt,
// which init no longer shows.
func noReviewPrompt(s string) bool {
	return strings.Contains(s, "Proceed / View details / Abort") || strings.Contains(s, "grants host access")
}

func TestInitNonInteractiveWritesWithoutReviewPrompt(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "shell",
		Extends:    []string{"lang/javascript"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if noReviewPrompt(stdout.String() + stderr.String()) {
		t.Errorf("should not prompt for review, got: %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "shell.yaml")); err != nil {
		t.Error("file should be written")
	}
}

func TestExplicitArgsWritesWithoutReviewPrompt(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:        "opencode",
		Extends:     []string{"lang/javascript", "creds/gitconfig", "creds/ssh"},
		Interactive: true,
		ProfileDir:  dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if noReviewPrompt(stdout.String() + stderr.String()) {
		t.Errorf("should not prompt for review, got: %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode.yaml")); err != nil {
		t.Error("file should be written without prompting")
	}
}

func TestInitWizardWritesWithoutReviewPrompt(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Interactive: true,
		ProfileDir:  dir,
	}, strings.NewReader("opencode\ncreds/ssh\n"), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if noReviewPrompt(stdout.String() + stderr.String()) {
		t.Errorf("should not prompt for review, got: %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode.yaml")); err != nil {
		t.Error("should write the profile file")
	}
}
