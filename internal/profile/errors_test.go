package profile

import "testing"

func TestProfileErrorFormat(t *testing.T) {
	err := ProfileError{Path: "/home/me/.config/toolpod/opencode.yaml", Line: 5, Message: "extends: cycle detected"}
	want := "/home/me/.config/toolpod/opencode.yaml:5: extends: cycle detected"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestProfileErrorNoLine(t *testing.T) {
	err := ProfileError{Path: "/home/me/.config/toolpod/opencode.yaml", Message: "missing required field: command"}
	want := "/home/me/.config/toolpod/opencode.yaml: missing required field: command"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestProfileErrorExitCode(t *testing.T) {
	err := ProfileError{Message: "boom"}
	if err.ExitCode() != 2 {
		t.Errorf("ExitCode() = %d, want 2", err.ExitCode())
	}
}

func TestProfileErrorNoPath(t *testing.T) {
	err := ProfileError{Message: "profile not found: az"}
	want := "profile not found: az"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q (no stray leading colon when Path is empty)", err.Error(), want)
	}
}
