package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/jgillich/tpd/internal/workspace"
	"golang.org/x/sys/unix"
)

// fakeServicesDaemon serves the Docker API endpoints the service lifecycle
// uses. The name-filtered ContainerList returns f.containers (find-or-start,
// stop target); the label-filtered one returns f.consumers. On a fresh
// ContainerStart the daemon "starts" the service by listening on the host
// sockets configured in f.sockets for that container name, which makes the
// probe dial succeed exactly when a test wants it to.
type fakeServicesDaemon struct {
	containers []types.Container
	consumers  []types.Container

	imagePresent bool
	imageID      string
	pulls        int

	createCount  int
	createReqs   []container.CreateRequest
	createdNames []string
	createdIDs   []string
	nameByID     map[string]string
	startCount   int
	stopCount    int
	removed      []string
	copyCount    int
	failStop     map[string]bool

	sockets   map[string][]string
	listeners []net.Listener
}

func newFakeServicesDaemon() *fakeServicesDaemon {
	return &fakeServicesDaemon{
		imagePresent: true,
		imageID:      "sha256:base",
		nameByID:     map[string]string{},
	}
}

func (f *fakeServicesDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The SDK prefixes every path with the negotiated API version.
	p := strings.TrimPrefix(r.URL.Path, "/v1.41/")
	switch {
	case p == "version" && r.Method == http.MethodGet:
		fmt.Fprint(w, `{"Version":"28.0.0"}`)
	case p == "containers/json" && r.Method == http.MethodGet:
		f.serveContainerList(w, r)
	case p == "containers/create" && r.Method == http.MethodPost:
		f.serveContainerCreate(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/start"):
		f.startCount++
		id := strings.TrimPrefix(strings.TrimSuffix(p, "/start"), "containers/")
		f.createSockets(f.nameByID[id])
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/stop"):
		f.stopCount++
		id := strings.TrimPrefix(strings.TrimSuffix(p, "/stop"), "containers/")
		if f.failStop[id] {
			http.Error(w, `{"message":"stop failed"}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "containers/"):
		f.removed = append(f.removed, strings.TrimPrefix(p, "containers/"))
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut && strings.HasSuffix(p, "/archive"):
		f.copyCount++
		w.WriteHeader(http.StatusOK)
	case p == "volumes/create" && r.Method == http.MethodPost:
		io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, `{"Name":"vol","Driver":"local"}`)
	case p == "images/create" && r.Method == http.MethodPost:
		f.pulls++
		io.Copy(io.Discard, r.Body)
		f.imagePresent = true
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"Pull complete"}`+"\n")
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/json"):
		if !f.imagePresent {
			http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"Id":%q}`, f.imageID)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeServicesDaemon) serveContainerList(w http.ResponseWriter, r *http.Request) {
	var flt map[string]map[string]bool
	_ = json.Unmarshal([]byte(r.URL.Query().Get("filters")), &flt)
	out := f.containers
	if _, ok := flt["label"]; ok {
		out = f.consumers
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (f *fakeServicesDaemon) serveContainerCreate(w http.ResponseWriter, r *http.Request) {
	var req container.CreateRequest
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)
	f.createCount++
	id := fmt.Sprintf("svc%d", f.createCount)
	name := r.URL.Query().Get("name")
	f.createReqs = append(f.createReqs, req)
	f.createdNames = append(f.createdNames, name)
	f.createdIDs = append(f.createdIDs, id)
	f.nameByID[id] = name
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"Id":%q}`, id)
}

// createSockets binds the configured host socket paths. It deliberately does
// not unlink an existing file: the production unlink in createService must have
// cleared the path first, so a stale file left for the daemon is proof that
// the production unlink was removed.
func (f *fakeServicesDaemon) createSockets(name string) {
	for _, path := range f.sockets[name] {
		ln, err := net.Listen("unix", path)
		if err != nil {
			panic(fmt.Sprintf("fake daemon: listen on %s: %v", path, err))
		}
		f.listeners = append(f.listeners, ln)
	}
}

func (f *fakeServicesDaemon) closeListeners() {
	for _, ln := range f.listeners {
		ln.Close()
	}
}

func newServicesTestRuntime(t *testing.T, daemon *fakeServicesDaemon) *DockerRuntime {
	t.Helper()
	srv := httptest.NewServer(daemon)
	t.Cleanup(srv.Close)
	t.Cleanup(daemon.closeListeners)
	cli, err := client.NewClientWithOpts(
		client.WithHost("tcp://"+srv.Listener.Addr().String()),
		client.WithVersion("1.41"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return &DockerRuntime{cli: cli}
}

// overrideServicePaths redirects the lockfile and run-dir path functions to
// temp dirs and restores them on cleanup. runDir is the base dir; the override
// appends the service name so concurrent services stay in separate dirs.
func overrideServicePaths(t *testing.T) (lockDir, runDir string) {
	t.Helper()
	lockDir = t.TempDir()
	runDir = t.TempDir()

	oldLock := serviceLockfilePath
	serviceLockfilePath = func(name string) string { return filepath.Join(lockDir, "svc-"+name+".lock") }
	t.Cleanup(func() { serviceLockfilePath = oldLock })

	oldRun := serviceRunDir
	serviceRunDir = func(name string, _ workspace.Mode) string { return filepath.Join(runDir, name) }
	t.Cleanup(func() { serviceRunDir = oldRun })
	return lockDir, runDir
}

func overrideProbeTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := serviceProbeTimeout
	serviceProbeTimeout = d
	t.Cleanup(func() { serviceProbeTimeout = old })
}

func serviceSpec(name, hash string, exposes map[string]string) Spec {
	return Spec{
		Workspace: WorkspaceSpec{Mode: workspace.ModeRootless},
		Services: []ServiceSpec{{
			Name:    name,
			Hash:    hash,
			Image:   "debian:13-slim",
			Command: []string{"sleep", "infinity"},
			Labels: map[string]string{
				OwnershipLabel:   "true",
				ServiceLabel:     name,
				ServiceHashLabel: hash,
			},
			Exposes: exposes,
		}},
	}
}

func TestStartServicesCreatesNewService(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.sockets = map[string][]string{
		"tpd-svc-db": {filepath.Join(runDir, "db", "run", "db.sock")},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices: %v", err)
	}
	defer bindings.Release()

	if daemon.createCount != 1 {
		t.Fatalf("ContainerCreate calls = %d, want 1", daemon.createCount)
	}
	cfg := daemon.createReqs[0].Config
	if cfg == nil {
		t.Fatal("create request has no Config")
	}
	labels := cfg.Labels
	if labels[ServiceLabel] != "db" || labels[ServiceHashLabel] != "hash123" || labels[OwnershipLabel] != "true" || labels[ServiceRoleLabel] != ServiceRoleSidecar {
		t.Errorf("service labels = %v, want tpd.service=db, tpd.service-hash=hash123, tpd.managed=true, tpd.service-role=sidecar", labels)
	}
	if cfg.User != "0:0" {
		t.Errorf("User = %q, want 0:0", cfg.User)
	}
	if cfg.WorkingDir != "/" {
		t.Errorf("WorkingDir = %q, want /", cfg.WorkingDir)
	}
	env := strings.Join(cfg.Env, "\n")
	if !strings.Contains(env, "HOME=/root") {
		t.Errorf("env missing HOME=/root: %v", cfg.Env)
	}
	if daemon.createReqs[0].HostConfig == nil || daemon.createReqs[0].HostConfig.Init == nil || !*daemon.createReqs[0].HostConfig.Init {
		t.Errorf("HostConfig.Init not true: %+v", daemon.createReqs[0].HostConfig)
	}

	want := filepath.Join(runDir, "db", "run", "db.sock")
	if got := bindings.Sockets["db/port"]; got != want {
		t.Errorf("binding db/port = %q, want %q", got, want)
	}
}

func TestStartServicesPrivileged(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.sockets = map[string][]string{
		"tpd-svc-db": {filepath.Join(runDir, "db", "run", "db.sock")},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	spec.Services[0].Privileged = true
	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices: %v", err)
	}
	defer bindings.Release()

	hc := daemon.createReqs[0].HostConfig
	if hc == nil || !hc.Privileged {
		t.Errorf("HostConfig.Privileged not true: %+v", hc)
	}
}

func TestStartServicesAcceptsNilServiceLabels(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.sockets = map[string][]string{
		"tpd-svc-db": {filepath.Join(runDir, "db", "run", "db.sock")},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	spec.Services[0].Labels = nil
	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices with nil ServiceSpec.Labels: %v", err)
	}
	defer bindings.Release()

	cfg := daemon.createReqs[0].Config
	if cfg == nil || cfg.Labels[ServiceRoleLabel] != ServiceRoleSidecar {
		t.Errorf("created container must carry the sidecar role label; labels = %v", cfg.Labels)
	}
}

func TestStartServicesDoesNotMutateSpecServiceLabels(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.sockets = map[string][]string{
		"tpd-svc-db": {filepath.Join(runDir, "db", "run", "db.sock")},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	before := make(map[string]string, len(spec.Services[0].Labels))
	for k, v := range spec.Services[0].Labels {
		before[k] = v
	}
	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices: %v", err)
	}
	defer bindings.Release()

	if !reflect.DeepEqual(spec.Services[0].Labels, before) {
		t.Errorf("StartServices mutated spec.Services[0].Labels: %v, want %v", spec.Services[0].Labels, before)
	}
}

func TestStartServicesReusesRunningSameHash(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.containers = []types.Container{{
		ID:     "old-svc",
		Names:  []string{"/tpd-svc-db"},
		State:  "running",
		Labels: map[string]string{ServiceHashLabel: "hash123"},
	}}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices: %v", err)
	}
	defer bindings.Release()

	if daemon.createCount != 0 {
		t.Errorf("ContainerCreate called %d times on reuse, want 0", daemon.createCount)
	}
	if daemon.startCount != 0 {
		t.Errorf("reuse must not probe sockets, got %d start calls", daemon.startCount)
	}
	want := filepath.Join(runDir, "db", "run", "db.sock")
	if got := bindings.Sockets["db/port"]; got != want {
		t.Errorf("binding db/port = %q, want %q (deterministic run-dir path)", got, want)
	}
}

func TestStartServicesRecreatesOnHashChangeZeroConsumers(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.containers = []types.Container{{
		ID:     "old-svc",
		Names:  []string{"/tpd-svc-db"},
		State:  "running",
		Labels: map[string]string{ServiceHashLabel: "oldhash"},
	}}
	daemon.sockets = map[string][]string{
		"tpd-svc-db": {filepath.Join(runDir, "db", "run", "db.sock")},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices: %v", err)
	}
	defer bindings.Release()

	if !containsString(daemon.removed, "old-svc") {
		t.Errorf("old service container not removed; removed = %v", daemon.removed)
	}
	if daemon.createCount != 1 {
		t.Errorf("ContainerCreate calls = %d, want 1", daemon.createCount)
	}
	want := filepath.Join(runDir, "db", "run", "db.sock")
	if got := bindings.Sockets["db/port"]; got != want {
		t.Errorf("binding db/port = %q, want %q", got, want)
	}
}

func TestStartServicesRecreatesOnHashChangeWithConsumers(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.containers = []types.Container{{
		ID:     "old-svc",
		Names:  []string{"/tpd-svc-db"},
		State:  "running",
		Labels: map[string]string{ServiceHashLabel: "oldhash"},
	}}
	daemon.consumers = []types.Container{{
		ID:     "consumer1",
		Names:  []string{"/tpd-profile-abc123"},
		State:  "running",
		Labels: map[string]string{UsesServiceLabel: "db"},
	}}
	daemon.sockets = map[string][]string{
		"tpd-svc-db": {filepath.Join(runDir, "db", "run", "db.sock")},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices with a live consumer and a changed config must recreate, not fail: %v", err)
	}
	defer bindings.Release()

	if daemon.stopCount != 1 {
		t.Errorf("ContainerStop calls = %d, want 1 (old service must be stopped)", daemon.stopCount)
	}
	if !containsString(daemon.removed, "old-svc") {
		t.Errorf("old service container not removed despite live consumers; removed = %v", daemon.removed)
	}
	if daemon.createCount != 1 {
		t.Errorf("ContainerCreate calls = %d, want 1", daemon.createCount)
	}
	want := filepath.Join(runDir, "db", "run", "db.sock")
	if got := bindings.Sockets["db/port"]; got != want {
		t.Errorf("binding db/port = %q, want %q", got, want)
	}
}

func TestStartServicesRemovesContainerOnProbeTimeout(t *testing.T) {
	overrideServicePaths(t)
	overrideProbeTimeout(t, 50*time.Millisecond)
	daemon := newFakeServicesDaemon()
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	_, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err == nil {
		t.Fatal("StartServices must fail when the socket never appears")
	}
	if !strings.Contains(err.Error(), "db") || !strings.Contains(err.Error(), "port") {
		t.Errorf("error %q should name the service and the socket", err)
	}
	if daemon.createCount != 1 || len(daemon.removed) != 1 {
		t.Errorf("created service container must be removed after a probe timeout (create=%d removed=%v)", daemon.createCount, daemon.removed)
	}
}

func TestStartServicesPullsServiceImage(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.imagePresent = false
	daemon.sockets = map[string][]string{
		"tpd-svc-db": {filepath.Join(runDir, "db", "run", "db.sock")},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices: %v", err)
	}
	defer bindings.Release()

	if daemon.pulls != 1 {
		t.Errorf("missing service image must be pulled; pulls = %d, want 1", daemon.pulls)
	}
}

func TestStartServicesAcquiresLocksInSortedOrder(t *testing.T) {
	lockDir := t.TempDir()
	runDir := t.TempDir()
	var mu sync.Mutex
	var acquired []string

	oldLock := serviceLockfilePath
	serviceLockfilePath = func(name string) string {
		mu.Lock()
		acquired = append(acquired, name)
		mu.Unlock()
		return filepath.Join(lockDir, "svc-"+name+".lock")
	}
	t.Cleanup(func() { serviceLockfilePath = oldLock })
	oldRun := serviceRunDir
	serviceRunDir = func(name string, _ workspace.Mode) string { return filepath.Join(runDir, name) }
	t.Cleanup(func() { serviceRunDir = oldRun })

	daemon := newFakeServicesDaemon()
	daemon.sockets = map[string][]string{
		"tpd-svc-a": {filepath.Join(runDir, "a", "run", "x.sock")},
		"tpd-svc-b": {filepath.Join(runDir, "b", "run", "y.sock")},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := Spec{
		Workspace: WorkspaceSpec{Mode: workspace.ModeRootless},
		Services: []ServiceSpec{
			{Name: "b", Hash: "hb", Image: "debian:13-slim", Command: []string{"sleep"}, Labels: map[string]string{ServiceLabel: "b", ServiceHashLabel: "hb", OwnershipLabel: "true"}, Exposes: map[string]string{"y": "/run/y.sock"}},
			{Name: "a", Hash: "ha", Image: "debian:13-slim", Command: []string{"sleep"}, Labels: map[string]string{ServiceLabel: "a", ServiceHashLabel: "ha", OwnershipLabel: "true"}, Exposes: map[string]string{"x": "/run/x.sock"}},
		},
	}
	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices: %v", err)
	}
	bindings.Release()

	if !reflect.DeepEqual(acquired, []string{"a", "b"}) {
		t.Errorf("locks acquired in order %v, want [a b]", acquired)
	}
}

func TestStopServicesRemovesWhenNoConsumers(t *testing.T) {
	overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.containers = []types.Container{{
		ID:     "svc-1",
		Names:  []string{"/tpd-svc-db"},
		State:  "running",
		Labels: map[string]string{ServiceHashLabel: "hash123"},
	}}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	if err := rt.StopServices(context.Background(), spec); err != nil {
		t.Fatalf("StopServices: %v", err)
	}
	if daemon.stopCount != 1 {
		t.Errorf("ContainerStop calls = %d, want 1", daemon.stopCount)
	}
	if !containsString(daemon.removed, "svc-1") {
		t.Errorf("service container not removed; removed = %v", daemon.removed)
	}
}

func TestStopServicesKeepsWhenConsumersRemain(t *testing.T) {
	overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.containers = []types.Container{{
		ID:     "svc-1",
		Names:  []string{"/tpd-svc-db"},
		State:  "running",
		Labels: map[string]string{ServiceHashLabel: "hash123"},
	}}
	daemon.consumers = []types.Container{{
		ID:     "main-1",
		Names:  []string{"/tpd-profile-def456"},
		State:  "running",
		Labels: map[string]string{UsesServiceLabel: "db"},
	}}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	if err := rt.StopServices(context.Background(), spec); err != nil {
		t.Fatalf("StopServices: %v", err)
	}
	if daemon.stopCount != 0 {
		t.Errorf("service must not be stopped while a consumer remains; stop calls = %d", daemon.stopCount)
	}
	if len(daemon.removed) != 0 {
		t.Errorf("service container must not be removed while a consumer remains; removed = %v", daemon.removed)
	}
}

func TestStopServicesContinuesAfterOneFails(t *testing.T) {
	overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.containers = []types.Container{
		{ID: "svc-a", Names: []string{"/tpd-svc-a"}, State: "running", Labels: map[string]string{ServiceHashLabel: "ha"}},
		{ID: "svc-b", Names: []string{"/tpd-svc-b"}, State: "running", Labels: map[string]string{ServiceHashLabel: "hb"}},
	}
	daemon.failStop = map[string]bool{"svc-a": true}
	rt := newServicesTestRuntime(t, daemon)

	spec := Spec{
		Workspace: WorkspaceSpec{Mode: workspace.ModeRootless},
		Services: []ServiceSpec{
			{Name: "b", Hash: "hb", Image: "debian:13-slim", Command: []string{"sleep"}, Labels: map[string]string{ServiceLabel: "b", ServiceHashLabel: "hb", OwnershipLabel: "true"}, Exposes: map[string]string{}},
			{Name: "a", Hash: "ha", Image: "debian:13-slim", Command: []string{"sleep"}, Labels: map[string]string{ServiceLabel: "a", ServiceHashLabel: "ha", OwnershipLabel: "true"}, Exposes: map[string]string{}},
		},
	}
	err := rt.StopServices(context.Background(), spec)
	if err == nil {
		t.Fatal("StopServices must report the failed service stop")
	}
	if !strings.Contains(err.Error(), "svc-a") {
		t.Errorf("error %q should name the failed container", err)
	}
	if daemon.stopCount != 2 {
		t.Errorf("both services must be attempted despite one failure; stop calls = %d, want 2", daemon.stopCount)
	}
	if !containsString(daemon.removed, "svc-b") {
		t.Errorf("service b must still be stopped after a's failure; removed = %v", daemon.removed)
	}
}

func TestStartServicesDedupesExposeParentDirs(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.sockets = map[string][]string{
		"tpd-svc-db": {
			filepath.Join(runDir, "db", "run", "a", "x.sock"),
			filepath.Join(runDir, "db", "run", "a", "y.sock"),
		},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{
		"x": "/run/a/x.sock",
		"y": "/run/a/y.sock",
	})
	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices: %v", err)
	}
	defer bindings.Release()

	var aMounts []string
	for _, m := range daemon.createReqs[0].HostConfig.Mounts {
		if m.Target == "/run/a" {
			aMounts = append(aMounts, m.Source)
		}
	}
	if len(aMounts) != 1 {
		t.Errorf("shared expose parent /run/a bound %d times, want 1 (%v)", len(aMounts), aMounts)
	}
	wantX := filepath.Join(runDir, "db", "run", "a", "x.sock")
	wantY := filepath.Join(runDir, "db", "run", "a", "y.sock")
	if bindings.Sockets["db/x"] != wantX || bindings.Sockets["db/y"] != wantY {
		t.Errorf("bindings = %v, want db/x=%q db/y=%q", bindings.Sockets, wantX, wantY)
	}
}

func TestStartServicesStaleContainerRemovedBeforeCreate(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.containers = []types.Container{{
		ID:     "old-svc",
		Names:  []string{"/tpd-svc-db"},
		State:  "exited",
		Labels: map[string]string{ServiceHashLabel: "hash123"},
	}}
	daemon.sockets = map[string][]string{
		"tpd-svc-db": {filepath.Join(runDir, "db", "run", "db.sock")},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices: %v", err)
	}
	defer bindings.Release()

	if !containsString(daemon.removed, "old-svc") {
		t.Errorf("stopped straggler not removed; removed = %v", daemon.removed)
	}
	if daemon.stopCount != 0 {
		t.Errorf("stopped straggler needs no ContainerStop; stop calls = %d", daemon.stopCount)
	}
	if daemon.createCount != 1 {
		t.Errorf("ContainerCreate calls = %d, want 1", daemon.createCount)
	}
}

func TestStartServicesStaleSocketUnlinked(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	stale := filepath.Join(runDir, "db", "run", "db.sock")
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	daemon := newFakeServicesDaemon()
	daemon.sockets = map[string][]string{"tpd-svc-db": {stale}}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	if _, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false); err != nil {
		t.Fatalf("StartServices: %v", err)
	}

	fi, err := os.Lstat(stale)
	if err != nil {
		t.Fatalf("lstat socket path: %v", err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		t.Errorf("socket path is %v, want a unix socket (stale file must have been unlinked)", fi.Mode())
	}
}

func TestStopServicesCountsCreatedStateConsumers(t *testing.T) {
	overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.containers = []types.Container{{
		ID:     "svc-1",
		Names:  []string{"/tpd-svc-db"},
		State:  "running",
		Labels: map[string]string{ServiceHashLabel: "hash123"},
	}}
	daemon.consumers = []types.Container{{
		ID:     "main-1",
		Names:  []string{"/tpd-profile-ghi789"},
		State:  "created",
		Labels: map[string]string{UsesServiceLabel: "db"},
	}}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	if err := rt.StopServices(context.Background(), spec); err != nil {
		t.Fatalf("StopServices: %v", err)
	}
	if daemon.stopCount != 0 {
		t.Errorf("a created-but-not-started main container is still a consumer; stop calls = %d", daemon.stopCount)
	}
}

func TestStartStopRaceDoesNotKillLiveService(t *testing.T) {
	// Simulates launch B's StartServices having created (but not yet started)
	// its main container while launch A's StopServices runs. A must treat B's
	// created-state container as a consumer and leave the service running.
	overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.containers = []types.Container{{
		ID:     "svc-live",
		Names:  []string{"/tpd-svc-db"},
		State:  "running",
		Labels: map[string]string{ServiceHashLabel: "hash123"},
	}}
	daemon.consumers = []types.Container{{
		ID:     "b-main",
		Names:  []string{"/tpd-b-abc123"},
		State:  "created",
		Labels: map[string]string{UsesServiceLabel: "db"},
	}}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	if err := rt.StopServices(context.Background(), spec); err != nil {
		t.Fatalf("StopServices: %v", err)
	}
	if daemon.stopCount != 0 || len(daemon.removed) != 0 {
		t.Errorf("A's stop must not kill the service under B's in-flight launch (stop=%d removed=%v)", daemon.stopCount, daemon.removed)
	}
}

func TestStartServicesReleasesLocksOnProbeTimeout(t *testing.T) {
	lockDir, runDir := overrideServicePaths(t)
	overrideProbeTimeout(t, 50*time.Millisecond)
	daemon := newFakeServicesDaemon()
	daemon.sockets = map[string][]string{
		"tpd-svc-a": {filepath.Join(runDir, "a", "run", "x.sock")},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := Spec{
		Workspace: WorkspaceSpec{Mode: workspace.ModeRootless},
		Services: []ServiceSpec{
			{Name: "a", Hash: "ha", Image: "debian:13-slim", Command: []string{"sleep"}, Labels: map[string]string{ServiceLabel: "a", ServiceHashLabel: "ha", OwnershipLabel: "true"}, Exposes: map[string]string{"x": "/run/x.sock"}},
			{Name: "b", Hash: "hb", Image: "debian:13-slim", Command: []string{"sleep"}, Labels: map[string]string{ServiceLabel: "b", ServiceHashLabel: "hb", OwnershipLabel: "true"}, Exposes: map[string]string{"y": "/run/y.sock"}},
		},
	}
	if _, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false); err == nil {
		t.Fatal("StartServices must fail when service b's socket never appears")
	}

	// Both lockfiles must be re-acquirable: the internal defer released every
	// lock acquired before the failure.
	for _, name := range []string{"a", "b"} {
		f, err := os.OpenFile(filepath.Join(lockDir, "svc-"+name+".lock"), os.O_RDWR, 0o600)
		if err != nil {
			t.Fatalf("open lockfile for %s: %v", name, err)
		}
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			t.Errorf("lock for service %s still held after StartServices error: %v", name, err)
		}
		unix.Flock(int(f.Fd()), unix.LOCK_UN)
		f.Close()
	}
}

func TestStartServicesRejectsRootful(t *testing.T) {
	overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	spec.Workspace.Mode = workspace.ModeRootful
	_, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err == nil || !strings.Contains(err.Error(), "rootful") {
		t.Fatalf("StartServices must reject rootful mode, got %v", err)
	}
	if daemon.createCount != 0 {
		t.Errorf("no service container must be created in rootful mode; created %d", daemon.createCount)
	}
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
