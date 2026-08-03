package runtime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/jgillich/tpd/internal/workspace"
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

func TestSubpathSupportedConcurrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/version") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"Version":"27.1.0"}`)
	}))
	defer srv.Close()

	cli, err := client.NewClientWithOpts(
		client.WithHost("tcp://"+srv.Listener.Addr().String()),
		client.WithVersion("1.41"),
	)
	if err != nil {
		t.Fatal(err)
	}
	rt := &DockerRuntime{cli: cli}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := rt.subpathSupported(context.Background()); !got {
				t.Error("subpathSupported = false, want true (docker 27.1.0 supports subpaths)")
			}
		}()
	}
	wg.Wait()
}

func TestBuildEnvDropsEmptyEnvValues(t *testing.T) {
	// Empty-string values previously meant "passthrough from host". That is
	// gone: passthrough must use a template (rendered before buildEnv), and
	// any value that resolves empty is not set in the container.
	t.Setenv("LEGACY_PASSTHROUGH", "host-value")
	spec := Spec{
		RuntimeHome: "/root",
		Env: map[string]string{
			"LEGACY_PASSTHROUGH": "", // host has a value; must NOT be forwarded
			"RENDERED_EMPTY":     "", // template rendered to "" (host var missing)
			"LITERAL":            "value",
			"RENDERED":           "hello",
		},
	}
	env := buildEnv(spec, "/root")

	got := map[string]bool{}
	for _, e := range env {
		if i := strings.IndexByte(e, '='); i >= 0 {
			got[e[:i]] = true
		}
	}
	for _, absent := range []string{"LEGACY_PASSTHROUGH", "RENDERED_EMPTY"} {
		if got[absent] {
			t.Errorf("env should not contain %s (empty values are dropped); got %v", absent, env)
		}
	}
	for _, present := range []string{"LITERAL", "RENDERED"} {
		if !got[present] {
			t.Errorf("env should contain %s; got %v", present, env)
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

func TestBuildEnvPersistsAubeStore(t *testing.T) {
	// aube (mise's npm backend) defaults its cache and store to $HOME, which is
	// ephemeral inside the container; the mise profile declares an `aube` cache
	// volume at ~/.aube, so point aube there to survive container teardown.
	spec := Spec{RuntimeHome: "/root"}
	env := buildEnv(spec, "/root")
	got := map[string]string{}
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			got[k] = v
		}
	}
	if got["AUBE_CACHE_DIR"] != "/root/.aube/cache" {
		t.Errorf("AUBE_CACHE_DIR = %q, want /root/.aube/cache", got["AUBE_CACHE_DIR"])
	}
	if got["AUBE_STORE_DIR"] != "/root/.aube/store" {
		t.Errorf("AUBE_STORE_DIR = %q, want /root/.aube/store", got["AUBE_STORE_DIR"])
	}
}

func TestBuildMountsUsesDeclaredCacheForMise(t *testing.T) {
	// The mise data dir is a plain cache. With volume-subpath support the
	// shared volume mounts at each target via Subpath; without it, a dedicated
	// hashed volume backs each target so paths stay separate.
	spec := Spec{
		Workspace: WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: workspace.ModeRootful},
		Caches:    []CacheSpec{{Name: "tpd-cache-mise", Target: "/root/.local/share/mise", Subpath: "deadbeef"}},
	}
	for _, subpath := range []bool{true, false} {
		m, err := buildMounts(spec, "/root", subpath)
		if err != nil {
			t.Fatalf("buildMounts(subpath=%v): %v", subpath, err)
		}
		var miseMounts []mount.Mount
		for _, mt := range m {
			if mt.Target == "/root/.local/share/mise" {
				miseMounts = append(miseMounts, mt)
			}
		}
		if len(miseMounts) != 1 {
			t.Fatalf("expected exactly one mise mount, got %d: %+v", len(miseMounts), miseMounts)
		}
		mm := miseMounts[0]
		if subpath {
			if mm.Source != "tpd-cache-mise" || mm.Type != mount.TypeVolume || mm.VolumeOptions == nil || mm.VolumeOptions.Subpath != "deadbeef" {
				t.Errorf("subpath mount = %+v, want volume tpd-cache-mise subpath=deadbeef", mm)
			}
		} else if mm.Source != "tpd-cache-mise-deadbeef" || mm.VolumeOptions != nil {
			t.Errorf("fallback mount = %+v, want volume tpd-cache-mise-deadbeef", mm)
		}
	}
}

