package scaffold

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jgillich/tpd/internal/profile"
)

// TestMain routes the wizard at the stable testdata/catalog fixture instead of
// the live embedded catalog, so catalog renames never ripple into these tests.
func TestMain(m *testing.M) {
	loadCatalog = fixtureLoader
	os.Exit(m.Run())
}

func fixtureLoader(userDir string) (profile.Catalog, error) {
	fsys, ok := os.DirFS(filepath.Join("testdata", "catalog")).(fs.ReadFileFS)
	if !ok {
		return profile.Catalog{}, os.ErrInvalid
	}
	return profile.LoadCatalog(fsys, fsys, userDir)
}

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

func TestInitGeneratedFileOmitsResolvedReference(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"lang/javascript", "creds/gitconfig", "creds/ssh"},
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
	// ...and does not embed the resolved container view as a comment.
	if strings.Contains(content, "Resolved profile (reference)") {
		t.Errorf("generated file should not carry a resolved-reference banner, got:\n%s", content)
	}
	if strings.Contains(content, "image: debian:13-slim") {
		t.Errorf("generated file should not inline the resolved image, got:\n%s", content)
	}
}

func TestGenerateYAMLWithCachesAndMounts(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"lang/javascript", "lang/go", "creds/gitconfig", "creds/ssh"},
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
	if !strings.Contains(output, "- core/lang/javascript") {
		t.Errorf("missing javascript in extends list, got:\n%s", output)
	}
	if !strings.Contains(output, "- core/lang/go") {
		t.Errorf("missing go in extends list, got:\n%s", output)
	}
	if !strings.Contains(output, "- core/creds/gitconfig") {
		t.Errorf("missing gitconfig in extends list, got:\n%s", output)
	}
	if !strings.Contains(output, "- core/creds/ssh") {
		t.Errorf("missing ssh in extends list, got:\n%s", output)
	}

	// No command: [] (omitempty should handle this)
	if strings.Contains(output, "command:") {
		t.Errorf("should not emit command: in override, got:\n%s", output)
	}

	// Should NOT contain inlined cache/mount content (live-linked via extends)
	if strings.Contains(output, "npm: ~/.npm") {
		t.Errorf("should not inline npm cache, got:\n%s", output)
	}
	if strings.Contains(output, "~/.gitconfig:") {
		t.Errorf("should not inline gitconfig mount, got:\n%s", output)
	}
	if strings.Contains(output, "~/.ssh:") {
		t.Errorf("should not inline ssh mount, got:\n%s", output)
	}
}

func TestIntegrationResolveGeneratedProfile(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"lang/javascript", "lang/go", "creds/gitconfig", "creds/ssh"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}

	cat, err := fixtureLoader(dir)
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
		Extends:    []string{"lang/javascript"},
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
		Extends:    []string{"lang/javascript"},
		Force:      true,
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "opencode.yaml"))
	if !strings.Contains(string(data), "- core/lang/javascript") {
		t.Errorf("file should reference javascript fragment after force overwrite")
	}
}

func TestDryRunDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"lang/javascript"},
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
		Extends:    []string{"lang/javascript"},
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
		Extends:     []string{"lang/javascript"},
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
	if !strings.Contains(string(data), "- core/lang/javascript") {
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
	}, strings.NewReader("opencode\nlang/javascript\na\n"), &stdout, &stderr)
	if err != nil {
		t.Fatalf("aborting prompt should not error, got: %v", err)
	}
	if !strings.Contains(stdout.String(), "skipped") {
		t.Error("should print skipped")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "opencode.yaml"))
	if string(data) != "version: 1\n" {
		t.Error("file should be unchanged after aborting overwrite")
	}
}

