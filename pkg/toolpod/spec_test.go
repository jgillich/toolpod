package toolpod

import (
	"testing"

	"github.com/jgillich/toolpod/internal/config"
)

func TestBuildSpecBasic(t *testing.T) {
	cfg := config.Config{
		Version: 1,
		Image:   "myimage:latest",
		Command: []string{"opencode"},
		Tools:   map[string]string{"opencode": "latest", "node": "20"},
		Mounts: map[string]config.Mount{
			"~/.config/opencode": {Source: "~/.config/opencode", ReadOnly: true},
		},
		Caches:  map[string]string{"npm": "~/.npm"},
		Network: "bridge",
	}
	opts := LaunchOpts{Args: []string{"--model", "foo"}, Workspace: "/home/me/proj"}
	spec := buildSpec(opts, cfg, "A", "/home/me", "/home/me")

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
	mount, ok := spec.Mounts[0], true
	_ = ok
	if mount.Target != "/home/me/.config/opencode" {
		t.Errorf("mount[0].Target = %q, want /home/me/.config/opencode", mount.Target)
	}
}

func TestBuildSpecModeBWorkspace(t *testing.T) {
	cfg := config.Config{Version: 1, Image: "x", Command: []string{"sh"}}
	opts := LaunchOpts{Workspace: "/home/me/proj"}
	spec := buildSpec(opts, cfg, "B", "/home/me", "/root")
	if spec.Workspace.Target != "/workspace" {
		t.Errorf("Mode B workspace target = %q, want /workspace", spec.Workspace.Target)
	}
}
