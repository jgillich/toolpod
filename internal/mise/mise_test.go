package mise

import "testing"

func TestActivateCommand(t *testing.T) {
	cmd := ActivateCommand("/root")
	want := "eval \"$(/root/.local/share/mise/mise activate sh)\""
	if cmd != want {
		t.Errorf("ActivateCommand(/root) = %q, want %q", cmd, want)
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
