package profile

import "errors"

// Resolve walks the extends chain for name and produces a fully merged Profile.
// Cycles are detected and rejected. Validation runs on the result.
func ResolveProfile(cat Catalog, name string) (Profile, error) {
	res, err := ResolveProfileWithProv(cat, name)
	if err != nil {
		return Profile{}, err
	}
	return res.Profile, nil
}

// ResolveProfileWithProv resolves name into a fully merged Profile with
// provenance and catalog identity. The FullName is the resolved catalog
// key (e.g. "core/opencode"); DisplayName is the unqualified name for
// human-facing output.
func ResolveProfileWithProv(cat Catalog, name string) (Resolved, error) {
	ref, err := cat.ParseRefForCatalog(name)
	if err != nil {
		return Resolved{}, ProfileError{Message: err.Error()}
	}
	key, ok := cat.ResolveRef(ref)
	if !ok {
		return Resolved{}, ProfileError{Message: "profile not found: " + name}
	}
	rc, _ := cat.Get(key)
	cs := &chainState{seen: map[string]bool{}}
	merged, err := resolveChain(cat, key, map[string]bool{}, cs)
	if err != nil {
		return Resolved{}, err
	}
	merged.Path = rc.Path
	for name, svc := range merged.Services {
		svc.Hash = computeServiceHash(svc)
		merged.Services[name] = svc
	}
	if err := validate(merged); err != nil {
		return Resolved{}, err
	}
	return Resolved{
		Profile:     merged.Profile,
		Prov:        merged.Provenance,
		FullName:    key,
		DisplayName: rc.DisplayName(),
		Chain:       cs.entries,
	}, nil
}

// ResolveFragment resolves a fragment's extends chain into a merged Profile
// without the profile-only validation. Fragments are composition-only and
// carry no image/command, which ResolveProfile requires; resolving them is
// still useful for showing the effective merged view (e.g. edit seeds).
func ResolveFragment(cat Catalog, name string) (Profile, error) {
	res, err := ResolveFragmentWithProv(cat, name)
	if err != nil {
		return Profile{}, err
	}
	return res.Profile, nil
}

// ResolveFragmentWithProv is the fragment analogue of ResolveProfileWithProv.
func ResolveFragmentWithProv(cat Catalog, name string) (Resolved, error) {
	ref, err := cat.ParseRefForCatalog(name)
	if err != nil {
		return Resolved{}, ProfileError{Message: err.Error()}
	}
	key, ok := cat.ResolveRef(ref)
	if !ok {
		return Resolved{}, ProfileError{Message: "fragment not found: " + name}
	}
	rc, _ := cat.Get(key)
	cs := &chainState{seen: map[string]bool{}}
	merged, err := resolveChain(cat, key, map[string]bool{}, cs)
	if err != nil {
		return Resolved{}, err
	}
	merged.Path = rc.Path
	return Resolved{
		Profile:     merged.Profile,
		Prov:        merged.Provenance,
		FullName:    key,
		DisplayName: rc.DisplayName(),
		Chain:       cs.entries,
	}, nil
}

// chainState accumulates chain entries during resolution. seen is
// whole-resolution (unlike resolveChain's per-path cycle stack), so a parent
// shared across two sibling subtrees is recorded once.
type chainState struct {
	entries []ChainEntry
	seen    map[string]bool
}

func resolveChain(cat Catalog, key string, seen map[string]bool, chain *chainState) (RawProfile, error) {
	rc, ok := cat.Get(key)
	if !ok {
		return RawProfile{}, ProfileError{Message: "profile not found: " + key}
	}
	if !chain.seen[key] {
		chain.seen[key] = true
		chain.entries = append(chain.entries, ChainEntry{
			FullName:    rc.FullName(),
			DisplayName: rc.DisplayName(),
			Path:        rc.Path,
			Extends:     rc.ExtendsList.Raw,
		})
	}
	if seen[key] {
		return RawProfile{}, ProfileError{Path: rc.Path, Message: "extends cycle detected at: " + key}
	}
	if len(rc.ExtendsList.Resolved) == 0 {
		rc.Provenance = initProvenance(rc)
		return rc, nil
	}
	// Fragments are composition-only: they may extend other fragments, but
	// must not pull in profile identity (image/command/version).
	if cat.IsFragment(key) {
		for _, ref := range rc.ExtendsList.Resolved {
			pkey, ok := cat.ResolveRef(ref)
			if !ok {
				return RawProfile{}, ProfileError{Path: rc.Path, Message: "fragment not found: " + ref.FullName()}
			}
			if !cat.IsFragment(pkey) {
				return RawProfile{}, ProfileError{Path: rc.Path, Message: "fragment " + key + " may only extend fragments, not profile " + pkey}
			}
		}
	}
	seen[key] = true
	defer delete(seen, key)

	// Resolve each extends entry depth-first, merge left-to-right.
	// Duplicates are ignored after first resolution.
	merged := RawProfile{}
	resolved := map[string]bool{}
	for _, ref := range rc.ExtendsList.Resolved {
		pkey, ok := cat.ResolveRef(ref)
		if !ok {
			return RawProfile{}, withParentPath(ProfileError{Message: "profile not found: " + ref.FullName()}, rc)
		}
		if resolved[pkey] {
			continue
		}
		resolved[pkey] = true
		parent, err := resolveChain(cat, pkey, seen, chain)
		if err != nil {
			return RawProfile{}, withParentPath(err, rc)
		}
		merged = MergeProfiles(merged, parent)
	}

	// Merge the profile's own body last (wins over all extends).
	merged = MergeProfiles(merged, rc)
	merged.Path = rc.Path
	merged.Meta = rc.Meta
	return merged, nil
}

