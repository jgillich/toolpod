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
	"testing"

	"github.com/jgillich/tpd/internal/approval"
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

func writeProfile(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
