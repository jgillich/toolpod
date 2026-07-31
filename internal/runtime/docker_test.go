package runtime

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"golang.org/x/sys/unix"
)

func TestIsLikelyRootlessSocket(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"unix:///run/user/1000/podman/podman.sock", true},
		{"unix:///var/run/docker.sock", false},
		{"unix:///run/podman/podman.sock", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isLikelyRootlessSocket(tt.host); got != tt.want {
			t.Errorf("isLikelyRootlessSocket(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}

func TestBuildEnvSetsMiseConfigDir(t *testing.T) {
	spec := Spec{
		RuntimeHome: "/root",
		Env:         map[string]string{"OPENCODE_CONFIG_CONTENT": "..."},
	}
	env := buildEnv(spec, "/root")
	var hasHome, hasMiseConfigDir, hasProfileEnv bool
	for _, e := range env {
		switch {
		case strings.HasPrefix(e, "HOME="):
			hasHome = true
			if e != "HOME=/root" {
				t.Errorf("HOME = %q, want HOME=/root", e)
			}
		case strings.HasPrefix(e, "MISE_CONFIG_DIR="):
			hasMiseConfigDir = true
			if e != "MISE_CONFIG_DIR=/root/.config/mise" {
				t.Errorf("MISE_CONFIG_DIR = %q, want /root/.config/mise", e)
			}
		case strings.HasPrefix(e, "OPENCODE_CONFIG_CONTENT="):
			hasProfileEnv = true
		}
	}
	if !hasHome {
		t.Error("missing HOME in env")
	}
	if !hasMiseConfigDir {
		t.Error("missing MISE_CONFIG_DIR in env")
	}
	if !hasProfileEnv {
		t.Error("missing profile env OPENCODE_CONFIG_CONTENT")
	}
}

func TestBuildMountsSkipsOptionalMissing(t *testing.T) {
	spec := Spec{
		Workspace: WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: "B"},
		Mounts: []MountSpec{
			{Target: "/etc/hosts", Source: "/etc/hosts", ReadOnly: true, Optional: true},
			{Target: "/nonexistent", Source: "/this/does/not/exist", Optional: true},
		},
	}
	m := buildMounts(spec, "/root")
	// Should contain workspace + /etc/hosts but skip the nonexistent optional mount.
	found := map[string]bool{}
	for _, mt := range m {
		found[mt.Target] = true
	}
	if !found["/workspace"] {
		t.Error("workspace mount missing")
	}
	if !found["/etc/hosts"] {
		t.Error("/etc/hosts optional mount should be present (source exists)")
	}
	if found["/nonexistent"] {
		t.Error("nonexistent optional mount should be skipped")
	}
}

func TestBuildPortBindings(t *testing.T) {
	spec := Spec{PortSpecs: []PortSpec{
		{Container: "8080", HostPort: "40001", Protocol: "tcp"},
		{Container: "53", HostIP: "127.0.0.1", HostPort: "40002", Protocol: "udp"},
	}}
	exposed, bindings := buildPortBindings(spec)
	if _, ok := exposed["8080/tcp"]; !ok {
		t.Errorf("ExposedPorts missing 8080/tcp: %v", exposed)
	}
	if _, ok := exposed["53/udp"]; !ok {
		t.Errorf("ExposedPorts missing 53/udp: %v", exposed)
	}
	tcp := bindings["8080/tcp"]
	if len(tcp) != 1 || tcp[0].HostPort != "40001" || tcp[0].HostIP != "" {
		t.Errorf("bindings[8080/tcp] = %+v, want [{ 40001}]", tcp)
	}
	udp := bindings["53/udp"]
	if len(udp) != 1 || udp[0].HostIP != "127.0.0.1" {
		t.Errorf("bindings[53/udp] = %+v, want [{127.0.0.1 40002}]", udp)
	}
}

func TestBuildDevicesSkipsMissingSource(t *testing.T) {
	spec := Spec{DeviceSpecs: []DeviceSpec{
		{Container: "/dev/null", Host: "/dev/null", Perms: "rwm"},
		{Container: "/dev/nonexistent-xyz", Host: "/dev/nonexistent-xyz", Perms: "rwm"},
	}}
	devices := buildDevices(spec)
	if len(devices) != 1 {
		t.Fatalf("Devices = %+v, want only /dev/null (missing source skipped)", devices)
	}
	if devices[0].PathInContainer != "/dev/null" || devices[0].PathOnHost != "/dev/null" || devices[0].CgroupPermissions != "rwm" {
		t.Errorf("device mapping = %+v, want /dev/null -> /dev/null rwm", devices[0])
	}
}

func TestBuildDeviceCgroupRulesScoped(t *testing.T) {
	spec := Spec{DeviceSpecs: []DeviceSpec{
		{Container: "/dev/null", Host: "/dev/null", Perms: "rwm", Cgroup: true},
		{Container: "/dev/fuse", Host: "/dev/fuse", Cgroup: false},
	}}
	rules := buildDeviceCgroupRules(spec)
	if len(rules) != 1 {
		t.Fatalf("rules = %v, want exactly one (cgroup: false must not emit rules)", rules)
	}
	// /dev/null is char major 1; either the scoped 1:<minor> form or the
	// 1:* fallback must be used — never a blanket rule.
	if !strings.HasPrefix(rules[0], "c 1:") || !strings.HasSuffix(rules[0], " rwm") {
		t.Errorf("rule = %q, want \"c 1:<minor> rwm\" or \"c 1:* rwm\"", rules[0])
	}
	if strings.Contains(rules[0], "*:*") {
		t.Errorf("blanket c *:* rule must never be emitted, got %q", rules[0])
	}
}

func TestDeviceRulePrefix(t *testing.T) {
	var st unix.Stat_t
	if err := unix.Stat("/dev/null", &st); err != nil {
		t.Fatalf("stat /dev/null: %v", err)
	}
	major := int(unix.Major(uint64(st.Rdev)))
	minor := int(unix.Minor(uint64(st.Rdev)))
	if p := deviceRulePrefix(major, minor); p != "c" {
		t.Errorf("deviceRulePrefix(%d:%d) = %q, want \"c\"", major, minor, p)
	}

	entries, err := os.ReadDir("/sys/dev/block")
	if err != nil || len(entries) == 0 {
		t.Skip("no block device sysfs entries to test")
	}
	for _, e := range entries {
		parts := strings.SplitN(e.Name(), ":", 2)
		if len(parts) != 2 {
			continue
		}
		m, err1 := strconv.Atoi(parts[0])
		n, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			continue
		}
		if p := deviceRulePrefix(m, n); p != "b" {
			t.Errorf("deviceRulePrefix(%d:%d) = %q, want \"b\"", m, n, p)
		}
	}
}

func TestIntegrationRunShellEcho(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if os.Getenv("DOCKER_HOST") == "" {
		t.Skip("DOCKER_HOST not set")
	}
	rt, err := NewDockerRuntime()
	if err != nil {
		t.Fatalf("NewDockerRuntime: %v", err)
	}
	spec := Spec{
		ProfileName: "test-shell",
		Image:       "alpine:latest",
		Command:     []string{"sh", "-c", "echo hi"},
		Workspace:   WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: "B"},
		Network:     "none",
	}
	if _, err := rt.Prepare(context.Background(), spec, NoopProgressWriter{}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	code, err := rt.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func TestIntegrationRunPublishesPort(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if os.Getenv("DOCKER_HOST") == "" {
		t.Skip("DOCKER_HOST not set")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hostPort := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	ln.Close()

	rt, err := NewDockerRuntime()
	if err != nil {
		t.Fatalf("NewDockerRuntime: %v", err)
	}
	// Note: network must be the default bridge (NOT "none") — published
	// ports cannot reach a container with no network interface.
	spec := Spec{
		ProfileName: "test-port",
		Image:       "alpine:latest",
		Command:     []string{"sh", "-c", "printf hi | nc -l -p 8080 -s 0.0.0.0"},
		Workspace:   WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: "B"},
		PortSpecs:   []PortSpec{{Container: "8080", HostPort: hostPort, Protocol: "tcp"}},
	}
	if _, err := rt.Prepare(context.Background(), spec, NoopProgressWriter{}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := rt.Run(context.Background(), spec)
		done <- err
	}()

	// The container start + nc listen races the host dial; retry until the
	// published port accepts (or the deadline expires).
	//
	// The published port is bound by the engine's userland proxy. When the
	// tests run inside a container that only mounts the engine's socket
	// (e.g. rootless Podman), that proxy binds on the outer host's loopback,
	// which is unreachable from here — but the container's own IP on the
	// shared bridge is. Fall back to dialing the container directly.
	var conn net.Conn
	deadline := time.Now().Add(20 * time.Second)
	for conn == nil {
		if time.Now().After(deadline) {
			t.Fatal("timed out dialing published port")
		}
		for _, addr := range []string{"127.0.0.1:" + hostPort} {
			conn, err = net.DialTimeout("tcp", addr, time.Second)
			if err == nil {
				break
			}
		}
		if conn == nil {
			ip, ierr := containerIPOf(rt, "test-port")
			if ierr == nil {
				conn, err = net.DialTimeout("tcp", ip+":8080", time.Second)
			}
		}
		if err != nil {
			time.Sleep(500 * time.Millisecond)
		}
	}
	defer conn.Close()
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read from published port: %v", err)
	}
	if string(buf[:n]) != "hi" {
		t.Errorf("got %q from published port, want \"hi\"", string(buf[:n]))
	}
	// Close the connection before waiting for Run to return. The container's
	// busybox nc serves one connection and only exits once the client closes
	// it; Run blocks on ContainerWait until the container stops.
	conn.Close()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// containerIPOf resolves the bridge IP of the newest running container for
// profileName. The container is named toolpod-<profile>-<randomID> by Run.
// Stale (exited) containers matching the prefix are skipped.
func containerIPOf(rt *DockerRuntime, profileName string) (string, error) {
	cli := rt.cli
	containers, err := cli.ContainerList(context.Background(), container.ListOptions{})
	if err != nil {
		return "", err
	}
	prefix := "toolpod-" + profileName + "-"
	var best types.Container
	for _, c := range containers {
		if c.State != "running" {
			continue
		}
		matches := false
		for _, name := range c.Names {
			if strings.HasPrefix(name, "/"+prefix) || strings.HasPrefix(name, prefix) {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}
		if best.ID == "" || c.Created > best.Created {
			best = c
		}
	}
	if best.ID == "" {
		return "", fmt.Errorf("container for profile %s not found", profileName)
	}
	for _, net := range best.NetworkSettings.Networks {
		if net.IPAddress != "" {
			return net.IPAddress, nil
		}
	}
	return "", fmt.Errorf("container for profile %s has no IP", profileName)
}
