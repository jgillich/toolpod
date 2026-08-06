package catalog_test

import (
	"strings"
	"testing"

	"github.com/jgillich/tpd/internal/profile"
)

// guiWaylandMount is the raw (unrendered) mount key the gui fragment uses for
// the guarded wayland socket: it renders empty (and the optional mount is
// skipped) unless both XDG_RUNTIME_DIR and WAYLAND_DISPLAY are set.
const guiWaylandMount = "{{ if and .Env.XDG_RUNTIME_DIR .Env.WAYLAND_DISPLAY }}{{ .Env.XDG_RUNTIME_DIR }}/{{ .Env.WAYLAND_DISPLAY }}{{ end }}"

// TestPodmanNestedFragment is the canary for the fragment split: `podman` now
// wires an isolated nested engine as a service (no host socket), while
// `podman-host`/`docker-host` keep the host engine socket.
func TestPodmanNestedFragment(t *testing.T) {
	cat, err := profile.LoadProfiles("")
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}

	host, err := profile.ResolveFragment(cat, "services/podman-host")
	if err != nil {
		t.Fatalf("ResolveFragment(services/podman-host): %v", err)
	}
	if len(host.Services) != 0 {
		t.Error("podman-host must not declare services")
	}

	cfg, err := profile.ResolveFragment(cat, "services/podman")
	if err != nil {
		t.Fatalf("ResolveFragment(services/podman): %v", err)
	}
	if len(cfg.Mounts) != 1 {
		t.Fatalf("podman mounts = %v, want only the service-socket mount", cfg.Mounts)
	}
	svcMount, ok := cfg.Mounts["/var/run/docker.sock"]
	if !ok {
		t.Fatal("podman should mount /var/run/docker.sock from the service")
	}
	if svcMount.Service != "podman" || svcMount.Socket != "podman" || svcMount.Source != "" {
		t.Errorf("socket mount = %+v, want service=podman socket=podman with no host source", svcMount)
	}

	svc, ok := cfg.Services["podman"]
	if !ok {
		t.Fatal("podman fragment should declare a service named podman")
	}
	if svc.Image != "debian:13-slim" {
		t.Errorf("service image = %q, want debian:13-slim", svc.Image)
	}
	if len(svc.Command) == 0 {
		t.Error("service command must be set (podman system service)")
	}
	if len(svc.Exposes) != 1 || svc.Exposes["podman"] != "/run/podman/podman.sock" {
		t.Errorf("service exposes = %v, want podman -> /run/podman/podman.sock", svc.Exposes)
	}
	if len(svc.Packages) == 0 {
		t.Error("service should install podman via packages")
	}
	if len(svc.Caches["podman-storage"]) == 0 {
		t.Error("service should cache the nested engine's image store")
	}
	// An unprivileged service container cannot run a nested engine (kernel
	// blocks the nested userns's /proc mount, and there is no /dev/net/tun),
	// so the fragment opts into a privileged rootful sidecar. The service
	// runs podman as root directly — no user/subuid/setpriv machinery.
	if !svc.Privileged {
		t.Error("service must be privileged for the nested rootful engine")
	}
	// The nested engine must avoid the 10.88.0.0/16 default subnet (it
	// collides with the outer network the service container itself is on) and
	// carry a DNS backend, so the fragment writes containers.conf.
	conf, ok := svc.Files["/etc/containers/containers.conf"]
	if !ok {
		t.Fatal("service should write /etc/containers/containers.conf")
	}
	if !strings.Contains(conf.Content, "default_subnet") || !strings.Contains(conf.Content, "172.20.0.0/16") {
		t.Errorf("containers.conf should set a non-colliding default_subnet, got:\n%s", conf.Content)
	}
	cmd := strings.Join(svc.Command, " ")
	if !strings.Contains(cmd, "podman system service") {
		t.Error("service command should run `podman system service`")
	}
	if strings.Contains(cmd, "setpriv") || strings.Contains(cmd, "useradd") {
		t.Error("rootful service must not set up a drop-privilege user")
	}
	if cfg.Env["DOCKER_HOST"] != "unix:///var/run/docker.sock" {
		t.Errorf("DOCKER_HOST = %q, want unix:///var/run/docker.sock", cfg.Env["DOCKER_HOST"])
	}
}

// TestGuiRuntimeSplit is the H-03 canary: gui mounts only the guarded wayland
// socket, broad $XDG_RUNTIME_DIR access moved to the opt-in gui-runtime
// fragment, and the built-in GUI profiles extend both.
func TestGuiRuntimeSplit(t *testing.T) {
	cat, err := profile.LoadProfiles("")
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}

	guiCfg, err := profile.ResolveFragment(cat, "gui/gui")
	if err != nil {
		t.Fatalf("ResolveFragment(gui/gui): %v", err)
	}
	if _, ok := guiCfg.Mounts["{{ .Env.XDG_RUNTIME_DIR }}"]; ok {
		t.Error("gui must not mount $XDG_RUNTIME_DIR wholesale; use gui-runtime")
	}
	if _, ok := guiCfg.Mounts[guiWaylandMount]; !ok {
		t.Error("gui should mount only the guarded wayland socket")
	}

	rtCfg, err := profile.ResolveFragment(cat, "gui/gui-runtime")
	if err != nil {
		t.Fatalf("ResolveFragment(gui/gui-runtime): %v", err)
	}
	if _, ok := rtCfg.Mounts["{{ .Env.XDG_RUNTIME_DIR }}"]; !ok {
		t.Error("gui-runtime should mount $XDG_RUNTIME_DIR wholesale")
	}

	for _, name := range []string{"buzz", "t3code"} {
		cfg, err := profile.ResolveProfile(cat, name)
		if err != nil {
			t.Fatalf("ResolveProfile(%s): %v", name, err)
		}
		if _, ok := cfg.Mounts["{{ .Env.XDG_RUNTIME_DIR }}"]; !ok {
			t.Errorf("%s: resolved mounts should include the gui-runtime runtime-dir mount", name)
		}
		if _, ok := cfg.Mounts[guiWaylandMount]; !ok {
			t.Errorf("%s: resolved mounts should include the gui wayland socket mount", name)
		}
	}
}

// TestBuiltinAppimageToolsStayLatest is the canary for the H-04 design: the
// appimage backend resolves `latest` at install time instead of the catalog
// pinning versions, so the built-in profiles must still load and validate
// with a bare `latest` (no checksum).
func TestBuiltinAppimageToolsStayLatest(t *testing.T) {
	cat, err := profile.LoadProfiles("")
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	want := map[string]string{
		"buzz":   "appimage:block/buzz",
		"t3code": "appimage:pingdotgg/t3code",
	}
	for name, toolName := range want {
		cfg, err := profile.ResolveProfile(cat, name)
		if err != nil {
			t.Fatalf("ResolveProfile(%s): %v", name, err)
		}
		tool, ok := cfg.Tools[toolName]
		if !ok {
			t.Fatalf("%s: missing tool %s", name, toolName)
		}
		if tool.Version != "latest" {
			t.Errorf("%s: %s version = %q, want latest", name, toolName, tool.Version)
		}
		if tool.SHA256 != "" || len(tool.SHA256ByArch) > 0 {
			t.Errorf("%s: %s should carry no checksum in the catalog, got %+v", name, toolName, tool)
		}
	}
}
