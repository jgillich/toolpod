package runtime

import (
	"testing"

	"github.com/docker/docker/api/types"
)

func TestSubpathSupportedByVersion(t *testing.T) {
	cases := []struct {
		name string
		ver  types.Version
		want bool
	}{
		{"podman 5.8.4", types.Version{Version: "5.8.4", Components: []types.ComponentVersion{{Name: "Podman Engine", Version: "5.8.4"}}}, false},
		{"podman 6.0.0", types.Version{Version: "6.0.0", Components: []types.ComponentVersion{{Name: "Podman Engine", Version: "6.0.0"}}}, true},
		{"podman 6.0.2", types.Version{Version: "6.0.2", Components: []types.ComponentVersion{{Name: "Podman Engine", Version: "6.0.2"}}}, true},
		{"docker 27.1.0", types.Version{Version: "27.1.0"}, true},
		{"docker 27.0.0", types.Version{Version: "27.0.0"}, false},
		{"docker 20.10", types.Version{Version: "20.10.12"}, false},
		{"unknown engine no version", types.Version{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := subpathSupportedByVersion(tc.ver); got != tc.want {
				t.Errorf("subpathSupportedByVersion(%+v) = %v, want %v", tc.ver, got, tc.want)
			}
		})
	}
}
