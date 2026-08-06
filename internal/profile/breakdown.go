package profile

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProvenanceYAML renders the resolved profile as one section per chain entry,
// in chain (pre-)order. Each section shows the entry's own declared extends
// plus only the keys it owns in the final merge. Sections are diagnostic
// output, not a single parseable YAML document. yaml.v3 sorts map keys, so
// key order within a section is deterministic.
func (r Resolved) ProvenanceYAML() (string, error) {
	var b strings.Builder
	for i, e := range r.Chain {
		sec := r.ownedSection(e.FullName, e.Extends)
		data, err := yaml.Marshal(sec)
		if err != nil {
			return "", err
		}
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "# %s  (%s)\n", e.FullName, e.Path)
		if len(sec) > 0 {
			b.Write(data)
		}
	}
	return b.String(), nil
}

// ownedSection returns the YAML body for one chain entry: its own extends and
// the keys attributed to it (values taken from the merged profile).
func (r Resolved) ownedSection(fullName string, extends []string) map[string]any {
	sec := map[string]any{}
	if len(extends) > 0 {
		sec["extends"] = extends
	}
	owned := func(prov map[string]Contributor, vals map[string]any) map[string]any {
		out := map[string]any{}
		for k, c := range prov {
			if c.FullName == fullName {
				out[k] = vals[k]
			}
		}
		return out
	}
	if m := owned(r.Prov.Tools, asAnyMap(r.Tools)); len(m) > 0 {
		sec["tools"] = m
	}
	if m := owned(r.Prov.Caches, asAnyMap(r.Caches)); len(m) > 0 {
		sec["caches"] = m
	}
	if m := owned(r.Prov.Repos, asAnyMap(r.Repos)); len(m) > 0 {
		sec["repos"] = m
	}
	if m := owned(r.Prov.Files, asAnyMap(r.Files)); len(m) > 0 {
		sec["files"] = m
	}
	if m := owned(r.Prov.Labels, asAnyMap(r.Labels)); len(m) > 0 {
		sec["labels"] = m
	}
	pkgs := map[string]bool{}
	for p, c := range r.Prov.Packages {
		if c.FullName == fullName {
			pkgs[p] = true
		}
	}
	if len(pkgs) > 0 {
		sec["packages"] = sortedKeys(pkgs)
	}
	if m := owned(r.Prov.Mounts, asAnyMap(r.Mounts)); len(m) > 0 {
		sec["mounts"] = m
	}
	if m := owned(r.Prov.Env, asAnyMap(r.Env)); len(m) > 0 {
		sec["environment"] = m
	}
	if m := owned(r.Prov.Ports, asAnyMap(r.Ports)); len(m) > 0 {
		sec["ports"] = m
	}
	if m := owned(r.Prov.Devices, asAnyMap(r.Devices)); len(m) > 0 {
		sec["devices"] = m
	}
	if m := owned(r.Prov.Services, asAnyMap(r.Services)); len(m) > 0 {
		sec["services"] = m
	}
	if r.Dbus != nil {
		dbus := map[string]any{}
		if m := owned(r.Prov.Dbus.Talk, asAnyMap(r.Dbus.Talk)); len(m) > 0 {
			dbus["talk"] = m
		}
		if m := owned(r.Prov.Dbus.Own, asAnyMap(r.Dbus.Own)); len(m) > 0 {
			dbus["own"] = m
		}
		if len(dbus) > 0 {
			sec["dbus"] = dbus
		}
	}
	if r.Prov.Image.FullName == fullName {
		sec["image"] = r.Image
	}
	if r.Prov.Command.FullName == fullName {
		sec["command"] = r.Command
	}
	if r.Prov.TTY.FullName == fullName {
		sec["tty"] = r.TTY
	}
	if r.Prov.Network.FullName == fullName {
		sec["network"] = r.Network
	}
	if r.Resources != nil && (r.Prov.Resources.Memory.FullName == fullName || r.Prov.Resources.CPUs.FullName == fullName) {
		rc := map[string]any{}
		if r.Prov.Resources.Memory.FullName == fullName {
			rc["memory"] = r.Resources.Memory
		}
		if r.Prov.Resources.CPUs.FullName == fullName {
			rc["cpus"] = r.Resources.CPUs
		}
		sec["resources"] = rc
	}
	return sec
}

// asAnyMap widens a typed map to map[string]any for the owned() helper.
func asAnyMap[V any](m map[string]V) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