// withParentPath attaches the referencing profile's path to a child resolution
// error that doesn't already name a source, so "profile not found" pinpoints
// which file has the bad extends target.
func withParentPath(err error, rc RawProfile) error {
	var pe ProfileError
	if errors.As(err, &pe) && pe.Path == "" {
		pe.Path = rc.Path
		return pe
	}
	return err
}

// scalars replace, maps merge key-by-key with null-to-delete, lists replace.
func MergeProfiles(parent, child RawProfile) RawProfile {
	out := parent
	childContrib := Contributor{FullName: child.FullName(), Namespace: child.Namespace}

	if child.Version != 0 {
		out.Version = child.Version
	}
	if len(child.ExtendsList.Raw) > 0 {
		out.ExtendsList = child.ExtendsList
	}
	if child.Network != "" {
		out.Network = child.Network
		out.Provenance.Network = child.Provenance.Network
		if out.Provenance.Network.FullName == "" {
			out.Provenance.Network = childContrib
		}
	} else {
		out.Provenance.Network = parent.Provenance.Network
	}
	if child.TTY != "" {
		out.TTY = child.TTY
		out.Provenance.TTY = child.Provenance.TTY
		if out.Provenance.TTY.FullName == "" {
			out.Provenance.TTY = childContrib
		}
	}

	if child.Image != "" {
		out.Image = child.Image
		out.Provenance.Image = child.Provenance.Image
		if out.Provenance.Image.FullName == "" {
			out.Provenance.Image = childContrib
		}
	}

	if child.Command != nil {
		out.Command = child.Command
		out.Provenance.Command = child.Provenance.Command
		if out.Provenance.Command.FullName == "" {
			out.Provenance.Command = childContrib
		}
	}

	out.Packages = mergePackages(parent.Packages, child.Packages, child.NullKeys["packages"])
	out.Provenance.Packages = mergePackageProv(parent.Provenance.Packages, child.Packages, child.Provenance.Packages, child.NullKeys["packages"], childContrib)

	out.Repos = mergeMap(parent.Repos, child.Repos, child.NullKeys["repos"])
	out.Provenance.Repos = mergeProvMap(parent.Provenance.Repos, child.Provenance.Repos, keysOf(child.Repos), child.NullKeys["repos"], childContrib)

	out.Files = mergeMap(parent.Files, child.Files, child.NullKeys["files"])
	out.Provenance.Files = mergeProvMap(parent.Provenance.Files, child.Provenance.Files, keysOf(child.Files), child.NullKeys["files"], childContrib)

	out.Mounts = mergeMounts(parent.Mounts, child.Mounts, child.NullKeys["mounts"])
	out.Provenance.Mounts = mergeProvMap(parent.Provenance.Mounts, child.Provenance.Mounts, keysOf(child.Mounts), child.NullKeys["mounts"], childContrib)
	out.Env = mergeStringMap(parent.Env, child.Env, child.NullKeys["environment"])
	out.Provenance.Env = mergeProvMap(parent.Provenance.Env, child.Provenance.Env, keysOf(child.Env), child.NullKeys["environment"], childContrib)
	out.Tools = mergeMap(parent.Tools, child.Tools, child.NullKeys["tools"])
	out.Provenance.Tools = mergeProvMap(parent.Provenance.Tools, child.Provenance.Tools, keysOf(child.Tools), child.NullKeys["tools"], childContrib)
	out.Caches = mergeMap(parent.Caches, child.Caches, child.NullKeys["caches"])
	out.Provenance.Caches = mergeProvMap(parent.Provenance.Caches, child.Provenance.Caches, keysOf(child.Caches), child.NullKeys["caches"], childContrib)
	out.Labels = mergeStringMap(parent.Labels, child.Labels, child.NullKeys["labels"])
	out.Provenance.Labels = mergeProvMap(parent.Provenance.Labels, child.Provenance.Labels, keysOf(child.Labels), child.NullKeys["labels"], childContrib)
	out.Ports = mergePortMap(parent.Ports, child.Ports, child.NullKeys["ports"])
	out.Provenance.Ports = mergeProvMap(parent.Provenance.Ports, child.Provenance.Ports, keysOf(child.Ports), child.NullKeys["ports"], childContrib)
	out.Devices = mergeDeviceMap(parent.Devices, child.Devices, child.NullKeys["devices"])
	out.Provenance.Devices = mergeProvMap(parent.Provenance.Devices, child.Provenance.Devices, keysOf(child.Devices), child.NullKeys["devices"], childContrib)
	out.Dbus = mergeDbus(parent.Dbus, child.Dbus, child.NullKeys["dbus"])
	dbNull := child.NullKeys["dbus"]
	if dbNull != nil && dbNull["*"] {
		// both sub-maps cleared
		out.Provenance.Dbus.Talk = map[string]Contributor{}
		out.Provenance.Dbus.Own = map[string]Contributor{}
	} else {
		var childTalk, childOwn map[string]bool
		if child.Dbus != nil {
			childTalk = keysOf(child.Dbus.Talk)
			childOwn = keysOf(child.Dbus.Own)
		}
		if dbNull != nil && dbNull["talk"] {
			out.Provenance.Dbus.Talk = map[string]Contributor{}
		} else if childTalk != nil || parent.Provenance.Dbus.Talk != nil {
			out.Provenance.Dbus.Talk = mergeProvMap(parent.Provenance.Dbus.Talk, child.Provenance.Dbus.Talk, childTalk, nil, childContrib)
		}
		if dbNull != nil && dbNull["own"] {
			out.Provenance.Dbus.Own = map[string]Contributor{}
		} else if childOwn != nil || parent.Provenance.Dbus.Own != nil {
			out.Provenance.Dbus.Own = mergeProvMap(parent.Provenance.Dbus.Own, child.Provenance.Dbus.Own, childOwn, nil, childContrib)
		}
		// If mergeDbus returned nil (both sub-maps empty in the value), there
		// are no dbus keys to attribute — leave provenance talk/own empty.
		if out.Dbus == nil {
			out.Provenance.Dbus = DbusProvenance{}
		}
	}
	out.Services = mergeMap(parent.Services, child.Services, child.NullKeys["services"])
	out.Provenance.Services = mergeProvMap(parent.Provenance.Services, child.Provenance.Services, keysOf(child.Services), child.NullKeys["services"], childContrib)

	if child.Resources != nil {
		out.Resources = &Resources{}
		if parent.Resources != nil {
			out.Resources.Memory = parent.Resources.Memory
			out.Resources.CPUs = parent.Resources.CPUs
		}
		if child.Resources.Memory != "" {
			out.Resources.Memory = child.Resources.Memory
			out.Provenance.Resources.Memory = child.Provenance.Resources.Memory
			if out.Provenance.Resources.Memory.FullName == "" {
				out.Provenance.Resources.Memory = childContrib
			}
		}
		if child.Resources.CPUs != "" {
			out.Resources.CPUs = child.Resources.CPUs
			out.Provenance.Resources.CPUs = child.Provenance.Resources.CPUs
			if out.Provenance.Resources.CPUs.FullName == "" {
				out.Provenance.Resources.CPUs = childContrib
			}
		}
	}

	out.ExtendsList = ExtendsList{}
	out.NullKeys = nil
	return out
}