func TestBuildMountsCreatesSourceWhenRequested(t *testing.T) {
	src := filepath.Join(t.TempDir(), "data")
	spec := Spec{
		Workspace: WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: workspace.ModeRootful},
		Mounts: []MountSpec{
			{Target: "/data", Source: src, Create: true},
		},
	}
	m, err := buildMounts(spec, "/root", false)
	if err != nil {
		t.Fatalf("buildMounts: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("mount with create should create source %s: %v", src, err)
	}
	found := false
	for _, mt := range m {
		if mt.Target == "/data" {
			found = true
		}
	}
	if !found {
		t.Error("created mount missing from list")
	}
}

func TestBuildMountsDoesNotCreateWithoutFlag(t *testing.T) {
	src := filepath.Join(t.TempDir(), "data")
	spec := Spec{
		Workspace: WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: workspace.ModeRootful},
		Mounts: []MountSpec{
			{Target: "/data", Source: src},
		},
	}
	if _, err := buildMounts(spec, "/root", false); err != nil {
		t.Fatalf("buildMounts: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("mount without create should not create source; stat err=%v", err)
	}
}

func TestBuildMountsFailsCreateForRequiredMount(t *testing.T) {
	// A dangling symlink component makes MkdirAll fail (mkdir on the existing
	// symlink → EEXIST) while os.Stat(source) still reports ENOENT; a required
	// mount must fail the launch, not be silently dropped.
	link := filepath.Join(t.TempDir(), "dangling")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), link); err != nil {
		t.Fatal(err)
	}
	spec := Spec{
		Workspace: WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: workspace.ModeRootful},
		Mounts: []MountSpec{
			{Target: "/data", Source: filepath.Join(link, "sub"), Create: true},
		},
	}
	if _, err := buildMounts(spec, "/root", false); err == nil {
		t.Fatal("required mount with failed create should error, got nil")
	}
}

func TestBuildMountsSkipsOptionalMissing(t *testing.T) {
	spec := Spec{
		Workspace: WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: workspace.ModeRootful},
		Mounts: []MountSpec{
			{Target: "/etc/hosts", Source: "/etc/hosts", ReadOnly: true, Optional: true},
			{Target: "/nonexistent", Source: "/this/does/not/exist", Optional: true},
		},
	}
	m, err := buildMounts(spec, "/root", false)
	if err != nil {
		t.Fatalf("buildMounts: %v", err)
	}
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

func TestDeviceTypePrefix(t *testing.T) {
	tests := []struct {
		name string
		mode uint32
		want string
	}{
		{"block", unix.S_IFBLK | 0o660, "b"},
		{"char", unix.S_IFCHR | 0o666, "c"},
		{"other", 0o644, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deviceTypePrefix(tt.mode); got != tt.want {
				t.Errorf("deviceTypePrefix(mode=%#x) = %q, want %q", tt.mode, got, tt.want)
			}
		})
	}
}

func TestDeviceTypeFromRealNode(t *testing.T) {
	_, _, prefix, ok := deviceMajorMinor("/dev/null")
	if !ok {
		t.Fatal("stat /dev/null failed")
	}
	if prefix != "c" {
		t.Errorf("deviceMajorMinor(/dev/null) prefix = %q, want \"c\"", prefix)
	}
}

