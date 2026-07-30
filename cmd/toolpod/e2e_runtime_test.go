package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runToolpod(t *testing.T, args ...string) (string, error) {
	t.Helper()
	bin, err := exec.LookPath("toolpod")
	if err != nil {
		buildPath := "/tmp/toolpod"
		if _, err := os.Stat(buildPath); os.IsNotExist(err) {
			cmd := exec.Command("go", "build", "-o", buildPath, "./cmd/toolpod")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("go build: %v\n%s", err, out)
			}
		}
		bin = buildPath
	}
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeShellProfile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "shell.yaml"), []byte("version: 1\nimage: alpine:latest\ncommand: [\"sh\", \"-c\"]\nargs_if_none: [\"echo\", \"ready\"]\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestE2EDoctor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if os.Getenv("DOCKER_HOST") == "" {
		t.Skip("DOCKER_HOST not set")
	}
	out, _ := runToolpod(t, "doctor")
	if !strings.Contains(out, "runtime:") {
		t.Errorf("doctor output missing runtime check; got:\n%s", out)
	}
	if !strings.Contains(out, "all checks passed") && !strings.Contains(out, "failure") && !strings.Contains(out, "warning") {
		t.Errorf("doctor output missing summary; got:\n%s", out)
	}
}

func TestE2EPruneForce(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	out, err := runToolpod(t, "prune", "--force", "--volumes")
	if err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Nothing to prune") && !strings.Contains(out, "Removed") {
		t.Errorf("prune output should say 'Nothing to prune' or 'Removed'; got:\n%s", out)
	}
}

func TestE2EShellLaunch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if os.Getenv("DOCKER_HOST") == "" {
		t.Skip("DOCKER_HOST not set")
	}
	configDir := writeShellProfile(t)
	out, err := runToolpod(t, "shell", "--profile-dir", configDir, "-c", "echo hello-from-toolpod")
	if err != nil {
		t.Fatalf("shell launch: %v\n%s", err, out)
	}
	if !strings.Contains(out, "hello-from-toolpod") {
		t.Errorf("shell launch output missing echo; got:\n%s", out)
	}
}
