package runtime

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/google/go-cmp/cmp"
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

	imagePresent   bool
	imageID        string
	derivedPresent bool
	derivedID      string
	buildReqs      []fakeBuildReq
	pulls          int

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

	execCreates  int
	execCmds     [][]string
	execExitCode int

	networkExists  bool
	connectReqs    []fakeServiceConnectReq
	connectAliases []string
	connectCode    int
	networksByID   map[string]map[string]*network.EndpointSettings
}

type fakeServiceConnectReq struct {
	containerID string
	aliases     []string
}

type fakeBuildReq struct {
	version    string
	dockerfile string
}

func newFakeServicesDaemon() *fakeServicesDaemon {
	return &fakeServicesDaemon{
		imagePresent:  true,
		imageID:       "sha256:base",
		nameByID:      map[string]string{},
		networkExists: true,
		networksByID:  map[string]map[string]*network.EndpointSettings{},
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
	case r.Method == http.MethodPost && strings.HasPrefix(p, "containers/") && strings.HasSuffix(p, "/start"):
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
	case r.Method == http.MethodGet && strings.HasPrefix(p, "containers/") && strings.HasSuffix(p, "/json"):
		f.serveContainerInspect(w, strings.TrimPrefix(strings.TrimSuffix(p, "/json"), "containers/"))
	case r.Method == http.MethodGet && strings.HasPrefix(p, "networks/"):
		if !f.networkExists {
			http.Error(w, `{"message":"network tpd-services not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ownedServiceNetwork())
	case r.Method == http.MethodPost && p == "networks/create":
		io.Copy(io.Discard, r.Body)
		f.networkExists = true
		fmt.Fprint(w, `{"Id":"tpd-services"}`)
	case r.Method == http.MethodPost && strings.HasPrefix(p, "networks/") && strings.HasSuffix(p, "/connect"):
		var req network.ConnectOptions
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		var aliases []string
		if req.EndpointConfig != nil {
			aliases = req.EndpointConfig.Aliases
		}
		f.connectReqs = append(f.connectReqs, fakeServiceConnectReq{containerID: req.Container, aliases: aliases})
		f.connectAliases = append(f.connectAliases, aliases...)
		if f.connectCode != 0 {
			http.Error(w, `{"message":"connect failed"}`, f.connectCode)
			return
		}
		if f.networksByID[req.Container] == nil {
			f.networksByID[req.Container] = map[string]*network.EndpointSettings{}
		}
		f.networksByID[req.Container][ServiceNetworkName] = &network.EndpointSettings{Aliases: aliases}
		w.WriteHeader(http.StatusNoContent)
	case p == "volumes/create" && r.Method == http.MethodPost:
		io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, `{"Name":"vol","Driver":"local"}`)
	case p == "images/create" && r.Method == http.MethodPost:
		f.pulls++
		io.Copy(io.Discard, r.Body)
		f.imagePresent = true
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"Pull complete"}`+"\n")
	case r.Method == http.MethodPost && strings.HasPrefix(p, "containers/") && strings.HasSuffix(p, "/exec"):
		var execReq struct{ Cmd []string }
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &execReq)
		f.execCreates++
		f.execCmds = append(f.execCmds, execReq.Cmd)
		fmt.Fprintf(w, `{"Id":"exec%d"}`, f.execCreates)
	case r.Method == http.MethodPost && strings.HasPrefix(p, "exec/") && strings.HasSuffix(p, "/start"):
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.HasPrefix(p, "exec/") && strings.HasSuffix(p, "/json"):
		fmt.Fprintf(w, `{"Running":false,"ExitCode":%d}`, f.execExitCode)
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/json"):
		if !strings.HasPrefix(p, "images/") {
			fmt.Fprintf(w, `{"Id":%q}`, f.imageID)
			return
		}
		ref, err := url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(p, "images/"), "/json"))
		if err != nil {
			http.Error(w, "bad ref", http.StatusBadRequest)
			return
		}
		if strings.HasPrefix(ref, "tpd/packages:") {
			if !f.derivedPresent {
				http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
				return
			}
			fmt.Fprintf(w, `{"Id":%q}`, f.derivedID)
			return
		}
		if !f.imagePresent {
			http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"Id":%q}`, f.imageID)
	case r.Method == http.MethodPost && p == "build":
		f.buildReqs = append(f.buildReqs, readFakeBuildReq(w, r))
		io.Copy(io.Discard, r.Body)
		f.derivedPresent = true
		f.derivedID = "sha256:derived"
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"stream":"Successfully built derived\n"}`+"\n")
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

