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
	if !strings.Contains(string(data), "- mise") {
		t.Errorf("generated file should extend mise, got:\n%s", string(data))
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
		Extends:    []string{"opencode", "podman", "ruby"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "myagent.yaml"))
	content := string(data)
	for _, want := range []string{"- opencode", "- podman", "- ruby"} {
		if !strings.Contains(content, want) {
			t.Errorf("generated file missing %s, got:\n%s", want, content)
		}
	}
	cat, err := profile.LoadProfiles(dir)
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
	cat, err := profile.LoadProfiles(dir)
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
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		Name:       "javascript",
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for name colliding with fragment")
	}
	if !strings.Contains(err.Error(), "fragment") {
		t.Errorf("error should mention fragment, got: %v", err)
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
	// "New" → name "foo" → bases "mise,opencode" → fragments "javascript,gitconfig"
	// (the fragment picker appends to the same extends list).
	input := strings.NewReader("New\nfoo\nmise,opencode\njavascript,gitconfig\n")
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
	for _, want := range []string{"- mise", "- opencode", "- javascript", "- gitconfig"} {
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
		Extends:    []string{"javascript"},
		ProfileDir: dir,
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\nstderr: %s", err, stderr.String())
	}
	data, _ := os.ReadFile(filepath.Join(dir, "rustdev.yaml"))
	if !strings.Contains(string(data), "- mise") {
		t.Errorf("new profile should extend mise by default, got:\n%s", string(data))
	}
}
