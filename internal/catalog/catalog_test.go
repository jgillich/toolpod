package catalog_test

import (
	"testing"

	"github.com/jgillich/tpd/internal/catalog"
	"github.com/jgillich/tpd/internal/profile"
)

// guiWaylandMount is the raw (unrendered) mount key the gui fragment uses for
// the guarded wayland socket: it renders empty (and the optional mount is
// skipped) unless both XDG_RUNTIME_DIR and WAYLAND_DISPLAY are set.
const guiWaylandMount = "{{ if and .Env.XDG_RUNTIME_DIR .Env.WAYLAND_DISPLAY }}{{ .Env.XDG_RUNTIME_DIR }}/{{ .Env.WAYLAND_DISPLAY }}{{ end }}"

// TestBuiltinAppimageToolsStayLatest is the canary for the H-04 design: the
// appimage backend resolves `latest` at install time instead of the catalog
// pinning versions, so the built-in profiles must still load and validate
// with a bare `latest` (no checksum).
func TestAdvisory(t *testing.T) {
	for _, name := range []string{"docker", "podman", "gui", "gui-runtime", "ssh", "netrc", "aws", "azure", "gcloud", "github", "gitlab", "vault"} {
		if got := catalog.Advisory(name); got == "" {
			t.Errorf("Advisory(%q) should be non-empty", name)
		}
	}
	for _, name := range []string{"javascript", "go", "gitconfig", "bash", "mise", ""} {
		if got := catalog.Advisory(name); got != "" {
			t.Errorf("Advisory(%q) = %q, want empty", name, got)
		}
	}
}

// TestGuiRuntimeSplit is the H-03 canary: gui mounts only the guarded wayland
// socket, broad $XDG_RUNTIME_DIR access moved to the opt-in gui-runtime
// fragment, and the built-in GUI profiles extend both.
func TestGuiRuntimeSplit(t *testing.T) {
	cat, err := profile.LoadProfiles("")
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}

	guiCfg, err := profile.ResolveFragment(cat, "gui")
	if err != nil {
		t.Fatalf("ResolveFragment(gui): %v", err)
	}
	if _, ok := guiCfg.Mounts["{{ .Env.XDG_RUNTIME_DIR }}"]; ok {
		t.Error("gui must not mount $XDG_RUNTIME_DIR wholesale; use gui-runtime")
	}
	if _, ok := guiCfg.Mounts[guiWaylandMount]; !ok {
		t.Error("gui should mount only the guarded wayland socket")
	}

	rtCfg, err := profile.ResolveFragment(cat, "gui-runtime")
	if err != nil {
		t.Fatalf("ResolveFragment(gui-runtime): %v", err)
	}
	if _, ok := rtCfg.Mounts["{{ .Env.XDG_RUNTIME_DIR }}"]; !ok {
		t.Error("gui-runtime should mount $XDG_RUNTIME_DIR wholesale")
	}

	for _, name := range []string{"buzz", "t3code"} {
		cfg, err := profile.ResolveProfile(cat, name)
		if err != nil {
			t.Fatalf("ResolveProfile(%s): %v", name, err)
		}
		if _, ok := cfg.Mounts["{{ .Env.XDG_RUNTIME_DIR }}"]; !ok {
			t.Errorf("%s: resolved mounts should include the gui-runtime runtime-dir mount", name)
		}
		if _, ok := cfg.Mounts[guiWaylandMount]; !ok {
			t.Errorf("%s: resolved mounts should include the gui wayland socket mount", name)
		}
	}
}

func TestBuiltinAppimageToolsStayLatest(t *testing.T) {
	cat, err := profile.LoadProfiles("")
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	want := map[string]string{
		"buzz":   "appimage:block/buzz",
		"t3code": "appimage:pingdotgg/t3code",
	}
	for name, toolName := range want {
		cfg, err := profile.ResolveProfile(cat, name)
		if err != nil {
			t.Fatalf("ResolveProfile(%s): %v", name, err)
		}
		tool, ok := cfg.Tools[toolName]
		if !ok {
			t.Fatalf("%s: missing tool %s", name, toolName)
		}
		if tool.Version != "latest" {
			t.Errorf("%s: %s version = %q, want latest", name, toolName, tool.Version)
		}
		if tool.SHA256 != "" || len(tool.SHA256ByArch) > 0 {
			t.Errorf("%s: %s should carry no checksum in the catalog, got %+v", name, toolName, tool)
		}
	}
}
