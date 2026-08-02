package workspace

import "testing"

func TestComputeMountTargetRootless(t *testing.T) {
	got := ComputeMountTarget("/home/me/projects/myapp", ModeRootless)
	if got != "/home/me/projects/myapp" {
		t.Errorf("rootless target = %q, want /home/me/projects/myapp", got)
	}
}

func TestComputeMountTargetRootful(t *testing.T) {
	got := ComputeMountTarget("/home/me/projects/myapp", ModeRootful)
	if got != "/workspace" {
		t.Errorf("rootful target = %q, want /workspace", got)
	}
}
