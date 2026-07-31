package runtime

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestIsLikelyRootlessSocket(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"unix:///run/user/1000/podman/podman.sock", true},
		{"unix:///var/run/docker.sock", false},
		{"unix:///run/podman/podman.sock", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isLikelyRootlessSocket(tt.host); got != tt.want {
			t.Errorf("isLikelyRootlessSocket(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}

func TestBuildEnvSetsMiseConfigDir(t *testing.T) {
	spec := Spec{
		RuntimeHome: "/root",
		Env:         map[string]string{"OPENCODE_CONFIG_CONTENT": "..."},
	}
	env := buildEnv(spec, "/root")
	var hasHome, hasMiseConfigDir, hasProfileEnv bool
	for _, e := range env {
		switch {
		case strings.HasPrefix(e, "HOME="):
			hasHome = true
			if e != "HOME=/root" {
				t.Errorf("HOME = %q, want HOME=/root", e)
			}
		case strings.HasPrefix(e, "MISE_CONFIG_DIR="):
			hasMiseConfigDir = true
			if e != "MISE_CONFIG_DIR=/root/.config/mise" {
				t.Errorf("MISE_CONFIG_DIR = %q, want /root/.config/mise", e)
			}
		case strings.HasPrefix(e, "OPENCODE_CONFIG_CONTENT="):
			hasProfileEnv = true
		}
	}
	if !hasHome {
		t.Error("missing HOME in env")
	}
	if !hasMiseConfigDir {
		t.Error("missing MISE_CONFIG_DIR in env")
	}
	if !hasProfileEnv {
		t.Error("missing profile env OPENCODE_CONFIG_CONTENT")
	}
}

func TestIntegrationRunShellEcho(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if os.Getenv("DOCKER_HOST") == "" {
		t.Skip("DOCKER_HOST not set")
	}
	rt, err := NewDockerRuntime()
	if err != nil {
		t.Fatalf("NewDockerRuntime: %v", err)
	}
	spec := Spec{
		ProfileName: "test-shell",
		Image:       "alpine:latest",
		Command:     []string{"sh", "-c", "echo hi"},
		Workspace:   WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: "B"},
		Network:     "none",
	}
	if _, err := rt.Prepare(context.Background(), spec, NoopProgressWriter{}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	code, err := rt.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}
