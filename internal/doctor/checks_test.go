package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/jgillich/tpd/internal/runtime"
)

var probeVolumeRe = regexp.MustCompile(`^tpd-diag-[0-9a-f]{16}$`)

type fakeDocker struct {
	t          *testing.T
	srv        *httptest.Server
	images     []image.Summary
	containers []types.Container

	mu             sync.Mutex
	volumes        []*volume.Volume
	createdVolumes []string
	removedVolumes []string
	containerImage string
	createdCnts    []string
	removedCnts    []string
}

func newFakeDocker(t *testing.T, images []image.Summary, volumes []*volume.Volume) *fakeDocker {
	f := &fakeDocker{t: t, images: images, volumes: volumes}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDocker) client(t *testing.T) *client.Client {
	cli, err := client.NewClientWithOpts(
		client.WithHost("tcp://"+f.srv.Listener.Addr().String()),
		client.WithVersion("1.41"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	return cli
}

func (f *fakeDocker) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/volumes/create"):
		var opts volume.CreateOptions
		if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.createdVolumes = append(f.createdVolumes, opts.Name)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"Name":%q,"Driver":"local"}`, opts.Name)

	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1.41/volumes/"):
		f.removedVolumes = append(f.removedVolumes, path.Base(r.URL.Path))
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/volumes"):
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(volume.ListResponse{Volumes: f.volumes}); err != nil {
			f.t.Errorf("encode volumes: %v", err)
		}

	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/images/json"):
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(f.images); err != nil {
			f.t.Errorf("encode images: %v", err)
		}

	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
		out := f.containers
		if fs := r.URL.Query().Get("filters"); fs != "" {
			args, err := filters.FromJSON(fs)
			if err != nil {
				f.t.Errorf("parse container filters: %v", err)
			}
			var filtered []types.Container
			for _, c := range out {
				keep := true
				for _, v := range args.Get("label") {
					k, val, _ := strings.Cut(v, "=")
					if c.Labels[k] != val {
						keep = false
					}
				}
				if keep {
					filtered = append(filtered, c)
				}
			}
			out = filtered
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(out); err != nil {
			f.t.Errorf("encode containers: %v", err)
		}

	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/create"):
		var cfg container.Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.containerImage = cfg.Image
		f.createdCnts = append(f.createdCnts, "probe-container")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"Id":"probe-container","Warnings":[]}`)

	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1.41/containers/"):
		f.removedCnts = append(f.removedCnts, path.Base(r.URL.Path))
		w.WriteHeader(http.StatusNoContent)

	default:
		f.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

func TestCheckPermissionsProbeVolumeUnique(t *testing.T) {
	f := newFakeDocker(t, []image.Summary{{ID: "sha256:localbase"}}, nil)
	rt := &dockerRT{cli: f.client(t)}

	c := checkPermissions(context.Background(), rt)
	if c.Status != Pass {
		t.Fatalf("status = %s, want pass: %s", c.Status, c.Message)
	}
	if len(f.createdVolumes) != 1 {
		t.Fatalf("created volumes = %v, want exactly one probe", f.createdVolumes)
	}
	if !probeVolumeRe.MatchString(f.createdVolumes[0]) {
		t.Errorf("probe volume name = %q, want tpd-diag-<16 hex>", f.createdVolumes[0])
	}
	if len(f.removedVolumes) != 1 || f.removedVolumes[0] != f.createdVolumes[0] {
		t.Errorf("removed volumes = %v, want only the probe volume %q", f.removedVolumes, f.createdVolumes[0])
	}
}

func TestCheckPermissionsLeavesPreExistingFixedVolume(t *testing.T) {
	f := newFakeDocker(t, []image.Summary{{ID: "sha256:localbase"}},
		[]*volume.Volume{{Name: "tpd-perm-test"}})
	rt := &dockerRT{cli: f.client(t)}

	c := checkPermissions(context.Background(), rt)
	if c.Status != Pass {
		t.Fatalf("status = %s, want pass: %s", c.Status, c.Message)
	}
	for _, name := range f.removedVolumes {
		if name == "tpd-perm-test" {
			t.Error("checkPermissions removed a pre-existing tpd-perm-test volume")
		}
	}
}

