package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jgillich/tpd/internal/profile"
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
	out, err := runTpd(t, "show", "bash")
	if err != nil {
		t.Fatalf("profile show bash: %v\n%s", err, out)
	}
	if !strings.Contains(out, "command:") {
		t.Errorf("expected raw bash profile to declare command, got:\n%s", out)
	}
}

func TestProfileShowResolved(t *testing.T) {
	out, err := runTpd(t, "show", "--resolved", "bash")
	if err != nil {
		t.Fatalf("profile show --resolved bash: %v\n%s", err, out)
	}
	if !strings.Contains(out, "image:") {
		t.Errorf("expected resolved bash to inherit image from mise, got:\n%s", out)
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
	// Task 5 restores bare display names (no core/ keys) with core source.
	if strings.Contains(s, "core/bash") {
		t.Errorf("expected bare display names, got core/-qualified:\n%s", s)
	}
	if !strings.Contains(s, "bash") {
		t.Errorf("expected profile list to contain 'bash', got:\n%s", s)
	}
	if !strings.Contains(s, "core") {
		t.Errorf("expected profile list to label core entries, got:\n%s", s)
	}
	if !strings.Contains(s, "fragment") {
		t.Errorf("expected profile list to label fragments, got:\n%s", s)
	}
	// Regression: built-in fragments must be marked as core (not just
	// "fragment" with no origin).
	dockerMarked := false
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "docker-host") {
			if !strings.Contains(line, "fragment") || !strings.Contains(line, "core") {
				t.Errorf("docker-host fragment row should be marked 'fragment' 'core', got: %q", line)
			}
			dockerMarked = true
		}
	}
	if !dockerMarked {
		t.Errorf("expected profile list to contain the docker-host fragment")
	}
}

