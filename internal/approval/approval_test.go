package approval

import (
	"testing"

	"github.com/jgillich/tpd/internal/profile"
)

func TestFilterNoSensitiveFieldsNoPrompt(t *testing.T) {
	res := profile.Resolved{Profile: profile.Profile{Image: "img", Command: []string{"run"}}}
	store := &memStore{}
	got, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 0 {
		t.Errorf("no sensitive fields → no prompt items, got %d", len(req.Items))
	}
	if got.Image != "img" {
		t.Errorf("filtered profile should be unchanged, got %+v", got)
	}
}

func TestFilterAllUserSensitiveNoPrompt(t *testing.T) {
	res := profile.Resolved{
		Profile: profile.Profile{Mounts: map[string]profile.Mount{"~/x": {Source: "~/x"}}},
		Prov: profile.Provenance{Mounts: map[string]profile.Contributor{
			"~/x": {FullName: "myagent", Namespace: ""},
		}},
		FullName: "myagent",
	}
	store := &memStore{}
	_, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 0 {
		t.Errorf("all-user sensitive fields → no prompt items, got %d", len(req.Items))
	}
}

func TestFilterCoreSensitiveProducesPrompt(t *testing.T) {
	res := profile.Resolved{
		Profile: profile.Profile{Mounts: map[string]profile.Mount{"~/.ssh": {Source: "~/.ssh"}}},
		Prov: profile.Provenance{Mounts: map[string]profile.Contributor{
			"~/.ssh": {FullName: "core/creds/ssh", Namespace: "core"},
		}},
		FullName: "myagent", DisplayName: "myagent",
	}
	store := &memStore{}
	_, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 1 || req.Items[0].Key != "~/.ssh" {
		t.Errorf("expected one prompt item for ~/.ssh, got %+v", req.Items)
	}
}

func TestFilterStoredApprovalNoPrompt(t *testing.T) {
	res := profile.Resolved{
		Profile: profile.Profile{Mounts: map[string]profile.Mount{"~/.ssh": {Source: "~/.ssh"}}},
		Prov: profile.Provenance{Mounts: map[string]profile.Contributor{
			"~/.ssh": {FullName: "core/creds/ssh", Namespace: "core"},
		}},
		FullName: "myagent",
	}
	h := ComputeApprovalHash(res)
	store := &memStore{state: map[string]State{
		"myagent": {Hash: h, Approved: map[string]ApprovedField{
			"mounts": {Keys: []string{"~/.ssh"}},
		}},
	}}
	_, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 0 {
		t.Errorf("stored approval should produce no prompt, got %d items", len(req.Items))
	}
}

func TestFilterDeniedKeyDroppedFromProfile(t *testing.T) {
	res := profile.Resolved{
		Profile: profile.Profile{Mounts: map[string]profile.Mount{
			"~/.ssh": {Source: "~/.ssh"},
			"~/aws":  {Source: "~/aws"},
		}},
		Prov: profile.Provenance{Mounts: map[string]profile.Contributor{
			"~/.ssh": {FullName: "core/creds/ssh", Namespace: "core"},
			"~/aws":  {FullName: "core/creds/aws", Namespace: "core"},
		}},
		FullName: "myagent",
	}
	h := ComputeApprovalHash(res)
	store := &memStore{state: map[string]State{
		"myagent": {Hash: h, Approved: map[string]ApprovedField{
			"mounts": {Keys: []string{"~/.ssh"}}, // ~/.ssh approved, ~/aws denied (absent)
		}},
	}}
	got, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 0 {
		t.Errorf("all keys have stored choices, got %d items", len(req.Items))
	}
	if _, ok := got.Mounts["~/aws"]; ok {
		t.Error("denied key ~/aws should be dropped from filtered profile")
	}
	if _, ok := got.Mounts["~/.ssh"]; !ok {
		t.Error("approved key ~/.ssh should remain")
	}
}

// memStore is an in-memory Store for tests.
type memStore struct {
	state map[string]State
}

func (m *memStore) Load(name string) (State, error) {
	if m.state == nil {
		m.state = map[string]State{}
	}
	return m.state[name], nil
}
func (m *memStore) Save(name string, s State) error {
	if m.state == nil {
		m.state = map[string]State{}
	}
	m.state[name] = s
	return nil
}

