package mise

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestActivateCommand_WithTools(t *testing.T) {
	tools := map[string]Tool{"node": {Version: "20"}, "python": {Version: "3.12"}}
	cmd := ActivateCommand("/root/.config/mise", tools)

	if !strings.Contains(cmd, "/root/.config/mise/config.toml") {
		t.Errorf("missing config write in %q", cmd)
	}
	parsed := decodeTools(t, cmd)
	if parsed["node"] != "20" {
		t.Errorf("missing node pin in %q", cmd)
	}
	if parsed["python"] != "3.12" {
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
	tools := map[string]Tool{"npm:eslint": {Version: "latest"}, "node": {Version: "20"}, "pipx:black": {Version: "latest"}}
	cmd := ActivateCommand("/root/.config/mise", tools)

	parsed := decodeTools(t, cmd)
	if parsed["npm:eslint"] != "latest" {
		t.Errorf("missing npm:eslint in parsed tools: %v", parsed)
	}
	if parsed["pipx:black"] != "latest" {
		t.Errorf("missing pipx:black in parsed tools: %v", parsed)
	}
	if parsed["node"] != "20" {
		t.Errorf("missing node in parsed tools: %v", parsed)
	}
}

func TestActivateCommand_InjectionSafe(t *testing.T) {
	os.Remove("/tmp/pwn")
	configDir := t.TempDir()
	tools := map[string]Tool{"evil": {Version: `x' && touch /tmp/pwn`}}
	cmd := ActivateCommand(configDir, tools)

	if out, err := exec.Command("sh", "-c", cmd).CombinedOutput(); err != nil {
		t.Fatalf("command failed: %v\n%s", err, out)
	}
	if _, err := os.Stat("/tmp/pwn"); err == nil {
		t.Fatal("/tmp/pwn was created; version escaped into the shell")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat /tmp/pwn: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(configDir, "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if content := string(data); !strings.Contains(content, `x' && touch /tmp/pwn`) {
		t.Errorf("config.toml must contain the literal version, got:\n%s", content)
	}
}

func TestActivateCommand_ScalarSHA256(t *testing.T) {
	tools := map[string]Tool{"appimage:test": {Version: "1.2.3", SHA256: "deadbeef"}}
	cmd := ActivateCommand("/root/.config/mise", tools)

	entry := decodeTool(t, cmd, "appimage:test")
	if entry["version"] != "1.2.3" {
		t.Errorf("wrong version: %v", entry)
	}
	if entry["sha256"] != "deadbeef" {
		t.Errorf("wrong sha256: %v", entry)
	}
}

func TestActivateCommand_PerArchSHA256(t *testing.T) {
	tools := map[string]Tool{"appimage:test": {Version: "1.2.3", SHA256ByArch: map[string]string{"amd64": "aaa", "aarch64": "bbb"}}}
	cmd := ActivateCommand("/root/.config/mise", tools)

	entry := decodeTool(t, cmd, "appimage:test")
	if entry["version"] != "1.2.3" {
		t.Errorf("wrong version: %v", entry)
	}
	arch, ok := entry["sha256"].(map[string]any)
	if !ok {
		t.Fatalf("sha256 is not a per-arch table: %v", entry)
	}
	if arch["amd64"] != "aaa" || arch["aarch64"] != "bbb" {
		t.Errorf("wrong per-arch digests: %v", arch)
	}
}

func decodeTool(t *testing.T, cmd, name string) map[string]any {
	t.Helper()
	entry, ok := decodeTools(t, cmd)[name]
	if !ok {
		t.Fatalf("missing tool %q in %q", name, cmd)
	}
	m, ok := entry.(map[string]any)
	if !ok {
		t.Fatalf("tool %q is not a table: %T %v", name, entry, entry)
	}
	return m
}

func decodeTools(t *testing.T, cmd string) map[string]any {
	t.Helper()
	content := configContentFromCommand(t, cmd)
	var cfg struct {
		Tools map[string]any `toml:"tools"`
	}
	if _, err := toml.Decode(content, &cfg); err != nil {
		t.Fatalf("generated config is not valid TOML: %v\ncontent:\n%s", err, content)
	}
	return cfg.Tools
}

func configContentFromCommand(t *testing.T, cmd string) string {
	t.Helper()
	const prefix = "printf '%s' '"
	start := strings.Index(cmd, prefix)
	if start < 0 {
		t.Fatalf("cannot find base64 payload in command: %q", cmd)
	}
	start += len(prefix)
	rest := cmd[start:]
	end := strings.Index(rest, "' | base64 -d > ")
	if end < 0 {
		t.Fatalf("cannot find end of base64 payload in command: %q", cmd)
	}
	raw, err := base64.StdEncoding.DecodeString(rest[:end])
	if err != nil {
		t.Fatalf("cannot decode base64 payload in %q: %v", cmd, err)
	}
	return string(raw)
}

func TestBackendRuntimesCommand_NpmToolAddsNode(t *testing.T) {
	cmd := BackendRuntimesCommand("/root/.config/mise", map[string]Tool{"npm:eslint": {Version: "latest"}})
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
	cmd := BackendRuntimesCommand("/root/.config/mise", map[string]Tool{"gemini": {Version: "latest"}})
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
	cmd := BackendRuntimesCommand("/root/.config/mise", map[string]Tool{"gemini": {Version: "latest"}, "node": {Version: "20"}})
	if strings.Contains(cmd, `"node" = "latest"`) {
		t.Errorf("must not re-add a pinned node: %q", cmd)
	}
	if strings.Contains(cmd, "mise registry 'node'") {
		t.Errorf("must not look up the node runtime itself: %q", cmd)
	}
}

func TestBackendRuntimesCommand_PipxToolAddsUV(t *testing.T) {
	cmd := BackendRuntimesCommand("/root/.config/mise", map[string]Tool{"pipx:ruff": {Version: "latest"}})
	if cmd == "" {
		t.Fatal("expected non-empty command")
	}
	if !strings.Contains(cmd, `"uv" = "latest"`) {
		t.Errorf("missing uv append in %q", cmd)
	}
}

func TestBackendRuntimesCommand_NoOpWhenPipxOrUVPresent(t *testing.T) {
	for _, tools := range []map[string]Tool{
		{"pipx:ruff": {Version: "latest"}, "uv": {Version: "0.12"}},
		{"pipx:ruff": {Version: "latest"}, "pipx": {Version: "latest"}},
	} {
		if cmd := BackendRuntimesCommand("/root/.config/mise", tools); cmd != "" {
			t.Errorf("expected empty command for %v, got %q", tools, cmd)
		}
	}
}

func TestBackendRuntimesCommand_NoBackendTools(t *testing.T) {
	cmd := BackendRuntimesCommand("/root/.config/mise", map[string]Tool{"go": {Version: "latest"}})
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
		map[string]Tool{"gemini": {Version: "latest"}, "npm:eslint": {Version: "latest"}, "pipx:ruff": {Version: "latest"}, "go": {Version: "latest"}})
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
	cmd := BackendRuntimesCommand("/root/.config/mise", map[string]Tool{"npm:eslint": {Version: "latest"}})
	if !strings.Contains(cmd, "/root/.config/mise/config.toml") {
		t.Errorf("missing config path in %q", cmd)
	}
}

func TestNeedsEmbeddedPlugin(t *testing.T) {
	cases := []struct {
		name  string
		tools map[string]Tool
		want  bool
	}{
		{"empty", nil, false},
		{"plain-tools-only", map[string]Tool{"node": {Version: "20"}, "python": {Version: "3.12"}}, false},
		{"appimage-prefix", map[string]Tool{"appimage:pingdotgg/t3code": {Version: "latest"}}, true},
		{"mixed", map[string]Tool{"node": {Version: "20"}, "appimage:foo": {Version: "1.0"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsEmbeddedPlugin(tc.tools); got != tc.want {
				t.Errorf("NeedsEmbeddedPlugin(%v) = %v, want %v", tc.tools, got, tc.want)
			}
		})
	}
}