func TestInteractiveOverwritePromptAccept(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "opencode.yaml"), []byte("version: 1\n"), 0o644)
	var stdout, stderr bytes.Buffer
	// No Profile/Fragments provided → wizard triggers → overwrite prompt shows.
	err := Run(context.Background(), Options{
		Interactive: true,
		ProfileDir:  dir,
	}, strings.NewReader("opencode\nlang/javascript\no\n"), &stdout, &stderr)
	if err != nil {
		t.Fatalf("accepting prompt should not error, got: %v", err)
	}
	if !strings.Contains(stdout.String(), "created") {
		t.Errorf("should print created, got: %s", stdout.String())
	}
	data, _ := os.ReadFile(filepath.Join(dir, "opencode.yaml"))
	if !strings.Contains(string(data), "- core/lang/javascript") {
		t.Error("file should reference javascript fragment from new generation")
	}
}

func TestInteractiveOverwritePromptMerge(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "opencode.yaml"), []byte("version: 1\n# my shell\ncommand: [zsh]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	// No Profile/Fragments provided → wizard triggers → overwrite prompt → merge.
	err := Run(context.Background(), Options{
		Interactive: true,
		ProfileDir:  dir,
	}, strings.NewReader("opencode\nlang/javascript\nm\n"), &stdout, &stderr)
	if err != nil {
		t.Fatalf("merging prompt should not error, got: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "opencode.yaml"))
	content := string(data)
	if !strings.Contains(content, "# my shell") || !strings.Contains(content, "zsh") {
		t.Errorf("merge wiped existing comments/command:\n%s", content)
	}
	if !strings.Contains(content, "- core/lang/javascript") {
		t.Errorf("merge did not add the picked fragment:\n%s", content)
	}
}

func TestInitMergeFlagMergesExisting(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "opencode.yaml"), []byte("version: 1\ncommand: [zsh]\nextends:\n  - core/mise\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"lang/javascript"},
		Merge:      true,
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "created") {
		t.Errorf("should print created, got: %s", stdout.String())
	}
	data, _ := os.ReadFile(filepath.Join(dir, "opencode.yaml"))
	content := string(data)
	if !strings.Contains(content, "zsh") {
		t.Errorf("merge dropped existing command:\n%s", content)
	}
	mi := strings.Index(content, "- core/mise")
	oi := strings.Index(content, "- core/opencode")
	ji := strings.Index(content, "- core/lang/javascript")
	if mi < 0 || oi < 0 || ji < 0 || !(mi < oi && oi < ji) {
		t.Errorf("extends should be core/mise, core/opencode, core/lang/javascript in order:\n%s", content)
	}
}

func TestInitMergeFlagNoExistingFile(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"lang/javascript"},
		Merge:      true,
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}
	data, _ := os.ReadFile(filepath.Join(dir, "opencode.yaml"))
	if !strings.Contains(string(data), "- core/opencode") {
		t.Errorf("merge with no existing file should generate normally:\n%s", data)
	}
}

func TestInitMergeForceMutuallyExclusive(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "opencode.yaml"), []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"lang/javascript"},
		Force:      true,
		Merge:      true,
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for --force with --merge")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention mutual exclusivity, got: %v", err)
	}
}