func TestFilterReconcilesAndPersists(t *testing.T) {
	res := profile.Resolved{
		Profile: profile.Profile{Mounts: map[string]profile.Mount{"~/.ssh": {Source: "~/.ssh"}}},
		Prov: profile.Provenance{Mounts: map[string]profile.Contributor{
			"~/.ssh": {FullName: "core/creds/ssh", Namespace: "core"},
		}},
		FullName: "myagent",
	}
	h := ComputeApprovalHash(res)
	// State has a stale key ~/aws that's no longer in the profile.
	store := &memStore{state: map[string]State{
		"myagent": {Hash: h, Approved: map[string]ApprovedField{
			"mounts": {Keys: []string{"~/.ssh", "~/aws"}},
		}},
	}}
	_, _, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	saved := store.state["myagent"]
	if containsKey(saved.Approved["mounts"].Keys, "~/aws") {
		t.Error("stale key ~/aws should be dropped from persisted state")
	}
}

func TestFilterCoarseServiceDenyCascadesMounts(t *testing.T) {
	res := profile.Resolved{
		Profile: profile.Profile{
			Services: map[string]profile.Service{
				"podman": {
					Image: "img", Command: []string{"run"},
					Exposes: map[string]string{"registry": "/run/podman.sock"},
				},
			},
			Mounts: map[string]profile.Mount{
				"/run/podman.sock": {Service: "podman", Socket: "registry"},
			},
		},
		Prov: profile.Provenance{
			Services: map[string]profile.Contributor{
				"podman": {FullName: "core/services/podman", Namespace: "core"},
			},
		},
		FullName: "myagent",
	}
	h := ComputeApprovalHash(res)
	// Deny the service: "services" field present in state, hash matches,
	// "podman" absent from Keys → denied → dropped from cfg.Services.
	store := &memStore{state: map[string]State{
		"myagent": {Hash: h, Approved: map[string]ApprovedField{
			"services": {Keys: nil}, // all services denied
		}},
	}}
	got, _, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if _, ok := got.Services["podman"]; ok {
		t.Error("denied service should be dropped from filtered Services")
	}
	if _, ok := got.Mounts["/run/podman.sock"]; ok {
		t.Error("dependent mount should be cascaded off when its service is denied")
	}
}

func TestFilterCoarseServiceApproveKeepsService(t *testing.T) {
	res := profile.Resolved{
		Profile: profile.Profile{
			Services: map[string]profile.Service{
				"podman": {
					Image: "img", Command: []string{"run"},
					Exposes: map[string]string{"registry": "/run/podman.sock"},
				},
			},
			Mounts: map[string]profile.Mount{
				"/run/podman.sock": {Service: "podman", Socket: "registry"},
			},
		},
		Prov: profile.Provenance{
			Services: map[string]profile.Contributor{
				"podman": {FullName: "core/services/podman", Namespace: "core"},
			},
		},
		FullName: "myagent",
	}
	h := ComputeApprovalHash(res)
	// Approve the service: "podman" in Keys → kept.
	store := &memStore{state: map[string]State{
		"myagent": {Hash: h, Approved: map[string]ApprovedField{
			"services": {Keys: []string{"podman"}},
		}},
	}}
	got, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 0 {
		t.Errorf("approved service should produce no prompt, got %d items", len(req.Items))
	}
	if _, ok := got.Services["podman"]; !ok {
		t.Error("approved service should remain in filtered Services")
	}
	if _, ok := got.Mounts["/run/podman.sock"]; !ok {
		t.Error("dependent mount should remain when its service is approved")
	}
}

func TestFilterCoarseServicePromptItemShape(t *testing.T) {
	res := profile.Resolved{
		Profile: profile.Profile{
			Services: map[string]profile.Service{
				"podman": {
					Image: "img", Command: []string{"run"}, Privileged: true,
					Exposes: map[string]string{"podman": "/run/podman/podman.sock"},
				},
			},
		},
		Prov: profile.Provenance{
			Services: map[string]profile.Contributor{
				"podman": {FullName: "core/services/podman", Namespace: "core"},
			},
		},
		FullName: "myagent",
	}
	store := &memStore{}
	_, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 1 {
		t.Fatalf("expected one prompt item for the service, got %d", len(req.Items))
	}
	it := req.Items[0]
	if it.Field != "services" {
		t.Errorf("item Field = %q, want \"services\"", it.Field)
	}
	if it.Key != "podman" {
		t.Errorf("item Key = %q, want \"podman\"", it.Key)
	}
	if it.Value != "podman: "+renderServiceDefinition(res.Services["podman"]) {
		t.Errorf("item Value = %q, want service name prefix plus rendered definition", it.Value)
	}
}

