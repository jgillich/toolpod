package profile

// Contributor identifies a catalog entry that contributed a sensitive
// value. Stored in provenance so the approval filter can decide trust
// without access to the catalog: a user entry (Namespace == "") is
// trusted and not gated; a core or remote-namespace entry is gated.
type Contributor struct {
	FullName  string
	Namespace string
}

// Trusted reports whether this contributor is user-owned and therefore
// not subject to the approval gate.
func (c Contributor) Trusted() bool { return c.Namespace == "" }

// Provenance records, for each sensitive key, the Contributor that last
// wrote it. Keys whose final value came from a user entry are not gated;
// keys from a core/remote entry are.
type Provenance struct {
	Mounts   map[string]Contributor
	Devices  map[string]Contributor
	Env      map[string]Contributor
	Ports    map[string]Contributor
	Dbus     DbusProvenance
	Network  Contributor
	Services map[string]Contributor
}

type DbusProvenance struct {
	Talk map[string]Contributor
	Own  map[string]Contributor
}

// initProvenance stamps the rc's own Contributor onto every sensitive key
// rc declares. Used for leaf profiles (no extends) so a built-in leaf
// like core/bash does not bypass the gate with empty provenance.
func initProvenance(rc RawProfile) Provenance {
	c := Contributor{FullName: rc.FullName(), Namespace: rc.Namespace}
	prov := Provenance{}
	if len(rc.Mounts) > 0 {
		prov.Mounts = make(map[string]Contributor, len(rc.Mounts))
		for k := range rc.Mounts {
			prov.Mounts[k] = c
		}
	}
	if len(rc.Devices) > 0 {
		prov.Devices = make(map[string]Contributor, len(rc.Devices))
		for k := range rc.Devices {
			prov.Devices[k] = c
		}
	}
	if len(rc.Env) > 0 {
		prov.Env = make(map[string]Contributor, len(rc.Env))
		for k := range rc.Env {
			prov.Env[k] = c
		}
	}
	if len(rc.Ports) > 0 {
		prov.Ports = make(map[string]Contributor, len(rc.Ports))
		for k := range rc.Ports {
			prov.Ports[k] = c
		}
	}
	if rc.Dbus != nil {
		if len(rc.Dbus.Talk) > 0 {
			prov.Dbus.Talk = make(map[string]Contributor, len(rc.Dbus.Talk))
			for k := range rc.Dbus.Talk {
				prov.Dbus.Talk[k] = c
			}
		}
		if len(rc.Dbus.Own) > 0 {
			prov.Dbus.Own = make(map[string]Contributor, len(rc.Dbus.Own))
			for k := range rc.Dbus.Own {
				prov.Dbus.Own[k] = c
			}
		}
	}
	if rc.Network != "" {
		prov.Network = c
	}
	if len(rc.Services) > 0 {
		prov.Services = make(map[string]Contributor, len(rc.Services))
		for k := range rc.Services {
			prov.Services[k] = c
		}
	}
	return prov
}
