package runtime

import (
	"slices"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/mount"
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

func TestSubpathVolumeSpecs(t *testing.T) {
	mounts, mkdirs := subpathVolumeSpecs(map[string][]string{
		"tpd-cache-mise":  {"aa", "bb"},
		"tpd-cache-other": {"cc"},
	})
	wantMounts := []mount.Mount{
		{Type: mount.TypeVolume, Source: "tpd-cache-mise", Target: "/data/0"},
		{Type: mount.TypeVolume, Source: "tpd-cache-other", Target: "/data/1"},
	}
	if !slices.Equal(mounts, wantMounts) {
		t.Errorf("mounts = %+v, want %+v", mounts, wantMounts)
	}
	wantMkdirs := []string{"/data/0/aa", "/data/0/bb", "/data/1/cc"}
	if !slices.Equal(mkdirs, wantMkdirs) {
		t.Errorf("mkdirs = %v, want %v", mkdirs, wantMkdirs)
	}
}

func TestSubpathVolumeSpecsNoMkdirs(t *testing.T) {
	_, mkdirs := subpathVolumeSpecs(map[string][]string{"tpd-cache-other": nil})
	if len(mkdirs) != 0 {
		t.Errorf("mkdirs = %v, want none", mkdirs)
	}
}