func TestContainerIdentity(t *testing.T) {
	userns, rootUser, uid, gid := containerIdentity(true)
	if rootUser != "0:0" {
		t.Errorf("podman container user = %q, want 0:0 (root bootstrap)", rootUser)
	}
	if uid != os.Getuid() || gid != os.Getgid() {
		t.Errorf("podman uid/gid = %d/%d, want %d/%d", uid, gid, os.Getuid(), os.Getgid())
	}
	if userns != "keep-id" {
		t.Errorf("podman userns = %q, want keep-id", userns)
	}

	userns, rootUser, uid, gid = containerIdentity(false)
	if rootUser != "0:0" {
		t.Errorf("docker container user = %q, want 0:0 (root bootstrap)", rootUser)
	}
	if uid != os.Getuid() || gid != os.Getgid() {
		t.Errorf("docker uid/gid = %d/%d, want %d/%d", uid, gid, os.Getuid(), os.Getgid())
	}
	if userns != "" {
		t.Errorf("docker userns = %q, want empty", userns)
	}
}

func TestHomeParents(t *testing.T) {
	got := homeParents("/home/me", []string{
		"/home/me/.config/t3code",
		"/home/me/.local/share/app/data",
		"/workspace",    // outside home — ignored
		"/home/me/.npm", // direct child — no parents
	})
	want := map[string]bool{
		"/home/me/.config":          true,
		"/home/me/.local":           true,
		"/home/me/.local/share":     true,
		"/home/me/.local/share/app": true,
	}
	if len(got) != len(want) {
		t.Fatalf("homeParents = %v, want %v", got, want)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("homeParents returned unexpected %q", p)
		}
	}
}

func TestHomeParentsSkipsMountLeafParents(t *testing.T) {
	// A mount leaf at /home/me/.config (a bind mount of a host dir) must not
	// be chowned even when a deeper mount makes it appear as a parent.
	got := homeParents("/home/me", []string{
		"/home/me/.config",
		"/home/me/.config/foo",
	})
	if len(got) != 0 {
		t.Errorf("homeParents = %v, want [] (only mount leaves under home)", got)
	}
}

func TestWrapAsUser(t *testing.T) {
	cmd := wrapAsUser("mkdir -p /home/me && chown 1000:1000 /home/me", 1000, 1000, []string{"sh", "-c", "echo hi"})
	if len(cmd) != 3 || cmd[0] != "sh" || cmd[1] != "-c" {
		t.Fatalf("wrapAsUser returned %v, want [sh -c ...]", cmd)
	}
	if !strings.Contains(cmd[2], "mkdir -p /home/me") {
		t.Errorf("missing bootstrap in %q", cmd[2])
	}
	if !strings.Contains(cmd[2], "setpriv --reuid=1000 --regid=1000") {
		t.Errorf("missing setpriv drop in %q", cmd[2])
	}
	if !strings.Contains(cmd[2], "--clear-groups") {
		t.Errorf("missing group clearing in %q", cmd[2])
	}
	if !strings.Contains(cmd[2], "sh -c 'echo hi'") {
		t.Errorf("missing inner command in %q", cmd[2])
	}
	if !strings.Contains(cmd[2], "command -v setpriv") {
		t.Errorf("missing setpriv availability guard in %q", cmd[2])
	}
	if !strings.Contains(cmd[2], "exec sh -c 'echo hi'") {
		t.Errorf("missing fallback (no setpriv) in %q", cmd[2])
	}
}