func TestCheckPermissionsContainerProbeUsesLocalImage(t *testing.T) {
	f := newFakeDocker(t, []image.Summary{{ID: "sha256:localbase", RepoTags: []string{"debian:13-slim"}}}, nil)
	rt := &dockerRT{cli: f.client(t)}

	c := checkPermissions(context.Background(), rt)
	if c.Status != Pass {
		t.Fatalf("status = %s, want pass: %s", c.Status, c.Message)
	}
	if f.containerImage != "sha256:localbase" {
		t.Errorf("container probe used image %q, want local image ID sha256:localbase", f.containerImage)
	}
	if len(f.createdCnts) != 1 || len(f.removedCnts) != 1 {
		t.Errorf("container probe not cleaned up: created=%v removed=%v", f.createdCnts, f.removedCnts)
	}
}

func TestCheckPermissionsNoImageSkipsContainerProbe(t *testing.T) {
	f := newFakeDocker(t, nil, nil)
	rt := &dockerRT{cli: f.client(t)}

	c := checkPermissions(context.Background(), rt)
	if c.Status != Info {
		t.Fatalf("status = %s, want info (no local image): %s", c.Status, c.Message)
	}
	if len(f.createdCnts) != 0 {
		t.Errorf("container probe ran despite no local image: %v", f.createdCnts)
	}
	if !strings.Contains(c.Message, "no local image") {
		t.Errorf("message should explain the skipped probe; got %q", c.Message)
	}
}

func TestCheckWorkspaceWritableUsesUniqueProbe(t *testing.T) {
	dir := t.TempDir()
	fixed := filepath.Join(dir, ".tpd-write-test")
	if err := os.WriteFile(fixed, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := checkWorkspaceWritable(context.Background(), dir)
	if c.Status != Pass {
		t.Fatalf("status = %s, want pass: %s", c.Status, c.Message)
	}
	data, err := os.ReadFile(fixed)
	if err != nil {
		t.Fatalf("pre-existing probe file removed: %v", err)
	}
	if string(data) != "original" {
		t.Errorf("pre-existing probe file clobbered: %q", data)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("probe not cleaned up: %d entries left, want 1", len(entries))
	}
}

func TestCheckWorkspaceWritableDoesNotFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, ".tpd-write-test")); err != nil {
		t.Fatal(err)
	}

	c := checkWorkspaceWritable(context.Background(), dir)
	if c.Status != Pass {
		t.Fatalf("status = %s, want pass: %s", c.Status, c.Message)
	}
	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Errorf("victim file modified through symlink: %q", data)
	}
}

func TestCheckUnlabeledLegacyResources(t *testing.T) {
	volumes := []*volume.Volume{
		{Name: "tpd-mise", Labels: map[string]string{runtime.OwnershipLabel: "true"}},
		{Name: "tpd-cache-node"},
		{Name: "tpd-diag-f00dcafe"},
		{Name: "postgres-data"},
	}
	images := []image.Summary{
		{ID: "sha256:labeled", Labels: map[string]string{runtime.OwnershipLabel: "true"}, RepoTags: []string{"docker.io/tpd/packages:abc"}},
		{ID: "sha256:legacy", RepoTags: []string{"localhost/tpd/packages:def"}},
		{ID: "sha256:other", RepoTags: []string{"docker.io/library/nginx:latest"}},
	}
	f := newFakeDocker(t, images, volumes)
	rt := &dockerRT{cli: f.client(t)}

	c := checkUnlabeledLegacyResources(context.Background(), rt)
	if c.Status != Info {
		t.Fatalf("status = %s, want info: %s", c.Status, c.Message)
	}
	for _, want := range []string{"tpd-cache-node", "tpd/packages:def"} {
		if !strings.Contains(c.Message, want) {
			t.Errorf("message should mention %q; got %q", want, c.Message)
		}
	}
	for _, absent := range []string{"tpd-diag-", "tpd-mise", "nginx", "sha256:other"} {
		if strings.Contains(c.Message, absent) {
			t.Errorf("message should not mention %q; got %q", absent, c.Message)
		}
	}
}

func TestCheckUnlabeledLegacyResourcesNone(t *testing.T) {
	volumes := []*volume.Volume{
		{Name: "tpd-mise", Labels: map[string]string{runtime.OwnershipLabel: "true"}},
		{Name: "tpd-diag-0000"},
	}
	images := []image.Summary{
		{ID: "sha256:labeled", Labels: map[string]string{runtime.OwnershipLabel: "true"}, RepoTags: []string{"tpd/packages:abc"}},
	}
	f := newFakeDocker(t, images, volumes)
	rt := &dockerRT{cli: f.client(t)}

	c := checkUnlabeledLegacyResources(context.Background(), rt)
	if c.Status != Pass {
		t.Fatalf("status = %s, want pass: %s", c.Status, c.Message)
	}
}

