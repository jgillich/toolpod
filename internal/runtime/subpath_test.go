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
		{"podman 6.0.0-rc1", types.Version{Version: "6.0.0-rc1", Components: []types.ComponentVersion{{Name: "Podman Engine", Version: "6.0.0-rc1"}}}, false},
		{"podman 6.0.2", types.Version{Version: "6.0.2", Components: []types.ComponentVersion{{Name: "Podman Engine", Version: "6.0.2"}}}, true},
		{"docker 27.1.0", types.Version{Version: "27.1.0"}, true},
		{"docker 27.1.0-beta1", types.Version{Version: "27.1.0-beta1"}, false},
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

func TestAtLeast(t *testing.T) {
	cases := []struct {
		v            string
		maj, min, pa int
		want         bool
	}{
		{"6.0.0", 6, 0, 0, true},
		{"6.0.0-rc1", 6, 0, 0, false},
		{"6.0.0-beta1", 6, 0, 0, false},
		{"6.0.0-dev", 6, 0, 0, false},
		{"6.0.0+build.123", 6, 0, 0, false},
		// A prerelease of a newer minor still must not satisfy the threshold:
		// detection is conservative, and the feature is only known in a stable
		// release.
		{"6.1.0-rc1", 6, 0, 0, false},
		{"6.0.1", 6, 0, 0, true},
		{"6.0", 6, 0, 0, true},
		{"v6.0.0", 6, 0, 0, true},
		{"5.9.9", 6, 0, 0, false},
		{"6.1.0", 6, 0, 0, true},
		{"27.1.0", 27, 1, 0, true},
		{"27.1.0-beta1", 27, 1, 0, false},
		{"27.2.0", 27, 1, 0, true},
		{"27.0.9", 27, 1, 0, false},
		{"", 6, 0, 0, false},
	}
	for _, tc := range cases {
		if got := atLeast(tc.v, tc.maj, tc.min, tc.pa); got != tc.want {
			t.Errorf("atLeast(%q, %d, %d, %d) = %v, want %v", tc.v, tc.maj, tc.min, tc.pa, got, tc.want)
		}
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
