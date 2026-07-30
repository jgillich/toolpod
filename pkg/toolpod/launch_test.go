package toolpod

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBuiltinShell(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Use a user config dir with a valid profile so LoadCatalog succeeds.
	err := os.WriteFile(filepath.Join(dir, "shell.yaml"), []byte("version: 1\nimage: myimg:latest\ncommand: [\"sh\"]\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLaunchDryRunPrintsSpec(t *testing.T) {
	dir := writeBuiltinShell(t)
	var out strings.Builder
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		DryRun:      true,
		ConfigDir:   dir,
		Workspace:   "/home/me/proj",
	}, &out)
	if res.Err != nil {
		t.Fatalf("Launch: %v", res.Err)
	}
	output := out.String()
	if !strings.Contains(output, "image: myimg:latest") {
		t.Errorf("dry-run output missing image; got:\n%s", output)
	}
	if !strings.Contains(output, "command:") {
		t.Errorf("dry-run output missing command; got:\n%s", output)
	}
	if !strings.Contains(output, "workspace:") {
		t.Errorf("dry-run output missing workspace; got:\n%s", output)
	}
}

func TestLaunchProfileNotFound(t *testing.T) {
	dir := t.TempDir()
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "nope",
		DryRun:      true,
		ConfigDir:   dir,
	}, &strings.Builder{})
	if res.Err == nil {
		t.Fatal("expected error for missing profile")
	}
	if res.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2 (config error)", res.ExitCode)
	}
}

func TestLaunchNoRuntimeReturnsError(t *testing.T) {
	dir := writeBuiltinShell(t)
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		DryRun:      false,
		ConfigDir:   dir,
	}, &strings.Builder{})
	// Plan 1 has no runtime; non-dry-run should error.
	if res.Err == nil {
		t.Fatal("expected error (runtime not implemented in Plan 1)")
	}
}
