package tpd

import (
	"strings"
	"testing"

	"github.com/jgillich/tpd/internal/runtime"
	"github.com/jgillich/tpd/internal/workspace"
)

func TestRenderSpecResources(t *testing.T) {
	spec := Spec{
		ProfileName: "res",
		Image:       "img",
		Command:     []string{"x"},
		Workspace:   WorkspaceSpec{HostPath: "/p", Target: "/workspace", Mode: workspace.ModeRootful},
		Resources:   runtime.ResourceSpec{MemoryBytes: 512 << 20, NanoCPUs: 2e9},
	}
	var out strings.Builder
	if err := RenderSpec(&out, spec); err != nil {
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
	spec := Spec{
		ProfileName: "web",
		Image:       "img",
		Command:     []string{"x"},
		Workspace:   WorkspaceSpec{HostPath: "/p", Target: "/workspace", Mode: workspace.ModeRootful},
		PortSpecs: []PortSpec{
			{Container: "8080", HostPort: "40001", Protocol: "tcp"},
			{Container: "53", HostIP: "127.0.0.1", HostPort: "40002", Protocol: "udp"},
		},
		DeviceSpecs: []DeviceSpec{
			{Container: "/dev/fuse", Host: "/dev/fuse", Perms: "rwm"},
		},
	}
	var out strings.Builder
	if err := RenderSpec(&out, spec); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range []string{
		"ports:",
		"  8080/tcp -> 0.0.0.0:40001",
		"  53/udp -> 127.0.0.1:40002",
		"devices:",
		"  /dev/fuse <- /dev/fuse (rwm)",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("dry-run output missing %q; got:\n%s", want, output)
		}
	}
}
