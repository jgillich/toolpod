package toolpod

import (
	"net"
	"strconv"
	"testing"

	"github.com/jgillich/toolpod/internal/profile"
)

func fakePortAllocator(ports ...string) PortAllocator {
	i := 0
	return func(protocol, hostIP string) (string, error) {
		p := ports[i%len(ports)]
		i++
		return p, nil
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
	opts := LaunchOpts{ProfileName: "web", Workspace: "/p", PortAllocator: fakePortAllocator("40001", "40002")}
	spec, err := buildSpec(opts, cfg, "B", "/home/me", "/root")
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
	spec, err := buildSpec(opts, cfg, "B", "/home/me", "/root")
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
	// Hold a socket open: the allocator must never hand that port back out,
	// which is the property multi-instance launches rely on. Deterministic —
	// no sequential close/reuse flake.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	heldPort := strconv.Itoa(held.Addr().(*net.TCPAddr).Port)

	for _, proto := range []string{"tcp", "udp"} {
		got, err := defaultPortAllocator(proto, "127.0.0.1")
		if err != nil {
			t.Fatalf("%s alloc: %v", proto, err)
		}
		n, err := strconv.Atoi(got)
		if err != nil || n < 1 || n > 65535 {
			t.Errorf("%s alloc returned %q, want a port in 1-65535", proto, got)
		}
		if got == heldPort {
			t.Errorf("%s allocator returned port %s while it is bound", proto, got)
		}
	}
}

func TestBuildSpecBasic(t *testing.T) {
	cfg := profile.Profile{
		Version: 1,
		Image:   "myimage:latest",
		Command: []string{"opencode"},
		Tools:   map[string]string{"opencode": "latest", "node": "20"},
		Mounts: map[string]profile.Mount{
			"~/.config/opencode": {Source: "~/.config/opencode", ReadOnly: true},
		},
		Caches:  map[string]string{"npm": "~/.npm"},
		Network: "bridge",
	}
	opts := LaunchOpts{ProfileName: "opencode", Args: []string{"--model", "foo"}, Workspace: "/home/me/proj"}
	spec, err := buildSpec(opts, cfg, "A", "/home/me", "/home/me")
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
	if spec.Workspace.Mode != "A" {
		t.Errorf("workspace mode = %q, want A", spec.Workspace.Mode)
	}
	if spec.Tools["opencode"] != "latest" {
		t.Errorf("tools[opencode] = %q", spec.Tools["opencode"])
	}
	if len(spec.Caches) != 1 || spec.Caches[0].Name != "toolpod-cache-npm" {
		t.Errorf("Caches = %+v, want one entry toolpod-cache-npm", spec.Caches)
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
}

func TestBuildSpecModeBWorkspace(t *testing.T) {
	cfg := profile.Profile{Version: 1, Image: "x", Command: []string{"sh"}}
	opts := LaunchOpts{Workspace: "/home/me/proj"}
	spec, err := buildSpec(opts, cfg, "B", "/home/me", "/root")
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
	spec, err := buildSpec(opts, cfg, "B", "/home/me", "/root")
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
	spec, err := buildSpec(opts, cfg, "B", "/home/me", "/root")
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
	spec, err := buildSpec(opts, cfg, "B", "/home/me", "/root")
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