func TestEphemeralStoreDoesNotPersist(t *testing.T) {
	base := &memStore{state: map[string]State{}}
	eph := NewEphemeralStore(base, State{
		Hash:     "h",
		Approved: map[string]ApprovedField{"mounts": {Keys: []string{"~/.ssh"}}},
	})
	// Load returns the ephemeral overlay.
	got, _ := eph.Load("any")
	if got.Hash != "h" {
		t.Errorf("ephemeral Load should return overlay, got %+v", got)
	}
	// Save is a no-op (does not write to base).
	_ = eph.Save("any", State{Hash: "new"})
	if base.state["any"].Hash == "new" {
		t.Error("ephemeral Save should not persist to base store")
	}
}

func TestReadOnlyStoreDelegatesLoadButNotSave(t *testing.T) {
	base := &memStore{state: map[string]State{"p": {Hash: "h"}}}
	ro := NewReadOnlyStore(base)
	got, err := ro.Load("p")
	if err != nil || got.Hash != "h" {
		t.Fatalf("Load should delegate to base, got %+v err=%v", got, err)
	}
	_ = ro.Save("p", State{Hash: "other"})
	if base.state["p"].Hash != "h" {
		t.Error("Save should not persist to base store")
	}
}

func TestFilterNetworkScalar(t *testing.T) {
	core := profile.Contributor{FullName: "core/net", Namespace: "core"}
	res := profile.Resolved{
		Profile:  profile.Profile{Network: "slirp4netns"},
		Prov:     profile.Provenance{Network: core},
		FullName: "myagent",
	}
	h := ComputeApprovalHash(res)

	// (a) stored network:true at matching hash → kept, no prompt.
	store := &memStore{state: map[string]State{
		"myagent": {Hash: h, Approved: map[string]ApprovedField{
			"network": {Network: boolPtr(true)},
		}},
	}}
	got, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if got.Network != "slirp4netns" {
		t.Errorf("approved network = %q, want %q", got.Network, "slirp4netns")
	}
	if len(req.Items) != 0 {
		t.Errorf("approved network should produce no prompt, got %d items", len(req.Items))
	}

	// (b) stored network:false → dropped (value ""), no prompt.
	store = &memStore{state: map[string]State{
		"myagent": {Hash: h, Approved: map[string]ApprovedField{
			"network": {Network: boolPtr(false)},
		}},
	}}
	got, req, err = Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if got.Network != "" {
		t.Errorf("denied network should be dropped, got %q", got.Network)
	}
	if len(req.Items) != 0 {
		t.Errorf("denied network should produce no prompt, got %d items", len(req.Items))
	}

	// (c) no stored network → prompt item with empty Key.
	store = &memStore{}
	got, req, err = Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 1 {
		t.Fatalf("unapproved network should produce one prompt item, got %d", len(req.Items))
	}
	it := req.Items[0]
	if it.Field != "network" || it.Key != "" {
		t.Errorf("item = %+v, want field network with empty key", it)
	}
	if got.Network != "slirp4netns" {
		t.Errorf("network should remain while prompting, got %q", got.Network)
	}
}