// integrationImage is the production base image (bare debian:13-slim): it
// carries util-linux setpriv (for the launch wrapper). python3 moved from the
// base image to mise.yaml packages: in the runtime-oci-deps migration, so the
// port listener test installs it via a derived image (exercising the packages:
// build path the feature added).
const integrationImage = "debian:13-slim"

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
		Image:       integrationImage,
		Command:     []string{"sh", "-c", fmt.Sprintf("test \"$(id -u)\" = %d && test \"$(id -g)\" = %d && echo hi", os.Getuid(), os.Getgid())},
		Workspace:   WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: workspace.ModeRootful},
		RuntimeHome: "/root",
		Network:     "none",
	}
	if _, err := rt.Prepare(context.Background(), spec, NoopProgressWriter{}, false); err != nil {
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
		Image:       integrationImage,
		Packages:    []string{"python3"},
		Command:     []string{"sh", "-c", `python3 -c "import socket;s=socket.socket();s.bind(('0.0.0.0',8080));s.listen(1);c,_=s.accept();c.send(b'hi')"`},
		Workspace:   WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: workspace.ModeRootful},
		RuntimeHome: "/root",
		PortSpecs:   []PortSpec{{Container: "8080", HostPort: hostPort, Protocol: "tcp"}},
	}
	imageRef, err := rt.Prepare(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	spec.Image = imageRef
	done := make(chan error, 1)
	go func() {
		_, err := rt.Run(context.Background(), spec)
		done <- err
	}()

	// The container start + listener races the host dial; retry until the
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
	// Close the connection before waiting for Run to return: the python
	// listener serves one connection and only exits once the client closes
	// it; Run blocks on ContainerWait until the container stops.
	conn.Close()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// containerIPOf resolves the bridge IP of the newest running container for
// profileName. The container is named tpd-<profile>-<randomID> by Run.
// Stale (exited) containers matching the prefix are skipped.
func containerIPOf(rt *DockerRuntime, profileName string) (string, error) {
	cli := rt.cli
	containers, err := cli.ContainerList(context.Background(), container.ListOptions{})
	if err != nil {
		return "", err
	}
	prefix := "tpd-" + profileName + "-"
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

func TestBuildMountsHidesRealBusSocket(t *testing.T) {
	spec := Spec{
		Workspace: WorkspaceSpec{HostPath: "/ws", Target: "/ws"},
		Env:       map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"},
	}
	mounts, err := buildMounts(spec, "/root", false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range mounts {
		if m.Target == "/run/user/1000/bus" {
			found = true
			if m.Source != "/dev/null" {
				t.Errorf("bus overlay source = %q, want /dev/null", m.Source)
			}
		}
	}
	if !found {
		t.Error("expected a mount over /run/user/1000/bus when XDG_RUNTIME_DIR is set")
	}
}

func TestBuildMountsNoBusOverlayWithoutRuntimeDir(t *testing.T) {
	spec := Spec{
		Workspace: WorkspaceSpec{HostPath: "/ws", Target: "/ws"},
		Env:       map[string]string{},
	}
	mounts, err := buildMounts(spec, "/root", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mounts {
		if strings.HasSuffix(m.Target, "/bus") {
			t.Errorf("unexpected bus overlay mount: %+v", m)
		}
	}
}

func TestBuildMountsSkipsOverlayWhenBusAlreadyMounted(t *testing.T) {
	spec := Spec{
		Workspace: WorkspaceSpec{HostPath: "/ws", Target: "/ws"},
		Mounts:    []MountSpec{{Target: "/run/user/1000/bus", Source: "/host/socket", ReadOnly: true}},
		Env:       map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"},
	}
	mounts, err := buildMounts(spec, "/root", false)
	if err != nil {
		t.Fatal(err)
	}
	devNull := 0
	for _, m := range mounts {
		if m.Target == "/run/user/1000/bus" && m.Source == "/dev/null" {
			devNull++
		}
	}
	if devNull != 0 {
		t.Errorf("should not overlay /dev/null when a mount already targets the bus path (got %d overlays)", devNull)
	}
}

func TestIntegrationPrepareBuildsDerivedImage(t *testing.T) {
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
		ProfileName: "test-packages",
		Image:       integrationImage,
		Packages:    []string{"hello"},
		Command:     []string{"true"},
		Workspace:   WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: workspace.ModeRootful},
		RuntimeHome: "/root",
		Network:     "none",
	}
	imageRef, err := rt.Prepare(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !strings.HasPrefix(imageRef, "tpd/packages:") {
		t.Errorf("imageRef = %q, want tpd/packages: prefix", imageRef)
	}
	// Derived image must be present and inspectable.
	if _, _, err := rt.cli.ImageInspectWithRaw(context.Background(), imageRef); err != nil {
		t.Errorf("derived image %q not inspectable after Prepare: %v", imageRef, err)
	}
	// Second Prepare must reuse the cached image (idempotent).
	imageRef2, err := rt.Prepare(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("second Prepare: %v", err)
	}
	if imageRef2 != imageRef {
		t.Errorf("second Prepare returned %q, want same %q (cache reuse)", imageRef2, imageRef)
	}
	// Run a container from the derived image to prove hello is installed.
	code, err := rt.Run(context.Background(), Spec{
		ProfileName: "test-packages",
		Image:       imageRef,
		Command:     []string{"sh", "-c", `command -v hello >/dev/null && hello | grep -q "Hello"`},
		Workspace:   WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: workspace.ModeRootful},
		RuntimeHome: "/root",
		Network:     "none",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("hello run exit code = %d, want 0", code)
	}
	// Cleanup: remove the derived image we built.
	rt.cli.ImageRemove(context.Background(), imageRef, image.RemoveOptions{Force: true, PruneChildren: true})
}

func TestIntegrationReposEnablesMiseRepo(t *testing.T) {
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
		ProfileName: "test-repos",
		Image:       integrationImage,
		Repos:       map[string]Repo{"mise": {ExtRepo: "mise"}},
		Packages:    []string{"mise"},
		Command:     []string{"true"},
		Workspace:   WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: workspace.ModeRootful},
		RuntimeHome: "/root",
		Network:     "none",
	}
	imageRef, err := rt.Prepare(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer rt.cli.ImageRemove(context.Background(), imageRef, image.RemoveOptions{Force: true, PruneChildren: true})
	if !strings.HasPrefix(imageRef, "tpd/packages:") {
		t.Errorf("imageRef = %q, want tpd/packages: prefix", imageRef)
	}
	// The resolved repo must have produced a deb822 .sources and signing key
	// in the derived image (the extrepo reimplementation path), and the repo
	// must have let apt install mise from the mise repo.
	code, err := rt.Run(context.Background(), Spec{
		ProfileName: "test-repos",
		Image:       imageRef,
		Command:     []string{"sh", "-c", `test -x /usr/bin/mise && mise --version && test -f /etc/apt/keyrings/mise.asc && grep -q "Signed-By: /etc/apt/keyrings/mise.asc" /etc/apt/sources.list.d/extrepo_mise.sources`},
		Workspace:   WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: workspace.ModeRootful},
		RuntimeHome: "/root",
		Network:     "none",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("mise run exit code = %d, want 0", code)
	}
}

func TestIntegrationFilesWrittenIntoContainer(t *testing.T) {
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
	// Target under /root/.config with a parent dir that does not exist in the
	// base image — exercises implied-directory creation. The target uses the
	// resolved path (/root = Mode B RuntimeHome), since a Spec's Files targets
	// are post-ResolveTildes.
	spec := Spec{
		ProfileName: "test-files",
		Image:       integrationImage,
		Files: []FileSpec{
			{Target: "/root/.config/tpd-test/deep.conf", Content: "hello-files\n", Mode: 0o644},
		},
		// Existence + content + permissions + ownership are all exercised
		// end-to-end. Writing a sibling into the same parent dir proves the
		// parent was chowned to the execution user (a root-owned 0755 parent
		// would block the write).
		Command:     []string{"sh", "-c", `test "$(cat /root/.config/tpd-test/deep.conf)" = "hello-files" && test "$(stat -c %a /root/.config/tpd-test/deep.conf)" = "644" && test "$(stat -c %u /root/.config/tpd-test/deep.conf)" = "$(id -u)" && echo sibling > /root/.config/tpd-test/sibling.conf && test "$(cat /root/.config/tpd-test/sibling.conf)" = "sibling"`},
		Workspace:   WorkspaceSpec{HostPath: "/tmp", Target: "/workspace", Mode: workspace.ModeRootful},
		RuntimeHome: "/root",
		Network:     "none",
	}
	if _, err := rt.Prepare(context.Background(), spec, NoopProgressWriter{}, false); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	code, err := rt.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("cat-check exit code = %d, want 0", code)
	}
}
