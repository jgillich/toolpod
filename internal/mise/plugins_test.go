package mise

import (
	"context"
	"encoding/base64"
	"io/fs"
	"strings"
	"testing"
)

func TestPluginInstallCommandWritesEmbeddedPlugins(t *testing.T) {
	cmd := PluginInstallCommand()

	for _, p := range []string{
		"metadata.lua",
		"hooks/backend_list_versions.lua",
		"hooks/backend_install.lua",
		"hooks/backend_exec_env.lua",
	} {
		want := "/mise/plugins/appimage/" + p
		if !strings.Contains(cmd, "> "+want) {
			t.Errorf("command should write %s\ncmd: %s", want, cmd)
		}
	}
}

func TestPluginInstallCommandRemovesStalePluginDir(t *testing.T) {
	cmd := PluginInstallCommand()

	// A leftover file or dangling symlink at /mise/plugins/appimage makes
	// mkdir -p fail with "File exists"; the command must clear it first.
	if !strings.HasPrefix(cmd, "rm -rf /mise/plugins/appimage") {
		t.Errorf("command should remove any stale /mise/plugins/appimage first\ncmd: %s", cmd)
	}
}

func TestPluginInstallCommandEmbedsFileContents(t *testing.T) {
	cmd := PluginInstallCommand()

	err := fs.WalkDir(pluginsFS, "plugins", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(pluginsFS, p)
		if err != nil {
			return err
		}
		enc := base64.StdEncoding.EncodeToString(data)
		if !strings.Contains(cmd, enc) {
			t.Errorf("command should embed base64 of %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnsureToolsInstallsAppImagePlugin(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	runner := &fakeContainerRunner{}
	spec := ToolsSpec{
		Image: "img",
		Tools: map[string]string{"appimage:pingdotgg/t3code": "latest"},
	}
	if err := EnsureTools(context.Background(), runner, spec, "/root", discardProgress{}); err != nil {
		t.Fatalf("EnsureTools: %v", err)
	}

	cmd := runner.cmd[2]
	if !strings.HasPrefix(cmd, "rm -rf /mise/plugins/appimage") {
		t.Errorf("cmd %q should start with the appimage plugin install (rm -rf stale, then write)", cmd)
	}
	if !strings.Contains(cmd, "/mise/plugins/appimage/metadata.lua") {
		t.Errorf("cmd %q should install the embedded appimage plugin", cmd)
	}
	if !strings.HasSuffix(cmd, " && mise install") {
		t.Errorf("cmd %q should still end with mise install", cmd)
	}
}
