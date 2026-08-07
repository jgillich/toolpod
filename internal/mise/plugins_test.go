package mise

import (
	"encoding/base64"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestPluginInstallCommandVersionedPointer(t *testing.T) {
	cmd := PluginInstallCommand()
	v := pluginInstallVersion()
	plugins := `"$HOME"/.local/share/mise/plugins`

	for _, want := range []string{
		plugins + `/appimage-` + v + `/.tpd-plugin`,
		`rm -f ` + plugins + `/appimage.tmp`,
		`ln -s 'appimage-` + v + `' ` + plugins + `/appimage.tmp`,
		`mv -Tf ` + plugins + `/appimage.tmp ` + plugins + `/appimage`,
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command should contain %q\ncmd: %s", want, cmd)
		}
	}
	// A symlinked pointer must be swapped, never rm -rf'd through the link.
	if !strings.Contains(cmd, `[ ! -L `+plugins+`/appimage ]`) {
		t.Errorf("pointer removal must be guarded by a symlink check\ncmd: %s", cmd)
	}
}

func TestPluginInstallCommandWritesEmbeddedPlugins(t *testing.T) {
	cmd := PluginInstallCommand()
	v := pluginInstallVersion()

	for _, p := range []string{
		"metadata.lua",
		"hooks/backend_list_versions.lua",
		"hooks/backend_install.lua",
		"hooks/backend_exec_env.lua",
	} {
		want := `"$HOME"/.local/share/mise/plugins/appimage-` + v + `/` + p
		if !strings.Contains(cmd, "> "+want) {
			t.Errorf("command should write %s\ncmd: %s", want, cmd)
		}
	}
}

func TestPluginInstallCommandSkipsWhenContentMatches(t *testing.T) {
	cmd := PluginInstallCommand()
	v := pluginInstallVersion()
	// The whole write must be guarded so a complete, current install is a no-op.
	if !strings.Contains(cmd, `[ ! -f "$HOME"/.local/share/mise/plugins/appimage-`+v+`/.tpd-plugin ]`) {
		t.Errorf("command should skip work when the marker matches\ncmd: %s", cmd)
	}
}

func TestPluginInstallCommandWritesMarkerLast(t *testing.T) {
	cmd := PluginInstallCommand()
	v := pluginInstallVersion()
	marker := `printf '%s' '` + v + `' > "$HOME"/.local/share/mise/plugins/appimage-` + v + `/.tpd-plugin`
	swap := `mv -Tf "$HOME"/.local/share/mise/plugins/appimage.tmp "$HOME"/.local/share/mise/plugins/appimage`
	mi := strings.Index(cmd, marker)
	si := strings.Index(cmd, swap)
	if mi < 0 || si < 0 {
		t.Fatalf("command missing marker or pointer swap\ncmd: %s", cmd)
	}
	if mi > si {
		t.Errorf("marker must be written before the pointer swap\ncmd: %s", cmd)
	}
}

func TestPluginInstallCommandQuotesHome(t *testing.T) {
	cmd := PluginInstallCommand()
	unquoted := regexp.MustCompile(`\$HOME([^"])`)
	if m := unquoted.FindStringSubmatch(cmd); m != nil {
		t.Errorf("unquoted $HOME usage %q\ncmd: %s", m[0], cmd)
	}
	if !strings.Contains(cmd, `"$HOME"`) {
		t.Errorf("command should reference $HOME quoted\ncmd: %s", cmd)
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

// envWithHome returns the test environment with a single HOME entry pointing at
// home. os.Environ() + append would duplicate HOME; Go passes the list raw to
// execve, where Rust/glibc getenv is first-wins but sh is last-wins, so mise
// would read the real home's plugin dir instead of the temp one.
func envWithHome(home string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "HOME=") {
			env = append(env, e)
		}
	}
	return append(env, "HOME="+home)
}