func TestCheckDerivedImagesIgnoresUnlabeledImages(t *testing.T) {
	f := newFakeDocker(t, []image.Summary{
		{ID: "sha256:legacy", RepoTags: []string{"tpd/packages:legacy"}},
		{ID: "sha256:owned", Labels: map[string]string{runtime.OwnershipLabel: "true"}, RepoTags: []string{"tpd/packages:owned"}},
	}, nil)
	rt := &dockerRT{cli: f.client(t)}

	c := checkDerivedImages(context.Background(), rt)
	if c.Status != Pass || !strings.Contains(c.Message, "tpd/packages:owned") || strings.Contains(c.Message, "legacy") {
		t.Fatalf("derived image check = %+v, want only labeled image", c)
	}
}

func TestCheckVolumesIgnoresUnlabeledVolumes(t *testing.T) {
	f := newFakeDocker(t, nil, []*volume.Volume{
		{Name: "tpd-cache-legacy"},
		{Name: "tpd-cache-owned", Labels: map[string]string{runtime.OwnershipLabel: "true"}},
	})
	rt := &dockerRT{cli: f.client(t)}

	c := checkVolumes(context.Background(), rt)
	if c.Status != Pass || !strings.Contains(c.Message, "tpd-cache-owned") || strings.Contains(c.Message, "legacy") {
		t.Fatalf("volume check = %+v, want only labeled volume", c)
	}
}

func TestRandomSuffix(t *testing.T) {
	a, err := randomSuffix(8)
	if err != nil {
		t.Fatal(err)
	}
	b, err := randomSuffix(8)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 16 {
		t.Errorf("randomSuffix(8) length = %d, want 16 hex chars", len(a))
	}
	if a == b {
		t.Error("two random suffixes should differ")
	}
}

func TestCheckSELinux(t *testing.T) {
	c := checkSELinux(true)
	if c.Status != Pass || c.Name != "selinux" {
		t.Fatalf("enforcing: check = %+v, want pass 'selinux'", c)
	}
	if !strings.Contains(c.Message, "label=disable") {
		t.Errorf("enforcing: message should mention label=disable; got %q", c.Message)
	}

	c = checkSELinux(false)
	if c.Status != Pass {
		t.Fatalf("not enforcing: check = %+v, want pass", c)
	}
	if strings.Contains(c.Message, "label=disable") {
		t.Errorf("not enforcing: message should not mention label=disable; got %q", c.Message)
	}
}

func TestCheckWorkspaceWritable(t *testing.T) {
	dir := t.TempDir()
	c := checkWorkspaceWritable(context.Background(), dir)
	if c.Status != Pass {
		t.Errorf("writable dir: status = %s, want pass", c.Status)
	}
}

func TestCheckWorkspaceNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: file permissions are not enforced")
	}
	dir := t.TempDir()
	err := os.Chmod(dir, 0o444)
	if err != nil {
		t.Skip("cannot chmod on this OS")
	}
	defer os.Chmod(dir, 0o755)
	c := checkWorkspaceWritable(context.Background(), dir)
	if c.Status == Pass {
		t.Error("read-only dir should not pass")
	}
}

func TestCheckProjectTools(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".tool-versions"), []byte("node 22\npython 3.13\n"), 0o644)
	c := checkProjectTools(context.Background(), dir)
	if c.Status != Pass {
		t.Errorf("status = %s, want pass", c.Status)
	}
	if !strings.Contains(c.Message, "node@22") {
		t.Errorf("message should list node@22; got %q", c.Message)
	}
}

func TestCheckProjectToolsNone(t *testing.T) {
	dir := t.TempDir()
	c := checkProjectTools(context.Background(), dir)
	if c.Status != Info {
		t.Errorf("no tool files: status = %s, want info", c.Status)
	}
}

func TestCheckProfileValidityIgnoresFragments(t *testing.T) {
	// Built-in fragments resolve without version/command/image; they must not
	// be validated as launchable profiles. typescript extends javascript, which
	// used to trip the base-profile tolerance and fail the check.
	c := checkProfileValidity("")
	if c.Status != Pass {
		t.Fatalf("status = %s, want pass, message: %s", c.Status, c.Message)
	}
}

