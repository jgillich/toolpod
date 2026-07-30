package workspace

import "testing"

func TestComputeMountTargetModeA(t *testing.T) {
	got := ComputeMountTarget("/home/me/projects/myapp", "A")
	if got != "/home/me/projects/myapp" {
		t.Errorf("Mode A target = %q, want /home/me/projects/myapp", got)
	}
}

func TestComputeMountTargetModeB(t *testing.T) {
	got := ComputeMountTarget("/home/me/projects/myapp", "B")
	if got != "/workspace" {
		t.Errorf("Mode B target = %q, want /workspace", got)
	}
}

func TestModeFromRootless(t *testing.T) {
	if ModeFromRootless(true) != "A" {
		t.Error("rootless=true should be Mode A")
	}
	if ModeFromRootless(false) != "B" {
		t.Error("rootless=false should be Mode B")
	}
}