func TestExplicitArgsNoOverwritePrompt(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "opencode.yaml"), []byte("version: 1\n"), 0o644)
	var stdout, stderr bytes.Buffer
	// All args provided explicitly in a TTY-like test → no wizard → no prompt
	err := Run(context.Background(), Options{
		Name:        "opencode",
		Extends:     []string{"lang/javascript"},
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
	input := strings.NewReader("opencode\nlang/javascript\n")
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
		Extends:    []string{"lang/javascript", "yarn"},
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
	if !strings.Contains(output, "extends:") || !strings.Contains(output, "- core/bash") {
		t.Errorf("should contain extends list with core/bash, got:\n%s", output)
	}
	if strings.Contains(output, "caches:") {
		t.Errorf("should not contain caches with no fragments, got:\n%s", output)
	}
	if strings.Contains(output, "mounts:") {
		t.Errorf("should not contain mounts with no fragments, got:\n%s", output)
	}
}

func TestFragmentMergeProducesCorrectResult(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"lang/javascript", "creds/ssh"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}

	cat, _ := fixtureLoader(dir)
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
		Extends:    []string{"lang/javascript", "lang/go"},
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
	// Should contain extends list, not inlined caches/tools
	if !strings.Contains(content, "extends:") {
		t.Error("generated file should contain extends:")
	}
	if !strings.Contains(content, "core/lang/javascript") {
		t.Error("generated file should reference javascript fragment")
	}
	if !strings.Contains(content, "core/lang/go") {
		t.Error("generated file should reference go fragment")
	}
	// Should NOT contain inlined cache paths from npm fragment
	if strings.Contains(content, "~/.npm") {
		t.Error("generated file should not inline ~/.npm cache (should be live-linked via extends)")
	}
	// Should NOT contain inlined tool entries from fragments
	if strings.Contains(content, "node: latest") {
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
		Extends:    []string{"lang/javascript"},
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
	input := strings.NewReader("opencode\nlang/javascript,creds/gitconfig\n")
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
	if !strings.Contains(output, "- core/lang/javascript") {
		t.Errorf("missing javascript in extends list, got:\n%s", output)
	}
	if !strings.Contains(output, "- core/creds/gitconfig") {
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
		Extends:    []string{"lang/javascript"},
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
		Extends:    []string{"lang/javascript"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for broken sibling profile")
	}
	if !strings.Contains(err.Error(), "broken.yaml") {
		t.Errorf("error should reference the broken file, got: %v", err)
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
		Extends:    []string{"creds/gitconfig", "creds/ssh", "creds/netrc"},
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

func TestRedirectedStdoutUsesTextPrompts(t *testing.T) {
	dir := t.TempDir()
	input := strings.NewReader("opencode\nlang/javascript\n")
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Interactive: true,
		ProfileDir:  dir,
	}, input, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}
	if strings.Contains(stdout.String(), "\x1b[") {
		t.Error("redirected stdout must not receive ANSI escape codes (huh UI launched with non-TTY stdout)")
	}
	if !strings.Contains(stderr.String(), "Profile:") {
		t.Errorf("expected text prompt on stderr, got: %q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode.yaml")); err != nil {
		t.Errorf("profile should be generated via the text fallback: %v", err)
	}
}

func TestOverwriteActionMissingTargetReplaces(t *testing.T) {
	action, err := overwriteAction(false, Options{}, false, filepath.Join(t.TempDir(), "nope.yaml"), strings.NewReader(""), &bytes.Buffer{}, bufio.NewReader(strings.NewReader("")))
	if err != nil {
		t.Fatalf("overwriteAction: %v", err)
	}
	if action != writeReplace {
		t.Errorf("action = %v, want writeReplace", action)
	}
}

func TestOverwriteActionPermissionErrorSurfaced(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based tests are meaningless as root")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Skipf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	_, err := overwriteAction(false, Options{}, false, filepath.Join(dir, "opencode.yaml"), strings.NewReader(""), &bytes.Buffer{}, bufio.NewReader(strings.NewReader("")))
	if err == nil {
		t.Fatal("expected permission error from overwriteAction")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error should surface the permission failure, got: %v", err)
	}
}

func TestWritePermissionFailureSurfaced(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based tests are meaningless as root")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skipf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"lang/javascript"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected an error writing to a read-only profile dir")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error should surface the permission failure, got: %v", err)
	}
}

func TestDryRunExistingTargetDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	original := "version: 1\n# keep me\ncommand: [zsh]\n"
	if err := os.WriteFile(filepath.Join(dir, "opencode.yaml"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"lang/javascript"},
		DryRun:     true,
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "opencode.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("dry-run must not modify an existing target, got:\n%s", data)
	}
	if !strings.Contains(stdout.String(), "dry-run") {
		t.Errorf("expected dry-run output, got: %s", stdout.String())
	}
}

func TestForceMergeMutuallyExclusiveWithoutTarget(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"lang/javascript"},
		Force:      true,
		Merge:      true,
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for --force with --merge on a missing target")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention mutual exclusivity, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode.yaml")); !os.IsNotExist(err) {
		t.Error("no file should be written when flags conflict")
	}
}

