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

func TestBackendRuntimesCommand_NpmToolAddsNode(t *testing.T) {
	cmd := BackendRuntimesCommand("/root/.config/mise", map[string]string{"npm:eslint": "latest"})
	if cmd == "" {
		t.Fatal("expected non-empty command")
	}
	if !strings.Contains(cmd, `"node" = "latest"`) {
		t.Errorf("missing node append in %q", cmd)
	}
	if strings.Contains(cmd, "mise registry") {
		t.Errorf("prefixed tool must not trigger a registry lookup: %q", cmd)
	}
}

func TestBackendRuntimesCommand_UnprefixedToolUsesRegistry(t *testing.T) {
	cmd := BackendRuntimesCommand("/root/.config/mise", map[string]string{"gemini": "latest"})
	if cmd == "" {
		t.Fatal("expected non-empty command")
	}
	if !strings.Contains(cmd, "mise registry 'gemini'") {
		t.Errorf("missing registry lookup for gemini: %q", cmd)
	}
	if !strings.Contains(cmd, `"node" = "latest"`) {
		t.Errorf("missing node append in %q", cmd)
	}
}

func TestBackendRuntimesCommand_PresentNodeNotTouched(t *testing.T) {
	cmd := BackendRuntimesCommand("/root/.config/mise", map[string]string{"gemini": "latest", "node": "20"})
	if strings.Contains(cmd, `"node" = "latest"`) {
		t.Errorf("must not re-add a pinned node: %q", cmd)
	}
	if strings.Contains(cmd, "mise registry 'node'") {
		t.Errorf("must not look up the node runtime itself: %q", cmd)
	}
}

func TestBackendRuntimesCommand_PipxToolAddsUV(t *testing.T) {
	cmd := BackendRuntimesCommand("/root/.config/mise", map[string]string{"pipx:ruff": "latest"})
	if cmd == "" {
		t.Fatal("expected non-empty command")
	}
	if !strings.Contains(cmd, `"uv" = "latest"`) {
		t.Errorf("missing uv append in %q", cmd)
	}
}

func TestBackendRuntimesCommand_NoOpWhenPipxOrUVPresent(t *testing.T) {
	for _, tools := range []map[string]string{
		{"pipx:ruff": "latest", "uv": "0.12"},
		{"pipx:ruff": "latest", "pipx": "latest"},
	} {
		if cmd := BackendRuntimesCommand("/root/.config/mise", tools); cmd != "" {
			t.Errorf("expected empty command for %v, got %q", tools, cmd)
		}
	}
}

func TestBackendRuntimesCommand_NoBackendTools(t *testing.T) {
	cmd := BackendRuntimesCommand("/root/.config/mise", map[string]string{"go": "latest"})
	if cmd == "" {
		t.Fatal("expected registry-aware detection for unprefixed tool")
	}
	if !strings.Contains(cmd, "mise registry 'go'") {
		t.Errorf("missing registry lookup for go: %q", cmd)
	}
	if !strings.Contains(cmd, `"$__node" = 1`) || !strings.Contains(cmd, `"$__uv" = 1`) {
		t.Errorf("node/uv appends must be guarded by the registry result: %q", cmd)
	}
}

func TestBackendRuntimesCommand_MixedToolset(t *testing.T) {
	cmd := BackendRuntimesCommand("/root/.config/mise",
		map[string]string{"gemini": "latest", "npm:eslint": "latest", "pipx:ruff": "latest", "go": "latest"})
	if !strings.Contains(cmd, `"node" = "latest"`) {
		t.Errorf("missing node append in %q", cmd)
	}
	if !strings.Contains(cmd, `"uv" = "latest"`) {
		t.Errorf("missing uv append in %q", cmd)
	}
	if !strings.Contains(cmd, "mise registry 'gemini'") || !strings.Contains(cmd, "mise registry 'go'") {
		t.Errorf("missing registry lookups for unprefixed tools: %q", cmd)
	}
	if strings.Contains(cmd, "mise registry 'npm:eslint'") || strings.Contains(cmd, "mise registry 'pipx:ruff'") {
		t.Errorf("prefixed tools must not trigger registry lookups: %q", cmd)
	}
}

func TestBackendRuntimesCommand_UsesConfigDir(t *testing.T) {
	cmd := BackendRuntimesCommand("/root/.config/mise", map[string]string{"npm:eslint": "latest"})
	if !strings.Contains(cmd, "/root/.config/mise/config.toml") {
		t.Errorf("missing config path in %q", cmd)
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
