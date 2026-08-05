package profile

import (
	"fmt"
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
		Caches:  map[string]CachePaths{"npm": {"~/.npm"}},
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

func TestToolDecodeScalar(t *testing.T) {
	var tool Tool
	if err := yaml.Unmarshal([]byte(`latest`), &tool); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if tool.Version != "latest" {
		t.Errorf("Version = %q, want latest", tool.Version)
	}
	if tool.SHA256 != "" || len(tool.SHA256ByArch) != 0 {
		t.Errorf("unexpected checksum metadata: %+v", tool)
	}
}

func TestToolDecodeMapScalarDigest(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	var tool Tool
	body := fmt.Sprintf("{version: v1, sha256: %s}", digest)
	if err := yaml.Unmarshal([]byte(body), &tool); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if tool.Version != "v1" || tool.SHA256 != digest {
		t.Errorf("Tool = %+v, want version=v1 sha256=%s", tool, digest)
	}
}

func TestToolDecodeMapWithoutChecksum(t *testing.T) {
	var tool Tool
	if err := yaml.Unmarshal([]byte("{version: v1}"), &tool); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if tool.Version != "v1" || tool.SHA256 != "" || len(tool.SHA256ByArch) != 0 {
		t.Errorf("Tool = %+v, want version=v1 without checksum metadata", tool)
	}
}

func TestToolDecodeMapPerArchDigests(t *testing.T) {
	amd64 := strings.Repeat("ab", 32)
	aarch64 := strings.Repeat("cd", 32)
	var tool Tool
	body := fmt.Sprintf("{version: v1, sha256: {amd64: %s, aarch64: %s}}", amd64, aarch64)
	if err := yaml.Unmarshal([]byte(body), &tool); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if tool.Version != "v1" {
		t.Errorf("Version = %q, want v1", tool.Version)
	}
	if tool.SHA256 != "" {
		t.Errorf("SHA256 = %q, want empty (per-arch form)", tool.SHA256)
	}
	if tool.SHA256ByArch["amd64"] != amd64 || tool.SHA256ByArch["aarch64"] != aarch64 {
		t.Errorf("SHA256ByArch = %v, want amd64=%s aarch64=%s", tool.SHA256ByArch, amd64, aarch64)
	}
}

func TestToolMarshalScalarWhenNoChecksum(t *testing.T) {
	tool := Tool{Version: "latest"}
	data, err := yaml.Marshal(tool)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "latest" {
		t.Errorf("marshaled = %q, want scalar latest", got)
	}
}

func TestToolMarshalMapWithDigest(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	tool := Tool{Version: "v1", SHA256: digest}
	data, err := yaml.Marshal(tool)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := "version: v1\nsha256: " + digest + "\n"
	if string(data) != want {
		t.Errorf("marshaled = %q, want %q", string(data), want)
	}
}

func TestToolMarshalPerArchDigests(t *testing.T) {
	amd64 := strings.Repeat("ab", 32)
	aarch64 := strings.Repeat("cd", 32)
	tool := Tool{Version: "v1", SHA256ByArch: map[string]string{"amd64": amd64, "aarch64": aarch64}}
	data, err := yaml.Marshal(tool)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "version: v1") || !strings.Contains(out, "amd64: "+amd64) || !strings.Contains(out, "aarch64: "+aarch64) {
		t.Errorf("marshaled = %q, want version + per-arch sha256 map", out)
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

func TestServicesParse(t *testing.T) {
	var rc RawProfile
	body := `
version: 1
image: ubuntu
command: ["sh"]
services:
  registry:
    image: debian:13-slim
    command: ["registry", "serve", "/etc/docker/registry/config.yml"]
    caches:
      registry-data:
        - /var/lib/registry
    exposes:
      registry: /run/registry/registry.sock
    network: host
    privileged: true
`
	if err := yaml.Unmarshal([]byte(body), &rc.Profile); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(rc.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(rc.Services))
	}
	svc := rc.Services["registry"]
	if svc.Image != "debian:13-slim" {
		t.Errorf("service image = %q, want debian:13-slim", svc.Image)
	}
	if svc.Network != "host" {
		t.Errorf("service network = %q, want host (rejected field must be captured by YAML)", svc.Network)
	}
	if !svc.Privileged {
		t.Error("service privileged = false, want true")
	}
	if len(svc.Command) != 3 || svc.Command[0] != "registry" {
		t.Errorf("service command = %v, want [registry serve /etc/docker/registry/config.yml]", svc.Command)
	}
	if len(svc.Caches["registry-data"]) != 1 || svc.Caches["registry-data"][0] != "/var/lib/registry" {
		t.Errorf("service cache = %v, want [/var/lib/registry]", svc.Caches["registry-data"])
	}
	if svc.Exposes["registry"] != "/run/registry/registry.sock" {
		t.Errorf("service expose = %q, want /run/registry/registry.sock", svc.Exposes["registry"])
	}
}

func TestMountServiceSocketParses(t *testing.T) {
	var rc RawProfile
	body := `
version: 1
image: ubuntu
command: ["sh"]
services:
  registry:
    image: debian:13-slim
    command: ["registry"]
    exposes:
      registry: /run/registry/registry.sock
mounts:
  /run/registry/registry.sock:
    service: registry
    socket: registry
  /data:
    source: /host/data
    read_only: false
`
	if err := yaml.Unmarshal([]byte(body), &rc.Profile); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	svcMount := rc.Mounts["/run/registry/registry.sock"]
	if svcMount.Service != "registry" {
		t.Errorf("service mount Service = %q, want registry", svcMount.Service)
	}
	if svcMount.Socket != "registry" {
		t.Errorf("service mount Socket = %q, want registry", svcMount.Socket)
	}
	if svcMount.Source != "" {
		t.Errorf("service mount Source = %q, want empty", svcMount.Source)
	}
	if svcMount.ReadOnly {
		t.Error("service mount ReadOnly = true, want false (default for service-socket mounts)")
	}
	bindMount := rc.Mounts["/data"]
	if bindMount.Source != "/host/data" {
		t.Errorf("bind mount Source = %q, want /host/data", bindMount.Source)
	}
	if bindMount.ReadOnly {
		t.Error("bind mount ReadOnly = true, want false (explicitly set)")
	}
}

func TestMountBindReadOnlyDefaultsTrue(t *testing.T) {
	var rc RawProfile
	body := `
version: 1
image: ubuntu
command: ["sh"]
mounts:
  /data:
    source: /host/data
`
	if err := yaml.Unmarshal([]byte(body), &rc.Profile); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !rc.Mounts["/data"].ReadOnly {
		t.Error("bind mount without read_only should default to true")
	}
}