func TestForceMergeMutuallyExclusiveBeforeDryRun(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"lang/javascript"},
		Force:      true,
		Merge:      true,
		DryRun:     true,
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("dry-run must not bypass mutual exclusion, got: %v", err)
	}
}

func TestResolveGeneratedProfileDoesNotMutateCatalog(t *testing.T) {
	cat, err := fixtureLoader(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	before := cat.Names()
	content := "version: 1\nextends:\n  - core/lang/javascript\ncommand: [bash]\n"
	_, _ = resolveGeneratedProfile(content, "opencode", cat)
	if !reflect.DeepEqual(before, cat.Names()) {
		t.Errorf("catalog mutated: before %v, after %v", before, cat.Names())
	}
	if _, ok := cat.Get("opencode"); ok {
		t.Error("generated profile leaked into caller's catalog")
	}
}

func TestIsIncompleteProfileErr(t *testing.T) {
	for _, msg := range []string{"missing required field: image", "missing required field: command"} {
		if !isIncompleteProfileErr(profile.ProfileError{Message: msg}) {
			t.Errorf("isIncompleteProfileErr(%q) = false, want true", msg)
		}
	}
	for _, msg := range []string{"image: invalid image reference", "unsupported version: 2 (want 1)", "mount /x: service requires socket"} {
		if isIncompleteProfileErr(profile.ProfileError{Message: msg}) {
			t.Errorf("isIncompleteProfileErr(%q) = true, want false", msg)
		}
	}
	if isIncompleteProfileErr(fmt.Errorf("missing required field: image")) {
		t.Error("wrapped non-ProfileError should not count as incomplete")
	}
}

func TestValidateGeneratedIncompleteImageWarning(t *testing.T) {
	cat, err := fixtureLoader(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	content := "version: 1\nextends:\n  - core/lang/javascript\ncommand: [bash]\n"
	if err := validateGenerated(content, "opencode", filepath.Join("profiles", "opencode.yaml"), cat, &stderr); err != nil {
		t.Fatalf("validateGenerated should warn, not error: %v", err)
	}
	if !strings.Contains(stderr.String(), "not runnable") {
		t.Errorf("expected not-runnable warning, got: %q", stderr.String())
	}
}

func TestValidateGeneratedRejectsRealErrors(t *testing.T) {
	cat, err := fixtureLoader(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	content := "version: 1\nextends:\n  - core/lang/javascript\ncommand: [bash]\nimage: \"bad image\"\n"
	err = validateGenerated(content, "opencode", filepath.Join("profiles", "opencode.yaml"), cat, &stderr)
	if err == nil {
		t.Fatal("expected a validation error for a malformed image reference")
	}
	if !strings.Contains(err.Error(), "invalid image reference") {
		t.Errorf("error should cite the image problem, got: %v", err)
	}
}

func TestMergeRejectsInvalidMergedContent(t *testing.T) {
	dir := t.TempDir()
	// Schema-valid but semantically invalid: a service mount without a socket.
	// It loads as a catalog entry but fails resolution, so the merged-content
	// validation (not the catalog load) must surface it.
	if err := os.WriteFile(filepath.Join(dir, "opencode.yaml"), []byte("version: 1\nmounts:\n  /sock:\n    service: dbus\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "opencode",
		Extends:    []string{"lang/javascript"},
		Merge:      true,
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error when merged content fails validation")
	}
	if !strings.Contains(err.Error(), "generated config failed validation") {
		t.Errorf("error should name the validation failure, got: %v", err)
	}
}

func TestWriteFileAtomicLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "p.yaml")
	if err := writeFileAtomic(target, []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "p.yaml" {
		t.Errorf("expected only p.yaml in the dir, got: %v", entries)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("target mode = %o, want 0644", fi.Mode().Perm())
	}
}

func TestInitNamespacedProfileWritesSubfolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tpd", "profiles")
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "acme/go",
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}
	target := filepath.Join(dir, "acme", "go.yaml")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected generated file at %s: %v", target, err)
	}
	if !strings.Contains(string(data), "extends:") {
		t.Errorf("generated file should extend a base, got:\n%s", string(data))
	}
}