// mergePackageProv attributes each package to its first declarer in merge
// order (packages append+dedup, so there is no override). Seeds from the
// parent's accumulated provenance so attributions survive intermediate
// merges. A whole-field "packages: null" resets the map, so a later entry
// re-declaring a package owns it.
func mergePackageProv(parentProv map[string]Contributor, child []string, childProv map[string]Contributor, nullKeys map[string]bool, childContrib Contributor) map[string]Contributor {
	if nullKeys != nil && nullKeys["*"] {
		return map[string]Contributor{}
	}
	out := make(map[string]Contributor, len(parentProv)+len(child))
	for p, c := range parentProv {
		out[p] = c
	}
	for _, p := range child {
		if _, ok := out[p]; ok {
			continue
		}
		if c, ok := childProv[p]; ok {
			out[p] = c
		} else {
			out[p] = childContrib
		}
	}
	return out
}

func mergeMap[V any](parent, child map[string]V, nullKeys map[string]bool) map[string]V {
	if nullKeys != nil && nullKeys["*"] {
		return map[string]V{}
	}
	out := make(map[string]V, len(parent)+len(child))
	for k, v := range parent {
		out[k] = v
	}
	for k, v := range child {
		out[k] = v
	}
	for k := range nullKeys {
		delete(out, k)
	}
	return out
}