func (f *fakeServicesDaemon) serveContainerInspect(w http.ResponseWriter, id string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{ID: id},
		NetworkSettings:   &types.NetworkSettings{Networks: f.networksByID[id]},
	})
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

// listenAt binds a unix socket at path so the reuse path's socket check sees a
// real socket on disk.
func listenAt(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
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
	socket := filepath.Join(runDir, "db", "run", "db.sock")
	listenAt(t, socket)
	daemon := newFakeServicesDaemon()
	daemon.containers = []types.Container{{
		ID:     "old-svc",
		Names:  []string{"/tpd-svc-db"},
		State:  "running",
		Labels: map[string]string{ServiceHashLabel: "hash123", ServiceLabel: "db", OwnershipLabel: "true"},
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

func TestStartServicesNewServiceAttachesToNetwork(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.sockets = map[string][]string{
		"tpd-svc-registry": {filepath.Join(runDir, "registry", "run", "registry.sock")},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("registry", "hash123", map[string]string{"port": "/run/registry.sock"})
	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices: %v", err)
	}
	defer bindings.Release()

	if bindings.Network != ServiceNetworkName {
		t.Errorf("network = %q, want %q", bindings.Network, ServiceNetworkName)
	}
	want := filepath.Join(runDir, "registry", "run", "registry.sock")
	if got := bindings.Sockets["registry/port"]; got != want {
		t.Errorf("binding registry/port = %q, want %q", got, want)
	}
	if len(daemon.connectReqs) != 1 {
		t.Fatalf("network connect calls = %d, want 1", len(daemon.connectReqs))
	}
	if diff := cmp.Diff([]string{"tpd-svc-registry"}, daemon.connectAliases); diff != "" {
		t.Fatal(diff)
	}
}

func TestStartServicesReuseSkipsRedundantConnect(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	socket := filepath.Join(runDir, "db", "run", "db.sock")
	listenAt(t, socket)
	daemon := newFakeServicesDaemon()
	daemon.containers = []types.Container{{
		ID:     "old-svc",
		Names:  []string{"/tpd-svc-db"},
		State:  "running",
		Labels: map[string]string{ServiceHashLabel: "hash123", ServiceLabel: "db", OwnershipLabel: "true"},
	}}
	daemon.networksByID["old-svc"] = map[string]*network.EndpointSettings{
		ServiceNetworkName: {Aliases: []string{"tpd-svc-db"}},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices: %v", err)
	}
	defer bindings.Release()

	if len(daemon.connectReqs) != 0 {
		t.Errorf("an already-attached reused container must not be reconnected; connects = %v", daemon.connectReqs)
	}
	if bindings.Network != ServiceNetworkName {
		t.Errorf("network = %q, want %q", bindings.Network, ServiceNetworkName)
	}
}

func TestStartServicesReuseRepairsMissingNetwork(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	socket := filepath.Join(runDir, "db", "run", "db.sock")
	listenAt(t, socket)
	daemon := newFakeServicesDaemon()
	daemon.containers = []types.Container{{
		ID:     "old-svc",
		Names:  []string{"/tpd-svc-db"},
		State:  "running",
		Labels: map[string]string{ServiceHashLabel: "hash123", ServiceLabel: "db", OwnershipLabel: "true"},
	}}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices: %v", err)
	}
	defer bindings.Release()

	if len(daemon.connectReqs) != 1 {
		t.Fatalf("network connect calls = %d, want 1", len(daemon.connectReqs))
	}
	if daemon.connectReqs[0].containerID != "old-svc" {
		t.Errorf("connect container = %q, want old-svc", daemon.connectReqs[0].containerID)
	}
	if diff := cmp.Diff([]string{"tpd-svc-db"}, daemon.connectReqs[0].aliases); diff != "" {
		t.Fatal(diff)
	}
}

func TestStartServicesNetworkConnectFailureReleasesLocks(t *testing.T) {
	lockDir, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.connectCode = http.StatusInternalServerError
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
	_, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err == nil {
		t.Fatal("StartServices must fail when the network connect fails")
	}
	if !strings.Contains(err.Error(), "connect") {
		t.Errorf("error %q should mention the failed connect", err)
	}
	if !containsString(daemon.removed, daemon.createdIDs[0]) {
		t.Errorf("created service container must be removed after a connect failure; removed = %v", daemon.removed)
	}
	// Both lockfiles must be re-acquirable: the internal defer released every
	// lock acquired before the failure.
	for _, name := range []string{"a", "b"} {
		f, err := os.OpenFile(filepath.Join(lockDir, "svc-"+name+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
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

func TestStartServicesForeignContainerConflict(t *testing.T) {
	// A foreign container must never be removed or created over regardless of
	// its state: "exited" covers the destructive stale-straggler removal branch,
	// which must not fire for a container tpd does not own.
	for _, state := range []string{"running", "exited"} {
		t.Run(state, func(t *testing.T) {
			_, runDir := overrideServicePaths(t)
			daemon := newFakeServicesDaemon()
			daemon.containers = []types.Container{{
				ID:     "foreign-svc",
				Names:  []string{"/tpd-svc-db"},
				State:  state,
				Labels: map[string]string{"com.example.owner": "someone-else"},
			}}
			daemon.sockets = map[string][]string{
				"tpd-svc-db": {filepath.Join(runDir, "db", "run", "db.sock")},
			}
			rt := newServicesTestRuntime(t, daemon)

			spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
			_, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
			var foreign *ForeignServiceContainerError
			if !errors.As(err, &foreign) {
				t.Fatalf("StartServices error = %v, want *ForeignServiceContainerError", err)
			}
			if foreign.ContainerName != "tpd-svc-db" {
				t.Errorf("conflict container name = %q, want tpd-svc-db", foreign.ContainerName)
			}
			if daemon.createCount != 0 {
				t.Errorf("ContainerCreate called %d times over a foreign container, want 0", daemon.createCount)
			}
			if daemon.stopCount != 0 || len(daemon.removed) != 0 {
				t.Errorf("foreign container must never be stopped or removed (stop=%d removed=%v)", daemon.stopCount, daemon.removed)
			}
		})
	}
}

func TestStartServicesRecreatesWhenSocketUnreusable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		writeFile bool
	}{
		{name: "absent"},
		{name: "regular file", writeFile: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, runDir := overrideServicePaths(t)
			socket := filepath.Join(runDir, "db", "run", "db.sock")
			if tc.writeFile {
				if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(socket, []byte("stale"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			daemon := newFakeServicesDaemon()
			daemon.containers = []types.Container{{
				ID:     "old-svc",
				Names:  []string{"/tpd-svc-db"},
				State:  "running",
				Labels: map[string]string{ServiceHashLabel: "hash123", ServiceLabel: "db", OwnershipLabel: "true"},
			}}
			daemon.sockets = map[string][]string{"tpd-svc-db": {socket}}
			rt := newServicesTestRuntime(t, daemon)

			spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
			bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
			if err != nil {
				t.Fatalf("StartServices: %v", err)
			}
			defer bindings.Release()

			if daemon.createCount != 1 {
				t.Errorf("ContainerCreate calls = %d, want 1 (a hash match with an unusable socket must recreate)", daemon.createCount)
			}
			if !containsString(daemon.removed, "old-svc") {
				t.Errorf("old service not removed; removed = %v", daemon.removed)
			}
		})
	}
}

func TestStartServicesRecreatesIgnoringForeignConsumer(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.containers = []types.Container{{
		ID:     "old-svc",
		Names:  []string{"/tpd-svc-db"},
		State:  "running",
		Labels: map[string]string{ServiceHashLabel: "oldhash", ServiceLabel: "db", OwnershipLabel: "true"},
	}}
	daemon.consumers = []types.Container{{
		ID:     "foreign-consumer",
		Names:  []string{"/not-a-tpd-container"},
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
		t.Fatalf("StartServices: %v", err)
	}
	defer bindings.Release()

	if daemon.createCount != 1 {
		t.Errorf("ContainerCreate calls = %d, want 1 (a foreign consumer must not block recreation)", daemon.createCount)
	}
	if !containsString(daemon.removed, "old-svc") {
		t.Errorf("old service not removed; removed = %v", daemon.removed)
	}
}

func TestStopServicesIgnoresForeignConsumers(t *testing.T) {
	overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.containers = []types.Container{{
		ID:     "svc-1",
		Names:  []string{"/tpd-svc-db"},
		State:  "running",
		Labels: map[string]string{ServiceHashLabel: "hash123", ServiceLabel: "db", OwnershipLabel: "true"},
	}}
	daemon.consumers = []types.Container{{
		ID:     "foreign-consumer",
		Names:  []string{"/not-a-tpd-container"},
		State:  "running",
		Labels: map[string]string{UsesServiceLabel: "db"},
	}}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	if err := rt.StopServices(context.Background(), spec); err != nil {
		t.Fatalf("StopServices: %v", err)
	}
	if daemon.stopCount != 1 {
		t.Errorf("a foreign consumer must not keep the service alive; stop calls = %d, want 1", daemon.stopCount)
	}
	if !containsString(daemon.removed, "svc-1") {
		t.Errorf("service container not removed; removed = %v", daemon.removed)
	}
}

func TestStopServicesForeignContainerNotRemoved(t *testing.T) {
	overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.containers = []types.Container{{
		ID:     "foreign-svc",
		Names:  []string{"/tpd-svc-db"},
		State:  "running",
		Labels: map[string]string{"com.example.owner": "someone-else"},
	}}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	err := rt.StopServices(context.Background(), spec)
	var foreign *ForeignServiceContainerError
	if !errors.As(err, &foreign) {
		t.Fatalf("StopServices error = %v, want *ForeignServiceContainerError", err)
	}
	if foreign.ContainerName != "tpd-svc-db" {
		t.Errorf("conflict container name = %q, want tpd-svc-db", foreign.ContainerName)
	}
	if daemon.stopCount != 0 || len(daemon.removed) != 0 {
		t.Errorf("foreign container must never be stopped or removed (stop=%d removed=%v)", daemon.stopCount, daemon.removed)
	}
}

func TestStartServicesRecreatesOnHashChangeZeroConsumers(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	daemon.containers = []types.Container{{
		ID:     "old-svc",
		Names:  []string{"/tpd-svc-db"},
		State:  "running",
		Labels: map[string]string{ServiceHashLabel: "oldhash", ServiceLabel: "db", OwnershipLabel: "true"},
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
		Labels: map[string]string{ServiceHashLabel: "oldhash", ServiceLabel: "db", OwnershipLabel: "true"},
	}}
	daemon.consumers = []types.Container{{
		ID:     "consumer1",
		Names:  []string{"/tpd-profile-abc123"},
		State:  "running",
		Labels: map[string]string{UsesServiceLabel: "db", OwnershipLabel: "true"},
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

func TestStartServicesBuildPassesBaseID(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	// The fake creates the exposed socket on ContainerStart, so the socket
	// poll in StartServices succeeds (mirrors TestStartServicesCreatesNewService).
	daemon.sockets = map[string][]string{
		"tpd-svc-db": {filepath.Join(runDir, "db", "run", "db.sock")},
	}
	oldDir := buildLockDir
	buildLockDir = t.TempDir()
	t.Cleanup(func() { buildLockDir = oldDir })
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	spec.Services[0].Packages = []string{"pkg1"}

	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices: %v", err)
	}
	defer bindings.Release()

	if len(daemon.buildReqs) != 1 {
		t.Fatalf("build requests = %d, want 1", len(daemon.buildReqs))
	}
	req := daemon.buildReqs[0]
	if req.version != "2" {
		t.Errorf("service build version = %q, want %q", req.version, "2")
	}
	// The fake's base image id is "sha256:base"; the cache ids must derive from
	// it, proving createService threads its resolved baseID through.
	aptID, listsID := cacheMountIDs(daemon.imageID, nil)
	for _, want := range []string{
		"--mount=type=cache,id=" + aptID + ",target=/var/cache/apt,sharing=locked",
		"--mount=type=cache,id=" + listsID + ",target=/var/lib/apt,sharing=locked",
	} {
		if !strings.Contains(req.dockerfile, want) {
			t.Errorf("service build Dockerfile must contain %q:\n%s", want, req.dockerfile)
		}
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
		Labels: map[string]string{ServiceHashLabel: "hash123", ServiceLabel: "db", OwnershipLabel: "true"},
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
		Labels: map[string]string{ServiceHashLabel: "hash123", ServiceLabel: "db", OwnershipLabel: "true"},
	}}
	daemon.consumers = []types.Container{{
		ID:     "main-1",
		Names:  []string{"/tpd-profile-def456"},
		State:  "running",
		Labels: map[string]string{UsesServiceLabel: "db", OwnershipLabel: "true"},
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
		{ID: "svc-a", Names: []string{"/tpd-svc-a"}, State: "running", Labels: map[string]string{ServiceHashLabel: "ha", ServiceLabel: "a", OwnershipLabel: "true"}},
		{ID: "svc-b", Names: []string{"/tpd-svc-b"}, State: "running", Labels: map[string]string{ServiceHashLabel: "hb", ServiceLabel: "b", OwnershipLabel: "true"}},
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
		Labels: map[string]string{ServiceHashLabel: "hash123", ServiceLabel: "db", OwnershipLabel: "true"},
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
		Labels: map[string]string{ServiceHashLabel: "hash123", ServiceLabel: "db", OwnershipLabel: "true"},
	}}
	daemon.consumers = []types.Container{{
		ID:     "main-1",
		Names:  []string{"/tpd-profile-ghi789"},
		State:  "created",
		Labels: map[string]string{UsesServiceLabel: "db", OwnershipLabel: "true"},
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
		Labels: map[string]string{ServiceHashLabel: "hash123", ServiceLabel: "db", OwnershipLabel: "true"},
	}}
	daemon.consumers = []types.Container{{
		ID:     "b-main",
		Names:  []string{"/tpd-b-abc123"},
		State:  "created",
		Labels: map[string]string{UsesServiceLabel: "db", OwnershipLabel: "true"},
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

func TestStartServicesRootfulLifecycle(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	svcName := fmt.Sprintf("tpd-svc-db-%d", os.Getuid())
	daemon.sockets = map[string][]string{
		svcName: {filepath.Join(runDir, "db", "run", "db.sock")},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	spec.Workspace.Mode = workspace.ModeRootful
	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices: %v", err)
	}
	defer bindings.Release()

	if daemon.createCount != 1 {
		t.Fatalf("ContainerCreate calls = %d, want 1", daemon.createCount)
	}
	if daemon.createdNames[0] != svcName {
		t.Errorf("rootful service container name = %q, want %q", daemon.createdNames[0], svcName)
	}
	want := filepath.Join(runDir, "db", "run", "db.sock")
	if got := bindings.Sockets["db/port"]; got != want {
		t.Errorf("binding db/port = %q, want %q", got, want)
	}
}

func TestStopServicesRootful(t *testing.T) {
	overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	svcName := fmt.Sprintf("tpd-svc-db-%d", os.Getuid())
	daemon.containers = []types.Container{{
		ID:     "svc-1",
		Names:  []string{"/" + svcName},
		State:  "running",
		Labels: map[string]string{ServiceHashLabel: "hash123", ServiceLabel: "db", OwnershipLabel: "true"},
	}}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	spec.Workspace.Mode = workspace.ModeRootful
	if err := rt.StopServices(context.Background(), spec); err != nil {
		t.Fatalf("StopServices: %v", err)
	}
	if daemon.stopCount != 1 {
		t.Errorf("ContainerStop calls = %d, want 1", daemon.stopCount)
	}
	if !containsString(daemon.removed, "svc-1") {
		t.Errorf("rootful service container not removed; removed = %v", daemon.removed)
	}
}

func TestStartServicesRootfulChownsSocket(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	svcName := fmt.Sprintf("tpd-svc-db-%d", os.Getuid())
	daemon.sockets = map[string][]string{
		svcName: {filepath.Join(runDir, "db", "run", "db.sock")},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	spec.Workspace.Mode = workspace.ModeRootful
	bindings, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err != nil {
		t.Fatalf("StartServices: %v", err)
	}
	defer bindings.Release()

	wantUID := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	if len(daemon.execCmds) != 2 {
		t.Fatalf("exec calls = %v, want exactly one chown + one chmod", daemon.execCmds)
	}
	if daemon.execCmds[0][0] != "chown" || daemon.execCmds[0][1] != wantUID || daemon.execCmds[0][2] != "/run/db.sock" {
		t.Errorf("first exec = %v, want chown %s /run/db.sock", daemon.execCmds[0], wantUID)
	}
	if daemon.execCmds[1][0] != "chmod" || daemon.execCmds[1][1] != "0770" || daemon.execCmds[1][2] != "/run/db.sock" {
		t.Errorf("second exec = %v, want chmod 0770 /run/db.sock", daemon.execCmds[1])
	}
}

func TestStartServicesRootfulChownFailure(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	overrideProbeTimeout(t, time.Second)
	daemon := newFakeServicesDaemon()
	daemon.execExitCode = 127
	svcName := fmt.Sprintf("tpd-svc-db-%d", os.Getuid())
	daemon.sockets = map[string][]string{
		svcName: {filepath.Join(runDir, "db", "run", "db.sock")},
	}
	rt := newServicesTestRuntime(t, daemon)

	spec := serviceSpec("db", "hash123", map[string]string{"port": "/run/db.sock"})
	spec.Workspace.Mode = workspace.ModeRootful
	_, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
	if err == nil || !strings.Contains(err.Error(), "chown") || !strings.Contains(err.Error(), "exit code 127") {
		t.Fatalf("a failing chown exec must surface as an error, got %v", err)
	}
}

func TestServiceRunDirPaths(t *testing.T) {
	uid := os.Getuid()
	if got := serviceRunDir("db", workspace.ModeRootless); got != fmt.Sprintf("/run/user/%d/tpd-svc-db/", uid) {
		t.Errorf("rootless run dir = %q, want /run/user/%d/tpd-svc-db/", got, uid)
	}
	if got := serviceRunDir("db", workspace.ModeRootful); got != fmt.Sprintf("/tmp/tpd-svc-db-%d/", uid) {
		t.Errorf("rootful run dir = %q, want /tmp/tpd-svc-db-%d/", got, uid)
	}
}

func TestEnsureServiceRunDir(t *testing.T) {
	base := t.TempDir()
	if err := ensureServiceRunDir(filepath.Join(base, "ok")); err != nil {
		t.Fatalf("fresh dir must be accepted: %v", err)
	}
	if err := ensureServiceRunDir(filepath.Join(base, "ok")); err != nil {
		t.Fatalf("existing own dir must be accepted: %v", err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureServiceRunDir(file); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("a regular file must be rejected, got %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(filepath.Join(base, "ok"), link); err != nil {
		t.Fatal(err)
	}
	if err := ensureServiceRunDir(link); err == nil {
		t.Error("a symlink must be rejected (Lstat, not Stat)")
	}
}

func TestValidateServiceExposePath(t *testing.T) {
	for _, path := range []string{"/run/app/db.sock", "/run/registry/registry.sock", "/var/lib/postgres/run.sock"} {
		if err := validateServiceExposePath(path); err != nil {
			t.Errorf("validateServiceExposePath(%q) = %v, want nil", path, err)
		}
	}
	for _, path := range []string{"/db.sock", "/", "/run/../x.sock", "/run/app/../db.sock", "run/app/db.sock"} {
		if err := validateServiceExposePath(path); err == nil {
			t.Errorf("validateServiceExposePath(%q) = nil, want error", path)
		}
	}
}

func TestStartServicesRejectsDangerousExposePaths(t *testing.T) {
	_, runDir := overrideServicePaths(t)
	daemon := newFakeServicesDaemon()
	rt := newServicesTestRuntime(t, daemon)

	for _, exposePath := range []string{"/db.sock", "/", "/run/../x.sock", "/run/app/../db.sock"} {
		spec := serviceSpec("db", "hash123", map[string]string{"port": exposePath})
		_, err := rt.StartServices(context.Background(), spec, NoopProgressWriter{}, false)
		if err == nil {
			t.Errorf("StartServices with expose %q must fail", exposePath)
			continue
		}
		if !strings.Contains(err.Error(), "db") || !strings.Contains(err.Error(), exposePath) {
			t.Errorf("error for expose %q = %q, want it to name the service and path", exposePath, err)
		}
	}
	if daemon.createCount != 0 {
		t.Errorf("ContainerCreate called %d times for rejected exposes, want 0", daemon.createCount)
	}
	if _, err := os.Stat(filepath.Join(runDir, "db")); !os.IsNotExist(err) {
		t.Errorf("host run-dir must not be created for rejected exposes")
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

func readFakeBuildReq(w http.ResponseWriter, r *http.Request) fakeBuildReq {
	var req fakeBuildReq
	req.version = r.URL.Query().Get("version")
	tr := tar.NewReader(r.Body)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return req
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			return req
		}
		if hdr.Name == "Dockerfile" {
			req.dockerfile = string(content)
		}
	}
	return req
}
