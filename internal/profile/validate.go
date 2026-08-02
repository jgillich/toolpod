package profile

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var busNameRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)*(\.[*])?$`)

var packageNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)

var reservedNames = map[string]bool{
	"config":     true,
	"doctor":     true,
	"help":       true,
	"version":    true,
	"completion": true,
	"prune":      true,
	"init":       true,
}

// validate checks a resolved profile for required fields and invariants.
// It runs on the merged result (after extends resolution).
func validate(rc RawProfile) error {
	if rc.Version == 0 {
		return ProfileError{Path: rc.Path, Message: "missing required field: version"}
	}
	if len(rc.Command) == 0 {
		return ProfileError{Path: rc.Path, Message: "missing required field: command"}
	}
	if rc.Image == "" {
		return ProfileError{Path: rc.Path, Message: "missing required field: image"}
	}
	if err := validatePorts(rc); err != nil {
		return err
	}
	if err := validateDevices(rc); err != nil {
		return err
	}
	if err := validateDbus(rc); err != nil {
		return err
	}
	if err := validatePackages(rc); err != nil {
		return err
	}
	if err := validateRepos(rc); err != nil {
		return err
	}
	if rc.Network == "host" && len(rc.Ports) > 0 {
		fmt.Fprintln(os.Stderr, "warning: network: host makes ports redundant; ports are ignored by the engine")
	}
	return nil
}

func validatePorts(rc RawProfile) error {
	for key, bind := range rc.Ports {
		if err := checkPortNum(key, "container port", rc.Path); err != nil {
			return err
		}
		if bind.Host != "" && bind.Host != "0" {
			if err := checkPortNum(bind.Host, "host port for container port "+key, rc.Path); err != nil {
				return err
			}
		}
		proto := bind.Protocol
		if proto == "" {
			proto = "tcp"
		}
		switch proto {
		case "tcp", "udp", "sctp":
		default:
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("ports: container port %s: invalid protocol %q (want tcp, udp, or sctp)", key, bind.Protocol)}
		}
		if proto == "sctp" && (bind.Host == "" || bind.Host == "0") {
			return ProfileError{Path: rc.Path, Message: "ports: container port " + key + ": sctp requires an explicit host port (cannot auto-allocate)"}
		}
	}
	return nil
}

func validateDevices(rc RawProfile) error {
	for key, bind := range rc.Devices {
		switch bind.Permissions {
		case "", "r", "rw", "rwm":
		default:
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("devices: %s: invalid permissions %q (want r, rw, or rwm)", key, bind.Permissions)}
		}
	}
	return nil
}

func validateDbus(rc RawProfile) error {
	if rc.Dbus == nil {
		return nil
	}
	for name := range rc.Dbus.Talk {
		if !busNameRe.MatchString(name) {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("dbus.talk: invalid bus name %q", name)}
		}
	}
	for name := range rc.Dbus.Own {
		if !busNameRe.MatchString(name) {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("dbus.own: invalid bus name %q", name)}
		}
	}
	return nil
}

// validatePackages checks that each declared system package matches Debian's
// package-name grammar (Policy §5.6.7): lowercase alphanumeric start, then
// [a-z0-9+.-]. Rejects whitespace, shell metacharacters, and version pinning
// (`=`), which v1 doesn't support.
func validatePackages(rc RawProfile) error {
	for _, pkg := range rc.Packages {
		if !packageNameRe.MatchString(pkg) {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("packages: invalid package name %q (want [a-z0-9][a-z0-9+.-]*)", pkg)}
		}
	}
	return nil
}

// validateRepos checks each apt source: the map key and extrepo catalog name
// follow the package-name grammar, and each repo is either an extrepo name or
// a complete inline custom repo (url + key_url). Custom repos are schema-ready
// but v1 synthesis only handles extrepo, so the build path rejects them.
func validateRepos(rc RawProfile) error {
	for name, repo := range rc.Repos {
		if !packageNameRe.MatchString(name) {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("repos: invalid repo name %q (want [a-z0-9][a-z0-9+.-]*)", name)}
		}
		if repo.ExtRepo != "" {
			if !packageNameRe.MatchString(repo.ExtRepo) {
				return ProfileError{Path: rc.Path, Message: fmt.Sprintf("repos: %s: invalid extrepo name %q (want [a-z0-9][a-z0-9+.-]*)", name, repo.ExtRepo)}
			}
			if repo.URL != "" || repo.KeyURL != "" || repo.Suites != "" || repo.Components != "" {
				return ProfileError{Path: rc.Path, Message: fmt.Sprintf("repos: %s: extrepo repos must not set url/key_url/suites/components", name)}
			}
			continue
		}
		if repo.URL == "" {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("repos: %s: repo requires extrepo: <name> or a url", name)}
		}
		if repo.KeyURL == "" {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("repos: %s: custom repo requires key_url", name)}
		}
	}
	return nil
}

func checkPortNum(s, what, path string) error {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return ProfileError{Path: path, Message: fmt.Sprintf("%s: invalid port %q (want 1-65535)", what, s)}
	}
	return nil
}

// validateReservedName rejects profile names that collide with subcommands.
// Called during catalog load, not on the merged profile.
func validateReservedName(rc RawProfile, name string) error {
	if reservedNames[name] {
		return ProfileError{Path: rc.Path, Message: "profile name " + name + " is reserved (collides with a subcommand)"}
	}
	return nil
}

// ValidateName checks a user-supplied profile name for the init flow. It
// rejects empty names, names unsafe for use as a file path (slashes, "..",
// whitespace), and names reserved for subcommands. Fragment collisions are
// checked separately by the caller against the catalog.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("profile name is required")
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") || strings.ContainsAny(name, " \t\n\r") {
		return fmt.Errorf("invalid profile name %q: must not contain slashes, whitespace, or '..'", name)
	}
	if reservedNames[name] {
		return fmt.Errorf("profile name %q is reserved (collides with a subcommand)", name)
	}
	return nil
}

// ProfileNameFromPath extracts the profile name from a profile file path.
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
