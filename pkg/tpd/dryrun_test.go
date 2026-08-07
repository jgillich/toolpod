package tpd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jgillich/tpd/internal/runtime"
	"github.com/jgillich/tpd/internal/workspace"
)

func TestRenderSpecResources(t *testing.T) {
	spec := runtime.Spec{
		ProfileName: "res",
		Image:       "img",
		Command:     []string{"x"},
		Workspace:   runtime.WorkspaceSpec{HostPath: "/p", Target: "/workspace", Mode: workspace.ModeRootful},
		Resources:   runtime.ResourceSpec{MemoryBytes: 512 << 20, NanoCPUs: 2e9},
	}
	var out strings.Builder
	if err := renderSpec(&out, spec); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range []string{
		"resources:",
		"  memory: 536870912",
		"  cpus: 2000000000 (nano)",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("dry-run output missing %q; got:\n%s", want, output)
		}
	}
}

func TestRenderSpecPortsAndDevices(t *testing.T) {
	spec := runtime.Spec{
		ProfileName: "web",
		Image:       "img",
		Command:     []string{"x"},
		Workspace:   runtime.WorkspaceSpec{HostPath: "/p", Target: "/workspace", Mode: workspace.ModeRootful},
		PortSpecs: []runtime.PortSpec{
			{Container: "8080", HostIP: "127.0.0.1", HostPort: "40001", Protocol: "tcp"},
			{Container: "7000", HostIP: "0.0.0.0", HostPort: "7000", Protocol: "tcp"},
			{Container: "53", HostIP: "127.0.0.1", HostPort: "40002", Protocol: "udp"},
		},
		DeviceSpecs: []runtime.DeviceSpec{
			{Container: "/dev/fuse", Host: "/dev/fuse", Perms: "rwm"},
		},
	}
	var out strings.Builder
	if err := renderSpec(&out, spec); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range []string{
		"ports:",
		"  8080/tcp -> 127.0.0.1:40001",
		"  7000/tcp -> 0.0.0.0:7000",
		"  53/udp -> 127.0.0.1:40002",
		"devices:",
		"  /dev/fuse <- /dev/fuse (rwm)",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("dry-run output missing %q; got:\n%s", want, output)
		}
	}
}

func TestRenderSpecServices(t *testing.T) {
	var buf bytes.Buffer
	spec := runtime.Spec{
		ProfileName: "test",
		Image:       "ubuntu",
		Command:     []string{"sh"},
		Services: []runtime.ServiceSpec{
			{
				Name:    "registry",
				Hash:    "abcd1234",
				Image:   "debian:13-slim",
				Command: []string{"registry"},
				Exposes: map[string]string{"registry": "/run/registry/registry.sock"},
				Caches: []runtime.CacheSpec{
					{Name: "tpd-cache-data", Target: "/var/lib/registry"},
				},
			},
		},
		Mounts: []runtime.MountSpec{
			{Target: "/sock", Service: "registry", Socket: "registry"},
		},
	}
	if err := renderSpec(&buf, spec); err != nil {
		t.Fatalf("renderSpec: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "services:") {
		t.Error("expected 'services:' section in output")
	}
	if !strings.Contains(out, "registry:") {
		t.Error("expected service name 'registry' in output")
	}
	if !strings.Contains(out, "service:registry socket:registry") {
		t.Error("expected service-socket mount rendered with service/socket")
	}
}

func TestRenderSpecUnknownWorkspaceTarget(t *testing.T) {
	spec := runtime.Spec{
		ProfileName: "preview",
		Image:       "img",
		Command:     []string{"sh"},
		Workspace:   runtime.WorkspaceSpec{HostPath: "/home/me/proj", Target: "", Mode: workspace.ModeUnknown},
	}
	var out strings.Builder
	if err := renderSpec(&out, spec); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range []string{
		"  host: /home/me/proj",
		"  target: <unknown>",
		"  mode: unknown",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("dry-run output missing %q; got:\n%s", want, output)
		}
	}
}

func TestRenderSpecReposFilesTTY(t *testing.T) {
	spec := runtime.Spec{
		ProfileName: "full",
		Image:       "img",
		Command:     []string{"sh"},
		TTY:         "true",
		Repos: map[string]runtime.Repo{
			"docker": {ExtRepo: "docker"},
			"custom": {URL: "https://deb.example.com", KeyURL: "https://deb.example.com/key.asc", Suites: "stable", Components: "main"},
		},
		Files: []runtime.FileSpec{
			{Target: "/etc/motd", Content: "hello world", Mode: 0o644},
		},
	}
	var out strings.Builder
	if err := renderSpec(&out, spec); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range []string{
		"tty: true",
		"repos:",
		"  custom:",
		"    url: https://deb.example.com",
		"    key-url: https://deb.example.com/key.asc",
		"    suites: stable",
		"    components: main",
		"  docker: extrepo docker",
		"files:",
		"  /etc/motd:",
		"    mode: 0644",
		`    content: "hello world"`,
	} {
		if !strings.Contains(output, want) {
			t.Errorf("dry-run output missing %q; got:\n%s", want, output)
		}
	}
}
