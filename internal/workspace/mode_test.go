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

func TestComputeMountTargetUnknown(t *testing.T) {
	got := ComputeMountTarget("/home/me/projects/myapp", ModeUnknown)
	if got != "" {
		t.Errorf("unknown target = %q, want empty (no claim without a daemon)", got)
	}
}

func TestModeUnknownString(t *testing.T) {
	if ModeUnknown.String() != "unknown" {
		t.Errorf("ModeUnknown.String() = %q, want unknown", ModeUnknown.String())
	}
}
