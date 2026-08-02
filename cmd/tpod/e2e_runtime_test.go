package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func runTpod(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildTpod(t), args...)
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
	out, _ := runTpod(t, "doctor")
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
	out, err := runTpod(t, "prune", "--force", "--volumes")
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
	out, err := runTpod(t, "-c", "echo hello-from-tpod", "shell")
	if err != nil {
		t.Fatalf("shell launch: %v\n%s", err, out)
	}
	if !strings.Contains(out, "hello-from-tpod") {
		t.Errorf("shell launch output missing echo; got:\n%s", out)
	}
}

// TestE2EShellMiseOnPath pins the fix for 4418c8a: a login shell (bash -l)
// resets PATH via /etc/profile, and the /etc/profile.d/mise.sh hook written
// by the shell profile must re-apply mise's env so tools like jq resolve.
func TestE2EShellMiseOnPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if !dockerAvailable() {
		t.Skip("docker/podman not available")
	}
	out, err := runTpod(t, "-c", "command -v jq", "shell")
	if err != nil {
		t.Fatalf("shell jq lookup: %v\n%s", err, out)
	}
	if !strings.Contains(out, "/.local/share/mise/installs/jq/") {
		t.Errorf("mise jq not on PATH in login shell; got:\n%s", out)
	}
}
