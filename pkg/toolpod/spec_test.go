package toolpod

import (
	"testing"

	"github.com/jgillich/toolpod/internal/profile"
)

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
	spec := buildSpec(opts, cfg, "B", "/home/me", "/root")
	if spec.Workspace.Target != "/workspace" {
		t.Errorf("Mode B workspace target = %q, want /workspace", spec.Workspace.Target)
	}
}

func TestBuildSpecCommandFlagForShellProfile(t *testing.T) {
	cfg := profile.Profile{Version: 1, Image: "img", Command: []string{"bash"}}
	opts := LaunchOpts{Command: "echo hello", Workspace: "/home/me/proj"}
	spec := buildSpec(opts, cfg, "B", "/home/me", "/root")
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
	spec := buildSpec(opts, cfg, "B", "/home/me", "/root")
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
	spec := buildSpec(opts, cfg, "B", "/home/me", "/root")
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
