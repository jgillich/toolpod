package profile

import (
	"strings"
	"testing"
)

func TestParseRawRejectsUnknownTopLevelField(t *testing.T) {
	_, err := parseRaw([]byte("version: 1\nimage: x\ncommand: [sh]\nbogus: 1\n"), "test")
	if err == nil {
		t.Fatal("expected error for unknown top-level field")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("error should name the unknown field, got: %v", err)
	}
}

func TestParseRawRejectsUnknownMountKey(t *testing.T) {
	_, err := parseRaw([]byte("version: 1\nimage: x\ncommand: [sh]\nmounts:\n  /data:\n    source: /host\n    mode: 0644\n"), "test")
	if err == nil {
		t.Fatal("expected error for unknown mount key")
	}
	if !strings.Contains(err.Error(), "mode") {
		t.Fatalf("error should name the unknown key, got: %v", err)
	}
}

func TestParseRawRejectsUnknownToolKey(t *testing.T) {
	_, err := parseRaw([]byte("version: 1\nimage: x\ncommand: [sh]\ntools:\n  node:\n    version: \"20\"\n    checksum: abc\n"), "test")
	if err == nil {
		t.Fatal("expected error for unknown tool key")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("error should name the unknown key, got: %v", err)
	}
}

func TestParseRawEmptyDocument(t *testing.T) {
	rc, err := parseRaw([]byte("# nothing here\n"), "test")
	if err != nil {
		t.Fatalf("comment-only document must parse to a zero profile, got: %v", err)
	}
	if rc.Version != 0 {
		t.Errorf("Version = %d, want 0", rc.Version)
	}
}

func TestParseRawAcceptsKnownToolKeys(t *testing.T) {
	rc, err := parseRaw([]byte("version: 1\nimage: x\ncommand: [sh]\ntools:\n  node:\n    version: \"20\"\n    sha256: abc\n"), "test")
	if err != nil {
		t.Fatalf("expected valid tool map to parse, got: %v", err)
	}
	if rc.Tools["node"].Version != "20" || rc.Tools["node"].SHA256 != "abc" {
		t.Errorf("Tool = %+v, want version=20 sha256=abc", rc.Tools["node"])
	}
}

func TestResolveProfileRequiresVersionOne(t *testing.T) {
	t.Run("version 1 accepted", func(t *testing.T) {
		dir := t.TempDir()
		mustWriteProfile(t, dir, "v1.yaml", "version: 1\nimage: x\ncommand: [sh]\n")
		cat, err := LoadProfiles(dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveProfile(cat, "v1"); err != nil {
			t.Fatalf("version-1 profile should resolve, got: %v", err)
		}
	})

	t.Run("version 0 rejected", func(t *testing.T) {
		dir := t.TempDir()
		mustWriteProfile(t, dir, "v0.yaml", "image: x\ncommand: [sh]\n")
		cat, err := LoadProfiles(dir)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ResolveProfile(cat, "v0")
		if err == nil || !strings.Contains(err.Error(), "missing required field: version") {
			t.Fatalf("want missing-version error, got: %v", err)
		}
	})

	t.Run("version 2 rejected", func(t *testing.T) {
		dir := t.TempDir()
		mustWriteProfile(t, dir, "v2.yaml", "version: 2\nimage: x\ncommand: [sh]\n")
		cat, err := LoadProfiles(dir)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ResolveProfile(cat, "v2")
		if err == nil || !strings.Contains(err.Error(), "unsupported version: 2") {
			t.Fatalf("want unsupported-version error, got: %v", err)
		}
	})

	t.Run("extends-only leaf inherits version", func(t *testing.T) {
		dir := t.TempDir()
		mustWriteProfile(t, dir, "base.yaml", "version: 1\nimage: base:1\ncommand: [\"x\"]\n")
		mustWriteProfile(t, dir, "leaf.yaml", "extends: base\ncommand: [\"y\"]\n")
		cat, err := LoadProfiles(dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveProfile(cat, "leaf"); err != nil {
			t.Fatalf("leaf extending a version-1 base must resolve, got: %v", err)
		}
	})
}

func TestLoadProfilesTolerantSkipsUnknownField(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "good.yaml", "version: 1\nimage: x\ncommand: [sh]\n")
	mustWriteProfile(t, dir, "bad.yaml", "version: 1\nimage: x\ncommand: [sh]\nbogus: 1\n")
	var warnings []string
	cat, err := LoadProfilesTolerant(dir, func(w string) { warnings = append(warnings, w) })
	if err != nil {
		t.Fatalf("LoadProfilesTolerant: %v", err)
	}
	if _, ok := cat.Get("good"); !ok {
		t.Error("valid profile should still load in tolerant mode")
	}
	if _, ok := cat.Get("bad"); ok {
		t.Error("malformed profile must be skipped")
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "bad.yaml") || !strings.Contains(warnings[0], "bogus") {
		t.Fatalf("expected a warning naming bad.yaml and the unknown field, got %v", warnings)
	}
}
