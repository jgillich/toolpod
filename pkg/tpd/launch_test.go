package tpd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgillich/tpd/internal/runtime"
)

func writeBuiltinShell(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "shell.yaml"), []byte("version: 1\nimage: myimg:latest\ncommand: [\"sh\"]\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLaunchDryRunPrintsSpec(t *testing.T) {
	dir := writeBuiltinShell(t)
	var out strings.Builder
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		DryRun:      true,
		ProfileDir:  dir,
		Workspace:   "/home/me/proj",
	}, &out)
	if res.Err != nil {
		t.Fatalf("Launch: %v", res.Err)
	}
	output := out.String()
	if !strings.Contains(output, "image: myimg:latest") {
		t.Errorf("dry-run output missing image; got:\n%s", output)
	}
	if !strings.Contains(output, "command:") {
		t.Errorf("dry-run output missing command; got:\n%s", output)
	}
	if !strings.Contains(output, "workspace:") {
		t.Errorf("dry-run output missing workspace; got:\n%s", output)
	}
}

func TestLaunchProfileNotFound(t *testing.T) {
	dir := t.TempDir()
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "nope",
		DryRun:      true,
		ProfileDir:  dir,
	}, &strings.Builder{})
	if res.Err == nil {
		t.Fatal("expected error for missing profile")
	}
	if res.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2 (profile error)", res.ExitCode)
	}
}

// writeBuiltinFragment writes a user fragment next to a user profile dir and
// returns the profile dir. User fragments load from the sibling fragments/ dir
// of the profile dir (see LoadProfiles).
func writeBuiltinFragment(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	fragDir := filepath.Join(filepath.Dir(dir), "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "pow.yaml"), []byte("version: 1\ntools:\n  powershell-core: latest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLaunchFragmentRejected(t *testing.T) {
	dir := writeBuiltinFragment(t)
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "pow",
		DryRun:      true,
		ProfileDir:  dir,
	}, &strings.Builder{})
	if res.Err == nil {
		t.Fatal("expected error launching a fragment")
	}
	if res.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2 (profile error)", res.ExitCode)
	}
	if !strings.Contains(res.Err.Error(), "fragment") {
		t.Errorf("error should explain fragments can't be launched, got: %v", res.Err)
	}
	if !strings.Contains(res.Err.Error(), "tpd init") {
		t.Errorf("error should point to the init path, got: %v", res.Err)
	}
}

func TestLaunchWithFakeRuntime(t *testing.T) {
	dir := writeBuiltinShell(t)
	fr := &runtime.FakeRuntime{ExitCode: 0}
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		DryRun:      false,
		ProfileDir:  dir,
		Runtime:     fr,
	}, &strings.Builder{})
	if res.Err != nil {
		t.Fatalf("Launch: %v", res.Err)
	}
	if fr.PreparedSpec == nil {
		t.Error("Prepare was not called")
	}
	if fr.RanSpec == nil {
		t.Error("Run was not called")
	}
}

func TestLaunchPrepareFails(t *testing.T) {
	dir := writeBuiltinShell(t)
	fr := &runtime.FakeRuntime{
		PrepareErr: fmt.Errorf("image pull failed"),
	}
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		DryRun:      false,
		ProfileDir:  dir,
		Runtime:     fr,
	}, &strings.Builder{})
	if res.Err == nil {
		t.Fatal("expected error from failed Prepare")
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3 (runtime error)", res.ExitCode)
	}
}

func TestLaunchRunFails(t *testing.T) {
	dir := writeBuiltinShell(t)
	fr := &runtime.FakeRuntime{
		RunErr: fmt.Errorf("container crashed"),
	}
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		DryRun:      false,
		ProfileDir:  dir,
		Runtime:     fr,
	}, &strings.Builder{})
	if res.Err == nil {
		t.Fatal("expected error from failed Run")
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
}

func TestLaunchPropagatesExitCode(t *testing.T) {
	dir := writeBuiltinShell(t)
	fr := &runtime.FakeRuntime{ExitCode: 42}
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		DryRun:      false,
		ProfileDir:  dir,
		Runtime:     fr,
	}, &strings.Builder{})
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if res.ExitCode != 42 {
		t.Errorf("ExitCode = %d, want 42 (profile exit code)", res.ExitCode)
	}
}

func TestLaunchOverridesBusAddressWhenDisabled(t *testing.T) {
	dir := writeBuiltinShell(t) // shell profile: no dbus config
	fr := &runtime.FakeRuntime{ExitCode: 0}
	var out strings.Builder
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		ProfileDir:  dir,
		Runtime:     fr,
	}, &out)
	if res.Err != nil {
		t.Fatalf("Launch: %v", res.Err)
	}
	if fr.RanSpec == nil {
		t.Fatal("Run not called")
	}
	if got, ok := fr.RanSpec.Env["DBUS_SESSION_BUS_ADDRESS"]; !ok || got != "" {
		t.Errorf("DBUS_SESSION_BUS_ADDRESS = %q, want empty (disabled)", got)
	}
}

func TestLaunchForwardsPullToPrepare(t *testing.T) {
	dir := writeBuiltinShell(t)
	fr := &runtime.FakeRuntime{ExitCode: 0}
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		ProfileDir:  dir,
		Runtime:     fr,
		Pull:        true,
	}, &strings.Builder{})
	if res.Err != nil {
		t.Fatalf("Launch: %v", res.Err)
	}
	if fr.PreparedSpec == nil {
		t.Fatal("Prepare was not called")
	}
	if !fr.PreparePull {
		t.Error("Prepare received pull=false, want true (LaunchOpts.Pull must thread through)")
	}
}