func TestProfileListShowsDisplayNameAndSource(t *testing.T) {
	cfg := t.TempDir()
	userProfile := filepath.Join(cfg, "tpd", "profiles", "bash.yaml")
	if err := os.MkdirAll(filepath.Dir(userProfile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userProfile, []byte("version: 1\ncommand: [\"bash\", \"-l\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runTpdEnv(t, []string{"XDG_CONFIG_HOME=" + cfg}, "list")
	if err != nil {
		t.Fatalf("profile list: %v\n%s", err, out)
	}
	s := string(out)
	if strings.Contains(s, "core/bash") {
		t.Errorf("expected bare display names, got core/-qualified:\n%s", s)
	}
	bashRow, dockerRow := false, false
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "bash":
			if !strings.Contains(line, "profile") || !strings.Contains(line, "user shadow") {
				t.Errorf("bash row should be 'profile' 'user shadow', got: %q", line)
			}
			bashRow = true
		case "docker-host":
			if !strings.Contains(line, "fragment") || !strings.Contains(line, "core") {
				t.Errorf("docker-host row should be 'fragment' 'core', got: %q", line)
			}
			dockerRow = true
		}
	}
	if !bashRow {
		t.Errorf("expected profile list to contain the bash row")
	}
	if !dockerRow {
		t.Errorf("expected profile list to contain the docker-host fragment row")
	}
}

func TestProfileEditBuiltInNoSaveRemovesSeed(t *testing.T) {
	cfg := t.TempDir()
	env := []string{
		"XDG_CONFIG_HOME=" + cfg,
		"EDITOR=" + writeEditor(t, cfg, "editor", "#!/bin/sh\nexit 0\n"),
	}
	out, err := runTpdEnv(t, env, "edit", "bash")
	if err != nil {
		t.Fatalf("edit bash (no save): %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(cfg, "tpd", "profiles", "bash.yaml")); !os.IsNotExist(err) {
		t.Errorf("expected no user profile after quitting without saving, stat err: %v", err)
	}
}

func TestProfileEditBuiltInSaveCreatesOverride(t *testing.T) {
	cfg := t.TempDir()
	env := []string{
		"XDG_CONFIG_HOME=" + cfg,
		"EDITOR=" + writeEditor(t, cfg, "editor", "#!/bin/sh\nprintf '\\n# saved by test\\n' >> \"$1\"\n"),
	}
	out, err := runTpdEnv(t, env, "edit", "bash")
	if err != nil {
		t.Fatalf("edit bash (save): %v\n%s", err, out)
	}
	target := filepath.Join(cfg, "tpd", "profiles", "bash.yaml")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected user override to be created on save: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "extends: core/bash") {
		t.Errorf("seed must extend the built-in itself, got:\n%s", s)
	}
	if !strings.Contains(s, `shadows the built-in "core/bash"`) {
		t.Errorf("seed must explain the shadow/merge, got:\n%s", s)
	}
	if !strings.Contains(s, "Resolved profile (reference)") {
		t.Errorf("seed must carry a resolved-reference banner, got:\n%s", s)
	}
	if !strings.Contains(s, "snapshot from when this file was created") || !strings.Contains(s, `tpd show --resolved core/bash`) {
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
	out, err := runTpdEnv(t, env, "edit", "docker-host")
	if err != nil {
		t.Fatalf("edit docker-host fragment (no save): %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(cfg, "tpd", "fragments", "docker-host.yaml")); !os.IsNotExist(err) {
		t.Errorf("expected no user fragment after quitting without saving, stat err: %v", err)
	}
}

func TestProfileEditBuiltInFragmentSaveCreatesOverride(t *testing.T) {
	cfg := t.TempDir()
	env := []string{
		"XDG_CONFIG_HOME=" + cfg,
		"EDITOR=" + writeEditor(t, cfg, "editor", "#!/bin/sh\nprintf '\\n# saved by test\\n' >> \"$1\"\n"),
	}
	out, err := runTpdEnv(t, env, "edit", "docker-host")
	if err != nil {
		t.Fatalf("edit docker-host fragment (save): %v\n%s", err, out)
	}
	target := filepath.Join(cfg, "tpd", "fragments", "docker-host.yaml")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected user fragment override to be created on save at %s: %v", target, err)
	}
	s := string(data)
	if !strings.Contains(s, "extends: core/docker-host") {
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
	out, err := runTpd(t, "show", "--resolved", "ssh")
	if err != nil {
		t.Fatalf("show --resolved ssh: %v\n%s", err, out)
	}
	if strings.Contains(out, "is a fragment") {
		t.Errorf("expected fragment to resolve, got refusal:\n%s", out)
	}
	if !strings.Contains(out, "openssh-client") {
		t.Errorf("expected resolved fragment output to contain ssh content, got:\n%s", out)
	}
}

func TestProfileEditCoreMiseSeedsUserMiseYaml(t *testing.T) {
	cfg := t.TempDir()
	env := []string{
		"XDG_CONFIG_HOME=" + cfg,
		"EDITOR=" + writeEditor(t, cfg, "editor", "#!/bin/sh\nprintf '\\n# saved by test\\n' >> \"$1\"\n"),
	}
	out, err := runTpdEnv(t, env, "edit", "core/mise")
	if err != nil {
		t.Fatalf("edit core/mise (save): %v\n%s", err, out)
	}
	// The namespace prefix is stripped: the seed lands in profiles/mise.yaml.
	target := filepath.Join(cfg, "tpd", "profiles", "mise.yaml")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected core/mise edit to seed user mise.yaml: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "extends: core/mise") {
		t.Errorf("seed must extend the built-in core/mise, got:\n%s", s)
	}
	if !strings.Contains(s, "# image: debian:13-slim") {
		t.Errorf("seed must carry the resolved mise profile commented out, got:\n%s", s)
	}
	if !strings.Contains(s, "# saved by test") {
		t.Errorf("expected override to carry the editor's write, got:\n%s", s)
	}
}

func TestResolveQualifiedCoreMise(t *testing.T) {
	cat, err := profile.LoadProfiles("")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := profile.ResolveProfile(cat, "core/mise")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Image != "debian:13-slim" {
		t.Errorf("ResolveProfile(core/mise).Image = %q, want debian:13-slim", cfg.Image)
	}
}
