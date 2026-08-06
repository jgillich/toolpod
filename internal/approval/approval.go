package approval

import (
	"fmt"

	"github.com/jgillich/tpd/internal/profile"
)

// SensitiveItem is one gated key the user must decide on.
type SensitiveItem struct {
	Field  string
	Key    string
	Value  string
	Source profile.Contributor
}

// PromptRequest is what the dialog renders. Empty Items = no prompt.
type PromptRequest struct {
	ProfileName string
	FullName    string
	Hash        string
	Items       []SensitiveItem
}

// Filter returns the profile with denied/dropped fields removed and a
// PromptRequest describing any still-unapproved sensitive fields.
func Filter(res profile.Resolved, store Store) (profile.Profile, PromptRequest, error) {
	hash := ComputeApprovalHash(res)
	st, err := store.Load(res.FullName)
	if err != nil {
		return profile.Profile{}, PromptRequest{}, err
	}
	// Reconcile: if hash matches, drop stored keys that no longer exist
	// in the profile. Persist if the state changed.
	reconciled, changed := reconcileState(st, hash, res)
	if changed {
		if err := store.Save(res.FullName, reconciled); err != nil {
			return profile.Profile{}, PromptRequest{}, err
		}
	}

	filtered := res.Profile
	req := PromptRequest{
		ProfileName: res.DisplayName,
		FullName:    res.FullName,
		Hash:        hash,
		Items:       nil,
	}

	// Walk sensitive fields; drop denied, collect unapproved into Items.
	filtered.Mounts, req = applyMountField(filtered.Mounts, res.Prov.Mounts, "mounts", reconciled, req)
	filtered.Devices, req = applyDeviceField(filtered.Devices, res.Prov.Devices, "devices", reconciled, req)
	filtered.Env, req = applyEnvField(filtered.Env, res.Prov.Env, "env", reconciled, req)
	filtered.Ports, req = applyPortField(filtered.Ports, res.Prov.Ports, "ports", reconciled, req)
	filtered.Dbus, req = applyDbusField(filtered.Dbus, res.Prov.Dbus, reconciled, req)
	filtered.Network, req = applyNetworkField(filtered.Network, res.Prov.Network, "network", reconciled, req)
	filtered.Services, req = applyServicesField(filtered.Services, res.Prov.Services, reconciled, req)

	// Dependent-mount cascade: drop top-level mounts whose service was
	// denied (absent from filtered.Services).
	filtered.Mounts = cascadeDependentMounts(filtered.Mounts, filtered.Services)

	return filtered, req, nil
}

// reconcileState drops stored keys that no longer exist in res when the
// stored hash matches the current hash. Returns the reconciled state and
// whether it changed. Network is a scalar stored in ApprovedField.Network;
// reconcileKeys skips it and it is handled inline. Services is a flat map
// field (Keys = service names) and reconciles like mounts/env/ports.
func reconcileState(st State, hash string, res profile.Resolved) (State, bool) {
	if st.Hash != hash {
		return st, false
	}
	changed := false
	approve := st.Approved
	maybeSet := func(field string, af ApprovedField, ch bool) {
		if ch {
			approve[field] = af
			changed = true
		}
	}

	if af, ok := approve["mounts"]; ok {
		n, ch := reconcileKeys(af, res.Mounts)
		maybeSet("mounts", n, ch)
	}
	if af, ok := approve["devices"]; ok {
		n, ch := reconcileKeys(af, res.Devices)
		maybeSet("devices", n, ch)
	}
	if af, ok := approve["env"]; ok {
		n, ch := reconcileKeys(af, res.Env)
		maybeSet("env", n, ch)
	}
	if af, ok := approve["ports"]; ok {
		n, ch := reconcileKeys(af, res.Ports)
		maybeSet("ports", n, ch)
	}
	if af, ok := approve["dbus.talk"]; ok {
		talk := map[string]struct{}{}
		if res.Dbus != nil {
			for k := range res.Dbus.Talk {
				talk[k] = struct{}{}
			}
		}
		n, ch := reconcileKeys(af, talk)
		maybeSet("dbus.talk", n, ch)
	}
	if af, ok := approve["dbus.own"]; ok {
		own := map[string]struct{}{}
		if res.Dbus != nil {
			for k := range res.Dbus.Own {
				own[k] = struct{}{}
			}
		}
		n, ch := reconcileKeys(af, own)
		maybeSet("dbus.own", n, ch)
	}
	// network is a scalar stored in ApprovedField.Network; nothing to
	// reconcile by key. It is dropped only when the hash changes.
	if af, ok := approve["services"]; ok {
		n, ch := reconcileKeys(af, res.Services)
		maybeSet("services", n, ch)
	}
	st.Approved = approve
	return st, changed
}

