package tpd

import (
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/jgillich/tpd/internal/profile"
	"github.com/jgillich/tpd/internal/runtime"
	"github.com/jgillich/tpd/internal/workspace"
)

func fakePortAllocator() PortAllocator {
	return func(protocol, hostIP string) (string, error) {
		if protocol == "udp" {
			return "40002", nil
		}
		return "40001", nil
	}
}

func TestBuildSpecPortsAllocationAndTemplates(t *testing.T) {
	cfg := profile.Profile{
		Version: 1,
		Image:   "img",
		Command: []string{"opencode", "web", "--port", `{{ index .Ports "8080" }}`},
		Env:     map[string]string{"WEB_PORT": `{{ index .Ports "8080" }}`},
		Ports: map[string]profile.PortBind{
			"8080": {},
			"5432": {Host: "5432"},
			"53":   {Protocol: "udp"},
			"9000": {Host: "9000", HostIP: "127.0.0.1"},
		},
	}
	opts := LaunchOpts{ProfileName: "web", Workspace: "/p", PortAllocator: fakePortAllocator()}
	spec, err := buildSpec(opts, cfg, workspace.ModeRootful, "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	wantPorts := []PortSpec{
		{Container: "53", HostPort: "40002", Protocol: "udp"},
		{Container: "5432", HostPort: "5432", Protocol: "tcp"},
		{Container: "8080", HostPort: "40001", Protocol: "tcp"},
		{Container: "9000", HostIP: "127.0.0.1", HostPort: "9000", Protocol: "tcp"},
	}
	if len(spec.PortSpecs) != len(wantPorts) {
		t.Fatalf("PortSpecs = %+v, want %+v", spec.PortSpecs, wantPorts)
	}
	for i, p := range spec.PortSpecs {
		if p != wantPorts[i] {
			t.Errorf("PortSpecs[%d] = %+v, want %+v", i, p, wantPorts[i])
		}
	}
	if spec.Command[3] != "40001" {
		t.Errorf("template command arg = %q, want 40001", spec.Command[3])
	}
	if spec.Env["WEB_PORT"] != "40001" {
		t.Errorf("template env = %q, want 40001", spec.Env["WEB_PORT"])
	}
}

func TestBuildSpecDevices(t *testing.T) {
	cfg := profile.Profile{
		Version: 1, Image: "img", Command: []string{"x"},
		Devices: map[string]profile.DeviceBind{
			"/dev/fuse":    {},
			"/dev/nvidia0": {Source: "/dev/nvidia0", Permissions: "rw"},
			"/dev/bus/usb": {Source: "/dev/bus/usb", Cgroup: true},
		},
	}
	opts := LaunchOpts{ProfileName: "x", Workspace: "/p"}
	spec, err := buildSpec(opts, cfg, workspace.ModeRootful, "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	want := []DeviceSpec{
		{Container: "/dev/bus/usb", Host: "/dev/bus/usb", Perms: "rwm", Cgroup: true},
		{Container: "/dev/fuse", Host: "/dev/fuse", Perms: "rwm"},
		{Container: "/dev/nvidia0", Host: "/dev/nvidia0", Perms: "rw"},
	}
	if len(spec.DeviceSpecs) != len(want) {
		t.Fatalf("DeviceSpecs = %+v, want %+v", spec.DeviceSpecs, want)
	}
	for i, d := range spec.DeviceSpecs {
		if d != want[i] {
			t.Errorf("DeviceSpecs[%d] = %+v, want %+v", i, d, want[i])
		}
	}
}

