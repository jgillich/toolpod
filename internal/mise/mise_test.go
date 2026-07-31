package mise

import (
	"context"
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

type fakeContainerRunner struct {
	env []string
	cmd []string
}

func (f *fakeContainerRunner) RunInContainer(_ context.Context, _ string, _ []VolumeMount, env []string, cmd []string) (int, error) {
	f.env = env
	f.cmd = cmd
	return 0, nil
}

type discardProgress struct{}

func (discardProgress) WriteProgress(string) {}

func TestEnsureToolsConfigDrivenInstall(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	runner := &fakeContainerRunner{}
	spec := ToolsSpec{
		Image: "img",
		Tools: map[string]string{"azure": "latest", "pipx": "latest"},
	}
	if err := EnsureTools(context.Background(), runner, spec, "/root", discardProgress{}); err != nil {
		t.Fatalf("EnsureTools: %v", err)
	}

	env := strings.Join(runner.env, " ")
	if !strings.Contains(env, "MISE_CONFIG_DIR=/root/.config/mise") {
		t.Errorf("env %q missing MISE_CONFIG_DIR=/root/.config/mise", env)
	}

	cmd := strings.Join(runner.cmd, " ")
	if !strings.Contains(cmd, "config.toml") {
		t.Errorf("cmd %q should write a mise config", cmd)
	}
	if !strings.HasSuffix(cmd, " && mise install") {
		t.Errorf("cmd %q should end with a single config-driven mise install", cmd)
	}
	if strings.Contains(cmd, "mise install azure@latest") || strings.Contains(cmd, "mise install pipx@latest") {
		t.Errorf("cmd %q must not install per-tool (mise must resolve ordering)", cmd)
	}
}
