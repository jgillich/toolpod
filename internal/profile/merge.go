package profile

import "errors"

// Resolve walks the extends chain for name and produces a fully merged Profile.
// Cycles are detected and rejected. Validation runs on the result.
func ResolveProfile(cat Catalog, name string) (Profile, error) {
	rc, ok := cat.Get(name)
	if !ok {
		return Profile{}, ProfileError{Message: "profile not found: " + name}
	}
	merged, err := resolveChain(cat, name, map[string]bool{})
	if err != nil {
		return Profile{}, err
	}
	merged.Path = rc.Path
	if err := validate(merged); err != nil {
		return Profile{}, err
	}
	return merged.Profile, nil
}

func resolveChain(cat Catalog, name string, seen map[string]bool) (RawProfile, error) {
	rc, ok := cat.Get(name)
	if !ok {
		return RawProfile{}, ProfileError{Message: "profile not found: " + name}
	}
	if seen[name] {
		return RawProfile{}, ProfileError{Path: rc.Path, Message: "extends cycle detected at: " + name}
	}
	if len(rc.ExtendsList) == 0 {
		return rc, nil
	}
	// Fragments are composition-only: they may extend other fragments, but
	// must not pull in profile identity (image/command/version).
	if cat.IsFragment(name) {
		for _, parentName := range rc.ExtendsList {
			if !cat.IsFragment(parentName) {
				return RawProfile{}, ProfileError{Path: rc.Path, Message: "fragment " + name + " may only extend fragments, not profile " + parentName}
			}
		}
	}
	// Special case: a user shadow that extends the built-in of the same name.
	// The first entry may be a self-reference (profileName) that must be
	// resolved as the built-in to avoid a cycle. IsUserShadow implies a
	// built-in exists, so GetBuiltin should always succeed here.
	if cat.IsUserShadow(name) && len(rc.ExtendsList) > 0 && rc.ExtendsList[0] == name {
		builtin, ok := cat.GetBuiltin(name)
		if !ok {
			return RawProfile{}, ProfileError{Path: rc.Path, Message: "extends cycle detected at: " + name}
		}
		parent, err := resolveBuiltinChain(cat, builtin, seen)
		if err != nil {
			return RawProfile{}, err
		}
		merged := parent
		resolved := map[string]bool{name: true}
		for _, parentName := range rc.ExtendsList[1:] {
			if resolved[parentName] {
				continue
			}
			resolved[parentName] = true
			p, err := resolveChain(cat, parentName, seen)
			if err != nil {
				return RawProfile{}, withParentPath(err, rc)
			}
			merged = MergeProfiles(merged, p)
		}
		merged = MergeProfiles(merged, rc)
		merged.Path = rc.Path
		return merged, nil
	}
	seen[name] = true
	defer delete(seen, name)

	// Resolve each extends entry depth-first, merge left-to-right.
	// Duplicates are ignored after first resolution.
	merged := RawProfile{}
	resolved := map[string]bool{}
	for _, parentName := range rc.ExtendsList {
		if resolved[parentName] {
			continue
		}
		resolved[parentName] = true
		parent, err := resolveChain(cat, parentName, seen)
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

// resolveBuiltinChain resolves the extends chain of a built-in profile using
// GetBuiltin at each step (so user shadows don't interfere). inheritedSeen
// carries names already visited in the outer resolution to detect cycles
// that span the shadow boundary.
func resolveBuiltinChain(cat Catalog, rc RawProfile, inheritedSeen map[string]bool) (RawProfile, error) {
	if len(rc.ExtendsList) == 0 {
		return rc, nil
	}
	merged := RawProfile{}
	resolved := map[string]bool{}
	for _, name := range rc.ExtendsList {
		if resolved[name] {
			continue
		}
		resolved[name] = true
		if inheritedSeen[name] {
			return RawProfile{}, ProfileError{Path: rc.Path, Message: "extends cycle detected at: " + name}
		}
		parent, ok := cat.GetBuiltin(name)
		if !ok {
			return RawProfile{}, ProfileError{Path: rc.Path, Message: "built-in profile not found: " + name}
		}
		// Copy seen so each sibling branch has its own scope but still
		// detects cycles back to the original shadow name.
		seen := make(map[string]bool, len(inheritedSeen)+1)
		for k := range inheritedSeen {
			seen[k] = true
		}
		seen[name] = true
		resolvedParent, err := resolveBuiltinChain(cat, parent, seen)
		if err != nil {
			return RawProfile{}, err
		}
		merged = MergeProfiles(merged, resolvedParent)
	}
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
	if len(child.ExtendsList) > 0 {
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

	out.Mounts = mergeMounts(parent.Mounts, child.Mounts, child.NullKeys["mounts"])
	out.Env = mergeStringMap(parent.Env, child.Env, child.NullKeys["environment"])
	out.Tools = mergeStringMap(parent.Tools, child.Tools, child.NullKeys["tools"])
	out.Caches = mergeStringMap(parent.Caches, child.Caches, child.NullKeys["caches"])
	out.Labels = mergeStringMap(parent.Labels, child.Labels, child.NullKeys["labels"])
	out.Ports = mergePortMap(parent.Ports, child.Ports, child.NullKeys["ports"])
	out.Devices = mergeDeviceMap(parent.Devices, child.Devices, child.NullKeys["devices"])
	out.Dbus = mergeDbus(parent.Dbus, child.Dbus, child.NullKeys["dbus"])

	if child.Resources != nil {
		out.Resources = child.Resources
	}

	out.ExtendsList = nil
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
