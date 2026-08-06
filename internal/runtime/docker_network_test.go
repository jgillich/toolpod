package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

type fakeInspectResult struct {
	code int
	net  network.Inspect
}

type fakeConnectReq struct {
	networkID   string
	containerID string
	aliases     []string
}

type fakeRemoveReq struct {
	id    string
	force bool
}

// fakeNetworkDaemon serves the Docker API endpoints the managed network uses:
// scripted network inspects, a recorded network create, network connect, and
// container removal.
type fakeNetworkDaemon struct {
	inspectResults []fakeInspectResult
	inspectCount   int

	createCount int
	createReq   network.CreateRequest
	createCode  int

	connectReqs []fakeConnectReq
	connectCode int

	removed []fakeRemoveReq
}

func (f *fakeNetworkDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/v1.41/")
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(p, "networks/"):
		var res fakeInspectResult
		if f.inspectCount < len(f.inspectResults) {
			res = f.inspectResults[f.inspectCount]
		}
		f.inspectCount++
		if res.code != 0 {
			http.Error(w, `{"message":"no such network"}`, res.code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res.net)
	case r.Method == http.MethodPost && p == "networks/create":
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &f.createReq)
		f.createCount++
		if f.createCode != 0 {
			http.Error(w, `{"message":"network already exists"}`, f.createCode)
			return
		}
		fmt.Fprint(w, `{"Id":"net-created"}`)
	case r.Method == http.MethodPost && strings.HasPrefix(p, "networks/") && strings.HasSuffix(p, "/connect"):
		var req network.ConnectOptions
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		id := strings.TrimPrefix(strings.TrimSuffix(p, "/connect"), "networks/")
		var aliases []string
		if req.EndpointConfig != nil {
			aliases = req.EndpointConfig.Aliases
		}
		f.connectReqs = append(f.connectReqs, fakeConnectReq{networkID: id, containerID: req.Container, aliases: aliases})
		if f.connectCode != 0 {
			http.Error(w, `{"message":"connect failed"}`, f.connectCode)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "containers/"):
		f.removed = append(f.removed, fakeRemoveReq{id: strings.TrimPrefix(p, "containers/"), force: r.URL.Query().Get("force") == "1"})
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func newNetworkTestRuntime(t *testing.T, daemon http.Handler) *DockerRuntime {
	t.Helper()
	srv := httptest.NewServer(daemon)
	t.Cleanup(srv.Close)
	cli, err := client.NewClientWithOpts(
		client.WithHost("tcp://"+srv.Listener.Addr().String()),
		client.WithVersion("1.41"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return &DockerRuntime{cli: cli}
}

func ownedServiceNetwork() network.Inspect {
	return network.Inspect{
		Name:   ServiceNetworkName,
		Driver: "bridge",
		Labels: map[string]string{
			OwnershipLabel:   "true",
			NetworkRoleLabel: NetworkRoleServices,
		},
	}
}

func TestEnsureServiceNetworkCreatesLabeledBridge(t *testing.T) {
	daemon := &fakeNetworkDaemon{inspectResults: []fakeInspectResult{{code: http.StatusNotFound}}}
	rt := newNetworkTestRuntime(t, daemon)

	name, err := rt.ensureServiceNetwork(context.Background())
	if err != nil {
		t.Fatalf("ensureServiceNetwork: %v", err)
	}
	if name != ServiceNetworkName {
		t.Errorf("network name = %q, want %q", name, ServiceNetworkName)
	}
	if daemon.createCount != 1 {
		t.Fatalf("create calls = %d, want 1", daemon.createCount)
	}
	if daemon.createReq.Name != ServiceNetworkName {
		t.Errorf("create name = %q, want %q", daemon.createReq.Name, ServiceNetworkName)
	}
	if daemon.createReq.Driver != "bridge" {
		t.Errorf("create driver = %q, want bridge", daemon.createReq.Driver)
	}
	wantLabels := map[string]string{OwnershipLabel: "true", NetworkRoleLabel: NetworkRoleServices}
	if !reflect.DeepEqual(daemon.createReq.Labels, wantLabels) {
		t.Errorf("create labels = %v, want exactly %v", daemon.createReq.Labels, wantLabels)
	}
}

func TestEnsureServiceNetworkRecoversFromConcurrentCreateConflict(t *testing.T) {
	daemon := &fakeNetworkDaemon{
		inspectResults: []fakeInspectResult{
			{code: http.StatusNotFound},
			{net: ownedServiceNetwork()},
		},
		createCode: http.StatusConflict,
	}
	rt := newNetworkTestRuntime(t, daemon)

	name, err := rt.ensureServiceNetwork(context.Background())
	if err != nil {
		t.Fatalf("ensureServiceNetwork after create conflict: %v", err)
	}
	if name != ServiceNetworkName {
		t.Errorf("network name = %q, want %q", name, ServiceNetworkName)
	}
	if daemon.createCount != 1 {
		t.Errorf("create calls = %d, want 1", daemon.createCount)
	}
	if daemon.inspectCount != 2 {
		t.Errorf("inspect calls = %d, want 2 (one before create, one after conflict)", daemon.inspectCount)
	}
}

func TestEnsureServiceNetworkReusesOwnedBridge(t *testing.T) {
	daemon := &fakeNetworkDaemon{inspectResults: []fakeInspectResult{{net: ownedServiceNetwork()}}}
	rt := newNetworkTestRuntime(t, daemon)

	name, err := rt.ensureServiceNetwork(context.Background())
	if err != nil {
		t.Fatalf("ensureServiceNetwork: %v", err)
	}
	if name != ServiceNetworkName {
		t.Errorf("network name = %q, want %q", name, ServiceNetworkName)
	}
	if daemon.createCount != 0 {
		t.Errorf("existing network must be reused, create calls = %d", daemon.createCount)
	}
}

func TestEnsureServiceNetworkRejectsUnownedCanonicalName(t *testing.T) {
	net := ownedServiceNetwork()
	delete(net.Labels, OwnershipLabel)
	daemon := &fakeNetworkDaemon{inspectResults: []fakeInspectResult{{net: net}}}
	rt := newNetworkTestRuntime(t, daemon)

	_, err := rt.ensureServiceNetwork(context.Background())
	if err == nil {
		t.Fatal("ensureServiceNetwork must reject an unowned tpd-services network")
	}
	if !strings.Contains(err.Error(), ServiceNetworkName) || !strings.Contains(err.Error(), OwnershipLabel) {
		t.Errorf("error %q should name the network and the missing ownership label", err)
	}
}

func TestEnsureServiceNetworkRejectsWrongRole(t *testing.T) {
	net := ownedServiceNetwork()
	net.Labels[NetworkRoleLabel] = "sidecar"
	daemon := &fakeNetworkDaemon{inspectResults: []fakeInspectResult{{net: net}}}
	rt := newNetworkTestRuntime(t, daemon)

	_, err := rt.ensureServiceNetwork(context.Background())
	if err == nil {
		t.Fatal("ensureServiceNetwork must reject a network with the wrong role label")
	}
	if !strings.Contains(err.Error(), ServiceNetworkName) || !strings.Contains(err.Error(), NetworkRoleLabel) {
		t.Errorf("error %q should name the network and the role label", err)
	}
}

func TestEnsureServiceNetworkRejectsNonBridge(t *testing.T) {
	net := ownedServiceNetwork()
	net.Driver = "macvlan"
	daemon := &fakeNetworkDaemon{inspectResults: []fakeInspectResult{{net: net}}}
	rt := newNetworkTestRuntime(t, daemon)

	_, err := rt.ensureServiceNetwork(context.Background())
	if err == nil {
		t.Fatal("ensureServiceNetwork must reject a non-bridge network")
	}
	if !strings.Contains(err.Error(), ServiceNetworkName) || !strings.Contains(err.Error(), "bridge") {
		t.Errorf("error %q should name the network and the expected driver", err)
	}
}

func TestConnectContainerToNetworkPassesAliases(t *testing.T) {
	daemon := &fakeNetworkDaemon{}
	rt := newNetworkTestRuntime(t, daemon)

	aliases := []string{"tpd-svc-db", "tpd-svc-cache"}
	err := rt.ConnectContainerToNetwork(context.Background(), "abc123", ServiceNetworkName, aliases)
	if err != nil {
		t.Fatalf("ConnectContainerToNetwork: %v", err)
	}
	aliases[0] = "mutated"
	if len(daemon.connectReqs) != 1 {
		t.Fatalf("connect calls = %d, want 1", len(daemon.connectReqs))
	}
	req := daemon.connectReqs[0]
	if req.networkID != ServiceNetworkName {
		t.Errorf("connect network = %q, want %q", req.networkID, ServiceNetworkName)
	}
	if req.containerID != "abc123" {
		t.Errorf("connect container = %q, want abc123", req.containerID)
	}
	want := []string{"tpd-svc-db", "tpd-svc-cache"}
	if !reflect.DeepEqual(req.aliases, want) {
		t.Errorf("connect aliases = %v, want %v (caller's slice must be copied)", req.aliases, want)
	}
}

func TestRemoveContainerForcesRemoval(t *testing.T) {
	daemon := &fakeNetworkDaemon{}
	rt := newNetworkTestRuntime(t, daemon)

	err := rt.RemoveContainer(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("RemoveContainer: %v", err)
	}
	if len(daemon.removed) != 1 {
		t.Fatalf("remove calls = %v, want exactly [abc123]", daemon.removed)
	}
	if daemon.removed[0].id != "abc123" || !daemon.removed[0].force {
		t.Errorf("remove request = %+v, want id abc123 with force=true", daemon.removed[0])
	}
}
