package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runTpdEnv runs the built binary with the given extra environment variables.
func runTpdEnv(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildTpd(t), args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// writeEditor writes an executable fake $EDITOR script to dir.
func writeEditor(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("writing editor script: %v", err)
	}
	return path
}

// seedUserConfig writes a user profile and a user fragment under a temp
// XDG_CONFIG_HOME so CLI assertions target test-owned entries, never the live
// embedded catalog.
func seedUserConfig(t *testing.T, cfg string) {
	t.Helper()
	profilesDir := filepath.Join(cfg, "tpd", "profiles")
	fragmentsDir := filepath.Join(cfg, "tpd", "fragments", "misc")
	if err := os.MkdirAll(profilesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fragmentsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profilesDir, "myapp.yaml"), []byte("version: 1\nimage: x\ncommand: [\"myapp\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragmentsDir, "util.yaml"), []byte("version: 1\ntools:\n  util: latest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runTpdCfg(t *testing.T, cfg string, args ...string) (string, error) {
	t.Helper()
	return runTpdEnv(t, []string{"XDG_CONFIG_HOME=" + cfg}, args...)
}

func TestProfileShowUser(t *testing.T) {
	cfg := t.TempDir()
	seedUserConfig(t, cfg)
	out, err := runTpdCfg(t, cfg, "show", "myapp")
	if err != nil {
		t.Fatalf("profile show myapp: %v\n%s", err, out)
	}
	if !strings.Contains(out, "command:") {
		t.Errorf("expected raw myapp profile to declare command, got:\n%s", out)
	}
}

func TestProfileShowResolvedUser(t *testing.T) {
	cfg := t.TempDir()
	profilesDir := filepath.Join(cfg, "tpd", "profiles")
	if err := os.MkdirAll(profilesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profilesDir, "base.yaml"), []byte("version: 1\nimage: debian:13-slim\ncommand: [\"sh\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profilesDir, "myapp.yaml"), []byte("version: 1\nextends: base\ncommand: [\"myapp\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runTpdCfg(t, cfg, "show", "--resolved", "myapp")
	if err != nil {
		t.Fatalf("profile show --resolved myapp: %v\n%s", err, out)
	}
	if !strings.Contains(out, "image: debian:13-slim") {
		t.Errorf("expected resolved myapp to inherit image from base, got:\n%s", out)
	}
}

func TestProfileShowNonexistent(t *testing.T) {
	out, _ := runTpd(t, "show", "nope")
	if !strings.Contains(out, "not found") {
		t.Errorf("expected 'not found' error for missing profile, got:\n%s", out)
	}
}

func TestProfileList(t *testing.T) {
	cfg := t.TempDir()
	seedUserConfig(t, cfg)
	out, err := runTpdCfg(t, cfg, "list")
	if err != nil {
		t.Fatalf("profile list: %v\n%s", err, out)
	}
	s := string(out)
	// Task 5 restores bare display names (no core/ keys) with core source.
	if strings.Contains(s, "core/myapp") {
		t.Errorf("expected bare display names, got core/-qualified:\n%s", s)
	}
	if !strings.Contains(s, "myapp") {
		t.Errorf("expected profile list to contain 'myapp', got:\n%s", s)
	}
	if !strings.Contains(s, "fragment") {
		t.Errorf("expected profile list to label fragments, got:\n%s", s)
	}
}

func TestProfileListShowsDisplayNameAndSource(t *testing.T) {
	cfg := t.TempDir()
	seedUserConfig(t, cfg)
	out, err := runTpdCfg(t, cfg, "list")
	if err != nil {
		t.Fatalf("profile list: %v\n%s", err, out)
	}
	s := string(out)
	if strings.Contains(s, "core/myapp") {
		t.Errorf("expected bare display names, got core/-qualified:\n%s", s)
	}
	myappRow, utilRow := false, false
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "myapp":
			if !strings.Contains(line, "profile") || !strings.Contains(line, "user") {
				t.Errorf("myapp row should be 'profile' 'user', got: %q", line)
			}
			myappRow = true
		case "misc/util":
			if !strings.Contains(line, "fragment") || !strings.Contains(line, "user") {
				t.Errorf("misc/util row should be 'fragment' 'user', got: %q", line)
			}
			utilRow = true
		}
	}
	if !myappRow {
		t.Errorf("expected profile list to contain the myapp row")
	}
	if !utilRow {
		t.Errorf("expected profile list to contain the misc/util fragment row")
	}
}

func TestProfileListShowsDescription(t *testing.T) {
	cfg := t.TempDir()
	profilesDir := filepath.Join(cfg, "tpd", "profiles")
	if err := os.MkdirAll(profilesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profilesDir, "myapp.yaml"), []byte("version: 1\nimage: x\ncommand: [\"myapp\"]\nmeta:\n  description: my app description\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runTpdCfg(t, cfg, "list")
	if err != nil {
		t.Fatalf("profile list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "DESCRIPTION") {
		t.Errorf("expected a DESCRIPTION column header, got:\n%s", out)
	}
	if !strings.Contains(out, "my app description") {
		t.Errorf("expected the list to show the meta description, got:\n%s", out)
	}
}

func TestProfileEditExistingUserFileUntouched(t *testing.T) {
	cfg := t.TempDir()
	target := filepath.Join(cfg, "tpd", "profiles", "bash.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	original := "version: 1\ncommand: [\"bash\", \"-l\"]\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"XDG_CONFIG_HOME=" + cfg,
		"EDITOR=" + writeEditor(t, cfg, "editor", "#!/bin/sh\nexit 0\n"),
	}
	out, err := runTpdEnv(t, env, "edit", "bash")
	if err != nil {
		t.Fatalf("edit existing user file: %v\n%s", err, out)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("existing user file must not be removed: %v", err)
	}
	if string(data) != original {
		t.Errorf("existing user file must be left untouched, got:\n%s", data)
	}
}

func TestProfileEditBrokenUserFileStillOpens(t *testing.T) {
	cfg := t.TempDir()
	target := filepath.Join(cfg, "tpd", "profiles", "bash.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	// Duplicate keys are a YAML unmarshal error; the editor must still open.
	if err := os.WriteFile(target, []byte("version: 1\nextends: core/mise\nextends: core/bash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"XDG_CONFIG_HOME=" + cfg,
		"EDITOR=" + writeEditor(t, cfg, "editor", "#!/bin/sh\nprintf '\\n# fixed by test\\n' >> \"$1\"\n"),
	}
	out, err := runTpdEnv(t, env, "edit", "bash")
	if err != nil {
		t.Fatalf("edit broken bash shadow should open the editor: %v\n%s", err, out)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("broken user file must remain: %v", err)
	}
	if !strings.Contains(string(data), "# fixed by test") {
		t.Errorf("expected the editor to open the broken file, got:\n%s", data)
	}
}

func TestProfileEditBrokenUserOnlyFileStillOpens(t *testing.T) {
	cfg := t.TempDir()
	target := filepath.Join(cfg, "tpd", "profiles", "myprof.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("version: 1\nextends: core/mise\nextends: core/bash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"XDG_CONFIG_HOME=" + cfg,
		"EDITOR=" + writeEditor(t, cfg, "editor", "#!/bin/sh\nprintf '\\n# fixed by test\\n' >> \"$1\"\n"),
	}
	out, err := runTpdEnv(t, env, "edit", "myprof")
	if err != nil {
		t.Fatalf("edit broken user-only profile should open the editor: %v\n%s", err, out)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("broken user file must remain: %v", err)
	}
	if !strings.Contains(string(data), "# fixed by test") {
		t.Errorf("expected the editor to open the broken file, got:\n%s", data)
	}
}

// A write can leave the mtime unchanged on overlayfs under load; the saved
// check must fall back to content so a real save is not mistaken for a quit.
func TestEditSavedDetectsContentChangeWithUnchangedMtime(t *testing.T) {
	before := fakeFileInfo{t: time.Now()}
	if !savedEdit(before, before, []byte("seed"), []byte("seed\n# edited\n")) {
		t.Fatal("changed content with unchanged mtime must count as saved")
	}
}

func TestEditSavedRequiresSomeChange(t *testing.T) {
	before := fakeFileInfo{t: time.Now()}
	if savedEdit(before, before, []byte("seed"), []byte("seed")) {
		t.Fatal("unchanged content and mtime must count as not saved")
	}
	after := fakeFileInfo{t: before.t.Add(time.Second)}
	if !savedEdit(before, after, []byte("seed"), []byte("seed")) {
		t.Fatal("advanced mtime must count as saved")
	}
}

type fakeFileInfo struct {
	t time.Time
}

func (f fakeFileInfo) Name() string       { return "" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return 0 }
func (f fakeFileInfo) ModTime() time.Time { return f.t }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() interface{}   { return nil }

func TestProfileShowResolvedFragment(t *testing.T) {
	cfg := t.TempDir()
	seedUserConfig(t, cfg)
	out, err := runTpdCfg(t, cfg, "show", "--resolved", "misc/util")
	if err != nil {
		t.Fatalf("show --resolved misc/util: %v\n%s", err, out)
	}
	if strings.Contains(out, "is a fragment") {
		t.Errorf("expected fragment to resolve, got refusal:\n%s", out)
	}
	if !strings.Contains(out, "util:") {
		t.Errorf("expected resolved fragment output to contain fragment content, got:\n%s", out)
	}
}
