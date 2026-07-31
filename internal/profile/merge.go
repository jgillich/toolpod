package profile

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
				return RawProfile{}, err
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
			return RawProfile{}, err
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
	name := rc.ExtendsList[0]
	if inheritedSeen[name] {
		return RawProfile{}, ProfileError{Path: rc.Path, Message: "extends cycle detected at: " + name}
	}
	parent, ok := cat.GetBuiltin(name)
	if !ok {
		return RawProfile{}, ProfileError{Path: rc.Path, Message: "built-in profile not found: " + name}
	}
	// Copy seen so the built-in chain has its own scope but still detects
	// cycles back to the original shadow name.
	seen := make(map[string]bool, len(inheritedSeen)+1)
	for k := range inheritedSeen {
		seen[k] = true
	}
	seen[name] = true
	resolved, err := resolveBuiltinChain(cat, parent, seen)
	if err != nil {
		return RawProfile{}, err
	}
	merged := MergeProfiles(resolved, rc)
	merged.Path = rc.Path
	return merged, nil
}
// scalars replace, maps merge key-by-key with null-to-delete, lists replace,
// image/build treated as a single slot.
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

	if child.Image != "" || child.Build != nil {
		out.Image = child.Image
		out.Build = child.Build
	}

	if child.Command != nil {
		out.Command = child.Command
	}
	if child.ArgsIfNone != nil {
		out.ArgsIfNone = child.ArgsIfNone
	}

	out.Mounts = mergeMounts(parent.Mounts, child.Mounts, child.NullKeys["mounts"])
	out.Env = mergeStringMap(parent.Env, child.Env, child.NullKeys["environment"])
	out.Tools = mergeStringMap(parent.Tools, child.Tools, child.NullKeys["tools"])
	out.Caches = mergeStringMap(parent.Caches, child.Caches, child.NullKeys["caches"])
	out.Labels = mergeStringMap(parent.Labels, child.Labels, child.NullKeys["labels"])

	if child.Resources != nil {
		out.Resources = child.Resources
	}

	out.ExtendsList = nil
	out.NullKeys = nil
	return out
}

func mergeMounts(parent, child map[string]Mount, nullKeys map[string]bool) map[string]Mount {
	if nullKeys != nil && nullKeys["*"] {
		return map[string]Mount{}
	}
	out := make(map[string]Mount, len(parent)+len(child))
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
