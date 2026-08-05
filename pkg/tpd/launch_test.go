package tpd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func TestLaunchWithServices(t *testing.T) {
	rt := &orderRuntime{FakeRuntime: &runtime.FakeRuntime{PrepareImage: "test-image"}}
	rt.ServiceBindings = runtime.ServiceBindings{
		Sockets: map[string]string{"registry/registry": "/tmp/test-sock"},
		Release: rt.release,
	}
	res := launchService(t, rt)
	if res.Err != nil {
		t.Fatalf("Launch: %v", res.Err)
	}
	wantCallOrder(t, rt, "prepare", "start-services", "create-container", "release", "run-container", "stop-services")
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
		Release: rt.release,
	}
	res := launchService(t, rt)
	if res.Err == nil {
		t.Fatal("expected error from failed RunContainer")
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3 (runtime error)", res.ExitCode)
	}
	wantCallOrder(t, rt, "prepare", "start-services", "create-container", "release", "run-container", "stop-services")
	if rt.StopServicesSpec == nil {
		t.Error("StopServices was not called after RunContainer failed")
	}
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
