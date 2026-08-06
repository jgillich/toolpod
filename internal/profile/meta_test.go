package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"gopkg.in/yaml.v3"
)

func TestMetaYAMLRoundTrip(t *testing.T) {
	in := &Meta{Description: "A test entry", Tags: []string{"lang", "toolchain"}}
	data, err := yaml.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Meta
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Description != "A test entry" {
		t.Errorf("Description = %q", out.Description)
	}
	if len(out.Tags) != 2 || out.Tags[0] != "lang" || out.Tags[1] != "toolchain" {
		t.Errorf("Tags = %v", out.Tags)
	}
}

func TestMetaOmittedWhenAbsent(t *testing.T) {
	data, err := yaml.Marshal(Profile{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "meta:") {
		t.Errorf("absent meta must be omitted, got:\n%s", data)
	}
}

func TestMetaNotInheritedThroughExtends(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\nmeta:\n  description: base description\n")
	mustWriteProfile(t, dir, "bare.yaml", "version: 1\nextends: base\ncommand: [\"y\"]\n")
	mustWriteProfile(t, dir, "own.yaml", "version: 1\nextends: base\ncommand: [\"y\"]\nmeta:\n  description: own description\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}

	bare, err := ResolveProfile(cat, "bare")
	if err != nil {
		t.Fatal(err)
	}
	if bare.Meta != nil {
		t.Errorf("child without meta must not inherit the base's meta, got %+v", bare.Meta)
	}

	own, err := ResolveProfile(cat, "own")
	if err != nil {
		t.Fatal(err)
	}
	if own.Meta == nil || own.Meta.Description != "own description" {
		t.Errorf("child with own meta must keep it, got %+v", own.Meta)
	}
}

func TestResolveProfileKeepsLeafMeta(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\nmeta:\n  description: base\n")
	mustWriteProfile(t, dir, "child.yaml", "version: 1\nextends: base\ncommand: [\"y\"]\nmeta:\n  description: child\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ResolveProfileWithProv(cat, "child")
	if err != nil {
		t.Fatal(err)
	}
	if res.Meta == nil || res.Meta.Description != "child" {
		t.Errorf("resolved meta = %+v, want the leaf's own", res.Meta)
	}
}

func TestCatalogDescription(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "core.yaml", "version: 1\nimage: x:1\ncommand: [\"x\"]\nmeta:\n  description: core entry\n")
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cat.Description("core"); got != "core entry" {
		t.Errorf("Description(core) = %q", got)
	}
	if got := cat.Description("nope"); got != "" {
		t.Errorf("Description(nope) = %q, want empty", got)
	}
}

func TestCatalogDescriptionUserShadowsCore(t *testing.T) {
	coreFS := fstest.MapFS{
		"profiles/myapp.yaml":  &fstest.MapFile{Data: []byte("version: 1\nimage: x:1\ncommand: [\"x\"]\nmeta:\n  description: core description\n")},
		"fragments/empty.yaml": &fstest.MapFile{Data: []byte("version: 1\ntools:\n  empty: latest\n")},
	}
	userDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(userDir, "myapp.yaml"), []byte("version: 1\nimage: x:1\ncommand: [\"x\"]\nmeta:\n  description: user description\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadCatalog(coreFS, coreFS, userDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cat.Description("myapp"); got != "user description" {
		t.Errorf("Description(myapp) = %q, want the user entry's", got)
	}
	if got := cat.Description("nope"); got != "" {
		t.Errorf("Description(nope) = %q, want empty", got)
	}
}

func TestMetaValidationRejectsControlChars(t *testing.T) {
	// yaml.v3 rejects raw control characters at parse time, so build the
	// struct directly to exercise validateMeta.
	rc := RawProfile{Profile: Profile{
		Version: 1,
		Image:   "x:1",
		Command: []string{"x"},
		Meta:    &Meta{Description: "bad\x00desc"},
	}}
	if err := validate(rc); err == nil {
		t.Error("expected validation error for control characters in meta.description")
	}
}
