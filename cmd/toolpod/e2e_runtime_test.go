package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func runToolpod(t *testing.T, args ...string) (string, error) {
	t.Helper()
	bin, err := exec.LookPath("toolpod")
	if err != nil {
		buildPath := "/tmp/toolpod"
		if _, err := os.Stat(buildPath); os.IsNotExist(err) {
			_, src, _, ok := runtime.Caller(0)
			if !ok {
				t.Fatal("unable to locate test source file")
			}
			pkgPath := filepath.Join(filepath.Dir(filepath.Dir(src)), "cmd", "toolpod")
			cmd := exec.Command("go", "build", "-o", buildPath, pkgPath)
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

func dockerAvailable() bool {
	if os.Getenv("DOCKER_HOST") != "" {
		return true
	}
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		return true
	}
	if _, err := os.Stat("/run/user/" + fmt.Sprint(os.Getuid()) + "/podman/podman.sock"); err == nil {
		return true
	}
	return false
}

func TestE2EDoctor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if !dockerAvailable() {
		t.Skip("docker/podman not available")
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
	if !dockerAvailable() {
		t.Skip("docker/podman not available")
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
	if !dockerAvailable() {
		t.Skip("docker/podman not available")
	}
	out, err := runToolpod(t, "-c", "echo hello-from-toolpod", "shell")
	if err != nil {
		t.Fatalf("shell launch: %v\n%s", err, out)
	}
	if !strings.Contains(out, "hello-from-toolpod") {
		t.Errorf("shell launch output missing echo; got:\n%s", out)
	}
}
