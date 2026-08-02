package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSELinuxEnforcing(t *testing.T) {
	enforce := filepath.Join(t.TempDir(), "enforce")
	if err := os.WriteFile(enforce, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !selinuxEnforcing(enforce) {
		t.Error("enforce file containing 1 should report enforcing")
	}

	permissive := filepath.Join(t.TempDir(), "enforce")
	if err := os.WriteFile(permissive, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if selinuxEnforcing(permissive) {
		t.Error("enforce file containing 0 should report not enforcing")
	}

	if selinuxEnforcing(filepath.Join(t.TempDir(), "missing")) {
		t.Error("missing enforce file should report not enforcing")
	}
}

func TestSecurityOpts(t *testing.T) {
	enforcing := (&DockerRuntime{selinux: true}).securityOpts()
	if len(enforcing) != 1 || enforcing[0] != "label=disable" {
		t.Errorf("enforcing: securityOpts = %v, want [label=disable]", enforcing)
	}

	if opts := (&DockerRuntime{}).securityOpts(); opts != nil {
		t.Errorf("not enforcing: securityOpts = %v, want nil", opts)
	}
}
