package profile

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestCommandOmitempty verifies that an empty (nil) Command slice is omitted
// from marshaled YAML output due to the omitempty tag. It also covers the
// non-nil empty slice case ([]string{}), which yaml.v3 should also omit.
func TestCommandOmitempty(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command []string
	}{
		{"nil", nil},
		{"empty slice", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Profile{
				Version: 1,
				Image:   "ubuntu",
				Command: tc.command,
			}
			data, err := yaml.Marshal(p)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			out := string(data)
			if strings.Contains(out, "command:") {
				t.Fatalf("expected command to be omitted, got:\n%s", out)
			}
		})
	}
}

// TestCachesBeforeMounts verifies that the Caches field is serialized
// before the Mounts field in marshaled YAML output, matching the struct
// field declaration order.
func TestCachesBeforeMounts(t *testing.T) {
	p := Profile{
		Version: 1,
		Image:   "ubuntu",
		Command: []string{"sh"},
		Caches:  map[string]string{"npm": "~/.npm"},
		Mounts:  map[string]Mount{"/src": {Source: ".", ReadOnly: false}},
		Env:     map[string]string{"FOO": "bar"},
	}
	data, err := yaml.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	out := string(data)
	cachesIdx := strings.Index(out, "caches:")
	mountsIdx := strings.Index(out, "mounts:")
	if cachesIdx < 0 || mountsIdx < 0 {
		t.Fatalf("missing caches or mounts key in:\n%s", out)
	}
	if cachesIdx > mountsIdx {
		t.Errorf("expected caches before mounts in YAML output:\n%s", out)
	}
}
