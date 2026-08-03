package mise

import (
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
		want := "$HOME/.local/share/mise/plugins/appimage/" + p
		if !strings.Contains(cmd, "> "+want) {
			t.Errorf("command should write %s\ncmd: %s", want, cmd)
		}
	}
}

func TestPluginInstallCommandRemovesStalePluginDir(t *testing.T) {
	cmd := PluginInstallCommand()

	// A leftover file or dangling symlink at the plugin dir makes
	// mkdir -p fail with "File exists"; the command must clear it first.
	if !strings.HasPrefix(cmd, "rm -rf $HOME/.local/share/mise/plugins/appimage") {
		t.Errorf("command should remove any stale plugin dir first\ncmd: %s", cmd)
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

func TestAppImageBackendGuardsChecksumsAndLauncherPaths(t *testing.T) {
	data, err := fs.ReadFile(pluginsFS, "plugins/appimage/hooks/backend_install.lua")
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	for _, want := range []string{
		"local sha256 = options.sha256",
		"expected = sha256[\"aarch64\"]",
		"a.name:match(\"%.AppImage$\") and a.name:match(options.asset_pattern)",
		"safe_relative_path(exe, true)",
		"safe_relative_path(name, false)",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("backend_install.lua missing %q", want)
		}
	}
}
