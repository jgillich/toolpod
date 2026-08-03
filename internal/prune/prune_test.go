package prune

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	"github.com/jgillich/tpd/internal/runtime"
)

func TestIsTpdVolume(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"tpd-mise", true},
		{"tpd-cache-npm", true},
		{"tpd-cache-cargo", true},
		{"my-volume", false},
		{"docker-volumes", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isTpdVolume(tt.name); got != tt.want {
			t.Errorf("isTpdVolume(%q) = %v, want %v", tt.name, got, tt.want)
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
// tpd/profiles/ and points XDG_CONFIG_HOME there so DefaultProfileDir()
// resolves to it. Returns the profiles dir.
func writeUserProfiles(t *testing.T, files map[string]string) string {
	t.Helper()
	xdg := t.TempDir()
	profilesDir := filepath.Join(xdg, "tpd", "profiles")
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
			{Name: "tpd-cache-usedcache", Labels: runtime.OwnershipLabels()},
			{Name: "tpd-cache-orphan", Labels: runtime.OwnershipLabels()},
		},
		images: []image.Summary{
			{RepoTags: []string{usedTag}, Labels: runtime.OwnershipLabels()},
			{RepoTags: []string{"tpd/packages:deadbeefdeadbeef"}, Labels: runtime.OwnershipLabels()},
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
	// Volumes: tpd-cache-usedcache is used; orphan pruned.
	wantV := []string{"tpd-cache-orphan"}
	if !equalSlice(res.VolumesRemoved, wantV) {
		t.Errorf("volumes removed = %v, want %v", res.VolumesRemoved, wantV)
	}
	// Images: usedTag kept, the other pruned.
	wantI := []string{"tpd/packages:deadbeefdeadbeef"}
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
			{RepoTags: []string{usedTag}, Labels: runtime.OwnershipLabels()},
			{RepoTags: []string{"localhost/tpd/packages:deadbeefdeadbeef"}, Labels: runtime.OwnershipLabels()},
			{RepoTags: []string{"quay.io/tpd/packages:cafebabe"}, Labels: runtime.OwnershipLabels()},
		},
	}
	res, err := run(context.Background(), fc, Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	wantI := []string{"tpd/packages:deadbeefdeadbeef", "tpd/packages:cafebabe"}
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

func TestPruneSkipsUnlabeledTpdResources(t *testing.T) {
	// Pre-labeling cruft: volumes/images matching the tpd name pattern but
	// missing the ownership label must survive prune (with a stderr warning)
	// so an unrelated user resource that merely starts with "tpd-" is never
	// removed.
	writeUserProfiles(t, map[string]string{"myagent": userProfileYAML})
	fc := &fakeClient{
		inspects: map[string]string{"mybase:latest": "sha256:baseid"},
		volumes: []*volume.Volume{
			{Name: "tpd-cache-orphan", Labels: runtime.OwnershipLabels()},
			{Name: "tpd-important-data"},
		},
		images: []image.Summary{
			{RepoTags: []string{"tpd/packages:deadbeefdeadbeef"}, Labels: runtime.OwnershipLabels()},
			{RepoTags: []string{"tpd/packages:cafebabe"}},
		},
	}
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()

	res, err := run(context.Background(), fc, Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	warned, _ := io.ReadAll(r)

	if !equalSlice(res.VolumesRemoved, []string{"tpd-cache-orphan"}) {
		t.Errorf("volumes removed = %v, want only labeled [tpd-cache-orphan]", res.VolumesRemoved)
	}
	if sliceContains(res.VolumesRemoved, "tpd-important-data") {
		t.Error("unlabeled volume tpd-important-data must not be removed")
	}
	if !equalSlice(res.ImagesRemoved, []string{"tpd/packages:deadbeefdeadbeef"}) {
		t.Errorf("images removed = %v, want only labeled [tpd/packages:deadbeefdeadbeef]", res.ImagesRemoved)
	}
	if sliceContains(res.ImagesRemoved, "tpd/packages:cafebabe") {
		t.Error("unlabeled image tpd/packages:cafebabe must not be removed")
	}
	for _, want := range []string{"tpd-important-data", "tpd/packages:cafebabe", "not tpd-owned"} {
		if !strings.Contains(string(warned), want) {
			t.Errorf("stderr should warn about skipped unlabeled resource %q; got %q", want, string(warned))
		}
	}
}

func TestRunAllRemovesEverything(t *testing.T) {
	fc, usedTag := setupFake(t)
	res, err := run(context.Background(), fc, Options{All: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	wantV := []string{"tpd-cache-orphan", "tpd-cache-usedcache"}
	sort.Strings(wantV)
	gotV := append([]string(nil), res.VolumesRemoved...)
	sort.Strings(gotV)
	if !equalSlice(gotV, wantV) {
		t.Errorf("volumes removed = %v, want %v (sorted)", gotV, wantV)
	}
	wantI := []string{"tpd/packages:deadbeefdeadbeef", usedTag}
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
	if !equalSlice(res.VolumesRemoved, []string{"tpd-cache-orphan"}) {
		t.Errorf("volumes removed = %v, want [tpd-cache-orphan]", res.VolumesRemoved)
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
	if !equalSlice(res.ImagesRemoved, []string{"tpd/packages:deadbeefdeadbeef"}) {
		t.Errorf("images removed = %v, want [tpd/packages:deadbeefdeadbeef]", res.ImagesRemoved)
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
	if !usedV["tpd-cache-usedcache"] {
		t.Errorf("usedcache from good profile should be marked used; got %v", usedV)
	}
	// Built-in tool profiles extend mise, so the consolidated mise cache stays
	// used even though this standalone user profile does not declare it.
	if !usedV["tpd-cache-mise"] {
		t.Errorf("tpd-cache-mise should be marked used via built-in mise-extending profiles; got %v", usedV)
	}
	// volumeUsed must also keep per-target fallback volumes derived from a base
	// cache name (e.g. tpd-cache-mise-<hash>) so prune never removes them.
	if !volumeUsed("tpd-cache-mise-1234abcd", usedV) {
		t.Error("volumeUsed should match base-<hash> fallback volumes")
	}
	// good profile has no packages (packages omitted), so no derived image.
	if len(usedI) != 0 {
		t.Errorf("expected no used derived images, got %v", usedI)
	}
}

func TestComputeUsedMarksMiseProfileCaches(t *testing.T) {
	// A profile extending mise inherits the consolidated `mise` cache; prune must
	// keep that volume while the profile resolves.
	writeUserProfiles(t, map[string]string{"myagent": `version: 1
extends: mise
command: ["echo"]
`})
	fc := &fakeClient{inspects: map[string]string{"debian:13-slim": "sha256:baseid"}}
	usedV, _, err := computeUsed(context.Background(), fc)
	if err != nil {
		t.Fatalf("computeUsed: %v", err)
	}
	if !usedV["tpd-cache-mise"] {
		t.Errorf("mise-extending profile should mark tpd-cache-mise used; got %v", usedV)
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
