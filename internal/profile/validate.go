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

var (
	envKeyRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	toolNameRe  = regexp.MustCompile(`^[A-Za-z0-9_@./:-]+$`)
	hexSHA256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)
	networkRe   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
)

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
	if err := validateFiles(rc); err != nil {
		return err
	}
	if err := validateTools(rc); err != nil {
		return err
	}
	if err := validateEnv(rc); err != nil {
		return err
	}
	if err := validateNetwork(rc); err != nil {
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

// validateFiles checks each file target: absolute or ~-prefixed, and free of
// ".." segments. The tar is rooted at "/", so a ".." target could traverse
// outside the intended location. Rejecting raw ".." segments covers the
// literal target; template expansion can inject new ".." segments, so
// ResolveTildes re-checks the expanded target and cleans the result.
func validateFiles(rc RawProfile) error {
	for target, f := range rc.Files {
		if target != "~" && !strings.HasPrefix(target, "~/") && !strings.HasPrefix(target, "/") {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("files: target %q must be an absolute path or ~-prefixed", target)}
		}
		for _, seg := range strings.Split(target, "/") {
			if seg == ".." {
				return ProfileError{Path: rc.Path, Message: fmt.Sprintf("files: target %q must not contain '..' segments", target)}
			}
		}
		if f.Mode > 0o7777 {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("files: target %q: mode %o out of range (want 0-07777)", target, f.Mode)}
		}
	}
	return nil
}

func validateTools(rc RawProfile) error {
	for name, tool := range rc.Tools {
		if !toolNameRe.MatchString(name) || containsControl(name) || containsControl(tool.Version) {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("tools: invalid tool name/version %q", name)}
		}
	}
	return nil
}

func validateEnv(rc RawProfile) error {
	for key, value := range rc.Env {
		if !envKeyRe.MatchString(key) || containsControl(key) || containsControl(value) {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("environment: invalid key/value %q", key)}
		}
	}
	return nil
}

func validateNetwork(rc RawProfile) error {
	if rc.Network != "" && (!networkRe.MatchString(rc.Network) || containsControl(rc.Network)) {
		return ProfileError{Path: rc.Path, Message: fmt.Sprintf("network: invalid value %q", rc.Network)}
	}
	return nil
}

func containsControl(s string) bool {
	for _, r := range s {
		if r == 0 || r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func checkPortNum(s, what, path string) error {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return ProfileError{Path: path, Message: fmt.Sprintf("%s: invalid port %q (want 1-65535)", what, s)}
	}
	return nil
}
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
