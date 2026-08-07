package tpd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jgillich/tpd/internal/approval"
	"github.com/jgillich/tpd/internal/runtime"
	"github.com/jgillich/tpd/internal/workspace"
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

func TestLaunchDryRunUnknownMode(t *testing.T) {
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
	for _, want := range []string{
		"workspace:",
		"  host: /home/me/proj",
		"  target: <unknown>",
		"  mode: unknown",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("dry-run output missing %q; got:\n%s", want, output)
		}
	}
	for _, notWant := range []string{"mode: rootful", "mode: rootless", "/workspace"} {
		if strings.Contains(output, notWant) {
			t.Errorf("dry-run output must not claim %q; got:\n%s", notWant, output)
		}
	}
}

func TestLaunchDryRunNoRootfulRuntimeHome(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "hm.yaml", []byte("version: 1\nimage: myimg:latest\ncommand: [\"sh\"]\nmounts:\n  ~/.config/foo:\n    source: /host/foo\n"))
	var out strings.Builder
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "hm",
		DryRun:      true,
		ProfileDir:  dir,
	}, &out)
	if res.Err != nil {
		t.Fatalf("Launch: %v", res.Err)
	}
	output := out.String()
	if !strings.Contains(output, "mounts:") {
		t.Fatalf("expected mounts section; got:\n%s", output)
	}
	// ~ in mount targets expands against runtimeHome; without a daemon the
	// dry-run must not claim any home — not the rootful /root and not the
	// host home (which would silently assert a rootless container).
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, " <- ") && strings.HasPrefix(trimmed, "/") {
			t.Errorf("mount target claims a concrete home: %q", trimmed)
		}
	}
	if !strings.Contains(output, "~/.config/foo <- /host/foo") {
		t.Errorf("tilde mount target should stay literal in dry-run; got:\n%s", output)
	}
}

func TestLaunchDryRunFullStaticSpec(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "full.yaml", []byte(`version: 1
image: myimg:latest
command: ["sh"]
packages: [curl]
repos:
  docker:
    extrepo: docker
files:
  /etc/motd:
    content: hello world
    mode: 0644
tty: true
mounts:
  /data:
    source: /host/data
  /run/registry/registry.sock:
    service: registry
    socket: registry
caches:
  npm:
    - /root/.npm
tools:
  node: "20"
ports:
  "8080": {}
  "7000":
    host: 7000
    host_ip: 0.0.0.0
devices:
  /dev/fuse: {}
environment:
  FOO: bar
services:
  registry:
    image: registry:2
    command: ["registry"]
    exposes:
      registry: /run/registry/registry.sock
`))
	var out strings.Builder
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName:   "full",
		DryRun:        true,
		ProfileDir:    dir,
		Workspace:     "/home/me/proj",
		PortAllocator: fakePortAllocator(),
	}, &out)
	if res.Err != nil {
		t.Fatalf("Launch: %v", res.Err)
	}
	output := out.String()
	for _, want := range []string{
		"image: myimg:latest",
		"command: [sh]",
		"packages: [curl]",
		"repos:",
		"  docker: extrepo docker",
		"files:",
		"  /etc/motd:",
		"    mode: 0644",
		`    content: "hello world"`,
		"tty: true",
		"workspace:",
		"  host: /home/me/proj",
		"  target: <unknown>",
		"mounts:",
		"  /data <- /host/data (ro)",
		"  /run/registry/registry.sock <- service:registry socket:registry",
		"caches:",
		"  tpd-cache-npm -> /root/.npm",
		"tools:",
		"  node: 20",
		"ports:",
		"  8080/tcp -> 127.0.0.1:40001",
		"  7000/tcp -> 0.0.0.0:7000",
		"devices:",
		"  /dev/fuse <- /dev/fuse (rwm)",
		"services:",
		"  registry:",
		"    image: registry:2",
		"    command: [registry]",
		"    exposes:",
		"      registry: /run/registry/registry.sock",
		"environment:",
		`  FOO: "bar"`,
		"  mode: unknown",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("dry-run output missing %q; got:\n%s", want, output)
		}
	}
	// Runtime-only values must stay out of the static preview.
	for _, notWant := range []string{
		"tpd/packages",
		"DBUS_SESSION_BUS_ADDRESS",
		"/tmp/tpd-svc-",
		"mode: rootful",
		"mode: rootless",
	} {
		if strings.Contains(output, notWant) {
			t.Errorf("dry-run output must not contain runtime-only %q; got:\n%s", notWant, output)
		}
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

// progressRuntime wraps FakeRuntime to emit Prepare status lines so the
// default progress wiring (spinner -> stderrProgress -> diagnostics writer)
// can be asserted end to end.
type progressRuntime struct {
	*runtime.FakeRuntime
}

func (r *progressRuntime) Prepare(ctx context.Context, spec runtime.Spec, w runtime.ProgressWriter, pull bool) (string, error) {
	w.WriteProgress("pull: myimg:latest")
	w.WriteProgress("build: tpd/packages:abc")
	return r.FakeRuntime.Prepare(ctx, spec, w, pull)
}

func TestLaunchProgressToStderrWriter(t *testing.T) {
	dir := writeBuiltinShell(t)
	fr := &progressRuntime{FakeRuntime: &runtime.FakeRuntime{ExitCode: 0}}
	var diag, preview strings.Builder
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		ProfileDir:  dir,
		Runtime:     fr,
		Stderr:      &diag,
	}, &preview)
	if res.Err != nil {
		t.Fatalf("Launch: %v", res.Err)
	}
	for _, want := range []string{"pull: myimg:latest", "build: tpd/packages:abc"} {
		if !strings.Contains(diag.String(), want) {
			t.Errorf("progress %q should go to the diagnostics writer, got %q", want, diag.String())
		}
	}
	if preview.Len() != 0 {
		t.Errorf("preview writer must not receive progress, got %q", preview.String())
	}
}

