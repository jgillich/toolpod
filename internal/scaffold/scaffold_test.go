package scaffold

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgillich/tpd/internal/profile"
)

func TestFragmentsAreValid(t *testing.T) {
	for name, p := range Fragments() {
		if err := validateFragment(name, p); err != nil {
			t.Errorf("fragment %q: %v", name, err)
		}
	}
}

func TestValidateFragmentRejectsIdentityFields(t *testing.T) {
	for name := range Fragments() {
		t.Run(name, func(t *testing.T) {
			p := Fragments()[name]
			// Mutate a copy to set a forbidden field and verify rejection.
			bad := p
			bad.Image = "evil:latest"
			if err := validateFragment(name, bad); err == nil {
				t.Errorf("expected error for fragment %q with image set", name)
			}
		})
	}
}

func TestInitEmbedsResolvedProfileReference(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"javascript", "gitconfig", "ssh"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "opencode.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	// The override stays live-linked to extends...
	if !strings.Contains(content, "extends:") {
		t.Errorf("override should keep the extends list, got:\n%s", content)
	}
	// ...and the resolved profile is embedded as a reference comment.
	if !strings.Contains(content, "Resolved profile (reference)") {
		t.Errorf("file should carry a resolved-reference banner, got:\n%s", content)
	}
	if !strings.Contains(content, "# image: debian:13-slim") {
		t.Errorf("resolved reference should inline the inherited image, got:\n%s", content)
	}
	if !strings.Contains(content, "# command:") {
		t.Errorf("resolved reference should inline the inherited command, got:\n%s", content)
	}
	if !strings.Contains(content, "# mounts:") {
		t.Errorf("resolved reference should inline the merged mounts, got:\n%s", content)
	}
}

func TestGenerateYAMLWithCachesAndMounts(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"javascript", "go", "gitconfig", "ssh"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}

	data, err := os.ReadFile(filepath.Join(dir, "opencode.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)

	// extends list references profile + fragments
	if !strings.Contains(output, "extends:") || !strings.Contains(output, "- core/opencode") {
		t.Errorf("missing extends list with core/opencode, got:\n%s", output)
	}
	if !strings.Contains(output, "- core/javascript") {
		t.Errorf("missing javascript in extends list, got:\n%s", output)
	}
	if !strings.Contains(output, "- core/go") {
		t.Errorf("missing go in extends list, got:\n%s", output)
	}
	if !strings.Contains(output, "- core/gitconfig") {
		t.Errorf("missing gitconfig in extends list, got:\n%s", output)
	}
	if !strings.Contains(output, "- core/ssh") {
		t.Errorf("missing ssh in extends list, got:\n%s", output)
	}

	override := overrideOnly(t, output)

	// No command: [] (omitempty should handle this)
	if strings.Contains(override, "command:") {
		t.Errorf("should not emit command: in override, got:\n%s", output)
	}

	// Should NOT contain inlined cache/mount content (live-linked via extends)
	if strings.Contains(override, "npm: ~/.npm") {
		t.Errorf("should not inline npm cache, got:\n%s", output)
	}
	if strings.Contains(override, "~/.gitconfig:") {
		t.Errorf("should not inline gitconfig mount, got:\n%s", output)
	}
	if strings.Contains(override, "~/.ssh:") {
		t.Errorf("should not inline ssh mount, got:\n%s", output)
	}
}

func TestIntegrationResolveGeneratedProfile(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"javascript", "go", "gitconfig", "ssh"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}

	cat, err := profile.LoadProfiles(dir)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	cfg, err := profile.ResolveProfile(cat, "opencode")
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if got := cfg.Caches["npm"]; len(got) != 1 || got[0] != "~/.npm" {
		t.Errorf("Caches[npm] = %v, want [~/.npm]", got)
	}
	if got := cfg.Caches["go"]; len(got) != 1 || got[0] != "~/go" {
		t.Errorf("Caches[go] = %v, want [~/go]", got)
	}
	if cfg.Mounts["~/.gitconfig"].Source != "~/.gitconfig" {
		t.Errorf("gitconfig source = %q", cfg.Mounts["~/.gitconfig"].Source)
	}
	if !cfg.Mounts["~/.gitconfig"].ReadOnly {
		t.Error("gitconfig should be read-only")
	}
	if !cfg.Mounts["~/.ssh"].ReadOnly {
		t.Error("ssh should be read-only")
	}
	if cfg.Mounts["~/.ssh/known_hosts"].ReadOnly {
		t.Error("known_hosts should be read-write")
	}
	if cfg.Image != "debian:13-slim" {
		t.Errorf("Image = %q, want inherited from built-in", cfg.Image)
	}
	if len(cfg.Command) != 1 || cfg.Command[0] != "opencode" {
		t.Errorf("Command = %v, want [opencode] inherited from built-in", cfg.Command)
	}
	// Fragments install mise tools alongside their caches
	if cfg.Tools["node"].Version != "latest" {
		t.Errorf("Tools[node].Version = %q, want latest (from javascript fragment)", cfg.Tools["node"].Version)
	}
	if cfg.Tools["go"].Version != "latest" {
		t.Errorf("Tools[go].Version = %q, want latest (from go fragment)", cfg.Tools["go"].Version)
	}
	if cfg.Tools["opencode"].Version != "latest" {
		t.Errorf("Tools[opencode].Version = %q, want latest (inherited from built-in)", cfg.Tools["opencode"].Version)
	}
}

