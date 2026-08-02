package prune

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	"github.com/jgillich/tpod/internal/runtime"
)

func TestIsTpodVolume(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"tpod-mise", true},
		{"tpod-cache-npm", true},
		{"tpod-cache-cargo", true},
		{"my-volume", false},
		{"docker-volumes", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isTpodVolume(tt.name); got != tt.want {
			t.Errorf("isTpodVolume(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// fakeClient is a stand-in dockerClient for exercising run()/computeUsed
// without a daemon. Inspect returns the configured ID for known image refs
// and a not-found-style error otherwise, so built-in profiles whose base
// image isn't configured contribute no derived-image hashes.
type fakeClient struct {
	volumes  []*volume.Volume
	images   []image.Summary
	inspects map[string]string // ref -> image ID
	removedV []string
	removedI []string
}

func (f *fakeClient) VolumeList(ctx context.Context, _ volume.ListOptions) (volume.ListResponse, error) {
	return volume.ListResponse{Volumes: f.volumes}, nil
}
func (f *fakeClient) VolumeRemove(ctx context.Context, name string, force bool) error {
	f.removedV = append(f.removedV, name)
	return nil
}
func (f *fakeClient) ImageList(ctx context.Context, _ image.ListOptions) ([]image.Summary, error) {
	return f.images, nil
}
func (f *fakeClient) ImageRemove(ctx context.Context, ref string, _ image.RemoveOptions) ([]image.DeleteResponse, error) {
	f.removedI = append(f.removedI, ref)
	return nil, nil
}
func (f *fakeClient) ImageInspectWithRaw(ctx context.Context, ref string) (types.ImageInspect, []byte, error) {
	if id, ok := f.inspects[ref]; ok {
		return types.ImageInspect{ID: id}, nil, nil
	}
	return types.ImageInspect{}, nil, errNotFound{}
}

type errNotFound struct{}

func (errNotFound) Error() string { return "not found" }

// writeUserProfiles writes the given profile files under a temp dir's
// tpod/profiles/ and points XDG_CONFIG_HOME there so DefaultProfileDir()
// resolves to it. Returns the profiles dir.
func writeUserProfiles(t *testing.T, files map[string]string) string {
	t.Helper()
	xdg := t.TempDir()
	profilesDir := filepath.Join(xdg, "tpod", "profiles")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(profilesDir, name+".yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	return profilesDir
}

// A standalone user profile that declares one cache and two packages against
// a base image the fake knows about. Does not extend mise, so the merged
// package set is exactly {curl, git}.
const userProfileYAML = `version: 1
image: mybase:latest
command: ["echo", "hi"]
packages:
  - curl
  - git
caches:
  usedcache: /cache
`

func setupFake(t *testing.T) (*fakeClient, string) {
	writeUserProfiles(t, map[string]string{"myagent": userProfileYAML})
	const baseID = "sha256:baseid"
	usedTag := runtime.DerivedTag(baseID, []string{"curl", "git"}, nil)

	fc := &fakeClient{
		inspects: map[string]string{"mybase:latest": baseID},
		volumes: []*volume.Volume{
			{Name: "tpod-mise"},
			{Name: "tpod-cache-usedcache"},
			{Name: "tpod-cache-orphan"},
		},
		images: []image.Summary{
			{RepoTags: []string{usedTag}},
			{RepoTags: []string{"tpod/packages:deadbeefdeadbeef"}},
		},
	}
	return fc, usedTag
}

func TestRunDefaultPrunesUnusedKeepsUsed(t *testing.T) {
	fc, usedTag := setupFake(t)
	res, err := run(context.Background(), fc, Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	// Volumes: tpod-mise and tpod-cache-usedcache are used; orphan pruned.
	wantV := []string{"tpod-cache-orphan"}
	if !equalSlice(res.VolumesRemoved, wantV) {
		t.Errorf("volumes removed = %v, want %v", res.VolumesRemoved, wantV)
	}
	// Images: usedTag kept, the other pruned.
	wantI := []string{"tpod/packages:deadbeefdeadbeef"}
	if !equalSlice(res.ImagesRemoved, wantI) {
		t.Errorf("images removed = %v, want %v", res.ImagesRemoved, wantI)
	}
	if sliceContains(res.ImagesRemoved, usedTag) {
		t.Errorf("used derived image %q must not be pruned", usedTag)
	}
}

func TestRunPrunesQualifiedDerivedImage(t *testing.T) {
	// Engines qualify RepoTags with a registry (docker.io/, localhost/, ...).
	// A qualified tag of a catalog-unused hash must still be pruned.
	writeUserProfiles(t, map[string]string{"myagent": userProfileYAML})
	const baseID = "sha256:baseid"
	usedTag := runtime.DerivedTag(baseID, []string{"curl", "git"}, nil)
	fc := &fakeClient{
		inspects: map[string]string{"mybase:latest": baseID},
		images: []image.Summary{
			{RepoTags: []string{usedTag}},
			{RepoTags: []string{"localhost/tpod/packages:deadbeefdeadbeef"}},
			{RepoTags: []string{"quay.io/tpod/packages:cafebabe"}},
		},
	}
	res, err := run(context.Background(), fc, Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	wantI := []string{"tpod/packages:deadbeefdeadbeef", "tpod/packages:cafebabe"}
	sort.Strings(wantI)
	gotI := append([]string(nil), res.ImagesRemoved...)
	sort.Strings(gotI)
	if !equalSlice(gotI, wantI) {
		t.Errorf("images removed = %v, want %v (qualified tags normalized to canonical form)", gotI, wantI)
	}
	if sliceContains(res.ImagesRemoved, usedTag) {
		t.Errorf("used derived image %q must not be pruned", usedTag)
	}
}

func TestRunAllRemovesEverything(t *testing.T) {
	fc, usedTag := setupFake(t)
	res, err := run(context.Background(), fc, Options{All: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	wantV := []string{"tpod-cache-orphan", "tpod-cache-usedcache", "tpod-mise"}
	sort.Strings(wantV)
	gotV := append([]string(nil), res.VolumesRemoved...)
	sort.Strings(gotV)
	if !equalSlice(gotV, wantV) {
		t.Errorf("volumes removed = %v, want %v (sorted)", gotV, wantV)
	}
	wantI := []string{"tpod/packages:deadbeefdeadbeef", usedTag}
	sort.Strings(wantI)
	gotI := append([]string(nil), res.ImagesRemoved...)
	sort.Strings(gotI)
	if !equalSlice(gotI, wantI) {
		t.Errorf("images removed = %v, want %v (sorted)", gotI, wantI)
	}
}

func TestRunVolumesScopeOnlyRemovesVolumes(t *testing.T) {
	fc, _ := setupFake(t)
	res, err := run(context.Background(), fc, Options{Volumes: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ImagesRemoved) != 0 {
		t.Errorf("Volumes scope must not remove images; got %v", res.ImagesRemoved)
	}
	if !equalSlice(res.VolumesRemoved, []string{"tpod-cache-orphan"}) {
		t.Errorf("volumes removed = %v, want [tpod-cache-orphan]", res.VolumesRemoved)
	}
}

func TestRunImagesScopeOnlyRemovesImages(t *testing.T) {
	fc, _ := setupFake(t)
	res, err := run(context.Background(), fc, Options{Images: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.VolumesRemoved) != 0 {
		t.Errorf("Images scope must not remove volumes; got %v", res.VolumesRemoved)
	}
	if !equalSlice(res.ImagesRemoved, []string{"tpod/packages:deadbeefdeadbeef"}) {
		t.Errorf("images removed = %v, want [tpod/packages:deadbeefdeadbeef]", res.ImagesRemoved)
	}
}

func TestRunWithoutForceOnNonTtySkipsRemoval(t *testing.T) {
	fc, _ := setupFake(t)
	// Test stdin is not a tty, so confirm() returns false and nothing is
	// removed unless Force is set.
	res, err := run(context.Background(), fc, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.VolumesRemoved) != 0 || len(res.ImagesRemoved) != 0 {
		t.Errorf("non-interactive run without --force must not remove anything; got volumes=%v images=%v", res.VolumesRemoved, res.ImagesRemoved)
	}
}

func TestComputeUsedSkipsMalformedUserYAML(t *testing.T) {
	// One good profile (declares usedcache) + one malformed file. Prune must
	// not abort and must still mark usedcache as used.
	good := `version: 1
image: mybase:latest
command: ["echo"]
caches:
  usedcache: /cache
`
	bad := "version: 1\nimage: mybase:latest\ncommand: [\"echo\"\n  caches: { oops }\n"
	writeUserProfiles(t, map[string]string{"good": good, "bad": bad})

	fc := &fakeClient{inspects: map[string]string{"mybase:latest": "sha256:baseid"}}
	usedV, usedI, err := computeUsed(context.Background(), fc)
	if err != nil {
		t.Fatalf("computeUsed should tolerate malformed user files: %v", err)
	}
	if !usedV["tpod-cache-usedcache"] {
		t.Errorf("usedcache from good profile should be marked used; got %v", usedV)
	}
	if !usedV["tpod-mise"] {
		t.Errorf("tpod-mise must be used when any profile resolves")
	}
	// good profile has no packages (packages omitted), so no derived image.
	if len(usedI) != 0 {
		t.Errorf("expected no used derived images, got %v", usedI)
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sliceContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
