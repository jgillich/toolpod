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
		Profile:    "opencode",
		Fragments:  []string{"ssh"},
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
	// npm fragment has only caches+tools, no mounts
	err := Run(context.Background(), Options{
		Profile:    "shell",
		Fragments:  []string{"npm"},
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

func TestInitReviewAbort(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	// Interactive + ssh fragment (has mounts) → review prompt → "a" aborts
	err := Run(context.Background(), Options{
		Profile:     "opencode",
		Fragments:   []string{"ssh"},
		Interactive: true,
		ProfileDir:  dir,
	}, strings.NewReader("a\n"), &stdout, &stderr)
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
	// Interactive + ssh fragment → review prompt → "p" (default) proceeds
	err := Run(context.Background(), Options{
		Profile:     "opencode",
		Fragments:   []string{"ssh"},
		Interactive: true,
		ProfileDir:  dir,
	}, strings.NewReader("\n"), &stdout, &stderr)
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
