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

func TestNewProfileDefaultBaseMise(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "myagent",
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "myagent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "- core/mise") {
		t.Errorf("generated file should extend core/mise, got:\n%s", string(data))
	}
	// mise provides a command, so the generated profile inherits it and needs
	// no explicit bash default.
	if strings.Contains(string(data), "- bash") {
		t.Errorf("generated file should not default to bash when mise provides a command, got:\n%s", string(data))
	}
	if strings.Contains(stderr.String(), "not runnable") {
		t.Errorf("profile inheriting mise's command should not be flagged not runnable, got stderr: %q", stderr.String())
	}
}

func TestNewProfileBaseWithoutCommandDefaultsBash(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nocmd.yaml"), []byte("version: 1\nimage: img:1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "myagent",
		Extends:    []string{"nocmd"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "myagent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// A base with no command gets a bash default so the profile runs.
	if !strings.Contains(string(data), "- bash") {
		t.Errorf("generated file should default the command to bash, got:\n%s", string(data))
	}
}

func TestNewProfileExtendsFlag(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "myagent",
		Extends:    []string{"opencode", "services/podman", "lang/ruby"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "myagent.yaml"))
	content := string(data)
	for _, want := range []string{"- core/opencode", "- core/services/podman", "- core/lang/ruby"} {
		if !strings.Contains(content, want) {
			t.Errorf("generated file missing %s, got:\n%s", want, content)
		}
	}
	cat, err := fixtureLoader(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := profile.ResolveProfile(cat, "myagent")
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if len(cfg.Command) != 1 || cfg.Command[0] != "opencode" {
		t.Errorf("Command = %v, want inherited [opencode]", cfg.Command)
	}
}

func TestNewProfileExtendsDedupsAfterCanonicalization(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "myagent",
		Extends:    []string{"mise", "core/mise"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "myagent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(data), "- core/mise"); count != 1 {
		t.Errorf("generated file should list core/mise once after dedup, got %d occurrences:\n%s", count, string(data))
	}
}

func TestNewProfileExtendsUserProfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "base.yaml"), []byte("version: 1\nextends: opencode\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "derived",
		Extends:    []string{"base"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "derived.yaml"))
	if !strings.Contains(string(data), "- base") {
		t.Errorf("generated file should extend base, got:\n%s", string(data))
	}
	cat, err := fixtureLoader(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := profile.ResolveProfile(cat, "derived")
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if len(cfg.Command) != 1 || cfg.Command[0] != "opencode" {
		t.Errorf("Command = %v, want inherited [opencode] via user base", cfg.Command)
	}
}

func TestNewProfileNameCollidesWithFragment(t *testing.T) {
	// No built-in fragment keeps a single-segment display name after the
	// catalog restructure (all live under lang/services/creds/...), so the
	// single-segment case collides with a user fragment, while a hierarchical
	// name collides with a built-in (core/lang/javascript).
	dir := t.TempDir()
	fragDir := filepath.Join(filepath.Dir(dir), "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "myfrag.yaml"), []byte("version: 1\ntools:\n  x: \"1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ profileName string }{
		"single-segment user fragment":   {profileName: "myfrag"},
		"hierarchical built-in fragment": {profileName: "lang/javascript"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := Run(context.Background(), Options{
				Name:       tc.profileName,
				ProfileDir: dir,
			}, strings.NewReader(""), &stdout, &stderr)
			if err == nil {
				t.Fatal("expected error for name colliding with fragment")
			}
			if !strings.Contains(err.Error(), "fragment") {
				t.Errorf("error should mention fragment, got: %v", err)
			}
		})
	}
}

func TestNewProfileReservedNameRejected(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "config",
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for reserved name")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error should mention reserved, got: %v", err)
	}
}

func TestNewProfileUnsafeNameRejected(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "../evil",
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for unsafe name")
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.yaml")); !os.IsNotExist(err) {
		t.Error("should not write a file for unsafe name")
	}
}

func TestUnknownExtendsRejected(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "myagent",
		Extends:    []string{"doesnotexist"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for unknown extends target")
	}
	if !strings.Contains(err.Error(), "doesnotexist") {
		t.Errorf("error should mention unknown target, got: %v", err)
	}
}

func TestWizardNewProfileFlow(t *testing.T) {
	dir := t.TempDir()
	// "New" → name "foo" → bases "mise,opencode" → fragments "lang/javascript,creds/gitconfig"
	// (the fragment picker appends to the same extends list).
	input := strings.NewReader("New\nfoo\nmise,opencode\nlang/javascript,creds/gitconfig\n")
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Interactive: true,
		ProfileDir:  dir,
	}, input, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "foo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{"- core/mise", "- core/opencode", "- core/lang/javascript", "- core/creds/gitconfig"} {
		if !strings.Contains(content, want) {
			t.Errorf("generated file missing %s, got:\n%s", want, content)
		}
	}
}

func TestUnknownNameCreatesNewProfile(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "rustdev",
		Extends:    []string{"lang/javascript"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}
	data, _ := os.ReadFile(filepath.Join(dir, "rustdev.yaml"))
	if !strings.Contains(string(data), "- core/mise") {
		t.Errorf("new profile should extend mise by default, got:\n%s", string(data))
	}
}

func TestGenerateEmitsCoreQualifiedBuiltinBase(t *testing.T) {
	cat, err := fixtureLoader(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content, err := generate("myagent", []string{"core/bash"}, cat)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "- core/bash") {
		t.Errorf("generated content should contain a core/-qualified base, got:\n%s", content)
	}
}

func TestGenerateEmitsCoreQualifiedFragment(t *testing.T) {
	cat, err := fixtureLoader(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content, err := generate("myagent", []string{"core/mise", "core/lang/javascript"}, cat)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "- core/mise") || !strings.Contains(content, "- core/lang/javascript") {
		t.Errorf("generated content missing core/-qualified extends, got:\n%s", content)
	}
}

func TestGenerateEmitsUserFragmentUnqualified(t *testing.T) {
	dir := t.TempDir()
	fragDir := filepath.Join(filepath.Dir(dir), "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "myfrag.yaml"), []byte("version: 1\ntools:\n  x: \"1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := fixtureLoader(dir)
	if err != nil {
		t.Fatal(err)
	}
	content, err := generate("myagent", []string{"myfrag"}, cat)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "- myfrag") {
		t.Errorf("user fragment should be emitted unqualified, got:\n%s", content)
	}
}
