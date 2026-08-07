package prune

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
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
	volumes             []*volume.Volume
	images              []image.Summary
	networks            []network.Summary
	inspects            map[string]string              // ref -> image ID
	containers          []types.Container              // returned by ContainerList
	containerInspects   map[string]types.ContainerJSON // ID -> inspect
	removedV            []string
	removedI            []string
	removedN            []string
	containerCalls      int
	activateVolume      string
	activateImage       string
	listContainersErr   error
	inspectContainerErr error
	inspectImageErrs    map[string]error
	removeVolErrs       map[string]error
	removeImgErrs       map[string]error
}

func (f *fakeClient) VolumeList(ctx context.Context, _ volume.ListOptions) (volume.ListResponse, error) {
	return volume.ListResponse{Volumes: f.volumes}, nil
}
func (f *fakeClient) VolumeRemove(ctx context.Context, name string, force bool) error {
	if err := f.removeVolErrs[name]; err != nil {
		return err
	}
	f.removedV = append(f.removedV, name)
	return nil
}
func (f *fakeClient) ImageList(ctx context.Context, _ image.ListOptions) ([]image.Summary, error) {
	return f.images, nil
}
func (f *fakeClient) ImageRemove(ctx context.Context, ref string, _ image.RemoveOptions) ([]image.DeleteResponse, error) {
	if err := f.removeImgErrs[ref]; err != nil {
		return nil, err
	}
	f.removedI = append(f.removedI, ref)
	return nil, nil
}
func (f *fakeClient) NetworkList(ctx context.Context, _ network.ListOptions) ([]network.Summary, error) {
	return f.networks, nil
}
func (f *fakeClient) NetworkRemove(ctx context.Context, networkID string) error {
	f.removedN = append(f.removedN, networkID)
	return nil
}
func (f *fakeClient) ImageInspectWithRaw(ctx context.Context, ref string) (types.ImageInspect, []byte, error) {
	if err := f.inspectImageErrs[ref]; err != nil {
		return types.ImageInspect{}, nil, err
	}
	if id, ok := f.inspects[ref]; ok {
		return types.ImageInspect{ID: id}, nil, nil
	}
	for _, img := range f.images {
		for _, tag := range img.RepoTags {
			if tag == ref || runtime.DerivedRef(tag) == ref {
				if img.ID == "" {
					return types.ImageInspect{ID: "sha256:" + strings.TrimPrefix(strings.ReplaceAll(ref, "/", "_"), "sha256:")}, nil, nil
				}
				return types.ImageInspect{ID: img.ID}, nil, nil
			}
		}
	}
	return types.ImageInspect{}, nil, errNotFound{}
}
func (f *fakeClient) ContainerList(ctx context.Context, _ container.ListOptions) ([]types.Container, error) {
	f.containerCalls++
	if f.listContainersErr != nil {
		return nil, f.listContainersErr
	}
	if f.activateVolume != "" && f.containerCalls > 1 {
		f.containers = []types.Container{{ID: "late", State: "running", Labels: runtime.OwnershipLabels()}}
		f.containerInspects = map[string]types.ContainerJSON{"late": {
			ContainerJSONBase: &types.ContainerJSONBase{Image: f.activateImage},
			Mounts:            []types.MountPoint{{Type: mount.TypeVolume, Name: f.activateVolume}},
		}}
	}
	return f.containers, nil
}
func (f *fakeClient) ContainerInspectWithRaw(ctx context.Context, id string, _ bool) (types.ContainerJSON, []byte, error) {
	if f.inspectContainerErr != nil {
		return types.ContainerJSON{}, nil, f.inspectContainerErr
	}
	if insp, ok := f.containerInspects[id]; ok {
		return insp, nil, nil
	}
	return types.ContainerJSON{}, nil, errNotFound{}
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
		inspects: map[string]string{"mybase:latest": baseID, "debian:13-slim": "sha256:builtinid"},
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
		inspects: map[string]string{"mybase:latest": baseID, "debian:13-slim": "sha256:builtinid"},
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
		inspects: map[string]string{"mybase:latest": "sha256:baseid", "debian:13-slim": "sha256:builtinid"},
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

func TestRunSkipsVolumesInUseByRunningContainer(t *testing.T) {
	// A labeled volume mounted by a running tpd container must survive prune
	// even though no profile declares it, while an unreferenced one is removed.
	writeUserProfiles(t, map[string]string{"myagent": userProfileYAML})
	fc := &fakeClient{
		inspects: map[string]string{"mybase:latest": "sha256:baseid"},
		volumes: []*volume.Volume{
			{Name: "tpd-cache-orphan", Labels: runtime.OwnershipLabels()},
			{Name: "tpd-cache-orphan2", Labels: runtime.OwnershipLabels()},
		},
		containers: []types.Container{{ID: "c1", State: "running", Labels: runtime.OwnershipLabels()}},
		containerInspects: map[string]types.ContainerJSON{
			"c1": {
				ContainerJSONBase: &types.ContainerJSONBase{Image: "sha256:unused"},
				Mounts:            []types.MountPoint{{Type: mount.TypeVolume, Name: "tpd-cache-orphan"}},
			},
		},
	}
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	res, err := run(context.Background(), fc, Options{Force: true})
	w.Close()
	os.Stderr = old
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(r)
	if sliceContains(res.VolumesRemoved, "tpd-cache-orphan") {
		t.Error("volume tpd-cache-orphan mounted by a running container must not be removed")
	}
	if !equalSlice(res.VolumesRemoved, []string{"tpd-cache-orphan2"}) {
		t.Errorf("volumes removed = %v, want only unreferenced [tpd-cache-orphan2]", res.VolumesRemoved)
	}
	if !strings.Contains(string(out), "skipping tpd-cache-orphan: in use by a running container") {
		t.Errorf("stderr should report the skip; got %q", string(out))
	}
}

func TestRunRechecksVolumeUseBeforeRemoval(t *testing.T) {
	fc := &fakeClient{
		volumes:        []*volume.Volume{{Name: "tpd-cache-race", Labels: runtime.OwnershipLabels()}},
		activateVolume: "tpd-cache-race",
	}

	res, err := run(context.Background(), fc, Options{All: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.VolumesRemoved) != 0 || len(fc.removedV) != 0 {
		t.Fatalf("late-mounted volume was removed: result=%v calls=%v", res.VolumesRemoved, fc.removedV)
	}
}

func TestRunAllSkipsVolumesInUseByRunningContainer(t *testing.T) {
	// --all relaxes the catalog-liveness check only; a volume mounted by a
	// running container must still survive.
	writeUserProfiles(t, map[string]string{"myagent": userProfileYAML})
	fc := &fakeClient{
		inspects: map[string]string{"mybase:latest": "sha256:baseid"},
		volumes: []*volume.Volume{
			{Name: "tpd-cache-usedcache", Labels: runtime.OwnershipLabels()},
			{Name: "tpd-cache-orphan", Labels: runtime.OwnershipLabels()},
		},
		containers: []types.Container{{ID: "c1", State: "running", Labels: runtime.OwnershipLabels()}},
		containerInspects: map[string]types.ContainerJSON{
			"c1": {
				ContainerJSONBase: &types.ContainerJSONBase{Image: "sha256:unused"},
				Mounts:            []types.MountPoint{{Type: mount.TypeVolume, Name: "tpd-cache-usedcache"}},
			},
		},
	}
	res, err := run(context.Background(), fc, Options{All: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if sliceContains(res.VolumesRemoved, "tpd-cache-usedcache") {
		t.Error("volume tpd-cache-usedcache mounted by a running container must survive --all")
	}
	if !equalSlice(res.VolumesRemoved, []string{"tpd-cache-orphan"}) {
		t.Errorf("volumes removed = %v, want only unreferenced [tpd-cache-orphan]", res.VolumesRemoved)
	}
}

func TestRunSkipsImagesInUseByRunningContainer(t *testing.T) {
	// A derived image whose ID a running container references must survive
	// prune, with and without --all; an unreferenced derived image is removed.
	writeUserProfiles(t, map[string]string{"myagent": userProfileYAML})
	const baseID = "sha256:baseid"
	usedTag := runtime.DerivedTag(baseID, []string{"curl", "git"}, nil)
	base := &fakeClient{
		inspects: map[string]string{"mybase:latest": baseID, "debian:13-slim": "sha256:builtinid", "tpd/packages:deadbeefdeadbeef": "sha256:orphanid"},
		images: []image.Summary{
			{RepoTags: []string{usedTag}, Labels: runtime.OwnershipLabels()},
			{RepoTags: []string{"tpd/packages:deadbeefdeadbeef"}, Labels: runtime.OwnershipLabels()},
			{RepoTags: []string{"tpd/packages:cafebabe"}, Labels: runtime.OwnershipLabels()},
		},
		containers: []types.Container{{ID: "c1", State: "running", Labels: runtime.OwnershipLabels()}},
		containerInspects: map[string]types.ContainerJSON{
			"c1": {ContainerJSONBase: &types.ContainerJSONBase{Image: "sha256:orphanid"}},
		},
	}
	for _, tt := range []struct {
		name string
		all  bool
		want []string
	}{
		{name: "default", want: []string{"tpd/packages:cafebabe"}},
		{name: "all", all: true, want: []string{"tpd/packages:cafebabe", usedTag}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fc := *base
			res, err := run(context.Background(), &fc, Options{All: tt.all, Force: true})
			if err != nil {
				t.Fatal(err)
			}
			if sliceContains(res.ImagesRemoved, "tpd/packages:deadbeefdeadbeef") {
				t.Error("derived image in use by a running container must not be removed")
			}
			sort.Strings(tt.want)
			got := append([]string(nil), res.ImagesRemoved...)
			sort.Strings(got)
			if !equalSlice(got, tt.want) {
				t.Errorf("images removed = %v, want %v", got, tt.want)
			}
		})
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
	u, err := computeUsed(context.Background(), fc)
	if err != nil {
		t.Fatalf("computeUsed should tolerate malformed user files: %v", err)
	}
	if !u.volumes["tpd-cache-usedcache"] {
		t.Errorf("usedcache from good profile should be marked used; got %v", u.volumes)
	}
	// Built-in tool profiles extend mise, so the consolidated mise cache stays
	// used even though this standalone user profile does not declare it.
	if !u.volumes["tpd-cache-mise"] {
		t.Errorf("tpd-cache-mise should be marked used via built-in mise-extending profiles; got %v", u.volumes)
	}
	// volumeUsed must also keep per-target fallback volumes derived from a base
	// cache name (e.g. tpd-cache-mise-<hash>) so prune never removes them.
	if !volumeUsed("tpd-cache-mise-1234abcd", u.volumes) {
		t.Error("volumeUsed should match base-<hash> fallback volumes")
	}
	// good profile has no packages (packages omitted), so no derived image.
	if len(u.images) != 0 {
		t.Errorf("expected no used derived images, got %v", u.images)
	}
	// The malformed file was skipped by tolerant loading: its caches can't be
	// assessed, so prune must defer volume pruning.
	if !u.deferVolumes {
		t.Error("deferVolumes should be set when a user file is skipped")
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
	u, err := computeUsed(context.Background(), fc)
	if err != nil {
		t.Fatalf("computeUsed: %v", err)
	}
	if !u.volumes["tpd-cache-mise"] {
		t.Errorf("mise-extending profile should mark tpd-cache-mise used; got %v", u.volumes)
	}
}

func TestComputeUsedServices(t *testing.T) {
	// A profile declaring a service with its own caches and packages must keep
	// the service's cache volumes and derived image.
	writeUserProfiles(t, map[string]string{"myagent": `version: 1
image: mybase:latest
command: ["echo", "hi"]
services:
  db:
    image: mysvcbase:latest
    command: ["postgres"]
    packages:
      - postgresql
    caches:
      svccache: /cache
`})
	const svcBaseID = "sha256:svcbaseid"
	svcTag := runtime.DerivedTag(svcBaseID, []string{"postgresql"}, nil)
	fc := &fakeClient{
		inspects: map[string]string{"mybase:latest": "sha256:baseid", "mysvcbase:latest": svcBaseID},
	}
	u, err := computeUsed(context.Background(), fc)
	if err != nil {
		t.Fatal(err)
	}
	if !u.volumes["tpd-cache-svccache"] {
		t.Errorf("service cache tpd-cache-svccache should be marked used; got %v", u.volumes)
	}
	if !u.images[svcTag] {
		t.Errorf("service derived image %q should be marked used; got %v", svcTag, u.images)
	}
}

// userProfileReposYAML is a standalone profile whose derived image depends on
// both packages and an extrepo. Its used-tag must be reproducible offline
// from the declared inputs plus the local base image ID.
const userProfileReposYAML = `version: 1
image: mybase:latest
command: ["echo", "hi"]
packages:
  - curl
  - git
repos:
  ghr:
    extrepo: mise
`

func TestComputeUsedDerivedTagOfflineRecomputable(t *testing.T) {
	// The used-derived-tag set must be a pure function of declared profile
	// inputs plus the local base image ID: the fake client serves no network,
	// so any fetch (repo catalog, key, ...) would fail the test. Recomputing
	// the tag locally from the same inputs must reproduce prune's used set.
	writeUserProfiles(t, map[string]string{"myagent": userProfileReposYAML})
	const baseID = "sha256:baseid"
	want := runtime.DerivedTag(baseID, []string{"curl", "git"}, map[string]runtime.Repo{"ghr": {ExtRepo: "mise"}})
	fc := &fakeClient{inspects: map[string]string{"mybase:latest": baseID}}
	u, err := computeUsed(context.Background(), fc)
	if err != nil {
		t.Fatal(err)
	}
	if !u.images[want] {
		t.Errorf("computeUsed must mark offline-recomputed tag %q used; got %v", want, u.images)
	}
	if len(u.images) != 1 {
		t.Errorf("expected exactly one used derived image, got %v", u.images)
	}
}

func TestComputeUsedReposOnlyDerivedTagOfflineRecomputable(t *testing.T) {
	// A repos-only profile still produces a derived image (repos are COPYed
	// in); its tag is a pure function of the declared repo inputs + base ID.
	writeUserProfiles(t, map[string]string{"myagent": `version: 1
image: mybase:latest
command: ["echo", "hi"]
repos:
  ghr:
    extrepo: mise
`})
	const baseID = "sha256:baseid"
	want := runtime.DerivedTag(baseID, nil, map[string]runtime.Repo{"ghr": {ExtRepo: "mise"}})
	fc := &fakeClient{inspects: map[string]string{"mybase:latest": baseID}}
	u, err := computeUsed(context.Background(), fc)
	if err != nil {
		t.Fatal(err)
	}
	if !u.images[want] {
		t.Errorf("computeUsed must mark repos-only offline-recomputed tag %q used; got %v", want, u.images)
	}
}

func TestRunKeepsOfflineRecomputableReposDerivedImage(t *testing.T) {
	// End to end: the derived image for a packages+repos profile is kept by
	// default prune, and an unused hash is removed, with no network.
	writeUserProfiles(t, map[string]string{"myagent": userProfileReposYAML})
	const baseID = "sha256:baseid"
	tag := runtime.DerivedTag(baseID, []string{"curl", "git"}, map[string]runtime.Repo{"ghr": {ExtRepo: "mise"}})
	fc := &fakeClient{
		inspects: map[string]string{"mybase:latest": baseID, "debian:13-slim": "sha256:builtinid"},
		images: []image.Summary{
			{RepoTags: []string{tag}, Labels: runtime.OwnershipLabels()},
			{RepoTags: []string{"tpd/packages:deadbeefdeadbeef"}, Labels: runtime.OwnershipLabels()},
		},
	}
	res, err := run(context.Background(), fc, Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if sliceContains(res.ImagesRemoved, tag) {
		t.Errorf("used derived image %q must not be pruned", tag)
	}
	if !equalSlice(res.ImagesRemoved, []string{"tpd/packages:deadbeefdeadbeef"}) {
		t.Errorf("images removed = %v, want only the unused [tpd/packages:deadbeefdeadbeef]", res.ImagesRemoved)
	}
}

func TestRunReportsVolumeRemovalFailure(t *testing.T) {
	writeUserProfiles(t, map[string]string{})
	fc := &fakeClient{
		inspects: map[string]string{"debian:13-slim": "sha256:builtinid"},
		volumes: []*volume.Volume{
			{Name: "tpd-cache-orphan", Labels: runtime.OwnershipLabels()},
			{Name: "tpd-cache-orphan2", Labels: runtime.OwnershipLabels()},
		},
		removeVolErrs: map[string]error{"tpd-cache-orphan": errors.New("boom")},
	}
	res, err := run(context.Background(), fc, Options{Force: true})
	if err == nil {
		t.Fatal("a failed volume removal must make Run return an error")
	}
	if !strings.Contains(err.Error(), "tpd-cache-orphan") {
		t.Errorf("error should name the failed volume, got %q", err)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("Result.Errors = %v, want exactly 1 failure", res.Errors)
	}
	// The failing volume is not reported removed; the sibling still is.
	if sliceContains(res.VolumesRemoved, "tpd-cache-orphan") {
		t.Error("failed volume must not be listed as removed")
	}
	if !equalSlice(res.VolumesRemoved, []string{"tpd-cache-orphan2"}) {
		t.Errorf("volumes removed = %v, want [tpd-cache-orphan2]", res.VolumesRemoved)
	}
}

func TestRunReportsImageRemovalFailure(t *testing.T) {
	writeUserProfiles(t, map[string]string{})
	fc := &fakeClient{
		inspects: map[string]string{"debian:13-slim": "sha256:builtinid"},
		images:   []image.Summary{{RepoTags: []string{"tpd/packages:deadbeefdeadbeef"}, Labels: runtime.OwnershipLabels()}},
		removeImgErrs: map[string]error{
			"tpd/packages:deadbeefdeadbeef": errors.New("boom"),
		},
	}
	res, err := run(context.Background(), fc, Options{Force: true})
	if err == nil {
		t.Fatal("a failed image removal must make Run return an error")
	}
	if len(res.Errors) != 1 {
		t.Fatalf("Result.Errors = %v, want exactly 1 failure", res.Errors)
	}
	if sliceContains(res.ImagesRemoved, "tpd/packages:deadbeefdeadbeef") {
		t.Error("failed image must not be listed as removed")
	}
}

func TestRunProtectsVolumesAcrossActiveContainerStates(t *testing.T) {
	// created/paused/restarting containers hold their mounts just like
	// running ones; an exited container releases them (it is a leak).
	for _, tt := range []struct {
		state string
		keep  bool
	}{
		{state: "created", keep: true},
		{state: "paused", keep: true},
		{state: "restarting", keep: true},
		{state: "running", keep: true},
		{state: "exited", keep: false},
		{state: "dead", keep: false},
	} {
		t.Run(tt.state, func(t *testing.T) {
			writeUserProfiles(t, map[string]string{})
			fc := &fakeClient{
				inspects:   map[string]string{"debian:13-slim": "sha256:builtinid"},
				volumes:    []*volume.Volume{{Name: "tpd-cache-orphan", Labels: runtime.OwnershipLabels()}},
				containers: []types.Container{{ID: "c1", State: tt.state}},
				containerInspects: map[string]types.ContainerJSON{
					"c1": {
						ContainerJSONBase: &types.ContainerJSONBase{Image: "sha256:unused"},
						Mounts:            []types.MountPoint{{Type: mount.TypeVolume, Name: "tpd-cache-orphan"}},
					},
				},
			}
			res, err := run(context.Background(), fc, Options{Force: true})
			if err != nil {
				t.Fatal(err)
			}
			got := sliceContains(res.VolumesRemoved, "tpd-cache-orphan")
			if got == tt.keep {
				t.Errorf("state %s: volume removed=%v, want keep=%v", tt.state, got, tt.keep)
			}
		})
	}
}

func TestRunProtectsResourcesMountedByUnlabeledContainers(t *testing.T) {
	// A container without the ownership label that mounts a tpd-named volume
	// or runs a derived image must protect those resources too: prune guards
	// resources, not the provenance of the container referencing them.
	writeUserProfiles(t, map[string]string{"myagent": userProfileYAML})
	const baseID = "sha256:baseid"
	usedTag := runtime.DerivedTag(baseID, []string{"curl", "git"}, nil)
	fc := &fakeClient{
		inspects: map[string]string{
			"mybase:latest":                 baseID,
			"debian:13-slim":                "sha256:builtinid",
			"tpd/packages:deadbeefdeadbeef": "sha256:orphanid",
		},
		volumes: []*volume.Volume{{Name: "tpd-cache-orphan", Labels: runtime.OwnershipLabels()}},
		images: []image.Summary{
			{RepoTags: []string{usedTag}, Labels: runtime.OwnershipLabels()},
			{RepoTags: []string{"tpd/packages:deadbeefdeadbeef"}, Labels: runtime.OwnershipLabels()},
		},
		containers: []types.Container{{ID: "foreign", State: "running"}},
		containerInspects: map[string]types.ContainerJSON{
			"foreign": {
				ContainerJSONBase: &types.ContainerJSONBase{Image: "sha256:orphanid"},
				Mounts:            []types.MountPoint{{Type: mount.TypeVolume, Name: "tpd-cache-orphan"}},
			},
		},
	}
	res, err := run(context.Background(), fc, Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if sliceContains(res.VolumesRemoved, "tpd-cache-orphan") {
		t.Error("volume mounted by an unlabeled live container must be protected")
	}
	if sliceContains(res.ImagesRemoved, "tpd/packages:deadbeefdeadbeef") {
		t.Error("derived image run by an unlabeled live container must be protected")
	}
}

func TestRunFailsClosedOnContainerListError(t *testing.T) {
	writeUserProfiles(t, map[string]string{})
	fc := &fakeClient{
		inspects:          map[string]string{"debian:13-slim": "sha256:builtinid"},
		volumes:           []*volume.Volume{{Name: "tpd-cache-orphan", Labels: runtime.OwnershipLabels()}},
		listContainersErr: errors.New("daemon gone"),
	}
	res, err := run(context.Background(), fc, Options{Force: true})
	if err == nil {
		t.Fatal("a container-list error must fail prune closed")
	}
	if len(fc.removedV) != 0 || len(fc.removedI) != 0 {
		t.Errorf("no removals may happen when resource use can't be established; got volumes=%v images=%v", fc.removedV, fc.removedI)
	}
	if len(res.VolumesRemoved) != 0 || len(res.ImagesRemoved) != 0 {
		t.Errorf("no removals may be reported; got volumes=%v images=%v", res.VolumesRemoved, res.ImagesRemoved)
	}
}

func TestRunFailsClosedOnContainerInspectError(t *testing.T) {
	// A live container that can't be inspected must abort pruning, not be
	// skipped (which would let its resources be removed).
	writeUserProfiles(t, map[string]string{})
	fc := &fakeClient{
		inspects:            map[string]string{"debian:13-slim": "sha256:builtinid"},
		volumes:             []*volume.Volume{{Name: "tpd-cache-orphan", Labels: runtime.OwnershipLabels()}},
		containers:          []types.Container{{ID: "c1", State: "running"}},
		inspectContainerErr: errors.New("gone"),
	}
	res, err := run(context.Background(), fc, Options{Force: true})
	if err == nil {
		t.Fatal("an inspect error on a live container must fail prune closed")
	}
	if len(fc.removedV) != 0 || len(fc.removedI) != 0 {
		t.Errorf("no removals may happen when resource use can't be established; got volumes=%v images=%v", fc.removedV, fc.removedI)
	}
	if len(res.VolumesRemoved) != 0 || len(res.ImagesRemoved) != 0 {
		t.Errorf("no removals may be reported; got volumes=%v images=%v", res.VolumesRemoved, res.ImagesRemoved)
	}
}

func TestRunFailsClosedOnImageInspectError(t *testing.T) {
	// A candidate derived image that can't be re-inspected (removed between
	// list and re-check) must abort pruning, not be treated as unused.
	writeUserProfiles(t, map[string]string{"myagent": userProfileYAML})
	fc := &fakeClient{
		inspects: map[string]string{"mybase:latest": "sha256:baseid", "debian:13-slim": "sha256:builtinid"},
		images:   []image.Summary{{RepoTags: []string{"tpd/packages:deadbeefdeadbeef"}, Labels: runtime.OwnershipLabels()}},
		inspectImageErrs: map[string]error{
			"tpd/packages:deadbeefdeadbeef": errors.New("gone"),
		},
	}
	res, err := run(context.Background(), fc, Options{Force: true})
	if err == nil {
		t.Fatal("an inspect error on a candidate image must fail prune closed")
	}
	if len(fc.removedV) != 0 || len(fc.removedI) != 0 {
		t.Errorf("no removals may happen when resource use can't be established; got volumes=%v images=%v", fc.removedV, fc.removedI)
	}
	if len(res.VolumesRemoved) != 0 || len(res.ImagesRemoved) != 0 {
		t.Errorf("no removals may be reported; got volumes=%v images=%v", res.VolumesRemoved, res.ImagesRemoved)
	}
}

func TestRunKeepsCachesWhenProfileFailsToResolve(t *testing.T) {
	// A profile that no longer resolves (extends a dropped fragment) owns
	// cache volumes prune can't enumerate; keep every cache rather than risk
	// dropping one.
	writeUserProfiles(t, map[string]string{"broken": `version: 1
extends: doesnotexist
command: ["echo"]
`})
	fc := &fakeClient{
		inspects: map[string]string{"debian:13-slim": "sha256:builtinid"},
		volumes:  []*volume.Volume{{Name: "tpd-cache-orphan", Labels: runtime.OwnershipLabels()}},
	}
	res, err := run(context.Background(), fc, Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if sliceContains(res.VolumesRemoved, "tpd-cache-orphan") {
		t.Error("cache volume must be preserved when a profile fails to resolve")
	}
}

func TestRunKeepsCachesWhenUserFileSkipped(t *testing.T) {
	// A malformed user file is skipped by tolerant loading; its caches can't
	// be assessed, so volume pruning is deferred.
	bad := "version: 1\nimage: mybase:latest\ncommand: [\"echo\"\n  caches: { oops }\n"
	writeUserProfiles(t, map[string]string{"bad": bad})
	fc := &fakeClient{
		inspects: map[string]string{"debian:13-slim": "sha256:builtinid"},
		volumes:  []*volume.Volume{{Name: "tpd-cache-orphan", Labels: runtime.OwnershipLabels()}},
	}
	res, err := run(context.Background(), fc, Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if sliceContains(res.VolumesRemoved, "tpd-cache-orphan") {
		t.Error("cache volume must be preserved when a user file is skipped")
	}
}

func TestRunKeepsDerivedImagesWhenBaseUnavailable(t *testing.T) {
	// A resolvable profile whose base image isn't present locally can't have
	// its derived tag recomputed (the tag hashes the base ID); keep every
	// derived image rather than prune one we can't associate.
	writeUserProfiles(t, map[string]string{"myagent": userProfileYAML})
	fc := &fakeClient{
		inspects: map[string]string{"debian:13-slim": "sha256:builtinid"},
		images:   []image.Summary{{RepoTags: []string{"tpd/packages:deadbeefdeadbeef"}, Labels: runtime.OwnershipLabels()}},
	}
	res, err := run(context.Background(), fc, Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if sliceContains(res.ImagesRemoved, "tpd/packages:deadbeefdeadbeef") {
		t.Error("derived image must be kept when the base image ID is unavailable")
	}
}

func TestComputeUsedDefersImagesOnUnavailableBase(t *testing.T) {
	writeUserProfiles(t, map[string]string{"myagent": userProfileYAML})
	fc := &fakeClient{inspects: map[string]string{"debian:13-slim": "sha256:builtinid"}}
	u, err := computeUsed(context.Background(), fc)
	if err != nil {
		t.Fatal(err)
	}
	if !u.deferImages {
		t.Error("deferImages should be set when a profile's base image is unavailable")
	}
	if u.deferVolumes {
		t.Error("an unavailable base image must not defer volume pruning")
	}
}

func TestConfirmSectionsCombinesScopes(t *testing.T) {
	sections := confirmSections(true, true, false, []string{"tpd-cache-orphan"}, []string{"tpd/packages:deadbeefdeadbeef"}, nil)
	if len(sections) != 2 {
		t.Fatalf("both scopes should yield one section per type, got %+v", sections)
	}
	if sections[0].kind != "volumes" || sections[1].kind != "images" {
		t.Errorf("unexpected section kinds: %+v", sections)
	}
	if onlyImages := confirmSections(true, false, false, []string{"v"}, []string{"i"}, nil); len(onlyImages) != 1 || onlyImages[0].kind != "volumes" {
		t.Errorf("volumes-only scope should yield a single volumes section, got %+v", onlyImages)
	}
	if onlyVolumes := confirmSections(true, true, false, nil, []string{"i"}, nil); len(onlyVolumes) != 1 || onlyVolumes[0].kind != "images" {
		t.Errorf("empty volumes candidate list should be omitted, got %+v", onlyVolumes)
	}
	withNetworks := confirmSections(false, false, true, nil, nil, []string{"tpd-services"})
	if len(withNetworks) != 1 || withNetworks[0].kind != "networks" {
		t.Errorf("networks-only scope should yield a single networks section, got %+v", withNetworks)
	}
}

func TestConfirmPrintsSinglePromptForCombinedSections(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	ok := confirm([]confirmSection{
		{kind: "volumes", items: []string{"tpd-cache-orphan"}},
		{kind: "images", items: []string{"tpd/packages:deadbeefdeadbeef"}},
	}, strings.NewReader("y\n"))
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	if !ok {
		t.Error("confirm should return true on y")
	}
	if strings.Count(string(out), "Proceed?") != 1 {
		t.Errorf("combined scopes must produce exactly one prompt; got %q", out)
	}
	for _, want := range []string{"The following volumes will be removed:", "The following images will be removed:"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("prompt should list %q; got %q", want, out)
		}
	}
}

// ownedServicesNetwork is the canonical tpd service network with the
// ownership and services-role labels the prune path requires.
func ownedServicesNetwork(id string) network.Summary {
	return network.Summary{
		Name: runtime.ServiceNetworkName,
		ID:   id,
		Labels: map[string]string{
			runtime.OwnershipLabel:   "true",
			runtime.NetworkRoleLabel: runtime.NetworkRoleServices,
		},
	}
}

func TestPruneNetworksRequiresNetworkScope(t *testing.T) {
	fc := &fakeClient{networks: []network.Summary{ownedServicesNetwork("n1")}}
	res, err := run(context.Background(), fc, Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NetworksRemoved) != 0 {
		t.Errorf("default run must not remove networks; got %v", res.NetworksRemoved)
	}
	if len(fc.removedN) != 0 {
		t.Errorf("NetworkRemove must not be called without --networks; got %v", fc.removedN)
	}
}

func TestPruneNetworkScopeCombinations(t *testing.T) {
	writeUserProfiles(t, map[string]string{"myagent": userProfileYAML})
	const baseID = "sha256:baseid"
	usedTag := runtime.DerivedTag(baseID, []string{"curl", "git"}, nil)
	tests := []struct {
		name  string
		opts  Options
		wantV []string
		wantI []string
		wantN []string
	}{
		{name: "default", opts: Options{Force: true}, wantV: []string{"tpd-cache-orphan"}, wantI: []string{"tpd/packages:deadbeefdeadbeef"}},
		{name: "volumes", opts: Options{Volumes: true, Force: true}, wantV: []string{"tpd-cache-orphan"}},
		{name: "images", opts: Options{Images: true, Force: true}, wantI: []string{"tpd/packages:deadbeefdeadbeef"}},
		{name: "networks", opts: Options{Networks: true, Force: true}, wantN: []string{runtime.ServiceNetworkName}},
		{name: "volumes+networks", opts: Options{Volumes: true, Networks: true, Force: true}, wantV: []string{"tpd-cache-orphan"}, wantN: []string{runtime.ServiceNetworkName}},
		{name: "images+networks", opts: Options{Images: true, Networks: true, Force: true}, wantI: []string{"tpd/packages:deadbeefdeadbeef"}, wantN: []string{runtime.ServiceNetworkName}},
		{name: "all", opts: Options{All: true, Force: true}, wantV: []string{"tpd-cache-orphan", "tpd-cache-usedcache"}, wantI: []string{"tpd/packages:deadbeefdeadbeef", usedTag}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := &fakeClient{
				inspects: map[string]string{"mybase:latest": baseID, "debian:13-slim": "sha256:builtinid"},
				volumes: []*volume.Volume{
					{Name: "tpd-cache-usedcache", Labels: runtime.OwnershipLabels()},
					{Name: "tpd-cache-orphan", Labels: runtime.OwnershipLabels()},
				},
				images: []image.Summary{
					{RepoTags: []string{usedTag}, Labels: runtime.OwnershipLabels()},
					{RepoTags: []string{"tpd/packages:deadbeefdeadbeef"}, Labels: runtime.OwnershipLabels()},
				},
				networks: []network.Summary{ownedServicesNetwork("n1")},
			}
			res, err := run(context.Background(), fc, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			sort.Strings(tt.wantV)
			gotV := append([]string(nil), res.VolumesRemoved...)
			sort.Strings(gotV)
			if !equalSlice(gotV, tt.wantV) {
				t.Errorf("volumes removed = %v, want %v", res.VolumesRemoved, tt.wantV)
			}
			sort.Strings(tt.wantI)
			gotI := append([]string(nil), res.ImagesRemoved...)
			sort.Strings(gotI)
			if !equalSlice(gotI, tt.wantI) {
				t.Errorf("images removed = %v, want %v", res.ImagesRemoved, tt.wantI)
			}
			if !equalSlice(res.NetworksRemoved, tt.wantN) {
				t.Errorf("networks removed = %v, want %v", res.NetworksRemoved, tt.wantN)
			}
		})
	}
}

func TestPruneNetworksUsesConfirmationPrompt(t *testing.T) {
	// Test stdin is not a tty, so confirm() declines and nothing is removed
	// unless Force is set.
	fc := &fakeClient{networks: []network.Summary{ownedServicesNetwork("n1")}}
	res, err := run(context.Background(), fc, Options{Networks: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NetworksRemoved) != 0 || len(fc.removedN) != 0 {
		t.Errorf("declined confirmation must not remove networks; result=%v calls=%v", res.NetworksRemoved, fc.removedN)
	}
}

func TestPruneNetworksSkipsUnmanagedCanonicalName(t *testing.T) {
	// A network using the canonical name without the ownership label must
	// survive prune (with a stderr warning) so an unrelated network is never
	// removed.
	fc := &fakeClient{
		networks: []network.Summary{{Name: runtime.ServiceNetworkName, ID: "n1"}},
	}
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	res, err := run(context.Background(), fc, Options{Networks: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	warned, _ := io.ReadAll(r)
	if len(res.NetworksRemoved) != 0 || len(fc.removedN) != 0 {
		t.Errorf("unowned canonical network must not be removed; result=%v calls=%v", res.NetworksRemoved, fc.removedN)
	}
	if !strings.Contains(string(warned), "not tpd-owned") {
		t.Errorf("stderr should warn the canonical-name network is not tpd-owned; got %q", string(warned))
	}
}

func TestPruneNetworksSkipsWrongRole(t *testing.T) {
	fc := &fakeClient{
		networks: []network.Summary{{
			Name: runtime.ServiceNetworkName,
			ID:   "n1",
			Labels: map[string]string{
				runtime.OwnershipLabel:   "true",
				runtime.NetworkRoleLabel: "something-else",
			},
		}},
	}
	res, err := run(context.Background(), fc, Options{Networks: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NetworksRemoved) != 0 || len(fc.removedN) != 0 {
		t.Errorf("wrong-role network must not be removed; result=%v calls=%v", res.NetworksRemoved, fc.removedN)
	}
}

func TestPruneNetworksSkipsRunningReference(t *testing.T) {
	// An unlabeled running container attached to the managed network must
	// protect it: ownership is not what grants protection, an active endpoint
	// is.
	fc := &fakeClient{
		networks:   []network.Summary{ownedServicesNetwork("n1")},
		containers: []types.Container{{ID: "c1"}},
		containerInspects: map[string]types.ContainerJSON{
			"c1": {
				NetworkSettings: &types.NetworkSettings{
					Networks: map[string]*network.EndpointSettings{
						runtime.ServiceNetworkName: {NetworkID: "n1"},
					},
				},
			},
		},
	}
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	res, err := run(context.Background(), fc, Options{Networks: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	out, _ := io.ReadAll(r)
	if len(res.NetworksRemoved) != 0 || len(fc.removedN) != 0 {
		t.Errorf("network referenced by a running container must not be removed; result=%v calls=%v", res.NetworksRemoved, fc.removedN)
	}
	if !strings.Contains(string(out), "skipping "+runtime.ServiceNetworkName+": in use by a running container") {
		t.Errorf("stderr should report the skip; got %q", string(out))
	}
}

func TestPruneNetworksRemovesOwnedUnusedNetwork(t *testing.T) {
	fc := &fakeClient{networks: []network.Summary{ownedServicesNetwork("n1")}}
	res, err := run(context.Background(), fc, Options{Networks: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if !equalSlice(res.NetworksRemoved, []string{runtime.ServiceNetworkName}) {
		t.Errorf("networks removed = %v, want [%s]", res.NetworksRemoved, runtime.ServiceNetworkName)
	}
	if !equalSlice(fc.removedN, []string{"n1"}) {
		t.Errorf("NetworkRemove called with %v, want [n1]", fc.removedN)
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
