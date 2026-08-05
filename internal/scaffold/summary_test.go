package scaffold

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitSummaryWithMounts(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	// Non-interactive, no TTY — summary prints but no editor prompt
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"creds/ssh"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	output := stdout.String() + stderr.String()
	if !strings.Contains(output, "mounts") {
		t.Errorf("summary should mention mounts, got: %s", output)
	}
	if !strings.Contains(output, "~/.ssh") {
		t.Errorf("summary should list ~/.ssh mount, got: %s", output)
	}
}

func TestInitNoEditorPromptWithoutMounts(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	// javascript fragment has only caches+tools, no mounts
	err := Run(context.Background(), Options{
		Name:       "shell",
		Extends:    []string{"lang/javascript"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	output := stdout.String() + stderr.String()
	if strings.Contains(output, "Review") {
		t.Errorf("should not prompt for review without mounts, got: %s", output)
	}
}

func TestExplicitArgsNoReviewPrompt(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	// Fully-specified args in a TTY-like invocation (Interactive) must not
	// prompt: print the container-access summary and write the file.
	err := Run(context.Background(), Options{
		Name:        "opencode",
		Extends:     []string{"lang/javascript", "creds/gitconfig", "creds/ssh"},
		Interactive: true,
		ProfileDir:  dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	output := stdout.String() + stderr.String()
	if strings.Contains(output, "Proceed / View details / Abort") {
		t.Errorf("explicit args should not show review prompt, got: %s", output)
	}
	if !strings.Contains(stdout.String(), "Container access") {
		t.Errorf("summary should print container access, got: %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode.yaml")); err != nil {
		t.Error("file should be written without prompting")
	}
}

func TestInitReviewAbort(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	// Wizard flow (no explicit args) + ssh fragment (has mounts) → review
	// prompt → "a" aborts.
	err := Run(context.Background(), Options{
		Interactive: true,
		ProfileDir:  dir,
	}, strings.NewReader("opencode\ncreds/ssh\na\n"), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(stdout.String(), "aborted") {
		t.Errorf("should print aborted, got: %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode.yaml")); err == nil {
		t.Error("should not write the profile file on abort")
	}
}

func TestInitReviewProceed(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	// Wizard flow + ssh fragment → review prompt → "p" (default) proceeds.
	err := Run(context.Background(), Options{
		Interactive: true,
		ProfileDir:  dir,
	}, strings.NewReader("opencode\ncreds/ssh\n\n"), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(stdout.String(), "aborted") {
		t.Errorf("should not abort on proceed, got: %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode.yaml")); err != nil {
		t.Error("should write the profile file on proceed")
	}
}
