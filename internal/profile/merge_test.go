package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustWriteProfile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveScalarOverride(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\nnetwork: bridge\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\nnetwork: host\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Network != "host" {
		t.Errorf("Network = %q, want host", cfg.Network)
	}
	if cfg.Image != "base:1" {
		t.Errorf("Image = %q, want base:1 (inherited)", cfg.Image)
	}
}

func TestResolveMapMergeAndNullDelete(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\ntools:\n  node: \"20\"\n  rust: \"1.74\"\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\ntools:\n  node: \"22\"\n  rust: null\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Tools["node"] != "22" {
		t.Errorf("node = %q, want 22 (overridden)", cfg.Tools["node"])
	}
	if _, exists := cfg.Tools["rust"]; exists {
		t.Error("rust should be deleted by null-to-delete rule")
	}
}

func TestResolveListReplaced(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"a\", \"--x\"]\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\ncommand: [\"b\", \"--y\"]\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Command) != 2 || cfg.Command[0] != "b" || cfg.Command[1] != "--y" {
		t.Errorf("command = %v, want [b --y] (replaced not concatenated)", cfg.Command)
	}
}

func TestResolveExtendsSelfViaBuiltin(t *testing.T) {
	dir := t.TempDir()
	// User file shadows built-in "opencode" and extends "opencode" (the built-in).
	mustWriteProfile(t, dir, "opencode.yaml", "version: 1\nextends: opencode\ncaches:\n  npm: ~/.npm\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	cfg, err := ResolveProfile(cat, "opencode")
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	// Should inherit image/command from the built-in opencode, plus the user caches.
	if cfg.Image != "ghcr.io/jgillich/tpod-mise:latest" {
		t.Errorf("Image = %q, want ghcr.io/jgillich/tpod-mise:latest (inherited from built-in)", cfg.Image)
	}
	if cfg.Caches["npm"] != "~/.npm" {
		t.Errorf("Caches[npm] = %q, want ~/.npm", cfg.Caches["npm"])
	}
}

func TestResolveCycle(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "a.yaml", "version: 1\nimage: x\ncommand: [\"x\"]\nextends: b\n")
	mustWriteProfile(t, dir, "b.yaml", "version: 1\nimage: y\ncommand: [\"y\"]\nextends: a\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResolveProfile(cat, "a")
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	ce, ok := err.(ProfileError)
	if !ok {
		t.Fatalf("expected ProfileError, got %T", err)
	}
	if ce.Message == "" || !strings.Contains(ce.Message, "cycle") {
		t.Errorf("error message %q should mention cycle", ce.Message)
	}
}

func TestResolveSelfExtendsNoBuiltin(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "foo.yaml", "version: 1\nimage: x\ncommand: [\"x\"]\nextends: foo\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResolveProfile(cat, "foo")
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention cycle, got: %v", err)
	}
}

func TestResolveUserCrossCycle(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "a.yaml", "version: 1\nimage: x\ncommand: [\"x\"]\nextends: b\n")
	mustWriteProfile(t, dir, "b.yaml", "version: 1\nimage: y\ncommand: [\"y\"]\nextends: a\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResolveProfile(cat, "a")
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention cycle, got: %v", err)
	}
}

func TestResolveExtendsMissingProfile(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nimage: x\ncommand: [\"x\"]\nextends: missing-parent\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResolveProfile(cat, "child")
	if err == nil {
		t.Fatal("expected error for extends referencing a missing profile, got nil")
	}
	if strings.HasPrefix(err.Error(), ":") {
		t.Errorf("error message should not start with a stray colon, got: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "missing-parent") {
		t.Errorf("error message should name the missing profile, got: %q", err.Error())
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, "child.yaml")) {
		t.Errorf("error message should name the file with the bad extends, got: %q", err.Error())
	}
}

func TestResolvePortsDevicesMergeAndNullDelete(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\n"+
		"ports:\n  8080: {host: 5173}\n  9000: {}\n"+
		"devices:\n  /dev/fuse: {}\n  /dev/nvidia0: {permissions: rw}\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\n"+
		"ports:\n  8080: {host: 0}\n  9000: null\n"+
		"devices:\n  /dev/fuse: null\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Ports["8080"].Host != "0" {
		t.Errorf("8080 host = %q, want \"0\" (overridden to random)", cfg.Ports["8080"].Host)
	}
	if _, exists := cfg.Ports["9000"]; exists {
		t.Error("9000 should be deleted by null-to-delete")
	}
	if _, exists := cfg.Devices["/dev/fuse"]; exists {
		t.Error("/dev/fuse should be deleted by null-to-delete")
	}
	if cfg.Devices["/dev/nvidia0"].Permissions != "rw" {
		t.Errorf("inherited /dev/nvidia0 permissions = %q, want rw", cfg.Devices["/dev/nvidia0"].Permissions)
	}
}

func TestResolvePortsWholeFieldNull(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\nports:\n  8080: {}\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\nports: null\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Ports) != 0 {
		t.Errorf("whole-field null should drop all inherited ports, got %v", cfg.Ports)
	}
}

