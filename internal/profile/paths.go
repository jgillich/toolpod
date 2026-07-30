package profile

import "strings"

// ResolveTildes expands leading ~/ on mount sources (→ hostHome) and
// mount/cache targets (→ runtimeHome) per spec §5.6. Absolute paths are
// left as-is. The mode ("A" or "B") is informational only here; the caller
// has already determined runtimeHome based on the mode.
func ResolveTildes(cfg Profile, mode, hostHome, runtimeHome string) Profile {
	out := cfg

	if out.Mounts != nil {
		expanded := make(map[string]Mount, len(out.Mounts))
		for target, m := range out.Mounts {
			newTarget := expandTarget(target, runtimeHome)
			m.Source = expandSource(m.Source, hostHome)
			expanded[newTarget] = m
		}
		out.Mounts = expanded
	}

	if out.Caches != nil {
		expanded := make(map[string]string, len(out.Caches))
		for name, target := range out.Caches {
			expanded[name] = expandTarget(target, runtimeHome)
		}
		out.Caches = expanded
	}

	return out
}

func expandTarget(path, runtimeHome string) string {
	if path == "~" {
		return runtimeHome
	}
	if strings.HasPrefix(path, "~/") {
		return runtimeHome + path[1:]
	}
	return path
}

func expandSource(path, hostHome string) string {
	if path == "~" {
		return hostHome
	}
	if strings.HasPrefix(path, "~/") {
		return hostHome + path[1:]
	}
	return path
}