func TestDefaultPortAllocatorAvoidsBoundPorts(t *testing.T) {
	heldTCP, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer heldTCP.Close()
	heldTCPPort := strconv.Itoa(heldTCP.Addr().(*net.TCPAddr).Port)

	heldUDP, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer heldUDP.Close()
	heldUDPPort := strconv.Itoa(heldUDP.LocalAddr().(*net.UDPAddr).Port)

	tcp, err := defaultPortAllocator("tcp", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if tcp == heldTCPPort {
		t.Errorf("tcp allocator returned port %s while it is bound", tcp)
	}
	udp, err := defaultPortAllocator("udp", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if udp == heldUDPPort {
		t.Errorf("udp allocator returned port %s while it is bound", udp)
	}
}

func TestBuildSpecBasic(t *testing.T) {
	cfg := profile.Profile{
		Version: 1,
		Image:   "myimage:latest",
		Command: []string{"opencode"},
		Tools:   map[string]profile.Tool{"opencode": {Version: "latest"}, "node": {Version: "20"}},
		Mounts: map[string]profile.Mount{
			"~/.config/opencode": {Source: "~/.config/opencode", ReadOnly: true},
		},
		Caches:  map[string]profile.CachePaths{"npm": {"~/.npm"}},
		Network: "bridge",
	}
	opts := LaunchOpts{ProfileName: "opencode", Args: []string{"--model", "foo"}, Workspace: "/home/me/proj"}
	spec, err := buildSpec(opts, cfg, workspace.ModeRootless, "/home/me", "/home/me")
	if err != nil {
		t.Fatal(err)
	}

	if spec.Image != "myimage:latest" {
		t.Errorf("Image = %q", spec.Image)
	}
	wantCmd := []string{"opencode", "--model", "foo"}
	if len(spec.Command) != len(wantCmd) {
		t.Fatalf("Command = %v, want %v", spec.Command, wantCmd)
	}
	for i, c := range spec.Command {
		if c != wantCmd[i] {
			t.Errorf("Command[%d] = %q, want %q", i, c, wantCmd[i])
		}
	}
	if spec.Workspace.Target != "/home/me/proj" {
		t.Errorf("workspace target in Mode A = %q, want /home/me/proj", spec.Workspace.Target)
	}
	if spec.Workspace.Mode != workspace.ModeRootless {
		t.Errorf("workspace mode = %s, want rootless", spec.Workspace.Mode)
	}
	if spec.Tools["opencode"].Version != "latest" {
		t.Errorf("tools[opencode].Version = %q", spec.Tools["opencode"].Version)
	}
	if len(spec.Caches) != 1 || spec.Caches[0].Name != "tpd-cache-npm" {
		t.Errorf("Caches = %+v, want one entry tpd-cache-npm", spec.Caches)
	}
	if len(spec.Mounts) == 0 {
		t.Fatal("expected at least one mount")
	}
	mount := spec.Mounts[0]
	if mount.Target != "/home/me/.config/opencode" {
		t.Errorf("mount[0].Target = %q, want /home/me/.config/opencode", mount.Target)
	}
	// Profile label is set dynamically from opts.ProfileName, not from YAML
	if spec.Labels["profile"] != "opencode" {
		t.Errorf("Labels[profile] = %q, want \"opencode\" (set dynamically from ProfileName)", spec.Labels["profile"])
	}
	// Every launched container carries the ownership label so prune and leak
	// detection can filter by label instead of the name prefix.
	if spec.Labels[runtime.OwnershipLabel] != "true" {
		t.Errorf("Labels[%s] = %q, want \"true\"", runtime.OwnershipLabel, spec.Labels[runtime.OwnershipLabel])
	}
}

func TestBuildSpecMountCreate(t *testing.T) {
	cfg := profile.Profile{
		Version: 1,
		Image:   "x",
		Command: []string{"sh"},
		Mounts: map[string]profile.Mount{
			"~/.data":       {Source: "~/.data", Create: true},
			"~/.config/app": {Source: "~/.config/app"},
		},
	}
	spec, err := buildSpec(LaunchOpts{ProfileName: "x", Workspace: "/tmp"}, cfg, workspace.ModeRootless, "/home/me", "/home/me")
	if err != nil {
		t.Fatal(err)
	}
	var dataCreate, cfgCreate bool
	for _, m := range spec.Mounts {
		switch m.Target {
		case "/home/me/.data":
			dataCreate = m.Create
		case "/home/me/.config/app":
			cfgCreate = m.Create
		}
	}
	if !dataCreate {
		t.Error("mount with create: true should set MountSpec.Create")
	}
	if cfgCreate {
		t.Error("mount without create should leave MountSpec.Create unset")
	}
}

func TestBuildSpecModeBWorkspace(t *testing.T) {
	cfg := profile.Profile{Version: 1, Image: "x", Command: []string{"sh"}}
	opts := LaunchOpts{Workspace: "/home/me/proj"}
	spec, err := buildSpec(opts, cfg, workspace.ModeRootful, "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Workspace.Target != "/workspace" {
		t.Errorf("Mode B workspace target = %q, want /workspace", spec.Workspace.Target)
	}
}

func TestBuildSpecCommandFlagForShellProfile(t *testing.T) {
	cfg := profile.Profile{Version: 1, Image: "img", Command: []string{"bash"}}
	opts := LaunchOpts{Command: "echo hello", Workspace: "/home/me/proj"}
	spec, err := buildSpec(opts, cfg, workspace.ModeRootful, "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	wantCmd := []string{"bash", "-c", "echo hello"}
	if len(spec.Command) != len(wantCmd) {
		t.Fatalf("Command = %v, want %v", spec.Command, wantCmd)
	}
	for i, c := range spec.Command {
		if c != wantCmd[i] {
			t.Errorf("Command[%d] = %q, want %q", i, c, wantCmd[i])
		}
	}
}

func TestBuildSpecCommandFlagForNonShellProfile(t *testing.T) {
	cfg := profile.Profile{Version: 1, Image: "img", Command: []string{"opencode"}}
	opts := LaunchOpts{Command: "/bin/bash", Workspace: "/home/me/proj"}
	spec, err := buildSpec(opts, cfg, workspace.ModeRootful, "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	wantCmd := []string{"sh", "-c", "/bin/bash"}
	if len(spec.Command) != len(wantCmd) {
		t.Fatalf("Command = %v, want %v", spec.Command, wantCmd)
	}
	for i, c := range spec.Command {
		if c != wantCmd[i] {
			t.Errorf("Command[%d] = %q, want %q", i, c, wantCmd[i])
		}
	}
}

func TestBuildSpecCommandFlagOverridesArgs(t *testing.T) {
	cfg := profile.Profile{Version: 1, Image: "img", Command: []string{"opencode"}}
	opts := LaunchOpts{Command: "/bin/bash", Args: []string{"config", "view"}, Workspace: "/home/me/proj"}
	spec, err := buildSpec(opts, cfg, workspace.ModeRootful, "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	wantCmd := []string{"sh", "-c", "/bin/bash"}
	if len(spec.Command) != len(wantCmd) {
		t.Fatalf("Command = %v, want %v (Command should override Args)", spec.Command, wantCmd)
	}
	for i, c := range spec.Command {
		if c != wantCmd[i] {
			t.Errorf("Command[%d] = %q, want %q", i, c, wantCmd[i])
		}
	}
}

func TestBuildSpecUserArgsReplaceDefaults(t *testing.T) {
	cfg := profile.Profile{Version: 1, Image: "img", Command: []string{"t3code", "--no-sandbox", "--disable-dev-shm-usage", "--ozone-platform=wayland"}}
	opts := LaunchOpts{ProfileName: "t3code", Args: []string{"--help"}, Workspace: "/p"}
	spec, err := buildSpec(opts, cfg, workspace.ModeRootful, "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	wantCmd := []string{"t3code", "--help"}
	if len(spec.Command) != len(wantCmd) {
		t.Fatalf("Command = %v, want %v", spec.Command, wantCmd)
	}
	for i, c := range spec.Command {
		if c != wantCmd[i] {
			t.Errorf("Command[%d] = %q, want %q", i, c, wantCmd[i])
		}
	}
}

func TestBuildSpecBareRunKeepsFullCommand(t *testing.T) {
	full := []string{"t3code", "--no-sandbox", "--disable-dev-shm-usage", "--ozone-platform=wayland"}
	cfg := profile.Profile{Version: 1, Image: "img", Command: full}
	opts := LaunchOpts{ProfileName: "t3code", Workspace: "/p"}
	spec, err := buildSpec(opts, cfg, workspace.ModeRootful, "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Command) != len(full) {
		t.Fatalf("Command = %v, want %v (bare run keeps full command)", spec.Command, full)
	}
	for i, c := range spec.Command {
		if c != full[i] {
			t.Errorf("Command[%d] = %q, want %q", i, c, full[i])
		}
	}
}

func TestBuildSpecMapsFiles(t *testing.T) {
	cfg := profile.Profile{
		Version: 1,
		Image:   "img:1",
		Command: []string{"sh"},
		Files: map[string]profile.File{
			"/root/.config/foo": {Content: "hello", Mode: 0o600},
		},
	}
	spec, err := buildSpec(LaunchOpts{ProfileName: "p"}, cfg, workspace.ModeRootful, "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Files) != 1 {
		t.Fatalf("spec.Files = %v, want 1 entry", spec.Files)
	}
	f := spec.Files[0]
	if f.Target != "/root/.config/foo" || f.Content != "hello" || f.Mode != 0o600 {
		t.Errorf("spec.Files[0] = %+v, want {/root/.config/foo hello 384}", f)
	}
}

func TestBuildSpecResources(t *testing.T) {
	cfg := profile.Profile{
		Version:   1,
		Image:     "img",
		Command:   []string{"x"},
		Resources: &profile.Resources{Memory: "512m", CPUs: "2"},
	}
	opts := LaunchOpts{ProfileName: "x", Workspace: "/p"}
	spec, err := buildSpec(opts, cfg, workspace.ModeRootful, "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Resources.MemoryBytes != 512<<20 {
		t.Errorf("MemoryBytes = %d, want %d", spec.Resources.MemoryBytes, 512<<20)
	}
	if spec.Resources.NanoCPUs != 2e9 {
		t.Errorf("NanoCPUs = %d, want %d", spec.Resources.NanoCPUs, int64(2e9))
	}
}

func TestBuildSpecFilesDefaultMode(t *testing.T) {
	cfg := profile.Profile{
		Version: 1,
		Image:   "img:1",
		Command: []string{"sh"},
		Files:   map[string]profile.File{"/root/.config/foo": {Content: "x"}},
	}
	spec, err := buildSpec(LaunchOpts{ProfileName: "p"}, cfg, workspace.ModeRootful, "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Files[0].Mode != 0o644 {
		t.Errorf("default mode = %o, want 644", spec.Files[0].Mode)
	}
}

func TestBuildSpecServices(t *testing.T) {
	cfg := profile.Profile{
		Image:   "ubuntu",
		Command: []string{"sh"},
		Services: map[string]profile.Service{
			"registry": {
				Hash:    "abcd1234",
				Image:   "debian:13-slim",
				Command: []string{"registry"},
				Caches: map[string]profile.CachePaths{
					"data": {"/var/lib/registry"},
				},
				Exposes: map[string]string{"registry": "/run/registry/registry.sock"},
				Labels:  map[string]string{"custom": "val"},
			},
		},
		Mounts: map[string]profile.Mount{
			"/sock": {Service: "registry", Socket: "registry"},
		},
	}
	spec, err := buildSpec(LaunchOpts{ProfileName: "test"}, cfg, workspace.ModeRootless, "/home/user", "/home/user")
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if len(spec.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(spec.Services))
	}
	svc := spec.Services[0]
	if svc.Name != "registry" || svc.Hash != "abcd1234" || svc.Image != "debian:13-slim" {
		t.Errorf("service = %+v", svc)
	}
	if svc.Labels["custom"] != "val" {
		t.Errorf("service label custom = %q, want val", svc.Labels["custom"])
	}
	if svc.Labels[runtime.OwnershipLabel] != "true" {
		t.Error("service should carry tpd.managed=true label")
	}
	if svc.Labels[runtime.ServiceLabel] != "registry" {
		t.Error("service should carry tpd.service=registry label")
	}
	if spec.Labels[runtime.UsesServiceLabel] != "registry" {
		t.Errorf("main container should carry tpd.uses-service=registry, got %q", spec.Labels[runtime.UsesServiceLabel])
	}
	var foundSock bool
	for _, m := range spec.Mounts {
		if m.Target == "/sock" && m.Service == "registry" && m.Socket == "registry" {
			foundSock = true
		}
	}
	if !foundSock {
		t.Error("service-socket mount not found in spec.Mounts with Service/Socket intact")
	}
}

func TestBuildSpecServiceHostVar(t *testing.T) {
	cfg := profile.Profile{
		Version: 1,
		Image:   "ubuntu",
		Command: []string{"sh"},
		Services: map[string]profile.Service{
			"postgres-main": {Image: "postgres:17", Command: []string{"postgres"}},
		},
	}
	spec, err := buildSpec(LaunchOpts{ProfileName: "web", Workspace: "/p"}, cfg, workspace.ModeRootless, "/home/me", "/home/me")
	if err != nil {
		t.Fatal(err)
	}
	if got := spec.Env["TPD_SERVICE_POSTGRES_MAIN_HOST"]; got != "tpd-svc-postgres-main" {
		t.Fatalf("host = %q, want tpd-svc-postgres-main", got)
	}
	if got := spec.Labels[runtime.UsesServiceLabel]; got != "postgres-main" {
		t.Fatalf("uses-service = %q, want postgres-main", got)
	}
}

func TestBuildSpecServiceLabel(t *testing.T) {
	cfg := profile.Profile{
		Version: 1,
		Image:   "ubuntu",
		Command: []string{"sh"},
		Services: map[string]profile.Service{
			"postgres-main": {Image: "postgres:17", Command: []string{"postgres"}},
			"alpha":         {Image: "img", Command: []string{"alpha"}},
		},
	}
	spec, err := buildSpec(LaunchOpts{ProfileName: "web", Workspace: "/p"}, cfg, workspace.ModeRootless, "/home/me", "/home/me")
	if err != nil {
		t.Fatal(err)
	}
	if got := spec.Labels[runtime.UsesServiceLabel]; got != "alpha,postgres-main" {
		t.Fatalf("uses-service = %q, want alpha,postgres-main", got)
	}
	if got := spec.Env["TPD_SERVICE_ALPHA_HOST"]; got != "tpd-svc-alpha" {
		t.Errorf("alpha host = %q, want tpd-svc-alpha", got)
	}
	if got := spec.Env["TPD_SERVICE_POSTGRES_MAIN_HOST"]; got != "tpd-svc-postgres-main" {
		t.Errorf("postgres host = %q, want tpd-svc-postgres-main", got)
	}
}

func TestBuildSpecServiceCollision(t *testing.T) {
	cfg := profile.Profile{
		Version: 1,
		Image:   "ubuntu",
		Command: []string{"sh"},
		Env:     map[string]string{"TPD_SERVICE_ALPHA_HOST": "custom"},
		Services: map[string]profile.Service{
			"alpha": {Image: "img", Command: []string{"alpha"}},
		},
	}
	_, err := buildSpec(LaunchOpts{ProfileName: "web", Workspace: "/p"}, cfg, workspace.ModeRootless, "/home/me", "/home/me")
	if err == nil {
		t.Fatal("expected error for reserved environment variable")
	}
	if !strings.Contains(err.Error(), "TPD_SERVICE_ALPHA_HOST") {
		t.Fatalf("error should name the reserved variable, got: %v", err)
	}
}

func TestBuildSpecServiceSocketOnlyHostVar(t *testing.T) {
	cfg := profile.Profile{
		Version: 1,
		Image:   "ubuntu",
		Command: []string{"sh"},
		Services: map[string]profile.Service{
			"redis": {Image: "redis", Command: []string{"redis-server"}, Exposes: map[string]string{"main": "/run/redis/redis.sock"}},
		},
		Mounts: map[string]profile.Mount{
			"/sock": {Service: "redis", Socket: "main"},
		},
	}
	spec, err := buildSpec(LaunchOpts{ProfileName: "web", Workspace: "/p"}, cfg, workspace.ModeRootless, "/home/me", "/home/me")
	if err != nil {
		t.Fatal(err)
	}
	if got := spec.Env["TPD_SERVICE_REDIS_HOST"]; got != "tpd-svc-redis" {
		t.Fatalf("host = %q, want tpd-svc-redis", got)
	}
}
