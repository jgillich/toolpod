package mise

import (
	"strings"
	"testing"
)

func TestActivateCommand_WithTools(t *testing.T) {
	tools := map[string]string{"node": "20", "python": "3.12"}
	cmd := ActivateCommand("/root", tools)

	if !strings.Contains(cmd, "/root/.config/mise/config.toml") {
		t.Errorf("missing config write in %q", cmd)
	}
	if !strings.Contains(cmd, `node = "20"`) {
		t.Errorf("missing node pin in %q", cmd)
	}
	if !strings.Contains(cmd, `python = "3.12"`) {
		t.Errorf("missing python pin in %q", cmd)
	}
	if !strings.Contains(cmd, "mise hook-env") {
		t.Errorf("missing activate in %q", cmd)
	}
}

func TestActivateCommand_NoTools(t *testing.T) {
	cmd := ActivateCommand("/root", nil)
	if strings.Contains(cmd, "config.toml") {
		t.Errorf("should not write config when no tools: %q", cmd)
	}
	if !strings.Contains(cmd, "mise hook-env") {
		t.Errorf("missing activate in %q", cmd)
	}
}

func TestBatchInstallCommand(t *testing.T) {
	tools := map[string]string{"node": "20", "python": "3.12"}
	cmd := batchInstallCommand(tools)
	if !contains(cmd, "mise install node@20") {
		t.Errorf("missing node@20 in %q", cmd)
	}
	if !contains(cmd, "mise install python@3.12") {
		t.Errorf("missing python@3.12 in %q", cmd)
	}
	if !contains(cmd, " && ") {
		t.Errorf("commands should be joined with && : %q", cmd)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