func TestSkipExistingWithoutForce(t *testing.T) {
	dir := t.TempDir()
	// Pre-create the file
	os.WriteFile(filepath.Join(dir, "opencode.yaml"), []byte("version: 1\n"), 0o644)

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"javascript"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for existing file without --force")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention 'already exists', got: %v", err)
	}
}

func TestForceOverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "opencode.yaml"), []byte("version: 1\n"), 0o644)

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"javascript"},
		Force:      true,
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "opencode.yaml"))
	if !strings.Contains(string(data), "- core/javascript") {
		t.Errorf("file should reference javascript fragment after force overwrite")
	}
}

func TestDryRunDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"javascript"},
		DryRun:     true,
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode.yaml")); !os.IsNotExist(err) {
		t.Error("dry-run should not write a file")
	}
	output := stdout.String()
	if !strings.Contains(output, "dry-run") {
		t.Errorf("dry-run output should mention 'dry-run', got: %s", output)
	}
	if strings.Contains(output, "created") {
		t.Error("dry-run should not say 'created'")
	}
}

func TestDryRunWithForceDoesNotPrompt(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "opencode.yaml"), []byte("version: 1\n"), 0o644)

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"javascript"},
		DryRun:     true,
		Force:      true,
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode.yaml")); err != nil {
		t.Error("dry-run+force should not modify existing file")
	}
}

func TestForceInteractiveNoPrompt(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "opencode.yaml"), []byte("version: 1\n"), 0o644)
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:        "opencode",
		Extends:     []string{"javascript"},
		Force:       true,
		Interactive: true,
		ProfileDir:  dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(stdout.String(), "skipped") {
		t.Error("--force should not prompt, got skipped")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "opencode.yaml"))
	if !strings.Contains(string(data), "- core/javascript") {
		t.Error("file should reference javascript fragment after force overwrite")
	}
}

func TestInteractiveOverwritePromptDecline(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "opencode.yaml"), []byte("version: 1\n"), 0o644)
	var stdout, stderr bytes.Buffer
	// No Profile/Fragments provided → wizard triggers → overwrite prompt shows
	err := Run(context.Background(), Options{
		Interactive: true,
		ProfileDir:  dir,
	}, strings.NewReader("opencode\njavascript\nn\n"), &stdout, &stderr)
	if err != nil {
		t.Fatalf("declining prompt should not error, got: %v", err)
	}
	if !strings.Contains(stdout.String(), "skipped") {
		t.Error("should print skipped")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "opencode.yaml"))
	if string(data) != "version: 1\n" {
		t.Error("file should be unchanged after declining overwrite")
	}
}

