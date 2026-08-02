package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runTpodEnv runs the built binary with the given extra environment variables.
func runTpodEnv(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildTpod(t), args...)
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
	out, err := runTpod(t, "show", "shell")
	if err != nil {
		t.Fatalf("profile show shell: %v\n%s", err, out)
	}
	if !strings.Contains(out, "command:") {
		t.Errorf("expected raw shell profile to declare command, got:\n%s", out)
	}
}

func TestProfileShowResolved(t *testing.T) {
	out, err := runTpod(t, "show", "--resolved", "shell")
	if err != nil {
		t.Fatalf("profile show --resolved shell: %v\n%s", err, out)
	}
	if !strings.Contains(out, "image:") {
		t.Errorf("expected resolved shell to inherit image from mise, got:\n%s", out)
	}
}

func TestProfileShowNonexistent(t *testing.T) {
	out, _ := runTpod(t, "show", "nope")
	if !strings.Contains(out, "not found") {
		t.Errorf("expected 'not found' error for missing profile, got:\n%s", out)
	}
}

func TestProfileList(t *testing.T) {
	bin := buildTpod(t)
	out, err := exec.Command(bin, "list").CombinedOutput()
	if err != nil {
		t.Fatalf("profile list: %v\n%s", err, out)
	}
	s := string(out)
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
	out, err := runTpodEnv(t, env, "edit", "shell")
	if err != nil {
		t.Fatalf("edit shell (no save): %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(cfg, "tpod", "profiles", "shell.yaml")); !os.IsNotExist(err) {
		t.Errorf("expected no user profile after quitting without saving, stat err: %v", err)
	}
}

func TestProfileEditBuiltInSaveCreatesOverride(t *testing.T) {
	cfg := t.TempDir()
	env := []string{
		"XDG_CONFIG_HOME=" + cfg,
		"EDITOR=" + writeEditor(t, cfg, "editor", "#!/bin/sh\nprintf '\\n# saved by test\\n' >> \"$1\"\n"),
	}
	out, err := runTpodEnv(t, env, "edit", "shell")
	if err != nil {
		t.Fatalf("edit shell (save): %v\n%s", err, out)
	}
	target := filepath.Join(cfg, "tpod", "profiles", "shell.yaml")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected user override to be created on save: %v", err)
	}
	if !strings.Contains(string(data), "extends: mise") {
		t.Errorf("expected override to carry the built-in content, got:\n%s", data)
	}
	if !strings.Contains(string(data), "# saved by test") {
		t.Errorf("expected override to carry the editor's write, got:\n%s", data)
	}
}

func TestProfileEditBuiltInFragmentNoSaveRemovesSeed(t *testing.T) {
	cfg := t.TempDir()
	env := []string{
		"XDG_CONFIG_HOME=" + cfg,
		"EDITOR=" + writeEditor(t, cfg, "editor", "#!/bin/sh\nexit 0\n"),
	}
	out, err := runTpodEnv(t, env, "edit", "docker")
	if err != nil {
		t.Fatalf("edit docker fragment (no save): %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(cfg, "tpod", "fragments", "docker.yaml")); !os.IsNotExist(err) {
		t.Errorf("expected no user fragment after quitting without saving, stat err: %v", err)
	}
}

func TestProfileEditBuiltInFragmentSaveCreatesOverride(t *testing.T) {
	cfg := t.TempDir()
	env := []string{
		"XDG_CONFIG_HOME=" + cfg,
		"EDITOR=" + writeEditor(t, cfg, "editor", "#!/bin/sh\nprintf '\\n# saved by test\\n' >> \"$1\"\n"),
	}
	out, err := runTpodEnv(t, env, "edit", "docker")
	if err != nil {
		t.Fatalf("edit docker fragment (save): %v\n%s", err, out)
	}
	target := filepath.Join(cfg, "tpod", "fragments", "docker.yaml")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected user fragment override to be created on save at %s: %v", target, err)
	}
	if !strings.Contains(string(data), "# saved by test") {
		t.Errorf("expected fragment override to carry the editor's write, got:\n%s", data)
	}
}

func TestProfileEditExistingUserFileUntouched(t *testing.T) {
	cfg := t.TempDir()
	target := filepath.Join(cfg, "tpod", "profiles", "shell.yaml")
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
	out, err := runTpodEnv(t, env, "edit", "shell")
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

func TestProfileShowResolvedFragmentRefused(t *testing.T) {
	out, _ := runTpod(t, "show", "--resolved", "ssh")
	if !strings.Contains(out, "fragment") {
		t.Errorf("expected --resolved on a fragment to be refused with a 'fragment' message, got:\n%s", out)
	}
}
