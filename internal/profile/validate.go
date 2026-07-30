package profile

import "strings"

var reservedNames = map[string]bool{
	"config":     true,
	"doctor":     true,
	"help":       true,
	"version":    true,
	"completion": true,
	"prune":      true,
}

// validate checks a resolved config for required fields and invariants.
// It runs on the merged result (after extends resolution).
func validate(rc RawProfile) error {
	if rc.Version == 0 {
		return ProfileError{Path: rc.Path, Message: "missing required field: version"}
	}
	if len(rc.Command) == 0 {
		return ProfileError{Path: rc.Path, Message: "missing required field: command"}
	}
	hasImage := rc.Image != ""
	hasBuild := rc.Build != nil
	if hasImage && hasBuild {
		return ProfileError{Path: rc.Path, Message: "exactly one of image or build is required (both set)"}
	}
	if !hasImage && !hasBuild {
		return ProfileError{Path: rc.Path, Message: "exactly one of image or build is required (neither set)"}
	}
	return nil
}

// validateReservedName rejects profile names that collide with subcommands.
// Called during catalog load, not on the merged config.
func validateReservedName(rc RawProfile, name string) error {
	if reservedNames[name] {
		return ProfileError{Path: rc.Path, Message: "profile name " + name + " is reserved (collides with a subcommand)"}
	}
	return nil
}

// ProfileNameFromPath extracts the profile name from a config file path.
func ProfileNameFromPath(path string) string {
	base := path
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	if idx := strings.LastIndex(base, "."); idx >= 0 {
		base = base[:idx]
	}
	return base
}
