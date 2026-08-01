package profile

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMountCreateParses(t *testing.T) {
	var rc RawProfile
	body := `
version: 1
image: ubuntu
command: ["bash"]
mounts:
  ~/.data:
    source: ~/.data
    create: true
  ~/.config/app:
    source: ~/.config/app
`
	if err := yaml.Unmarshal([]byte(body), &rc.Profile); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !rc.Mounts["~/.data"].Create {
		t.Error("mount with create: true should have Create set")
	}
	if rc.Mounts["~/.config/app"].Create {
		t.Error("mount without create should have Create unset")
	}
}

// TestCommandOmitempty verifies that an empty (nil) Command slice is omitted
// from marshaled YAML output due to the omitempty tag. It also covers the
// non-nil empty slice case ([]string{}), which yaml.v3 should also omit.
func TestCommandOmitempty(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command []string
	}{
		{"nil", nil},
		{"empty slice", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Profile{
				Version: 1,
				Image:   "ubuntu",
				Command: tc.command,
			}
			data, err := yaml.Marshal(p)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			out := string(data)
			if strings.Contains(out, "command:") {
				t.Fatalf("expected command to be omitted, got:\n%s", out)
			}
		})
	}
}

// TestCachesBeforeMounts verifies that the Caches field is serialized
// before the Mounts field in marshaled YAML output, matching the struct
// field declaration order.
func TestCachesBeforeMounts(t *testing.T) {
	p := Profile{
		Version: 1,
		Image:   "ubuntu",
		Command: []string{"sh"},
		Caches:  map[string]string{"npm": "~/.npm"},
		Mounts:  map[string]Mount{"/src": {Source: ".", ReadOnly: false}},
		Env:     map[string]string{"FOO": "bar"},
	}
	data, err := yaml.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	out := string(data)
	cachesIdx := strings.Index(out, "caches:")
	mountsIdx := strings.Index(out, "mounts:")
	if cachesIdx < 0 || mountsIdx < 0 {
		t.Fatalf("missing caches or mounts key in:\n%s", out)
	}
	if cachesIdx > mountsIdx {
		t.Errorf("expected caches before mounts in YAML output:\n%s", out)
	}
}

func TestParseAndMergeDbus(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "dbusbase.yaml", `version: 1
dbus:
  talk:
    org.freedesktop.portal.Desktop: {}
`)
	mustWriteProfile(t, dir, "app.yaml", `version: 1
extends: dbusbase
command: ["app"]
image: img:1
dbus:
  talk:
    org.freedesktop.Notifications: {}
  own:
    xyz.block.buzz.app: {}
`)
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "app")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dbus == nil {
		t.Fatal("dbus missing after merge")
	}
	if cfg.Dbus.Talk["org.freedesktop.portal.Desktop"] == nil {
		t.Error("talk from parent (dbusbase) lost")
	}
	if cfg.Dbus.Talk["org.freedesktop.Notifications"] == nil {
		t.Error("talk from child lost")
	}
	if cfg.Dbus.Own["xyz.block.buzz.app"] == nil {
		t.Error("own from child lost")
	}
}

func TestValidateDbusRejectsBadName(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "bad.yaml", `version: 1
command: ["x"]
image: img:1
dbus:
  talk:
    "not a bus name!": {}
`)
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveProfile(cat, "bad"); err == nil {
		t.Fatal("expected validation error for invalid dbus name")
	} else if !strings.Contains(err.Error(), "dbus") {
		t.Fatalf("error should mention dbus: %v", err)
	}
}

func TestMergeDbusNullClears(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "dbusbase.yaml", `version: 1
dbus:
  talk:
    org.freedesktop.portal.Desktop: {}
`)
	mustWriteProfile(t, dir, "app.yaml", `version: 1
extends: dbusbase
command: ["app"]
image: img:1
dbus: null
`)
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "app")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dbus != nil {
		t.Errorf("dbus should be cleared by null, got %+v", cfg.Dbus)
	}
}

func TestMergeDbusNullClearsTalkSubMap(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "dbusbase.yaml", `version: 1
dbus:
  talk:
    org.freedesktop.portal.Desktop: {}
  own:
    xyz.block.buzz.app: {}
`)
	mustWriteProfile(t, dir, "app.yaml", `version: 1
extends: dbusbase
command: ["app"]
image: img:1
dbus:
  talk: null
`)
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "app")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dbus == nil {
		t.Fatal("dbus should survive with own names remaining")
	}
	if len(cfg.Dbus.Talk) != 0 {
		t.Errorf("dbus.talk should be cleared by null, got %v", cfg.Dbus.Talk)
	}
	if cfg.Dbus.Own["xyz.block.buzz.app"] == nil {
		t.Error("dbus.own from base should survive talk: null")
	}
}

func TestMergeDbusNullClearsPerName(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "dbusbase.yaml", `version: 1
dbus:
  talk:
    org.freedesktop.portal.Desktop: {}
`)
	mustWriteProfile(t, dir, "app.yaml", `version: 1
extends: dbusbase
command: ["app"]
image: img:1
dbus:
  talk:
    org.freedesktop.portal.Desktop: null
`)
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveProfile(cat, "app")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dbus == nil {
		t.Fatal("dbus should survive the per-name null")
	}
	if cfg.Dbus.Talk["org.freedesktop.portal.Desktop"] != nil {
		t.Error("per-name null should clear the inherited talk entry")
	}
}
