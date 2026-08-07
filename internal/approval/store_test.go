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

func TestStoreSaveAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	s := NewFSStore(dir)
	st := State{Hash: "h", Approved: map[string]ApprovedField{"mounts": {Keys: []string{"~/.ssh"}}}}
	if err := s.Save("p", st); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := filepath.Join(dir, "approvals", "p.yaml")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("state file mode = %o, want 600", fi.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Join(dir, "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file %s left behind after successful Save", e.Name())
		}
	}
	got, err := s.Load("p")
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if got.Hash != "h" || !containsKey(got.Approved["mounts"].Keys, "~/.ssh") {
		t.Errorf("round-trip state = %+v", got)
	}
}

func TestStoreSaveCleansTempOnFailure(t *testing.T) {
	dir := t.TempDir()
	s := NewFSStore(dir)
	// A directory at the target path makes the atomic rename fail after the
	// temp file was already written and fsynced.
	if err := os.MkdirAll(filepath.Join(dir, "approvals", "p.yaml"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("p", State{Hash: "h"}); err == nil {
		t.Fatal("Save over a directory target should fail")
	}
	entries, err := os.ReadDir(filepath.Join(dir, "approvals"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file %s left behind after failed Save", e.Name())
		}
	}
}

func TestStoreRejectsSymlinkedStateFile(t *testing.T) {
	dir := t.TempDir()
	s := NewFSStore(dir)
	approvalsDir := filepath.Join(dir, "approvals")
	if err := os.MkdirAll(approvalsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "elsewhere.yaml")
	if err := os.WriteFile(target, []byte("hash: hijacked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(approvalsDir, "p.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load("p"); err == nil {
		t.Error("Load through a symlinked state file should fail")
	}
	if err := s.Save("p", State{Hash: "h"}); err == nil {
		t.Error("Save over a symlinked state file should fail")
	}
	// The symlink itself must not be clobbered by the rejected Save.
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("state file should still be the symlink after rejected Save, lstat=%v err=%v", fi, err)
	}
}

func TestStoreLoadMalformedYAMLErrorNamesProfileAndRepair(t *testing.T) {
	dir := t.TempDir()
	s := NewFSStore(dir)
	path := filepath.Join(dir, "approvals", "p.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("hash: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load("p")
	if err == nil {
		t.Fatal("Load of malformed YAML should fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, `"p"`) {
		t.Errorf("error should name the profile, got %q", msg)
	}
	if !strings.Contains(msg, "approvals") || !strings.Contains(msg, "p.yaml") {
		t.Errorf("error should name the state file path, got %q", msg)
	}
	if !strings.Contains(msg, "delete") {
		t.Errorf("error should suggest a repair command, got %q", msg)
	}
}