// reconcileKeys drops af.Keys entries that are not present in current and
// reports whether anything was dropped. The Network scalar slot is skipped
// (handled separately).
func reconcileKeys[V any](af ApprovedField, current map[string]V) (ApprovedField, bool) {
	if af.Network != nil {
		return af, false
	}
	currentKeys := map[string]bool{}
	for k := range current {
		currentKeys[k] = true
	}
	kept := af.Keys[:0]
	changed := false
	for _, k := range af.Keys {
		if currentKeys[k] {
			kept = append(kept, k)
		} else {
			changed = true
		}
	}
	af.Keys = kept
	return af, changed
}

// cascadeDependentMounts drops top-level mounts referencing a service that
// was denied (absent from the filtered services map). A denied service is
// gone from cfg.Services, so its socket mounts must also be dropped —
// validateMountServices (validate.go:374-382) would otherwise fail at
// service binding, and the main container cannot bind a socket the service
// isn't exposing to it. A kept service keeps all its exposes intact, so its
// socket mounts survive.
func cascadeDependentMounts(mounts map[string]profile.Mount, services map[string]profile.Service) map[string]profile.Mount {
	for target, m := range mounts {
		if m.Service == "" {
			continue
		}
		if _, ok := services[m.Service]; !ok {
			delete(mounts, target)
		}
	}
	return mounts
}

// decide is the shared keep/drop/prompt decision for one non-user key.
// Returns keep=true if the key should remain in the filtered profile, and
// appends a SensitiveItem to req.Items when the user must still decide.
//
// NOTE: a missing provenance entry (c is the zero value, Trusted()) is
// treated as user-trusted and skips the gate. This is deliberate fail-open
// — a provenance bug must not brick launches — but it means a provenance
// regression in the merge would silently bypass the approval gate. The
// provenance unit tests in internal/profile guard against that.
func decide(field, key, value string, c profile.Contributor, st State, req PromptRequest) (bool, PromptRequest) {
	if c.Trusted() {
		return true, req
	}
	// Hash mismatch or no stored state for this field → prompt.
	af, hasField := st.Approved[field]
	if st.Hash != req.Hash || !hasField {
		req.Items = append(req.Items, item(field, key, value, c))
		return true, req
	}
	if containsKey(af.Keys, key) {
		return true, req
	}
	return false, req
}

func applyMountField(mounts map[string]profile.Mount, prov map[string]profile.Contributor, field string, st State, req PromptRequest) (map[string]profile.Mount, PromptRequest) {
	out := make(map[string]profile.Mount, len(mounts))
	for k, v := range mounts {
		c := prov[k]
		keep, r := decide(field, k, renderMount(k, v), c, st, req)
		req = r
		if keep {
			out[k] = v
		}
	}
	return out, req
}

func applyDeviceField(devices map[string]profile.DeviceBind, prov map[string]profile.Contributor, field string, st State, req PromptRequest) (map[string]profile.DeviceBind, PromptRequest) {
	out := make(map[string]profile.DeviceBind, len(devices))
	for k, v := range devices {
		c := prov[k]
		keep, r := decide(field, k, renderDevice(k, v), c, st, req)
		req = r
		if keep {
			out[k] = v
		}
	}
	return out, req
}

func applyEnvField(env map[string]string, prov map[string]profile.Contributor, field string, st State, req PromptRequest) (map[string]string, PromptRequest) {
	out := make(map[string]string, len(env))
	for k, v := range env {
		c := prov[k]
		keep, r := decide(field, k, renderEnv(k, v), c, st, req)
		req = r
		if keep {
			out[k] = v
		}
	}
	return out, req
}

func applyPortField(ports map[string]profile.PortBind, prov map[string]profile.Contributor, field string, st State, req PromptRequest) (map[string]profile.PortBind, PromptRequest) {
	out := make(map[string]profile.PortBind, len(ports))
	for k, v := range ports {
		c := prov[k]
		keep, r := decide(field, k, renderPort(k, v), c, st, req)
		req = r
		if keep {
			out[k] = v
		}
	}
	return out, req
}

func applyDbusField(d *profile.DbusConfig, prov profile.DbusProvenance, st State, req PromptRequest) (*profile.DbusConfig, PromptRequest) {
	if d == nil {
		return d, req
	}
	out := &profile.DbusConfig{}
	if len(d.Talk) > 0 {
		out.Talk = make(map[string]*struct{}, len(d.Talk))
		for k, v := range d.Talk {
			c := prov.Talk[k]
			keep, r := decide("dbus.talk", k, k, c, st, req)
			req = r
			if keep {
				out.Talk[k] = v
			}
		}
	}
	if len(d.Own) > 0 {
		out.Own = make(map[string]*struct{}, len(d.Own))
		for k, v := range d.Own {
			c := prov.Own[k]
			keep, r := decide("dbus.own", k, k, c, st, req)
			req = r
			if keep {
				out.Own[k] = v
			}
		}
	}
	if len(out.Talk) == 0 && len(out.Own) == 0 {
		return nil, req
	}
	return out, req
}

