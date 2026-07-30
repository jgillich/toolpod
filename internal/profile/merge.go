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
	if rc.Extends == "" {
		return rc, nil
	}
	seen[name] = true
	defer delete(seen, name)

	parent, err := resolveChain(cat, rc.Extends, seen)
	if err != nil {
		return RawProfile{}, err
	}
	merged := mergeProfiles(parent, rc)
	merged.Path = rc.Path
	return merged, nil
}

// mergeProfiles merges child on top of parent per spec §4.3:
// scalars replace, maps merge key-by-key with null-to-delete, lists replace,
// image/build treated as a single slot.
func mergeProfiles(parent, child RawProfile) RawProfile {
	out := parent

	if child.Version != 0 {
		out.Version = child.Version
	}
	if child.Extends != "" {
		out.Extends = child.Extends
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

	out.Extends = ""
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