func TestInteractiveOverwritePromptAccept(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "opencode.yaml"), []byte("version: 1\n"), 0o644)
	var stdout, stderr bytes.Buffer
	// No Profile/Fragments provided → wizard triggers → overwrite prompt shows
	err := Run(context.Background(), Options{
		Interactive: true,
		ProfileDir:  dir,
	}, strings.NewReader("opencode\njavascript\nn\ny\n"), &stdout, &stderr)
	if err != nil {
		t.Fatalf("accepting prompt should not error, got: %v", err)
	}
	if !strings.Contains(stdout.String(), "created") {
		t.Errorf("should print created, got: %s", stdout.String())
	}
	data, _ := os.ReadFile(filepath.Join(dir, "opencode.yaml"))
	if !strings.Contains(string(data), "- core/javascript") {
		t.Error("file should reference javascript fragment from new generation")
	}
}

func TestExplicitArgsNoOverwritePrompt(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "opencode.yaml"), []byte("version: 1\n"), 0o644)
	var stdout, stderr bytes.Buffer
	// All args provided explicitly in a TTY-like test → no wizard → no prompt
	err := Run(context.Background(), Options{
		Name:        "opencode",
		Extends:     []string{"javascript"},
		Interactive: true,
		ProfileDir:  dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for existing file with explicit args and no --force")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention 'already exists', got: %v", err)
	}
}

func TestDryRunInteractivePrompts(t *testing.T) {
	dir := t.TempDir()
	input := strings.NewReader("opencode\njavascript\n")
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		DryRun:      true,
		Interactive: true,
		ProfileDir:  dir,
	}, input, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}
	// Prompts go to stderr
	if !strings.Contains(stderr.String(), "Profile:") {
		t.Error("should prompt for profile on stderr")
	}
	if !strings.Contains(stderr.String(), "Fragments") {
		t.Errorf("should prompt for fragments on stderr")
	}
	// YAML goes to stdout
	if !strings.Contains(stdout.String(), "- core/opencode") {
		t.Error("stdout should contain generated YAML")
	}
	// No file written
	if _, err := os.Stat(filepath.Join(dir, "opencode.yaml")); !os.IsNotExist(err) {
		t.Error("dry-run should not write a file")
	}
}

func TestUnknownExtendsTargetRejected(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"javascript", "yarn"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for unknown extends target")
	}
	if !strings.Contains(err.Error(), "unknown extends target: yarn") {
		t.Errorf("error should mention 'unknown extends target: yarn', got: %v", err)
	}
	// File should not be written
	if _, err := os.Stat(filepath.Join(dir, "opencode.yaml")); !os.IsNotExist(err) {
		t.Error("file should not be written when extends target is unknown")
	}
}

func TestNonInteractiveMissingProfile(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for missing profile in non-interactive mode")
	}
	if !strings.Contains(err.Error(), "profile name is required") {
		t.Errorf("error should mention 'profile name is required', got: %v", err)
	}
}

func TestNoFragmentsProducesJustExtends(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "bash",
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "bash.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	override := overrideOnly(t, output)
	if !strings.Contains(output, "extends:") || !strings.Contains(output, "- core/bash") {
		t.Errorf("should contain extends list with core/bash, got:\n%s", output)
	}
	if strings.Contains(override, "caches:") {
		t.Errorf("should not contain caches with no fragments, got:\n%s", output)
	}
	if strings.Contains(override, "mounts:") {
		t.Errorf("should not contain mounts with no fragments, got:\n%s", output)
	}
}

func TestFragmentMergeProducesCorrectResult(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"javascript", "ssh"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}

	cat, _ := profile.LoadProfiles(dir)
	cfg, _ := profile.ResolveProfile(cat, "opencode")
	if got := cfg.Caches["npm"]; len(got) != 1 || got[0] != "~/.npm" {
		t.Errorf("Caches[npm] = %v", got)
	}
	if _, ok := cfg.Mounts["~/.ssh"]; !ok {
		t.Error("missing ~/.ssh mount")
	}
	if _, ok := cfg.Mounts["~/.ssh/known_hosts"]; !ok {
		t.Error("missing ~/.ssh/known_hosts mount")
	}
}

