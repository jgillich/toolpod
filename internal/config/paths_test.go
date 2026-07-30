package config

import "testing"

func TestResolveTildesMountSourceAndTarget(t *testing.T) {
	cfg := Config{
		Mounts: map[string]Mount{
			"~/.config/opencode": {Source: "~/.config/opencode", ReadOnly: true},
			"/etc/hosts":          {Source: "/etc/hosts", ReadOnly: true},
		},
		Caches: map[string]string{
			"npm": "~/.npm",
		},
	}
	out := ResolveTildes(cfg, "A", "/home/me", "/home/me")
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
	cfg := Config{
		Mounts: map[string]Mount{
			"~/.config/opencode": {Source: "~/.config/opencode", ReadOnly: true},
		},
	}
	out := ResolveTildes(cfg, "B", "/home/me", "/root")
	if _, exists := out.Mounts["/root/.config/opencode"]; !exists {
		t.Error("target should expand to /root/.config/opencode in Mode B")
	}
	m := out.Mounts["/root/.config/opencode"]
	if m.Source != "/home/me/.config/opencode" {
		t.Errorf("source should expand to host home /home/me/.config/opencode, got %q", m.Source)
	}
}

func TestResolveTildesNoHomeSubstitution(t *testing.T) {
	cfg := Config{
		Mounts: map[string]Mount{
			"/data": {Source: "/data", ReadOnly: false},
		},
	}
	out := ResolveTildes(cfg, "B", "/home/me", "/root")
	if _, exists := out.Mounts["/data"]; !exists {
		t.Error("absolute /data should be unchanged")
	}
}
