package profile

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var busNameRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)*(\.[*])?$`)

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
