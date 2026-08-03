package catalog_test

import (
	"testing"

	"github.com/jgillich/tpd/internal/profile"
)

// TestBuiltinAppimageToolsStayLatest is the canary for the H-04 design: the
// appimage backend resolves `latest` at install time instead of the catalog
// pinning versions, so the built-in profiles must still load and validate
// with a bare `latest` (no checksum).
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