func TestPluginInstallCommandExecutesAndIsIdempotent(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("install shell relies on POSIX sh and coreutils")
	}
	home := t.TempDir()
	plugins := filepath.Join(home, ".local", "share", "mise", "plugins")
	v := pluginInstallVersion()

	run := func() {
		t.Helper()
		c := exec.Command("sh", "-c", PluginInstallCommand())
		c.Env = envWithHome(home)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("plugin install failed: %v\n%s\ncmd: %s", err, out, PluginInstallCommand())
		}
	}
	run()

	link, err := os.Readlink(filepath.Join(plugins, "appimage"))
	if err != nil {
		t.Fatalf("plugin pointer should be a symlink: %v", err)
	}
	if want := "appimage-" + v; link != want {
		t.Fatalf("pointer resolves to %q, want %q", link, want)
	}
	marker, err := os.ReadFile(filepath.Join(plugins, "appimage-"+v, ".tpd-plugin"))
	if err != nil || string(marker) != v {
		t.Fatalf("marker missing or wrong: %q %v", marker, err)
	}

	err = fs.WalkDir(pluginsFS, "plugins", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		wantData, err := fs.ReadFile(pluginsFS, p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, "plugins/appimage/")
		got, err := os.ReadFile(filepath.Join(plugins, "appimage-"+v, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if string(got) != string(wantData) {
			t.Errorf("%s differs from embedded content", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Second run must be a no-op (marker + pointer already match).
	installed := filepath.Join(plugins, "appimage-"+v, "metadata.lua")
	before, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	run()
	after, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("idempotent run should not rewrite plugin files")
	}
}

func TestPluginInstallCommandSelfHealsStaleTmp(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("install shell relies on POSIX sh and coreutils")
	}
	home := t.TempDir()
	plugins := filepath.Join(home, ".local", "share", "mise", "plugins")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatal(err)
	}
	// A SIGKILL between ln -s and mv -Tf leaves a stale pointer.tmp sibling
	// that would make the next ln -s fail with EEXIST; the install must clear
	// it and still land the atomic swap.
	if err := os.WriteFile(filepath.Join(plugins, "appimage.tmp"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := exec.Command("sh", "-c", PluginInstallCommand())
	c.Env = envWithHome(home)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("plugin install failed on stale .tmp: %v\n%s\ncmd: %s", err, out, PluginInstallCommand())
	}
	v := pluginInstallVersion()
	link, err := os.Readlink(filepath.Join(plugins, "appimage"))
	if err != nil {
		t.Fatalf("plugin pointer should be a symlink after self-heal: %v", err)
	}
	if want := "appimage-" + v; link != want {
		t.Fatalf("pointer resolves to %q, want %q", link, want)
	}
	if _, err := os.Lstat(filepath.Join(plugins, "appimage.tmp")); !os.IsNotExist(err) {
		t.Errorf("stale .tmp should be gone after install, Lstat err = %v", err)
	}
}

func TestPluginInstallCommandReplacesLegacyPluginDir(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("install shell relies on POSIX sh and coreutils")
	}
	home := t.TempDir()
	plugins := filepath.Join(home, ".local", "share", "mise", "plugins")
	if err := os.MkdirAll(filepath.Join(plugins, "appimage"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugins, "appimage", "metadata.lua"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := exec.Command("sh", "-c", PluginInstallCommand())
	c.Env = envWithHome(home)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("plugin install failed: %v\n%s", err, out)
	}
	info, err := os.Lstat(filepath.Join(plugins, "appimage"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("legacy real plugin dir should be replaced by a symlink")
	}
}

func TestPluginInstallCommandResolvesInMise(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("mise plugin loading is Linux-oriented")
	}
	miseBin, err := exec.LookPath("mise")
	if err != nil {
		t.Skip("mise not installed")
	}
	if testing.Short() {
		t.Skip("skipping mise integration test in short mode")
	}
	home := t.TempDir()
	c := exec.Command("sh", "-c", PluginInstallCommand())
	c.Env = envWithHome(home)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("plugin install failed: %v\n%s", err, out)
	}

	// `mise plugins ls` enumerates the plugin dir; it must see appimage through
	// the pointer symlink (no network or metadata loading involved).
	ls := exec.Command(miseBin, "plugins", "ls")
	ls.Env = envWithHome(home)
	out, err := ls.CombinedOutput()
	if err != nil {
		t.Fatalf("mise plugins ls failed: %v\n%s", err, out)
	}
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		if line == "appimage" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("mise should list the installed appimage plugin on its own line, got:\n%s", out)
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

func TestAppImageBackendValidatesBeforeDownload(t *testing.T) {
	data, err := fs.ReadFile(pluginsFS, "plugins/appimage/hooks/backend_install.lua")
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	valid := strings.Index(script, "invalid exe/name option")
	download := strings.Index(script, "http.download_file")
	extract := strings.Index(script, "--appimage-extract")
	if valid < 0 || download < 0 {
		t.Fatalf("cannot find validation or download in\n%s", script)
	}
	if valid > download {
		t.Errorf("exe/name validation must precede the download\n%s", script)
	}
	if valid > extract {
		t.Errorf("exe/name validation must precede extraction\n%s", script)
	}
}

func TestAppImageBackendHTTPIsNonThrowingAndChecksStatus(t *testing.T) {
	data, err := fs.ReadFile(pluginsFS, "plugins/appimage/hooks/backend_install.lua")
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	for _, want := range []string{
		"pcall(http.get,",
		"status_code ~= 200",
		"pcall(http.download_file,",
		"RUNTIME.osType ~= \"linux\"",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("backend_install.lua missing %q", want)
		}
	}
}

func TestAppImageBackendPreservesXdgOpenOnSwapFailure(t *testing.T) {
	data, err := fs.ReadFile(pluginsFS, "plugins/appimage/hooks/backend_install.lua")
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	// The replacement wrapper must be staged before the bundled xdg-open is
	// renamed aside, and a restore path must exist for a failed swap.
	stage := strings.Index(script, "cp /usr/local/bin/xdg-open")
	rename := strings.Index(script, `shq(xdg_real)`)
	restore := strings.LastIndex(script, `shq(xdg_real)`)
	if stage < 0 || rename < 0 {
		t.Fatalf("cannot find swap steps in\n%s", script)
	}
	if stage > rename {
		t.Errorf("replacement must be staged before the original is renamed\n%s", script)
	}
	if restore <= rename {
		t.Errorf("swap must restore the bundled xdg-open on failure\n%s", script)
	}
}

func TestAppImageListVersionsChecksStatus(t *testing.T) {
	data, err := fs.ReadFile(pluginsFS, "plugins/appimage/hooks/backend_list_versions.lua")
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	for _, want := range []string{
		"pcall(http.get,",
		"status_code ~= 200",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("backend_list_versions.lua missing %q", want)
		}
	}
}
