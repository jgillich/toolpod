package config

import "testing"

func TestConfigErrorFormat(t *testing.T) {
	err := ConfigError{Path: "/home/me/.config/toolpod/opencode.yaml", Line: 5, Message: "extends: cycle detected"}
	want := "/home/me/.config/toolpod/opencode.yaml:5: extends: cycle detected"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestConfigErrorNoLine(t *testing.T) {
	err := ConfigError{Path: "/home/me/.config/toolpod/opencode.yaml", Message: "missing required field: command"}
	want := "/home/me/.config/toolpod/opencode.yaml: missing required field: command"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestConfigErrorExitCode(t *testing.T) {
	err := ConfigError{Message: "boom"}
	if err.ExitCode() != 2 {
		t.Errorf("ExitCode() = %d, want 2", err.ExitCode())
	}
}