func TestFilterDbusTalkAndOwn(t *testing.T) {
	core := profile.Contributor{FullName: "core/dbus", Namespace: "core"}
	res := profile.Resolved{
		Profile: profile.Profile{Dbus: &profile.DbusConfig{
			Talk: map[string]*struct{}{
				"org.freedesktop.portal.Desktop": {},
				"org.freedesktop.secrets":        {},
			},
			Own: map[string]*struct{}{
				"org.freedesktop.MyApp": {},
				"org.example.Foo":       {},
			},
		}},
		Prov: profile.Provenance{Dbus: profile.DbusProvenance{
			Talk: map[string]profile.Contributor{
				"org.freedesktop.portal.Desktop": core,
				"org.freedesktop.secrets":        core,
			},
			Own: map[string]profile.Contributor{
				"org.freedesktop.MyApp": core,
				"org.example.Foo":       core,
			},
		}},
		FullName: "myagent",
	}
	h := ComputeApprovalHash(res)

	// No stored state → one prompt item per dbus key under dbus.talk/dbus.own.
	store := &memStore{}
	_, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 4 {
		t.Fatalf("unapproved dbus should produce 4 prompt items, got %d", len(req.Items))
	}
	for _, it := range req.Items {
		if it.Field != "dbus.talk" && it.Field != "dbus.own" {
			t.Errorf("item field = %q, want dbus.talk or dbus.own", it.Field)
		}
	}

	// Stored: approve one talk and one own, deny the rest → no prompt,
	// approved keys kept, denied keys dropped.
	store = &memStore{state: map[string]State{
		"myagent": {Hash: h, Approved: map[string]ApprovedField{
			"dbus.talk": {Keys: []string{"org.freedesktop.portal.Desktop"}},
			"dbus.own":  {Keys: []string{"org.freedesktop.MyApp"}},
		}},
	}}
	got, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 0 {
		t.Errorf("all dbus keys have stored choices, got %d items", len(req.Items))
	}
	if got.Dbus == nil {
		t.Fatal("filtered profile lost its dbus config")
	}
	if _, ok := got.Dbus.Talk["org.freedesktop.portal.Desktop"]; !ok {
		t.Error("approved dbus talk key should remain")
	}
	if _, ok := got.Dbus.Talk["org.freedesktop.secrets"]; ok {
		t.Error("denied dbus talk key should be dropped")
	}
	if _, ok := got.Dbus.Own["org.freedesktop.MyApp"]; !ok {
		t.Error("approved dbus own key should remain")
	}
	if _, ok := got.Dbus.Own["org.example.Foo"]; ok {
		t.Error("denied dbus own key should be dropped")
	}
}

func TestFilterPromptItemLabels(t *testing.T) {
	core := profile.Contributor{FullName: "core/gui", Namespace: "core"}
	res := profile.Resolved{
		Profile: profile.Profile{
			Mounts: map[string]profile.Mount{
				"~/.gitconfig":     {Source: "~/.gitconfig", ReadOnly: true},
				"~/code":           {Source: "~/code", ReadOnly: false},
				"~/.mise":          {Source: "/custom/mise", ReadOnly: true},
				"/run/podman.sock": {Service: "podman", Socket: "podman"},
			},
			Devices: map[string]profile.DeviceBind{
				"/dev/kvm":  {Source: "/dev/kvm", Permissions: "rwm"},
				"/dev/null": {Source: "/dev/null", Permissions: "ro"},
			},
			Env: map[string]string{
				"DOCKER_HOST": "unix:///var/run/docker.sock",
			},
			Ports: map[string]profile.PortBind{
				"8080": {Host: "8080", HostIP: "127.0.0.1", Protocol: "tcp"},
				"53":   {Host: "0", HostIP: "0.0.0.0", Protocol: "udp"},
			},
			Network: "host",
			Dbus: &profile.DbusConfig{
				Talk: map[string]*struct{}{"org.freedesktop.portal.Desktop": {}},
				Own:  map[string]*struct{}{"org.freedesktop.secrets": {}},
			},
		},
		Prov: profile.Provenance{
			Mounts: map[string]profile.Contributor{
				"~/.gitconfig":     core,
				"~/code":           core,
				"~/.mise":          core,
				"/run/podman.sock": core,
			},
			Devices: map[string]profile.Contributor{
				"/dev/kvm":  core,
				"/dev/null": core,
			},
			Env: map[string]profile.Contributor{"DOCKER_HOST": core},
			Ports: map[string]profile.Contributor{
				"8080": core,
				"53":   core,
			},
			Network: core,
			Dbus: profile.DbusProvenance{
				Talk: map[string]profile.Contributor{"org.freedesktop.portal.Desktop": core},
				Own:  map[string]profile.Contributor{"org.freedesktop.secrets": core},
			},
		},
		FullName: "myagent",
	}
	store := &memStore{}
	_, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	byItem := map[string]string{}
	for _, it := range req.Items {
		byItem[it.Field+"\x00"+it.Key] = it.Value
	}
	want := map[string]string{
		"mounts\x00~/.gitconfig":                      "~/.gitconfig",
		"mounts\x00~/code":                            "~/code (rw)",
		"mounts\x00~/.mise":                           "/custom/mise",
		"mounts\x00/run/podman.sock":                  "/run/podman.sock (via service podman)",
		"devices\x00/dev/kvm":                         "/dev/kvm",
		"devices\x00/dev/null":                        "/dev/null (ro)",
		"env\x00DOCKER_HOST":                          "DOCKER_HOST=unix:///var/run/docker.sock",
		"ports\x008080":                               "8080 → 127.0.0.1:8080",
		"ports\x0053":                                 "53 → auto/udp",
		"network\x00":                                 "host",
		"dbus.talk\x00org.freedesktop.portal.Desktop": "org.freedesktop.portal.Desktop",
		"dbus.own\x00org.freedesktop.secrets":         "org.freedesktop.secrets",
	}
	for itemKey, wantVal := range want {
		gotVal, ok := byItem[itemKey]
		if !ok {
			t.Errorf("no prompt item for %q", itemKey)
			continue
		}
		if gotVal != wantVal {
			t.Errorf("item %q label = %q, want %q", itemKey, gotVal, wantVal)
		}
	}
}

