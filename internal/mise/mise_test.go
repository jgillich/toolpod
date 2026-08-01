package mise

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestActivateCommand_WithTools(t *testing.T) {
	tools := map[string]string{"node": "20", "python": "3.12"}
	cmd := ActivateCommand("/root/.config/mise", tools)

	if !strings.Contains(cmd, "/root/.config/mise/config.toml") {
		t.Errorf("missing config write in %q", cmd)
	}
	if !strings.Contains(cmd, `"node" = "20"`) {
		t.Errorf("missing node pin in %q", cmd)
	}
	if !strings.Contains(cmd, `"python" = "3.12"`) {
		t.Errorf("missing python pin in %q", cmd)
	}
}

func TestActivateCommand_NoTools(t *testing.T) {
	cmd := ActivateCommand("/root/.config/mise", nil)
	if strings.Contains(cmd, "config.toml") {
		t.Errorf("should not write config when no tools: %q", cmd)
	}
}

func TestActivateCommand_ScopedToolKeysAreQuoted(t *testing.T) {
	// Scoped tools (npm:eslint, pipx:black) contain ":" which is not allowed in
	// TOML bare keys. The generated config.toml must quote them so mise can
	// parse it. Regression for: mise install fails with a TOML parse error.
	tools := map[string]string{"npm:eslint": "latest", "node": "20", "pipx:black": "latest"}
	cmd := ActivateCommand("/root/.config/mise", tools)

	start := strings.Index(cmd, "printf '%s' '") + len("printf '%s' '")
	end := strings.Index(cmd[start:], "' > ")
	if start < 0 || end < 0 {
		t.Fatalf("cannot extract config content from command: %q", cmd)
	}
	content := cmd[start : start+end]

	var cfg struct {
		Tools map[string]string `toml:"tools"`
	}
	if _, err := toml.Decode(content, &cfg); err != nil {
		t.Fatalf("generated config is not valid TOML: %v\ncontent:\n%s", err, content)
	}
	if cfg.Tools["npm:eslint"] != "latest" {
		t.Errorf("missing npm:eslint in parsed tools: %v", cfg.Tools)
	}
	if cfg.Tools["pipx:black"] != "latest" {
		t.Errorf("missing pipx:black in parsed tools: %v", cfg.Tools)
	}
	if cfg.Tools["node"] != "20" {
		t.Errorf("missing node in parsed tools: %v", cfg.Tools)
	}
}

func TestNeedsEmbeddedPlugin(t *testing.T) {
	cases := []struct {
		name  string
		tools map[string]string
		want  bool
	}{
		{"empty", nil, false},
		{"plain-tools-only", map[string]string{"node": "20", "python": "3.12"}, false},
		{"appimage-prefix", map[string]string{"appimage:pingdotgg/t3code": "latest"}, true},
		{"mixed", map[string]string{"node": "20", "appimage:foo": "1.0"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsEmbeddedPlugin(tc.tools); got != tc.want {
				t.Errorf("NeedsEmbeddedPlugin(%v) = %v, want %v", tc.tools, got, tc.want)
			}
		})
	}
}
