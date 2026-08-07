package externalconsumer

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgillich/tpd/pkg/tpd"
)

// TestExternalSurfaceUsable drives an engine-free dry-run launch through the
// public API, proving an external consumer can use the CLI-facing knobs
// (ProfileName, ProfileDir, Workspace, DryRun) without naming any internal
// type. The demo profile mirrors what tpd's tests write for a minimal
// profile so the preview renders without a daemon.
func TestExternalSurfaceUsable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "demo.yaml"), []byte("version: 1\nimage: debian:13-slim\ncommand: [echo, hi]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	res := tpd.LaunchWithWriter(context.Background(), tpd.LaunchOpts{
		ProfileName: "demo",
		ProfileDir:  dir,
		Workspace:   "/tmp",
		DryRun:      true,
	}, &out)
	if res.Err != nil {
		t.Fatalf("dry-run launch through the public API failed: %v", res.Err)
	}
	if !strings.Contains(out.String(), "profile: demo") {
		t.Errorf("preview missing profile header, got:\n%s", out.String())
	}
}

// TestInternalSurfaceBlocked builds the tagged boundary files and asserts each
// build fails exactly the way the boundary requires: tpd.Spec (the removed
// alias for internal/runtime.Spec) is undefined, and importing
// internal/runtime is blocked by the internal/ rule.
func TestInternalSurfaceBlocked(t *testing.T) {
	cases := []struct {
		tag  string
		want string
	}{
		{"leakcheck", "undefined: tpd.Spec"},
		{"internalcheck", "use of internal package"},
	}
	for _, tc := range cases {
		t.Run(tc.tag, func(t *testing.T) {
			out, err := exec.Command("go", "build", "-tags", tc.tag, ".").CombinedOutput()
			if err == nil {
				t.Fatalf("build with -tags %s unexpectedly succeeded:\n%s", tc.tag, out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("build output missing %q:\n%s", tc.want, out)
			}
		})
	}
}