func TestFilterMarksPriorApprovedOnHashChange(t *testing.T) {
	core := profile.Contributor{FullName: "core/creds/ssh", Namespace: "core"}
	res := profile.Resolved{
		Profile: profile.Profile{Mounts: map[string]profile.Mount{
			"~/.ssh": {Source: "~/.ssh"},
			"~/aws":  {Source: "~/aws"},
		}},
		Prov: profile.Provenance{Mounts: map[string]profile.Contributor{
			"~/.ssh": core,
			"~/aws":  core,
		}},
		FullName: "myagent",
	}
	// Stored state approved only ~/.ssh under an older hash (the profile
	// gained ~/aws since). The hash differs, so both keys re-prompt; the
	// previously approved one must be marked PriorApproved, the new one not.
	store := &memStore{state: map[string]State{
		"myagent": {Hash: "deadbeef", Approved: map[string]ApprovedField{
			"mounts": {Keys: []string{"~/.ssh"}},
		}},
	}}
	_, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 2 {
		t.Fatalf("expected 2 prompt items on hash change, got %d", len(req.Items))
	}
	byKey := map[string]bool{}
	for _, it := range req.Items {
		byKey[it.Key] = it.PriorApproved
	}
	if !byKey["~/.ssh"] {
		t.Error("previously approved key ~/.ssh should be marked PriorApproved")
	}
	if byKey["~/aws"] {
		t.Error("newly introduced key ~/aws should not be marked PriorApproved")
	}
}

func TestFilterMarksNetworkPriorApproved(t *testing.T) {
	core := profile.Contributor{FullName: "core/net", Namespace: "core"}
	res := profile.Resolved{
		Profile:  profile.Profile{Network: "slirp4netns"},
		Prov:     profile.Provenance{Network: core},
		FullName: "myagent",
	}
	// Network approved under an older hash → re-prompt with PriorApproved.
	store := &memStore{state: map[string]State{
		"myagent": {Hash: "old", Approved: map[string]ApprovedField{
			"network": {Network: boolPtr(true)},
		}},
	}}
	_, req, err := Filter(res, store)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 1 {
		t.Fatalf("expected 1 network prompt item on hash change, got %d", len(req.Items))
	}
	if !req.Items[0].PriorApproved {
		t.Error("previously approved network should be marked PriorApproved")
	}
}

func TestFilterNewItemsNotPriorApprovedOnFirstRun(t *testing.T) {
	core := profile.Contributor{FullName: "core/creds/ssh", Namespace: "core"}
	res := profile.Resolved{
		Profile: profile.Profile{Mounts: map[string]profile.Mount{"~/.ssh": {Source: "~/.ssh"}}},
		Prov: profile.Provenance{Mounts: map[string]profile.Contributor{
			"~/.ssh": core,
		}},
		FullName: "myagent",
	}
	// No stored state at all → the key is new, not prior-approved.
	_, req, err := Filter(res, &memStore{})
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(req.Items) != 1 {
		t.Fatalf("expected 1 prompt item, got %d", len(req.Items))
	}
	if req.Items[0].PriorApproved {
		t.Error("a key with no stored decision must not be marked PriorApproved")
	}
}