func TestLaunchVerbosePreviewToWriter(t *testing.T) {
	dir := writeBuiltinShell(t)
	fr := &runtime.FakeRuntime{ExitCode: 0}
	var diag, preview strings.Builder
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		ProfileDir:  dir,
		Runtime:     fr,
		Verbose:     true,
		Stderr:      &diag,
	}, &preview)
	if res.Err != nil {
		t.Fatalf("Launch: %v", res.Err)
	}
	if !strings.Contains(preview.String(), "profile: shell") {
		t.Errorf("verbose preview should go to the LaunchWithWriter writer, got %q", preview.String())
	}
	if strings.Contains(diag.String(), "profile: shell") {
		t.Errorf("preview must not leak into the diagnostics writer, got %q", diag.String())
	}
}

func TestLaunchStopServicesWarningToStderrWriter(t *testing.T) {
	rt := &orderRuntime{FakeRuntime: &runtime.FakeRuntime{
		PrepareImage:    "test-image",
		StopServicesErr: fmt.Errorf("stop failed"),
	}}
	rt.ServiceBindings = runtime.ServiceBindings{
		Sockets: map[string]string{"registry/registry": "/tmp/test-sock"},
		Release: rt.release,
	}
	var diag, preview strings.Builder
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "svc",
		ProfileDir:  writeServiceProfile(t),
		Runtime:     rt,
		Stderr:      &diag,
	}, &preview)
	if res.Err != nil {
		t.Fatalf("Launch: %v", res.Err)
	}
	if !strings.Contains(diag.String(), "tpd: warning: stop services: stop failed") {
		t.Errorf("stop-services warning should go to the diagnostics writer, got %q", diag.String())
	}
	if preview.Len() != 0 {
		t.Errorf("preview writer must not receive diagnostics, got %q", preview.String())
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

// writeServiceProfile writes a profile that declares a service and a
// service-socket mount pointing at it, so spec.Services is non-empty on launch.
func writeServiceProfile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "svc.yaml"), []byte(`version: 1
image: myimg:latest
command: ["sh"]
services:
  registry:
    image: registry:2
    command: ["registry"]
    exposes:
      registry: /run/registry/registry.sock
mounts:
  /run/registry/registry.sock:
    service: registry
    socket: registry
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// orderRuntime wraps FakeRuntime to record the launch-time call sequence so
// the service lifecycle order can be asserted exactly.
type orderRuntime struct {
	*runtime.FakeRuntime
	calls []string
	// startedMounts is a deep copy of the spec handed to StartServices, taken
	// before the launch rewrites service-socket mounts in place.
	startedMounts []runtime.MountSpec
}

func (r *orderRuntime) record(call string) {
	r.calls = append(r.calls, call)
}

func (r *orderRuntime) Prepare(ctx context.Context, spec runtime.Spec, w runtime.ProgressWriter, pull bool) (string, error) {
	r.record("prepare")
	return r.FakeRuntime.Prepare(ctx, spec, w, pull)
}

func (r *orderRuntime) CreateContainer(ctx context.Context, spec runtime.Spec) (runtime.CreateResult, error) {
	r.record("create-container")
	return r.FakeRuntime.CreateContainer(ctx, spec)
}

func (r *orderRuntime) RunContainer(ctx context.Context, spec runtime.Spec, created runtime.CreateResult) (int, error) {
	r.record("run-container")
	return r.FakeRuntime.RunContainer(ctx, spec, created)
}

func (r *orderRuntime) StartServices(ctx context.Context, spec runtime.Spec, w runtime.ProgressWriter, pull bool) (runtime.ServiceBindings, error) {
	r.record("start-services")
	r.startedMounts = append([]runtime.MountSpec(nil), spec.Mounts...)
	return r.FakeRuntime.StartServices(ctx, spec, w, pull)
}

func (r *orderRuntime) StopServices(ctx context.Context, spec runtime.Spec) error {
	r.record("stop-services")
	return r.FakeRuntime.StopServices(ctx, spec)
}

func (r *orderRuntime) ConnectContainerToNetwork(ctx context.Context, containerID, networkName string, aliases []string) error {
	r.record("connect")
	return r.FakeRuntime.ConnectContainerToNetwork(ctx, containerID, networkName, aliases)
}

func (r *orderRuntime) RemoveContainer(ctx context.Context, containerID string) error {
	r.record("remove")
	return r.FakeRuntime.RemoveContainer(ctx, containerID)
}

func (r *orderRuntime) release() {
	r.record("release")
}

func launchService(t *testing.T, rt runtime.Runtime) Result {
	t.Helper()
	return LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "svc",
		DryRun:      false,
		ProfileDir:  writeServiceProfile(t),
		Runtime:     rt,
	}, &strings.Builder{})
}

func wantCallOrder(t *testing.T, rt *orderRuntime, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(rt.calls, want) {
		t.Errorf("runtime call order = %v, want %v", rt.calls, want)
	}
}

func TestLaunchPropagatesDetectModeFailure(t *testing.T) {
	dir := writeBuiltinShell(t)
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/tpd-test.sock")
	rt, err := runtime.NewDockerRuntime()
	if err != nil {
		t.Fatal(err)
	}
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		ProfileDir:  dir,
		Runtime:     rt,
	}, &strings.Builder{})
	if res.Err == nil {
		t.Fatal("expected a runtime error when DetectMode cannot query the engine")
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3 (runtime error)", res.ExitCode)
	}
	if !strings.Contains(res.Err.Error(), "detect engine mode") {
		t.Errorf("error should wrap the DetectMode failure, got: %v", res.Err)
	}
}

// modeRuntime wraps FakeRuntime with a modeDetector, standing in for a custom
// runtime that can report the engine launch mode.
type modeRuntime struct {
	*runtime.FakeRuntime
	mode workspace.Mode
	err  error
}

func (r *modeRuntime) DetectMode(ctx context.Context) (workspace.Mode, error) {
	return r.mode, r.err
}

func TestLaunchCustomRuntimeModeDetectorHonored(t *testing.T) {
	dir := writeBuiltinShell(t)
	rt := &modeRuntime{FakeRuntime: &runtime.FakeRuntime{ExitCode: 0}, mode: workspace.ModeRootless}
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		ProfileDir:  dir,
		Runtime:     rt,
	}, &strings.Builder{})
	if res.Err != nil {
		t.Fatalf("Launch: %v", res.Err)
	}
	if rt.RanSpec == nil {
		t.Fatal("Run was not called")
	}
	if rt.RanSpec.Workspace.Mode != workspace.ModeRootless {
		t.Errorf("mode = %s, want rootless (custom runtime modeDetector must be honored)", rt.RanSpec.Workspace.Mode)
	}
}

func TestLaunchCustomRuntimeDetectModeFailure(t *testing.T) {
	dir := writeBuiltinShell(t)
	rt := &modeRuntime{FakeRuntime: &runtime.FakeRuntime{}, err: fmt.Errorf("engine gone")}
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		ProfileDir:  dir,
		Runtime:     rt,
	}, &strings.Builder{})
	if res.Err == nil {
		t.Fatal("expected a runtime error when a custom runtime's DetectMode fails")
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3 (runtime error)", res.ExitCode)
	}
	if !strings.Contains(res.Err.Error(), "detect engine mode") {
		t.Errorf("error should wrap the custom DetectMode failure, got: %v", res.Err)
	}
}

// recordingModeRuntime records whether DetectMode was queried, to prove a
// dry-run stays engine-free even when a detector is injectable.
type recordingModeRuntime struct {
	*runtime.FakeRuntime
	detected bool
}

func (r *recordingModeRuntime) DetectMode(ctx context.Context) (workspace.Mode, error) {
	r.detected = true
	return workspace.ModeRootless, nil
}

func TestLaunchDryRunNeverQueriesModeDetector(t *testing.T) {
	dir := writeBuiltinShell(t)
	rt := &recordingModeRuntime{FakeRuntime: &runtime.FakeRuntime{}}
	var out strings.Builder
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		DryRun:      true,
		ProfileDir:  dir,
		Runtime:     rt,
	}, &out)
	if res.Err != nil {
		t.Fatalf("Launch: %v", res.Err)
	}
	if rt.detected {
		t.Error("dry-run must not query a modeDetector")
	}
	if !strings.Contains(out.String(), "mode: unknown") {
		t.Errorf("dry-run should render mode unknown, got:\n%s", out.String())
	}
}

func TestParseToolFlag(t *testing.T) {
	tests := []struct {
		in   string
		name string
		ver  string
		err  bool
	}{
		{in: "node", name: "node", ver: "latest"},
		{in: "node=20", name: "node", ver: "20"},
		{in: "node=20.11.1", name: "node", ver: "20.11.1"},
		{in: "", err: true},
		{in: "=", err: true},
		{in: "name=", err: true},
		{in: "=v", err: true},
	}
	for _, tc := range tests {
		name, ver, err := parseToolFlag(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("parseToolFlag(%q) = (%q, %q, nil), want error", tc.in, name, ver)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseToolFlag(%q): %v", tc.in, err)
			continue
		}
		if name != tc.name || ver != tc.ver {
			t.Errorf("parseToolFlag(%q) = (%q, %q), want (%q, %q)", tc.in, name, ver, tc.name, tc.ver)
		}
	}
}

func TestLaunchExtraToolsMalformed(t *testing.T) {
	dir := writeBuiltinShell(t)
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		DryRun:      true,
		ProfileDir:  dir,
		ExtraTools:  []string{"node="},
	}, &strings.Builder{})
	if res.Err == nil {
		t.Fatal("expected error for malformed ExtraTools entry")
	}
	if res.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2 (config error)", res.ExitCode)
	}
	if !strings.Contains(res.Err.Error(), "malformed tool flag") {
		t.Errorf("error should explain the malformed flag, got: %v", res.Err)
	}
}

func TestLaunchExtraToolsParsed(t *testing.T) {
	dir := writeBuiltinShell(t)
	fr := &runtime.FakeRuntime{ExitCode: 0}
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		ProfileDir:  dir,
		Runtime:     fr,
		ExtraTools:  []string{"node", "python=3.12"},
	}, &strings.Builder{})
	if res.Err != nil {
		t.Fatalf("Launch: %v", res.Err)
	}
	if fr.PreparedSpec == nil {
		t.Fatal("Prepare was not called")
	}
	if got := fr.PreparedSpec.Tools["node"].Version; got != "latest" {
		t.Errorf("node version = %q, want latest (bare name)", got)
	}
	if got := fr.PreparedSpec.Tools["python"].Version; got != "3.12" {
		t.Errorf("python version = %q, want 3.12", got)
	}
}

func TestDefaultApprovalDirAbsolute(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/xdg")
	t.Setenv("HOME", "/home/user")
	if got := defaultApprovalDir(); got != "/xdg/tpd" {
		t.Errorf("with absolute XDG_DATA_HOME = %q, want /xdg/tpd", got)
	}
}

func TestDefaultApprovalDirFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/user")
	if got := defaultApprovalDir(); got != "/home/user/.local/share/tpd" {
		t.Errorf("want /home/user/.local/share/tpd, got %q", got)
	}
}

func TestDefaultApprovalDirRelativeXDGIgnored(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "rel/xdg")
	t.Setenv("HOME", "/home/user")
	if got := defaultApprovalDir(); got != "/home/user/.local/share/tpd" {
		t.Errorf("relative XDG_DATA_HOME should be ignored, got %q", got)
	}
}

func TestDefaultApprovalDirNoHomeAbsolute(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "")
	if got := defaultApprovalDir(); !filepath.IsAbs(got) {
		t.Errorf("fallback %q is not absolute", got)
	}
}

func TestLaunchWithServices(t *testing.T) {
	rt := &orderRuntime{FakeRuntime: &runtime.FakeRuntime{
		PrepareImage: "test-image",
		CreateResult: runtime.CreateResult{ContainerID: "container-1"},
	}}
	rt.ServiceBindings = runtime.ServiceBindings{
		Sockets: map[string]string{"registry/registry": "/tmp/test-sock"},
		Network: "tpd-services",
		Release: rt.release,
	}
	res := launchService(t, rt)
	if res.Err != nil {
		t.Fatalf("Launch: %v", res.Err)
	}
	wantCallOrder(t, rt, "prepare", "start-services", "create-container", "connect", "release", "run-container", "stop-services")
	if rt.ConnectedContainerID != "container-1" {
		t.Errorf("ConnectContainerToNetwork received container %q, want %q", rt.ConnectedContainerID, "container-1")
	}
	if rt.ConnectedNetworkName != "tpd-services" {
		t.Errorf("ConnectContainerToNetwork received network %q, want tpd-services", rt.ConnectedNetworkName)
	}
	if rt.ConnectedNetworkAliases != nil {
		t.Errorf("ConnectContainerToNetwork aliases = %v, want nil", rt.ConnectedNetworkAliases)
	}
	if rt.StartServicesSpec == nil {
		t.Error("StartServices was not called")
	}
	if rt.CreatedSpec == nil {
		t.Error("CreateContainer was not called")
	}
	if rt.RanSpec == nil {
		t.Error("RunContainer was not called")
	}
	if rt.StopServicesSpec == nil {
		t.Error("StopServices was not called")
	}
	if rt.CreatedSpec.Labels[runtime.UsesServiceLabel] != "registry" {
		t.Errorf("main container should carry tpd.uses-service=registry, got %q", rt.CreatedSpec.Labels[runtime.UsesServiceLabel])
	}
	if len(rt.StartServicesSpec.Services) != 1 {
		t.Fatalf("StartServices received %d services, want 1", len(rt.StartServicesSpec.Services))
	}
	svc := rt.StartServicesSpec.Services[0]
	if svc.Hash == "" {
		t.Error("buildSpec should populate ServiceSpec.Hash")
	}
	if svc.Hash != svc.Labels[runtime.ServiceHashLabel] {
		t.Errorf("ServiceSpec.Hash %q != ServiceHashLabel %q", svc.Hash, svc.Labels[runtime.ServiceHashLabel])
	}
	var started runtime.MountSpec
	for _, m := range rt.startedMounts {
		if m.Target == "/run/registry/registry.sock" {
			started = m
		}
	}
	if started.Service != "registry" || started.Socket != "registry" || started.Source != "" {
		t.Errorf("service-socket mount handed to StartServices = %+v, want unresolved service/socket", started)
	}
	var sock runtime.MountSpec
	for _, m := range rt.CreatedSpec.Mounts {
		if m.Target == "/run/registry/registry.sock" {
			sock = m
		}
	}
	if sock.Source != "/tmp/test-sock" || sock.Service != "" || sock.Socket != "" {
		t.Errorf("service mount not rewritten to host path: %+v", sock)
	}
	for _, m := range rt.RanSpec.Mounts {
		if m.Target == "/run/registry/registry.sock" && m.Source != "/tmp/test-sock" {
			t.Errorf("RunContainer should receive the rewritten service mount: %+v", m)
		}
	}
	if len(rt.CreatedSpec.SocketPaths) != 1 || rt.CreatedSpec.SocketPaths[0] != "/run/registry/registry.sock" {
		t.Errorf("SocketPaths = %v, want [/run/registry/registry.sock]", rt.CreatedSpec.SocketPaths)
	}
}

func TestLaunchStopsServicesOnCreateError(t *testing.T) {
	rt := &orderRuntime{FakeRuntime: &runtime.FakeRuntime{
		PrepareImage: "test-image",
		CreateErr:    fmt.Errorf("create failed"),
	}}
	rt.ServiceBindings = runtime.ServiceBindings{
		Sockets: map[string]string{"registry/registry": "/tmp/x"},
		Release: rt.release,
	}
	res := launchService(t, rt)
	if res.Err == nil {
		t.Fatal("expected error from failed CreateContainer")
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3 (runtime error)", res.ExitCode)
	}
	wantCallOrder(t, rt, "prepare", "start-services", "create-container", "release", "stop-services")
	if rt.StopServicesSpec == nil {
		t.Error("StopServices was not called after CreateContainer failed")
	}
	if rt.RanSpec != nil {
		t.Error("RunContainer should not run when CreateContainer fails")
	}
}

func TestLaunchStopsServicesOnRunError(t *testing.T) {
	rt := &orderRuntime{FakeRuntime: &runtime.FakeRuntime{
		PrepareImage: "test-image",
		RunErr:       fmt.Errorf("run failed"),
	}}
	rt.ServiceBindings = runtime.ServiceBindings{
		Sockets: map[string]string{"registry/registry": "/tmp/x"},
		Network: "tpd-services",
		Release: rt.release,
	}
	res := launchService(t, rt)
	if res.Err == nil {
		t.Fatal("expected error from failed RunContainer")
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3 (runtime error)", res.ExitCode)
	}
	wantCallOrder(t, rt, "prepare", "start-services", "create-container", "connect", "release", "run-container", "stop-services")
	if rt.StopServicesSpec == nil {
		t.Error("StopServices was not called after RunContainer failed")
	}
}

func TestLaunchServiceNetworkConnectError(t *testing.T) {
	rt := &orderRuntime{FakeRuntime: &runtime.FakeRuntime{
		PrepareImage: "test-image",
		CreateResult: runtime.CreateResult{ContainerID: "container-1"},
		ConnectErr:   fmt.Errorf("network connect failed"),
	}}
	rt.ServiceBindings = runtime.ServiceBindings{
		Sockets: map[string]string{"registry/registry": "/tmp/x"},
		Network: "tpd-services",
		Release: rt.release,
	}
	res := launchService(t, rt)
	if res.Err == nil {
		t.Fatal("expected error from failed ConnectContainerToNetwork")
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3 (runtime error)", res.ExitCode)
	}
	if !strings.Contains(res.Err.Error(), "connect service network") {
		t.Errorf("error should wrap connect service network, got: %v", res.Err)
	}
	if rt.RanSpec != nil {
		t.Error("RunContainer should not run when network attach fails")
	}
	if rt.RemovedContainerID != "container-1" {
		t.Errorf("RemoveContainer received %q, want %q", rt.RemovedContainerID, "container-1")
	}
	wantCallOrder(t, rt, "prepare", "start-services", "create-container", "connect", "remove", "release", "stop-services")
	if rt.StopServicesSpec == nil {
		t.Error("StopServices was not called after network attach failed")
	}
}

func TestLaunchServiceNetworkConnectRemoveError(t *testing.T) {
	rt := &orderRuntime{FakeRuntime: &runtime.FakeRuntime{
		PrepareImage: "test-image",
		CreateResult: runtime.CreateResult{ContainerID: "container-1"},
		ConnectErr:   fmt.Errorf("network connect failed"),
		RemoveErr:    fmt.Errorf("remove failed"),
	}}
	rt.ServiceBindings = runtime.ServiceBindings{
		Sockets: map[string]string{"registry/registry": "/tmp/x"},
		Network: "tpd-services",
		Release: rt.release,
	}
	stderr := captureStderr(t, func() {
		res := launchService(t, rt)
		if res.Err == nil {
			t.Fatal("expected error from failed ConnectContainerToNetwork")
		}
		if res.ExitCode != 3 {
			t.Errorf("ExitCode = %d, want 3 (runtime error)", res.ExitCode)
		}
		if !strings.Contains(res.Err.Error(), "connect service network") {
			t.Errorf("attachment failure should stay primary, got: %v", res.Err)
		}
		if rt.RanSpec != nil {
			t.Error("RunContainer should not run when network attach fails")
		}
		if rt.RemovedContainerID != "container-1" {
			t.Errorf("RemoveContainer received %q, want %q", rt.RemovedContainerID, "container-1")
		}
	})
	if !strings.Contains(stderr, "warning") || !strings.Contains(stderr, "remove container") {
		t.Errorf("cleanup failure should be emitted as a warning, stderr: %q", stderr)
	}
	wantCallOrder(t, rt, "prepare", "start-services", "create-container", "connect", "remove", "release", "stop-services")
	if rt.StopServicesSpec == nil {
		t.Error("StopServices was not called after network attach failed")
	}
}

func TestLaunchServiceNetworkNoServices(t *testing.T) {
	dir := writeBuiltinShell(t)
	rt := &orderRuntime{FakeRuntime: &runtime.FakeRuntime{ExitCode: 0}}
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName: "shell",
		DryRun:      false,
		ProfileDir:  dir,
		Runtime:     rt,
	}, &strings.Builder{})
	if res.Err != nil {
		t.Fatalf("Launch: %v", res.Err)
	}
	if rt.ConnectedNetworkName != "" {
		t.Errorf("no-service launch connected to %q, want none", rt.ConnectedNetworkName)
	}
	if rt.RemovedContainerID != "" {
		t.Errorf("no-service launch removed container %q, want none", rt.RemovedContainerID)
	}
	wantCallOrder(t, rt, "prepare", "create-container", "run-container")
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns the
// captured output, so cleanup-warning paths can be asserted.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() {
		os.Stderr = old
		w.Close()
		r.Close()
	}()
	fn()
	w.Close()
	data, _ := io.ReadAll(r)
	return string(data)
}

func TestLaunchStopsServicesOnStartServicesError(t *testing.T) {
	rt := &orderRuntime{FakeRuntime: &runtime.FakeRuntime{
		PrepareImage:     "test-image",
		StartServicesErr: fmt.Errorf("service failed to start"),
	}}
	rt.ServiceBindings = runtime.ServiceBindings{
		Sockets: map[string]string{"registry/registry": "/tmp/x"},
		Release: rt.release,
	}
	res := launchService(t, rt)
	if res.Err == nil {
		t.Fatal("expected error from failed StartServices")
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3 (runtime error)", res.ExitCode)
	}
	if !strings.Contains(res.Err.Error(), "start services") {
		t.Errorf("error should wrap start services, got: %v", res.Err)
	}
	wantCallOrder(t, rt, "prepare", "start-services", "stop-services")
	if rt.StopServicesSpec == nil {
		t.Error("StopServices was not called after StartServices failed")
	}
	if rt.CreatedSpec != nil || rt.RanSpec != nil {
		t.Error("CreateContainer/RunContainer should not run when StartServices fails")
	}
}

func TestLaunchApprovalNonInteractiveErrors(t *testing.T) {
	dir := t.TempDir()
	// User profile extending core/opencode inherits core/mise's ~/.config/mise
	// mount (Namespace "core") → gated. The user profile's own Namespace is ""
	// but the inherited mount stays attributed to core/mise, so the gate fires.
	writeProfile(t, dir, "myagent.yaml", []byte("version: 1\nextends: core/opencode\n"))
	opts := LaunchOpts{
		ProfileName:   "myagent",
		ProfileDir:    dir,
		DryRun:        true,
		In:            &bytes.Buffer{},
		IsTTY:         func(io.Reader) bool { return false },
		ApprovalStore: approval.NewFSStore(t.TempDir()),
	}
	res := LaunchWithWriter(context.Background(), opts, &bytes.Buffer{})
	if res.Err == nil || res.ExitCode != 2 {
		t.Fatalf("expected exit 2 for unapproved non-interactive, got %+v", res)
	}
}

func TestLaunchApprovalAssumeYesPersistsAndProceeds(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "myagent.yaml", []byte("version: 1\nextends: core/opencode\n"))
	// Fixture guard: a dry-run without --yes must error, proving the gate
	// fires. If the embedded catalog loses core/mise's mount, this fails
	// loudly instead of the test silently passing as a no-op.
	guard := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName:   "myagent",
		ProfileDir:    dir,
		DryRun:        true,
		In:            &bytes.Buffer{},
		IsTTY:         func(io.Reader) bool { return false },
		ApprovalStore: approval.NewFSStore(t.TempDir()),
	}, &bytes.Buffer{})
	if guard.Err == nil || guard.ExitCode != 2 {
		t.Fatalf("fixture guard: expected exit 2 for unapproved dry-run (embedded catalog changed?), got %+v", guard)
	}
	storeDir := t.TempDir()
	store := approval.NewFSStore(storeDir)
	opts := LaunchOpts{
		ProfileName:   "myagent",
		ProfileDir:    dir,
		DryRun:        true,
		In:            &bytes.Buffer{},
		IsTTY:         func(io.Reader) bool { return false },
		ApprovalStore: store,
		AssumeYes:     true,
	}
	res := LaunchWithWriter(context.Background(), opts, &bytes.Buffer{})
	if res.Err != nil {
		t.Fatalf("AssumeYes dry-run should succeed, got %v", res.Err)
	}
	// dry-run --yes uses the ephemeral store, so no state file is written.
	// The user profile myagent resolves to FullName "myagent" (Namespace ""),
	// so the would-be state file is approvals/myagent.yaml.
	if _, err := os.Stat(filepath.Join(storeDir, "approvals", "myagent.yaml")); !os.IsNotExist(err) {
		t.Errorf("dry-run --yes should not persist, file exists or err=%v", err)
	}
}

func TestLaunchApprovalInteractiveApprove(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "myagent.yaml", []byte("version: 1\nextends: core/opencode\n"))
	fr := &runtime.FakeRuntime{ExitCode: 0}
	opts := LaunchOpts{
		ProfileName:   "myagent",
		ProfileDir:    dir,
		In:            &bytes.Buffer{},
		IsTTY:         func(io.Reader) bool { return true },
		ApprovalStore: approval.NewFSStore(t.TempDir()),
		ApprovalPrompt: func(req approval.PromptRequest, stdin io.Reader, stdout io.Writer) (map[string]map[string]bool, error) {
			choices := map[string]map[string]bool{}
			for _, it := range req.Items {
				set, ok := choices[it.Field]
				if !ok {
					set = map[string]bool{}
					choices[it.Field] = set
				}
				set[it.Key] = true
			}
			return choices, nil
		},
		Runtime: fr,
	}
	res := LaunchWithWriter(context.Background(), opts, &bytes.Buffer{})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("interactive approve should launch, got %+v", res)
	}
	if fr.RanSpec == nil || len(fr.RanSpec.Mounts) == 0 {
		t.Errorf("approved mounts should survive filtering (RunSpec mounts = %+v)", fr.RanSpec)
	}
}

func TestLaunchApprovalInteractiveDeny(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "myagent.yaml", []byte("version: 1\nextends: core/opencode\n"))
	fr := &runtime.FakeRuntime{ExitCode: 0}
	opts := LaunchOpts{
		ProfileName:   "myagent",
		ProfileDir:    dir,
		In:            &bytes.Buffer{},
		IsTTY:         func(io.Reader) bool { return true },
		ApprovalStore: approval.NewFSStore(t.TempDir()),
		ApprovalPrompt: func(req approval.PromptRequest, stdin io.Reader, stdout io.Writer) (map[string]map[string]bool, error) {
			choices := map[string]map[string]bool{}
			for _, it := range req.Items {
				set, ok := choices[it.Field]
				if !ok {
					set = map[string]bool{}
					choices[it.Field] = set
				}
				set[it.Key] = false
			}
			return choices, nil
		},
		Runtime: fr,
	}
	res := LaunchWithWriter(context.Background(), opts, &bytes.Buffer{})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("interactive deny should launch (drop-and-continue), got %+v", res)
	}
	if fr.RanSpec != nil && len(fr.RanSpec.Mounts) != 0 {
		t.Errorf("denied mounts should be dropped from the run spec, got %d mounts", len(fr.RanSpec.Mounts))
	}
}

func TestLaunchApprovalAssumeNoPersistsAndProceeds(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "myagent.yaml", []byte("version: 1\nextends: core/opencode\n"))
	storeDir := t.TempDir()
	store := approval.NewFSStore(storeDir)
	fr := &runtime.FakeRuntime{ExitCode: 0}
	opts := LaunchOpts{
		ProfileName:   "myagent",
		ProfileDir:    dir,
		In:            &bytes.Buffer{},
		IsTTY:         func(io.Reader) bool { return false },
		ApprovalStore: store,
		AssumeNo:      true,
		Runtime:       fr,
	}
	res := LaunchWithWriter(context.Background(), opts, &bytes.Buffer{})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("--no should launch with denied fields dropped, got %+v", res)
	}
	// --no persists: the state file exists with the mounts field present
	// but empty (all denied).
	data, err := os.ReadFile(filepath.Join(storeDir, "approvals", "myagent.yaml"))
	if err != nil {
		t.Fatalf("--no should persist state: %v", err)
	}
	if !bytes.Contains(data, []byte("mounts:")) {
		t.Errorf("state should contain the mounts field (present, all denied):\n%s", data)
	}
	if fr.RanSpec != nil && len(fr.RanSpec.Mounts) != 0 {
		t.Errorf("denied mounts should be dropped, got %d mounts", len(fr.RanSpec.Mounts))
	}
}

func TestLaunchApprovalPartialPromptFailsClosed(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "myagent.yaml", []byte("version: 1\nextends: core/opencode\n"))
	storeDir := t.TempDir()
	store := approval.NewFSStore(storeDir)
	opts := LaunchOpts{
		ProfileName:   "myagent",
		ProfileDir:    dir,
		In:            &bytes.Buffer{},
		IsTTY:         func(io.Reader) bool { return true },
		ApprovalStore: store,
		ApprovalPrompt: func(req approval.PromptRequest, stdin io.Reader, stdout io.Writer) (map[string]map[string]bool, error) {
			if len(req.Items) < 2 {
				t.Fatalf("fixture must produce at least 2 gated items, got %d (embedded catalog changed?)", len(req.Items))
			}
			// Decide only the first item; leave the rest undecided.
			it := req.Items[0]
			return map[string]map[string]bool{it.Field: {it.Key: true}}, nil
		},
		Runtime: &runtime.FakeRuntime{ExitCode: 0},
	}
	res := LaunchWithWriter(context.Background(), opts, &bytes.Buffer{})
	if res.Err == nil || res.ExitCode != 2 {
		t.Fatalf("partial prompt should fail closed with exit 2, got %+v", res)
	}
	if _, err := os.Stat(filepath.Join(storeDir, "approvals", "myagent.yaml")); !os.IsNotExist(err) {
		t.Errorf("partial prompt should not persist state")
	}
}

func TestLaunchMalformedApprovalStateFailsClosed(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "myagent.yaml", []byte("version: 1\nextends: core/opencode\n"))
	storeDir := t.TempDir()
	store := approval.NewFSStore(storeDir)
	if err := os.MkdirAll(filepath.Join(storeDir, "approvals"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "approvals", "myagent.yaml"), []byte("hash: [broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := LaunchWithWriter(context.Background(), LaunchOpts{
		ProfileName:   "myagent",
		ProfileDir:    dir,
		DryRun:        true,
		ApprovalStore: store,
	}, &bytes.Buffer{})
	if res.Err == nil || res.ExitCode != 2 {
		t.Fatalf("expected exit 2 for corrupt approval state, got %+v", res)
	}
	msg := res.Err.Error()
	if !strings.Contains(msg, "myagent") {
		t.Errorf("error should name the profile, got %q", msg)
	}
	if !strings.Contains(msg, "approvals") || !strings.Contains(msg, "myagent.yaml") {
		t.Errorf("error should name the state file path, got %q", msg)
	}
	if !strings.Contains(msg, "delete") {
		t.Errorf("error should suggest a repair command, got %q", msg)
	}
}

func TestLaunchConcurrentApprovalRunsSerialized(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "myagent.yaml", []byte("version: 1\nextends: core/opencode\n"))
	storeDir := t.TempDir()
	store := approval.NewFSStore(storeDir)
	opts := LaunchOpts{
		ProfileName:   "myagent",
		ProfileDir:    dir,
		In:            &bytes.Buffer{},
		IsTTY:         func(io.Reader) bool { return false },
		ApprovalStore: store,
		AssumeYes:     true,
	}
	var wg sync.WaitGroup
	results := make([]Result, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o := opts
			o.Runtime = &runtime.FakeRuntime{ExitCode: 0}
			results[i] = LaunchWithWriter(context.Background(), o, &bytes.Buffer{})
		}(i)
	}
	wg.Wait()
	// The approval lock is non-blocking: one launch wins and commits its
	// approval; a loser fails fast with the contention error instead of
	// silently hanging behind the other's prompt.
	ok := 0
	for i, r := range results {
		if r.Err == nil && r.ExitCode == 0 {
			ok++
			continue
		}
		if r.Err == nil || r.ExitCode != 2 || !strings.Contains(r.Err.Error(), "another tpd process is awaiting approval") {
			t.Errorf("concurrent launch %d failed: %+v", i, r)
		}
	}
	if ok == 0 {
		t.Fatal("at least one concurrent launch should win the approval lock")
	}
	// The shared state file must end up complete and loadable (no torn
	// writes or lost approvals).
	if _, err := store.Load("myagent"); err != nil {
		t.Errorf("state corrupt after concurrent launches: %v", err)
	}
}

func writeProfile(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
