package approval

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/jgillich/tpd/internal/profile"
)

// ComputeApprovalHash returns a 12-hex-char hash of the non-user
// gated fields of res, pre-template-expansion. Contributor identity
// (FullName + Namespace) is included so approvals don't silently
// transfer across contributors with identical field values.
func ComputeApprovalHash(res profile.Resolved) string {
	h := sha256.New()
	emit := func(field, key string, c profile.Contributor, value string) {
		if c.Trusted() {
			return
		}
		fmt.Fprintf(h, "%s\n%s\n%s\n%s\n%s\n", field, key, c.FullName, c.Namespace, value)
	}

	for _, k := range sortedKeys(res.Mounts) {
		m := res.Mounts[k]
		c := res.Prov.Mounts[k]
		emit("mounts", k, c, fmt.Sprintf("mount %s %s %s %s %v %v %v", k, m.Source, m.Service, m.Socket, m.ReadOnly, m.Optional, m.Create))
	}
	for _, k := range sortedKeys(res.Devices) {
		d := res.Devices[k]
		c := res.Prov.Devices[k]
		emit("devices", k, c, fmt.Sprintf("device %s %s %s %v", k, d.Source, d.Permissions, d.Cgroup))
	}
	for _, k := range sortedKeys(res.Env) {
		c := res.Prov.Env[k]
		emit("env", k, c, fmt.Sprintf("env %s %s", k, res.Env[k]))
	}
	for _, k := range sortedKeys(res.Ports) {
		p := res.Ports[k]
		c := res.Prov.Ports[k]
		emit("ports", k, c, fmt.Sprintf("port %s %s %s %s", k, p.Host, p.HostIP, p.Protocol))
	}
	for _, k := range sortedKeys(res.Prov.Dbus.Talk) {
		emit("dbus.talk", k, res.Prov.Dbus.Talk[k], "talk")
	}
	for _, k := range sortedKeys(res.Prov.Dbus.Own) {
		emit("dbus.own", k, res.Prov.Dbus.Own[k], "own")
	}
	if res.Network != "" && !res.Prov.Network.Trusted() {
		emit("network", "", res.Prov.Network, res.Network)
	}
	for _, svcName := range sortedKeys(res.Services) {
		svc := res.Services[svcName]
		c := res.Prov.Services[svcName]
		if c.Trusted() {
			continue
		}
		fmt.Fprintf(h, "services\n%s\n%s\n%s\n%s\n", svcName, c.FullName, c.Namespace, renderServiceDefinition(svc))
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:])[:12]
}

// renderServiceDefinition builds a deterministic, sorted canonical string of
// the service's schema-valid gated sub-fields (privileged, exposes,
// the service's own mounts, env) — the shape the user is asked to approve
// under "use podman". A change to any of these (privileged flip, new expose
// socket, service-mount read_only flip, new service mount/env key) changes
// the rendered form and therefore the hash, re-prompting with the prior
// choice pre-checked. Devices, ports, dbus, and network never occur on
// services (validateServices rejects them) and are not rendered.
func renderServiceDefinition(svc profile.Service) string {
	var b strings.Builder
	fmt.Fprintf(&b, "privileged=%v;", svc.Privileged)
	exposes := sortedKeys(svc.Exposes)
	expParts := make([]string, 0, len(exposes))
	for _, k := range exposes {
		expParts = append(expParts, k+"="+svc.Exposes[k])
	}
	fmt.Fprintf(&b, "exposes={%s};", strings.Join(expParts, ","))
	mountKeys := sortedKeys(svc.Mounts)
	mntParts := make([]string, 0, len(mountKeys))
	for _, k := range mountKeys {
		m := svc.Mounts[k]
		mntParts = append(mntParts, fmt.Sprintf("%s=%s;ro=%v;opt=%v;create=%v", k, m.Source, m.ReadOnly, m.Optional, m.Create))
	}
	fmt.Fprintf(&b, "mounts={%s};", strings.Join(mntParts, ","))
	envKeys := sortedKeys(svc.Env)
	envParts := make([]string, 0, len(envKeys))
	for _, k := range envKeys {
		envParts = append(envParts, k+"="+svc.Env[k])
	}
	fmt.Fprintf(&b, "env={%s}", strings.Join(envParts, ","))
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
