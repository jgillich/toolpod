package profile

import (
	"os"
	"strconv"
	"testing"
)

func TestResolveTildesMountSourceAndTarget(t *testing.T) {
	cfg := Profile{
		Mounts: map[string]Mount{
			"~/.config/opencode": {Source: "~/.config/opencode", ReadOnly: true},
			"/etc/hosts":         {Source: "/etc/hosts", ReadOnly: true},
		},
		Caches: map[string]string{
			"npm": "~/.npm",
		},
	}
	out, err := ResolveTildes(cfg, "A", "/home/me", "/home/me")
	if err != nil {
		t.Fatal(err)
	}
	m := out.Mounts["/home/me/.config/opencode"]
	if m.Source != "/home/me/.config/opencode" {
		t.Errorf("target-expanded mount source = %q, want /home/me/.config/opencode", m.Source)
	}
	if _, exists := out.Mounts["~/.config/opencode"]; exists {
		t.Error("tilde target key should be replaced with absolute path")
	}
	if out.Caches["npm"] != "/home/me/.npm" {
		t.Errorf("cache target = %q, want /home/me/.npm", out.Caches["npm"])
	}
	if _, exists := out.Mounts["/etc/hosts"]; !exists {
		t.Error("absolute-path mount should be left as-is")
	}
}

func TestResolveTildesModeB(t *testing.T) {
	cfg := Profile{
		Mounts: map[string]Mount{
			"~/.config/opencode": {Source: "~/.config/opencode", ReadOnly: true},
		},
	}
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := out.Mounts["/root/.config/opencode"]; !exists {
		t.Error("target should expand to /root/.config/opencode in Mode B")
	}
	m := out.Mounts["/root/.config/opencode"]
	if m.Source != "/home/me/.config/opencode" {
		t.Errorf("source should expand to host home /home/me/.config/opencode, got %q", m.Source)
	}
}

func TestResolveTildesNoHomeSubstitution(t *testing.T) {
	cfg := Profile{
		Mounts: map[string]Mount{
			"/data": {Source: "/data", ReadOnly: false},
		},
	}
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := out.Mounts["/data"]; !exists {
		t.Error("absolute /data should be unchanged")
	}
}

func TestResolveTildesTemplateExpansion(t *testing.T) {
	os.Setenv("TOOLPOD_TEST_SOCK", "/run/user/1000/podman/podman.sock")
	t.Cleanup(func() { os.Unsetenv("TOOLPOD_TEST_SOCK") })

	cfg := Profile{
		Mounts: map[string]Mount{
			"/var/run/docker.sock": {Source: `{{ or (index .Env "TOOLPOD_TEST_SOCK") "/var/run/docker.sock" }}`, Optional: true},
		},
	}
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	m := out.Mounts["/var/run/docker.sock"]
	if m.Source != "/run/user/1000/podman/podman.sock" {
		t.Errorf("template-expanded source = %q, want /run/user/1000/podman/podman.sock", m.Source)
	}
}

func TestResolveTildesTemplateFallback(t *testing.T) {
	os.Unsetenv("TOOLPOD_UNSET_VAR")
	cfg := Profile{
		Mounts: map[string]Mount{
			"/var/run/docker.sock": {Source: `{{ or (index .Env "TOOLPOD_UNSET_VAR") "/var/run/docker.sock" }}`, Optional: true},
		},
	}
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	m := out.Mounts["/var/run/docker.sock"]
	if m.Source != "/var/run/docker.sock" {
		t.Errorf("fallback source = %q, want /var/run/docker.sock", m.Source)
	}
}

func TestResolveTildesNoDelimitersPassThrough(t *testing.T) {
	cfg := Profile{
		Mounts: map[string]Mount{
			"/data": {Source: "/data", ReadOnly: false},
		},
	}
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	if out.Mounts["/data"].Source != "/data" {
		t.Errorf("plain path = %q, want /data", out.Mounts["/data"].Source)
	}
}

func TestResolveTildesTrimPrefix(t *testing.T) {
	os.Setenv("DOCKER_HOST", "unix:///run/user/1000/podman/podman.sock")
	t.Cleanup(func() { os.Unsetenv("DOCKER_HOST") })

	cfg := Profile{
		Mounts: map[string]Mount{
			"/var/run/docker.sock": {
				Source:   `{{ or (trimPrefix (index .Env "DOCKER_HOST") "unix://") "/run/user/1000/podman/podman.sock" }}`,
				Optional: true,
			},
		},
	}
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	m := out.Mounts["/var/run/docker.sock"]
	want := "/run/user/1000/podman/podman.sock"
	if m.Source != want {
		t.Errorf("trimPrefix source = %q, want %q", m.Source, want)
	}
}

func TestResolveTildesTrimPrefixFallback(t *testing.T) {
	os.Unsetenv("DOCKER_HOST")
	cfg := Profile{
		Mounts: map[string]Mount{
			"/var/run/docker.sock": {
				Source:   `{{ or (trimPrefix (index .Env "DOCKER_HOST") "unix://") (printf "/run/user/%s/podman/podman.sock" (uid)) }}`,
				Optional: true,
			},
		},
	}
	out, err := ResolveTildes(cfg, "B", "/home/me", "/root")
	if err != nil {
		t.Fatal(err)
	}
	m := out.Mounts["/var/run/docker.sock"]
	want := "/run/user/" + strconv.Itoa(os.Getuid()) + "/podman/podman.sock"
	if m.Source != want {
		t.Errorf("fallback source = %q, want %q", m.Source, want)
	}
}