// applyNetworkField gates the scalar network value. The stored choice is
// kept in ApprovedField.Network (*bool): nil → prompt, true → keep, false →
// drop (set to ""). The item key for network is the empty string.
func applyNetworkField(v string, c profile.Contributor, field string, st State, req PromptRequest) (string, PromptRequest) {
	if v == "" || c.Trusted() {
		return v, req
	}
	af, hasField := st.Approved[field]
	if st.Hash == req.Hash && hasField && af.Network != nil {
		if *af.Network {
			return v, req
		}
		return "", req
	}
	req.Items = append(req.Items, item(field, "", v, c))
	return v, req
}

// applyServicesField gates each service as a single coarse item under the
// service's one contributor. Field is "services", Key = service name, Value =
// the rendered service definition (privileged, exposes, mounts, env). A
// trusted contributor (or a missing one) keeps the service ungated. A denied
// service is dropped from the output map, and the cascade step in Filter then
// drops top-level mounts referencing it. A kept service keeps its full
// definition — the shared daemon is never filtered.
func applyServicesField(services map[string]profile.Service, prov map[string]profile.Contributor, st State, req PromptRequest) (map[string]profile.Service, PromptRequest) {
	out := make(map[string]profile.Service, len(services))
	for name, svc := range services {
		c := prov[name]
		keep, r := decide("services", name, name+": "+renderServiceDefinition(svc), c, st, req)
		req = r
		if keep {
			out[name] = svc
		}
	}
	return out, req
}

func item(field, key, value string, source profile.Contributor) SensitiveItem {
	return SensitiveItem{Field: field, Key: key, Value: value, Source: source}
}

// renderMount renders the dialog label for one mount. The permission the user
// grants is exposing a host path to the container, so the label is the host
// source (the container target only appears when a service provides the
// socket). A writable bind mount is flagged rw; read-only is the default. The
// field is provided by the section title.
func renderMount(k string, m profile.Mount) string {
	if m.Service != "" {
		return fmt.Sprintf("%s (via service %s)", k, m.Service)
	}
	label := m.Source
	if label == "" {
		label = k
	}
	if !m.ReadOnly {
		return fmt.Sprintf("%s (rw)", label)
	}
	return label
}

// renderDevice renders the dialog label for one device: the container path
// plus permissions only when they differ from the rwm default.
func renderDevice(k string, d profile.DeviceBind) string {
	if d.Permissions != "" && d.Permissions != "rwm" {
		return fmt.Sprintf("%s (%s)", k, d.Permissions)
	}
	return k
}

// renderEnv renders the dialog label for one environment variable.
func renderEnv(k, v string) string {
	return fmt.Sprintf("%s=%s", k, v)
}

// renderPort renders the dialog label for one port: container port → host
// binding, with the protocol when it isn't the tcp default. An empty or zero
// Host means the host port is auto-allocated at launch.
func renderPort(k string, p profile.PortBind) string {
	host := p.Host
	if host == "" || host == "0" {
		host = "auto"
	} else if p.HostIP != "" {
		host = p.HostIP + ":" + host
	}
	label := fmt.Sprintf("%s → %s", k, host)
	if p.Protocol != "" && p.Protocol != "tcp" {
		label += "/" + p.Protocol
	}
	return label
}

func containsKey(keys []string, k string) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}

// EphemeralStore wraps a base Store with an in-memory overlay. Load
// returns the overlay (ignoring the base); Save is a no-op. Used by the
// --dry-run --yes/--no path so the re-filter sees the choice without
// persisting.
type EphemeralStore struct {
	base    Store
	overlay State
}

func NewEphemeralStore(base Store, overlay State) *EphemeralStore {
	return &EphemeralStore{base: base, overlay: overlay}
}

func (e *EphemeralStore) Load(string) (State, error) { return e.overlay, nil }
func (e *EphemeralStore) Save(string, State) error   { return nil }

// ReadOnlyStore wraps a base Store: Load delegates to the base; Save is a
// no-op. Used for the whole --dry-run flow so the initial Filter can read
// stored approvals (an approved profile must not prompt) but a
// reconciliation write-back never touches disk.
type ReadOnlyStore struct {
	base Store
}

func NewReadOnlyStore(base Store) *ReadOnlyStore {
	return &ReadOnlyStore{base: base}
}

func (r *ReadOnlyStore) Load(name string) (State, error) { return r.base.Load(name) }
func (r *ReadOnlyStore) Save(string, State) error        { return nil }
