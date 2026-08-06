package approval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewFSStore(dir)
	st := State{
		Hash: "abc123",
		Approved: map[string]ApprovedField{
			"mounts":   {Keys: []string{"~/.ssh"}},
			"network":  {Network: boolPtr(true)},
			"services": {Keys: []string{"podman"}},
		},
	}
	if err := s.Save("core/creds/ssh", st); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("core/creds/ssh")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Hash != st.Hash {
		t.Errorf("Hash = %q, want %q", got.Hash, st.Hash)
	}
	if len(got.Approved["mounts"].Keys) != 1 || got.Approved["mounts"].Keys[0] != "~/.ssh" {
		t.Errorf("mounts keys = %+v", got.Approved["mounts"].Keys)
	}
	if got.Approved["network"].Network == nil || !*got.Approved["network"].Network {
		t.Errorf("network should be approved")
	}
	if len(got.Approved["services"].Keys) != 1 || got.Approved["services"].Keys[0] != "podman" {
		t.Errorf("services keys = %+v, want [podman]", got.Approved["services"].Keys)
	}
}

func TestStoreMissingFileReturnsZero(t *testing.T) {
	s := NewFSStore(t.TempDir())
	got, err := s.Load("nope")
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if got.Hash != "" || len(got.Approved) != 0 {
		t.Errorf("missing file should return zero state, got %+v", got)
	}
}

func TestStoreRejectsBadFullName(t *testing.T) {
	s := NewFSStore(t.TempDir())
	bad := []string{"../etc/passwd", "a/../b", "a//b", "a\x00b"}
	for _, name := range bad {
		if err := s.Save(name, State{}); err == nil {
			t.Errorf("Save(%q) should fail", name)
		}
	}
}

func TestStoreNestedFullName(t *testing.T) {
	dir := t.TempDir()
	s := NewFSStore(dir)
	if err := s.Save("core/creds/ssh", State{Hash: "h"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	full := filepath.Join(dir, "approvals", "core", "creds", "ssh.yaml")
	if _, err := os.Stat(full); err != nil {
		t.Errorf("expected file at %s: %v", full, err)
	}
}

func boolPtr(b bool) *bool { return &b }

func TestStateMarshalDistinguishesDeniedFromAbsent(t *testing.T) {
	st := State{
		Approved: map[string]ApprovedField{
			"mounts":  {Keys: []string{"~/.ssh"}}, // field present, one approved
			"devices": {Keys: nil},                // field present, all denied
			// "env" absent → never decided
		},
	}
	data, err := yaml.Marshal(st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(data)
	if !contains(s, "mounts:") || !contains(s, "devices:") {
		t.Errorf("denied field should be present in YAML:\n%s", s)
	}
	if contains(s, "env:") {
		t.Errorf("absent field should be missing from YAML:\n%s", s)
	}
	// Round-trip
	var back State
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := back.Approved["devices"]; !ok {
		t.Error("denied field should survive round-trip as present-with-empty")
	}
	if _, ok := back.Approved["env"]; ok {
		t.Error("absent field should stay absent after round-trip")
	}
}

func TestStateMarshalNestsDbusOnly(t *testing.T) {
	yes := true
	st := State{
		Approved: map[string]ApprovedField{
			"dbus.talk": {Keys: []string{"org.freedesktop.portal.Desktop"}},
			"dbus.own":  {Keys: nil},
			"network":   {Network: &yes},
			"services":  {Keys: []string{"podman"}},
		},
	}
	data, err := yaml.Marshal(st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(data)
	// dbus is the only nested field.
	for _, want := range []string{"dbus:", "talk:", "network:", "services:"} {
		if !contains(s, want) {
			t.Errorf("YAML should contain key %q:\n%s", want, s)
		}
	}
	// dbus sub-fields must be nested, not flat dotted keys.
	if contains(s, "dbus.talk:") {
		t.Errorf("YAML should not contain flat dotted key dbus.talk:\n%s", s)
	}
	// services is a flat list of approved names, not a nested per-sub-field map.
	if contains(s, "services.podman.") {
		t.Errorf("YAML should not contain nested services.<name>.<field> keys:\n%s", s)
	}
	// Map fields marshal as bare lists (mounts: [~/.ssh]), not nested under keys:.
	if contains(s, "keys:") {
		t.Errorf("YAML should not contain a nested keys: field (map fields are bare lists):\n%s", s)
	}
	// Round-trip preserves the flat keyed State.
	var back State
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.Approved["dbus.talk"].Keys[0] != "org.freedesktop.portal.Desktop" {
		t.Errorf("dbus.talk round-trip failed: %+v", back.Approved["dbus.talk"])
	}
	if back.Approved["services"].Keys[0] != "podman" {
		t.Errorf("services round-trip failed: %+v", back.Approved["services"])
	}
	if back.Approved["network"].Network == nil || !*back.Approved["network"].Network {
		t.Errorf("network round-trip failed: %+v", back.Approved["network"])
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