func TestGenerateWritesExtendsList(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"javascript", "go"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "opencode.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	override := overrideOnly(t, content)
	// Should contain extends list, not inlined caches/tools
	if !strings.Contains(content, "extends:") {
		t.Error("generated file should contain extends:")
	}
	if !strings.Contains(content, "core/javascript") {
		t.Error("generated file should reference javascript fragment")
	}
	if !strings.Contains(content, "core/go") {
		t.Error("generated file should reference go fragment")
	}
	// Should NOT contain inlined cache paths from npm fragment
	if strings.Contains(override, "~/.npm") {
		t.Error("generated file should not inline ~/.npm cache (should be live-linked via extends)")
	}
	// Should NOT contain inlined tool entries from fragments
	if strings.Contains(override, "node: latest") {
		t.Error("generated file should not inline node tool (should be live-linked via extends)")
	}
}

func TestPromptsGoToStderr(t *testing.T) {
	dir := t.TempDir()
	// Non-interactive mode (Interactive not set, defaults to false) should
	// not write prompts. Uses "javascript" fragment which has no file mounts, so
	// no stderr output either.
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"javascript"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}
	if stderr.String() != "" {
		t.Errorf("non-interactive mode should not write to stderr, got: %q", stderr.String())
	}
}

func TestInteractiveWizard(t *testing.T) {
	dir := t.TempDir()
	// Simulated stdin: first line = profile name, second line = fragment names.
	input := strings.NewReader("opencode\njavascript,gitconfig\n")
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Interactive: true,
		ProfileDir:  dir,
	}, input, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "opencode.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	if !strings.Contains(output, "extends:") || !strings.Contains(output, "- core/opencode") {
		t.Errorf("missing extends list with core/opencode, got:\n%s", output)
	}
	if !strings.Contains(output, "- core/javascript") {
		t.Errorf("missing javascript in extends list, got:\n%s", output)
	}
	if !strings.Contains(output, "- core/gitconfig") {
		t.Errorf("missing gitconfig in extends list, got:\n%s", output)
	}
	// Prompts should go to stderr, not stdout
	if strings.Contains(stdout.String(), "Available built-in profiles") {
		t.Error("profile prompt should not go to stdout")
	}
}

func TestDirectoryCreatedIfAbsent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "profiles")
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"javascript"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("directory should exist: %v", err)
	}
	if !info.IsDir() {
		t.Error("should be a directory")
	}
}

func TestBrokenSiblingBlocksInit(t *testing.T) {
	dir := t.TempDir()
	// A broken sibling YAML makes the catalog load fail. Init hard-fails
	// rather than silently ignoring the malformed file.
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("key: \"[unterminated\n"), 0o644); err != nil {
		t.Fatalf("WriteFile broken.yaml: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"javascript"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for broken sibling profile")
	}
	if !strings.Contains(err.Error(), "broken.yaml") {
		t.Errorf("error should reference the broken file, got: %v", err)
	}
}

func TestScaffoldPrintsAdvisoryForSensitiveFragments(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"docker-host"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "note: docker-host grants:") {
		t.Errorf("expected advisory note on stderr, got: %q", stderr.String())
	}

	quietDir := t.TempDir()
	var quietOut, quietErr bytes.Buffer
	err = Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"javascript"},
		ProfileDir: quietDir,
	}, strings.NewReader(""), &quietOut, &quietErr)
	if err != nil {
		t.Fatalf("Run (javascript): %v", err)
	}
	if strings.Contains(quietErr.String(), "grants:") {
		t.Errorf("non-sensitive fragments should not print an advisory, got: %q", quietErr.String())
	}
}

func TestFragmentFileExistenceWarning(t *testing.T) {
	// Point HOME at an empty temp dir so mount fragment sources don't exist.
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"gitconfig", "ssh", "netrc"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// All fragment mounts are optional, so missing sources should not produce warnings.
	if strings.Contains(stderr.String(), "does not exist") {
		t.Errorf("stderr should not contain file-existence warnings for optional mounts, got: %q", stderr.String())
	}
}

// overrideOnly returns the active YAML before the embedded resolved-reference
// block, for assertions that target only the generated override.
func overrideOnly(t *testing.T, content string) string {
	t.Helper()
	if i := strings.Index(content, "# ─────"); i >= 0 {
		return content[:i]
	}
	return content
}