func TestResolvePackagesAdditiveDedup(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "frag1.yaml", "version: 1\nimage: frag1:1\ncommand: [\"x\"]\npackages: [libxml2-dev, libicu-dev]\n")
	mustWriteProfile(t, dir, "frag2.yaml", "version: 1\nimage: frag2:1\ncommand: [\"y\"]\npackages: [libicu-dev, libonig-dev]\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: [frag1, frag2]\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := []string{"libxml2-dev", "libicu-dev", "libonig-dev"}
	if len(cfg.Packages) != len(want) {
		t.Fatalf("Packages = %v, want %v", cfg.Packages, want)
	}
	for i := range want {
		if cfg.Packages[i] != want[i] {
			t.Errorf("Packages[%d] = %q, want %q (order preserved, deduped)", i, cfg.Packages[i], want[i])
		}
	}
}

func TestResolvePackagesWholeFieldNull(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\npackages: [libxml2-dev, libicu-dev]\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\npackages: null\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Packages) != 0 {
		t.Errorf("whole-field null should drop all inherited packages, got %v", cfg.Packages)
	}
}

func TestResolvePackagesChildExtendsWithoutOverride(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\npackages: [libssl-dev]\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\ncommand: [\"y\"]\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Packages) != 1 || cfg.Packages[0] != "libssl-dev" {
		t.Errorf("Packages = %v, want [libssl-dev] (inherited unchanged)", cfg.Packages)
	}
}

func TestResolveReposMergeAcrossExtends(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "frag1.yaml", "version: 1\nimage: frag1:1\ncommand: [\"x\"]\nrepos:\n  mise: {extrepo: mise}\n")
	mustWriteProfile(t, dir, "frag2.yaml", "version: 1\nimage: frag2:1\ncommand: [\"y\"]\nrepos:\n  nodejs: {extrepo: nodejs}\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: [frag1, frag2]\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Repos["mise"].ExtRepo != "mise" {
		t.Errorf("repos[mise].ExtRepo = %q, want mise (from frag1)", cfg.Repos["mise"].ExtRepo)
	}
	if cfg.Repos["nodejs"].ExtRepo != "nodejs" {
		t.Errorf("repos[nodejs].ExtRepo = %q, want nodejs (from frag2)", cfg.Repos["nodejs"].ExtRepo)
	}
}

func TestResolveReposOverrideByName(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\nrepos:\n  mise: {extrepo: mise}\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\nrepos:\n  mise: {extrepo: mise-edge}\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Repos["mise"].ExtRepo != "mise-edge" {
		t.Errorf("repos[mise].ExtRepo = %q, want mise-edge (child wins per key)", cfg.Repos["mise"].ExtRepo)
	}
}

func TestResolveReposNullDelete(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\nrepos:\n  mise: {extrepo: mise}\n  nodejs: {extrepo: nodejs}\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\nrepos:\n  mise: null\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, exists := cfg.Repos["mise"]; exists {
		t.Error("repos[mise] should be deleted by null-to-delete")
	}
	if cfg.Repos["nodejs"].ExtRepo != "nodejs" {
		t.Errorf("repos[nodejs].ExtRepo = %q, want nodejs (inherited unchanged)", cfg.Repos["nodejs"].ExtRepo)
	}
}

func TestResolveReposWholeFieldNull(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\nrepos:\n  mise: {extrepo: mise}\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\nrepos: null\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Repos) != 0 {
		t.Errorf("whole-field null should drop all inherited repos, got %v", cfg.Repos)
	}
}

func TestResolveFilesMergeAcrossExtends(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "frag1.yaml", "version: 1\nimage: frag1:1\ncommand: [\"x\"]\nfiles:\n  ~/.config/a: {content: \"one\"}\n")
	mustWriteProfile(t, dir, "frag2.yaml", "version: 1\nimage: frag2:1\ncommand: [\"y\"]\nfiles:\n  ~/.config/b: {content: \"two\"}\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: [frag1, frag2]\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Files["~/.config/a"].Content != "one" {
		t.Errorf("files[~/.config/a].Content = %q, want one (from frag1)", cfg.Files["~/.config/a"].Content)
	}
	if cfg.Files["~/.config/b"].Content != "two" {
		t.Errorf("files[~/.config/b].Content = %q, want two (from frag2)", cfg.Files["~/.config/b"].Content)
	}
}

func TestResolveFilesOverrideByTarget(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\nfiles:\n  ~/.config/a: {content: \"one\"}\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\nfiles:\n  ~/.config/a: {content: \"two\", mode: 0600}\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Files["~/.config/a"].Content != "two" {
		t.Errorf("files[~/.config/a].Content = %q, want two (child wins per key)", cfg.Files["~/.config/a"].Content)
	}
	if cfg.Files["~/.config/a"].Mode != 0o600 {
		t.Errorf("files[~/.config/a].Mode = %o, want 600", cfg.Files["~/.config/a"].Mode)
	}
}

func TestResolveFilesNullDelete(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\nfiles:\n  ~/.config/a: {content: \"one\"}\n  ~/.config/b: {content: \"two\"}\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\nfiles:\n  ~/.config/a: null\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, exists := cfg.Files["~/.config/a"]; exists {
		t.Error("files[~/.config/a] should be deleted by null-to-delete")
	}
	if cfg.Files["~/.config/b"].Content != "two" {
		t.Errorf("files[~/.config/b].Content = %q, want two (inherited unchanged)", cfg.Files["~/.config/b"].Content)
	}
}

func TestResolveFilesWholeFieldNull(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\nfiles:\n  ~/.config/a: {content: \"one\"}\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\nfiles: null\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Files) != 0 {
		t.Errorf("whole-field null should drop all inherited files, got %v", cfg.Files)
	}
}
