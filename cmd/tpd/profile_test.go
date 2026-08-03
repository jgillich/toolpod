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

func TestProfileShowBuiltIn(t *testing.T) {
	out, err := runTpd(t, "show", "shell")
	if err != nil {
		t.Fatalf("profile show shell: %v\n%s", err, out)
	}
	if !strings.Contains(out, "command:") {
		t.Errorf("expected raw shell profile to declare command, got:\n%s", out)
	}
}

func TestProfileShowResolved(t *testing.T) {
	out, err := runTpd(t, "show", "--resolved", "shell")
	if err != nil {
		t.Fatalf("profile show --resolved shell: %v\n%s", err, out)
	}
	if !strings.Contains(out, "image:") {
		t.Errorf("expected resolved shell to inherit image from mise, got:\n%s", out)
	}
}

func TestProfileShowNonexistent(t *testing.T) {
	out, _ := runTpd(t, "show", "nope")
	if !strings.Contains(out, "not found") {
		t.Errorf("expected 'not found' error for missing profile, got:\n%s", out)
	}
}

func TestProfileList(t *testing.T) {
	bin := buildTpd(t)
	out, err := exec.Command(bin, "list").CombinedOutput()
	if err != nil {
		t.Fatalf("profile list: %v\n%s", err, out)
	}
	s := string(out)
	// Task 5 restores bare display names; these expectations are temporary.
	if !strings.Contains(s, "core/shell") {
		t.Errorf("expected profile list to contain 'core/shell', got:\n%s", s)
	}
	if !strings.Contains(s, "shell") {
		t.Errorf("expected profile list to contain 'shell', got:\n%s", s)
	}
	if !strings.Contains(s, "built-in") {
		t.Errorf("expected profile list to label built-in entries, got:\n%s", s)
	}
	if !strings.Contains(s, "fragment") {
		t.Errorf("expected profile list to label fragments, got:\n%s", s)
	}
	// Regression: built-in fragments must be marked as built-in (not just
	// "fragment" with no origin).
	dockerMarked := false
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "docker") {
			if !strings.Contains(line, "fragment") || !strings.Contains(line, "built-in") {
				t.Errorf("docker fragment row should be marked 'fragment' 'built-in', got: %q", line)
			}
			dockerMarked = true
		}
	}
	if !dockerMarked {
		t.Errorf("expected profile list to contain the docker fragment")
	}
}

func TestProfileEditBuiltInNoSaveRemovesSeed(t *testing.T) {
	cfg := t.TempDir()
	env := []string{
		"XDG_CONFIG_HOME=" + cfg,
		"EDITOR=" + writeEditor(t, cfg, "editor", "#!/bin/sh\nexit 0\n"),
	}
	out, err := runTpdEnv(t, env, "edit", "shell")
	if err != nil {
		t.Fatalf("edit shell (no save): %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(cfg, "tpd", "profiles", "shell.yaml")); !os.IsNotExist(err) {
		t.Errorf("expected no user profile after quitting without saving, stat err: %v", err)
	}
}

func TestProfileEditBuiltInSaveCreatesOverride(t *testing.T) {
	cfg := t.TempDir()
	env := []string{
		"XDG_CONFIG_HOME=" + cfg,
		"EDITOR=" + writeEditor(t, cfg, "editor", "#!/bin/sh\nprintf '\\n# saved by test\\n' >> \"$1\"\n"),
	}
	out, err := runTpdEnv(t, env, "edit", "shell")
	if err != nil {
		t.Fatalf("edit shell (save): %v\n%s", err, out)
	}
	target := filepath.Join(cfg, "tpd", "profiles", "shell.yaml")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected user override to be created on save: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "extends: core/shell") {
		t.Errorf("seed must extend the built-in itself, got:\n%s", s)
	}
	if !strings.Contains(s, `shadows the built-in "core/shell"`) {
		t.Errorf("seed must explain the shadow/merge, got:\n%s", s)
	}
	if !strings.Contains(s, "Resolved profile (reference)") {
		t.Errorf("seed must carry a resolved-reference banner, got:\n%s", s)
	}
	if !strings.Contains(s, "snapshot from when this file was created") || !strings.Contains(s, `tpd show --resolved core/shell`) {
		t.Errorf("seed must note the resolved block is a stale snapshot and how to refresh it, got:\n%s", s)
	}
	if !strings.Contains(s, "# image: debian:13-slim") {
		t.Errorf("seed must carry the resolved profile commented out, got:\n%s", s)
	}
	if !strings.Contains(s, "# saved by test") {
		t.Errorf("expected override to carry the editor's write, got:\n%s", s)
	}
}

func TestProfileEditBuiltInFragmentNoSaveRemovesSeed(t *testing.T) {
	cfg := t.TempDir()
	env := []string{
		"XDG_CONFIG_HOME=" + cfg,
		"EDITOR=" + writeEditor(t, cfg, "editor", "#!/bin/sh\nexit 0\n"),
	}
	out, err := runTpdEnv(t, env, "edit", "docker")
	if err != nil {
		t.Fatalf("edit docker fragment (no save): %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(cfg, "tpd", "fragments", "docker.yaml")); !os.IsNotExist(err) {
		t.Errorf("expected no user fragment after quitting without saving, stat err: %v", err)
	}
}

func TestProfileEditBuiltInFragmentSaveCreatesOverride(t *testing.T) {
	cfg := t.TempDir()
	env := []string{
		"XDG_CONFIG_HOME=" + cfg,
		"EDITOR=" + writeEditor(t, cfg, "editor", "#!/bin/sh\nprintf '\\n# saved by test\\n' >> \"$1\"\n"),
	}
	out, err := runTpdEnv(t, env, "edit", "docker")
	if err != nil {
		t.Fatalf("edit docker fragment (save): %v\n%s", err, out)
	}
	target := filepath.Join(cfg, "tpd", "fragments", "docker.yaml")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected user fragment override to be created on save at %s: %v", target, err)
	}
	s := string(data)
	if !strings.Contains(s, "extends: core/docker") {
		t.Errorf("fragment seed must extend the built-in fragment, got:\n%s", s)
	}
	if !strings.Contains(s, "Resolved fragment (reference)") {
		t.Errorf("fragment seed must include the resolved-reference banner, got:\n%s", s)
	}
	if !strings.Contains(s, "# saved by test") {
		t.Errorf("expected fragment override to carry the editor's write, got:\n%s", s)
	}
}

func TestProfileEditExistingUserFileUntouched(t *testing.T) {
	cfg := t.TempDir()
	target := filepath.Join(cfg, "tpd", "profiles", "shell.yaml")
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
	out, err := runTpdEnv(t, env, "edit", "shell")
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

func TestProfileShowResolvedFragmentRefused(t *testing.T) {
	out, _ := runTpd(t, "show", "--resolved", "ssh")
	if !strings.Contains(out, "fragment") {
		t.Errorf("expected --resolved on a fragment to be refused with a 'fragment' message, got:\n%s", out)
	}
}