func TestCheckProfileValidityReportsResources(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "reslimit.yaml"), []byte(
		"version: 1\nimage: debian:13-slim\ncommand: [sh]\nresources:\n  memory: 512m\n  cpus: \"2\"\n"), 0o644)

	c := checkProfileValidity(dir)
	if c.Status != Pass {
		t.Fatalf("status = %s, want pass: %s", c.Status, c.Message)
	}
	if !strings.Contains(c.Message, "resources: reslimit(512m,2)") {
		t.Errorf("message should report reslimit resources; got %q", c.Message)
	}
}

func TestCheckUserOverridesNoGitconfig(t *testing.T) {
	dir := t.TempDir()
	// Create a user override for opencode that mounts no gitconfig.
	os.WriteFile(filepath.Join(dir, "opencode.yaml"), []byte(
		"# Generated by tpd init.\nversion: 1\nextends: core/opencode\n"), 0o644)

	checks := checkUserOverrides(dir)
	if len(checks) != 1 {
		t.Fatalf("expected 1 check, got %d", len(checks))
	}
	if checks[0].Name != "gitconfig" || checks[0].Status != Info {
		t.Errorf("expected gitconfig Info check, got %+v", checks[0])
	}
	if !strings.Contains(checks[0].Message, "not mounted") {
		t.Errorf("expected 'not mounted' hint, got: %s", checks[0].Message)
	}
}

func TestCheckUserOverridesWithCachesAndGitconfig(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "opencode.yaml"), []byte(
		"version: 1\nextends: core/opencode\ncaches:\n  npm: ~/.npm\nmounts:\n  ~/.gitconfig:\n    source: ~/.gitconfig\n    read_only: true\n"), 0o644)

	checks := checkUserOverrides(dir)
	if len(checks) != 1 {
		t.Fatalf("expected 1 Pass check, got %d", len(checks))
	}
	if checks[0].Status != Pass {
		t.Errorf("Status = %v, want Pass (caches and gitconfig present)", checks[0].Status)
	}
}

func TestCheckUserOverridesNoUserFiles(t *testing.T) {
	dir := t.TempDir()

	checks := checkUserOverrides(dir)
	if len(checks) != 1 {
		t.Fatalf("expected 1 check, got %d", len(checks))
	}
	if checks[0].Status != Info {
		t.Errorf("Status = %v, want Info (migration notice for no user overrides)", checks[0].Status)
	}
}

func TestCheckLeakedContainersWarnsOnLabeled(t *testing.T) {
	f := newFakeDocker(t, nil, nil)
	f.containers = []types.Container{
		{ID: "abc123", Names: []string{"/tpd-node"}, Labels: map[string]string{runtime.OwnershipLabel: "true"}},
	}
	rt := &dockerRT{cli: f.client(t)}

	c := checkLeakedContainers(context.Background(), rt)
	if c.Status != Warn {
		t.Fatalf("status = %s, want warn: %s", c.Status, c.Message)
	}
	for _, want := range []string{"tpd-node", "docker rm -f"} {
		if !strings.Contains(c.Message, want) {
			t.Errorf("message should contain %q; got %q", want, c.Message)
		}
	}
}

func TestCheckLeakedContainersPassWhenNone(t *testing.T) {
	f := newFakeDocker(t, nil, nil)
	rt := &dockerRT{cli: f.client(t)}

	c := checkLeakedContainers(context.Background(), rt)
	if c.Status != Pass {
		t.Fatalf("status = %s, want pass: %s", c.Status, c.Message)
	}
}

func TestCheckLeakedContainersIgnoresUnlabeled(t *testing.T) {
	f := newFakeDocker(t, nil, nil)
	f.containers = []types.Container{
		{ID: "unlabeled", Names: []string{"/nginx"}},
	}
	rt := &dockerRT{cli: f.client(t)}

	c := checkLeakedContainers(context.Background(), rt)
	if c.Status != Pass {
		t.Fatalf("status = %s, want pass (unlabeled container filtered out): %s", c.Status, c.Message)
	}
}

func TestCheckStaleBusSocketsWarns(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "tpd-bus-123.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	c := checkStaleBusSockets()
	if c.Status != Warn {
		t.Fatalf("status = %s, want warn: %s", c.Status, c.Message)
	}
	if !strings.Contains(c.Message, "tpd-bus-123.sock") {
		t.Errorf("message should name the stale socket; got %q", c.Message)
	}
}

func TestCheckStaleBusSocketsPass(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)

	c := checkStaleBusSockets()
	if c.Status != Pass {
		t.Fatalf("status = %s, want pass: %s", c.Status, c.Message)
	}
}
