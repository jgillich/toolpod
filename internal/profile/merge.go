package profile

import "errors"

// Resolve walks the extends chain for name and produces a fully merged Profile.
// Cycles are detected and rejected. Validation runs on the result.
func ResolveProfile(cat Catalog, name string) (Profile, error) {
	ref, err := cat.ParseRefForCatalog(name)
	if err != nil {
		return Profile{}, ProfileError{Message: err.Error()}
	}
	key, ok := cat.ResolveRef(ref)
	if !ok {
		return Profile{}, ProfileError{Message: "profile not found: " + name}
	}
	rc, _ := cat.Get(key)
	merged, err := resolveChain(cat, key, map[string]bool{})
	if err != nil {
		return Profile{}, err
	}
	merged.Path = rc.Path
	for name, svc := range merged.Services {
		svc.Hash = computeServiceHash(svc)
		merged.Services[name] = svc
	}
	if err := validate(merged); err != nil {
		return Profile{}, err
	}
	return merged.Profile, nil
}

// ResolveFragment resolves a fragment's extends chain into a merged Profile
// without the profile-only validation. Fragments are composition-only and
// carry no image/command, which ResolveProfile requires; resolving them is
// still useful for showing the effective merged view (e.g. edit seeds).
func ResolveFragment(cat Catalog, name string) (Profile, error) {
	ref, err := cat.ParseRefForCatalog(name)
	if err != nil {
		return Profile{}, ProfileError{Message: err.Error()}
	}
	key, ok := cat.ResolveRef(ref)
	if !ok {
		return Profile{}, ProfileError{Message: "fragment not found: " + name}
	}
	rc, _ := cat.Get(key)
	merged, err := resolveChain(cat, key, map[string]bool{})
	if err != nil {
		return Profile{}, err
	}
	merged.Path = rc.Path
	return merged.Profile, nil
}

func resolveChain(cat Catalog, key string, seen map[string]bool) (RawProfile, error) {
	rc, ok := cat.Get(key)
	if !ok {
		return RawProfile{}, ProfileError{Message: "profile not found: " + key}
	}
	if seen[key] {
		return RawProfile{}, ProfileError{Path: rc.Path, Message: "extends cycle detected at: " + key}
	}
	if len(rc.ExtendsList.Resolved) == 0 {
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
		parent, err := resolveChain(cat, pkey, seen)
		if err != nil {
			return RawProfile{}, withParentPath(err, rc)
		}
		merged = MergeProfiles(merged, parent)
	}

	// Merge the profile's own body last (wins over all extends).
	merged = MergeProfiles(merged, rc)
	merged.Path = rc.Path
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

	if child.Version != 0 {
		out.Version = child.Version
	}
	if len(child.ExtendsList.Raw) > 0 {
		out.ExtendsList = child.ExtendsList
	}
	if child.Network != "" {
		out.Network = child.Network
	}
	if child.TTY != "" {
		out.TTY = child.TTY
	}

	if child.Image != "" {
		out.Image = child.Image
	}

	if child.Command != nil {
		out.Command = child.Command
	}

	out.Packages = mergePackages(parent.Packages, child.Packages, child.NullKeys["packages"])

	out.Repos = mergeMap(parent.Repos, child.Repos, child.NullKeys["repos"])

	out.Files = mergeMap(parent.Files, child.Files, child.NullKeys["files"])

	out.Mounts = mergeMounts(parent.Mounts, child.Mounts, child.NullKeys["mounts"])
	out.Env = mergeStringMap(parent.Env, child.Env, child.NullKeys["environment"])
	out.Tools = mergeMap(parent.Tools, child.Tools, child.NullKeys["tools"])
	out.Caches = mergeMap(parent.Caches, child.Caches, child.NullKeys["caches"])
	out.Labels = mergeStringMap(parent.Labels, child.Labels, child.NullKeys["labels"])
	out.Ports = mergePortMap(parent.Ports, child.Ports, child.NullKeys["ports"])
	out.Devices = mergeDeviceMap(parent.Devices, child.Devices, child.NullKeys["devices"])
	out.Dbus = mergeDbus(parent.Dbus, child.Dbus, child.NullKeys["dbus"])
	out.Services = mergeMap(parent.Services, child.Services, child.NullKeys["services"])

	if child.Resources != nil {
		out.Resources = &Resources{}
		if parent.Resources != nil {
			out.Resources.Memory = parent.Resources.Memory
			out.Resources.CPUs = parent.Resources.CPUs
		}
		if child.Resources.Memory != "" {
			out.Resources.Memory = child.Resources.Memory
		}
		if child.Resources.CPUs != "" {
			out.Resources.CPUs = child.Resources.CPUs
		}
	}

	out.ExtendsList = ExtendsList{}
	out.NullKeys = nil
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