func mergeMounts(parent, child map[string]Mount, nullKeys map[string]bool) map[string]Mount {
	return mergeMap(parent, child, nullKeys)
}

func mergePortMap(parent, child map[string]PortBind, nullKeys map[string]bool) map[string]PortBind {
	return mergeMap(parent, child, nullKeys)
}

func mergeDeviceMap(parent, child map[string]DeviceBind, nullKeys map[string]bool) map[string]DeviceBind {
	return mergeMap(parent, child, nullKeys)
}

// mergeDbus unions talk/own key-by-key (child wins per key; there are no
// per-key values). nullKeys supports the repo's null-to-delete convention:
// "*" clears the whole dbus config; "talk"/"own" clear that sub-map.
func mergeDbus(parent, child *DbusConfig, nullKeys map[string]bool) *DbusConfig {
	if nullKeys["*"] {
		return nil
	}
	if child == nil {
		return parent
	}
	if parent == nil {
		parent = &DbusConfig{}
	}
	out := &DbusConfig{}
	if nullKeys["talk"] {
		out.Talk = map[string]*struct{}{}
	} else {
		out.Talk = mergeMap(parent.Talk, child.Talk, nil)
	}
	if nullKeys["own"] {
		out.Own = map[string]*struct{}{}
	} else {
		out.Own = mergeMap(parent.Own, child.Own, nil)
	}
	if len(out.Talk) == 0 && len(out.Own) == 0 {
		return nil
	}
	return out
}

// mergeProvMap merges parent/child provenance maps with the same key
// semantics as the value merges: child wins per key, nullKeys deletes.
// childKeys is the set of keys the child contributed for this field
// (built via keysOf from the child's value map); nullKeys["*"] clears
// everything. childProv is the child's own accumulated provenance for the
// field: when a key is recorded there (leaf init or an intermediate merge
// result), that attribution wins, since re-attributing to childContrib
// would mis-credit keys the child only inherited. childContrib applies
// only to keys a bare entry wrote itself.
func mergeProvMap(parent, childProv map[string]Contributor, childKeys map[string]bool, nullKeys map[string]bool, childContrib Contributor) map[string]Contributor {
	if nullKeys != nil && nullKeys["*"] {
		return map[string]Contributor{}
	}
	out := make(map[string]Contributor, len(parent)+len(childKeys))
	for k, v := range parent {
		out[k] = v
	}
	for k := range childKeys {
		if c, ok := childProv[k]; ok {
			out[k] = c
		} else {
			out[k] = childContrib
		}
	}
	for k := range nullKeys {
		delete(out, k)
	}
	return out
}

// keysOf returns the key set of m as a map[string]bool.
func keysOf[V any](m map[string]V) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func mergeStringMap(parent, child map[string]string, nullKeys map[string]bool) map[string]string {
	if nullKeys != nil && nullKeys["*"] {
		return map[string]string{}
	}
	out := make(map[string]string, len(parent)+len(child))
	for k, v := range parent {
		out[k] = v
	}
	for k, v := range child {
		out[k] = v
	}
	for k := range nullKeys {
		delete(out, k)
	}
	return out
}

// mergePackages appends child's packages to parent's, preserving order and
// dropping duplicates. packages: null ("*" in nullKeys) clears the inherited
// list entirely. Unlike command (which replaces), packages compose additively
// across fragments — e.g. the php and gui fragments both contribute entries
// and neither supersedes the other.
func mergePackages(parent, child []string, nullKeys map[string]bool) []string {
	if nullKeys != nil && nullKeys["*"] {
		return nil
	}
	if len(child) == 0 {
		return parent
	}
	out := make([]string, 0, len(parent)+len(child))
	seen := make(map[string]bool, len(parent)+len(child))
	for _, p := range parent {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range child {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
