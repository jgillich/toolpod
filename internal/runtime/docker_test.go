package runtime

import (
	"context"
	"os"
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
