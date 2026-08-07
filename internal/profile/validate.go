package profile

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/distribution/reference"
	units "github.com/docker/go-units"
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
	if rc.Version != 1 {
		if rc.Version == 0 {
			return ProfileError{Path: rc.Path, Message: "missing required field: version"}
		}
		return ProfileError{Path: rc.Path, Message: fmt.Sprintf("unsupported version: %d (want 1)", rc.Version)}
	}
	if len(rc.Command) == 0 {
		return ProfileError{Path: rc.Path, Message: "missing required field: command"}
	}
	if rc.Image == "" {
		return ProfileError{Path: rc.Path, Message: "missing required field: image"}
	}
	if rc.Image != "" {
		if strings.ContainsAny(rc.Image, "\x00\n\r") {
			return ProfileError{Path: rc.Path, Message: "image: must not contain control characters"}
		}
		if _, err := reference.ParseNormalizedNamed(rc.Image); err != nil {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("image: invalid image reference %q: %v", rc.Image, err)}
		}
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
	if err := validateMounts(rc); err != nil {
		return err
	}
	if err := validateCaches(rc); err != nil {
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
	if err := validateMeta(rc); err != nil {
		return err
	}
	if err := validateResources(rc); err != nil {
		return err
	}
	if err := validateServices(rc); err != nil {
		return err
	}
	if err := validateMountServices(rc); err != nil {
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

// isAbsOrTilde reports whether s is an absolute path or a ~-prefixed home
// reference ("~", "~/..."). ~user forms are not valid tpd paths.
func isAbsOrTilde(s string) bool {
	return filepath.IsAbs(s) || s == "~" || strings.HasPrefix(s, "~/")
}

// hasDotDot reports whether any "/"-separated segment of path is "..".
func hasDotDot(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// checkPath validates a pre-expansion path used as a mount/cache/file target
// or bind-mount source: absolute or ~-prefixed, free of ".." segments and
// control characters. A {{ }} template is exempt from the prefix rule (it
// cannot be evaluated here) but must still be free of ".." and control chars;
// ResolveTildes re-checks the rendered result.
func checkPath(value, what string) error {
	if value == "" {
		return nil
	}
	if containsControl(value) {
		return fmt.Errorf("%s %q: must not contain control characters", what, value)
	}
	if !strings.Contains(value, "{{") && !isAbsOrTilde(value) {
		return fmt.Errorf("%s %q: must be an absolute path or ~-prefixed", what, value)
	}
	if hasDotDot(value) {
		return fmt.Errorf("%s %q: must not contain '..' segments", what, value)
	}
	return nil
}

// checkExposePath validates a service expose socket path: absolute, under a
// non-root parent dir, and free of ".." segments. The runtime bind-mounts the
// socket's parent dir from the host run-dir and joins the socket name onto it,
// so a root parent or a ".." segment would place the socket outside the
// run-dir. ResolveTildes re-checks the rendered path.
func checkExposePath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("path must be absolute")
	}
	if hasDotDot(path) {
		return fmt.Errorf("path %q must not contain '..' segments", path)
	}
	if filepath.Dir(path) == "/" {
		return fmt.Errorf("path %q must be inside a non-root directory", path)
	}
	return nil
}

// validateFiles checks each file target: absolute or ~-prefixed, and free of
// ".." segments and control characters. The tar is rooted at "/", so a ".."
// target could traverse outside the intended location. Rejecting raw ".."
// segments covers the literal target; template expansion can inject new ".."
// segments, so ResolveTildes re-checks the expanded target and cleans the
// result.
func validateFiles(rc RawProfile) error {
	for target, f := range rc.Files {
		if err := checkPath(target, "files: target"); err != nil {
			return ProfileError{Path: rc.Path, Message: err.Error()}
		}
		if f.Mode > 0o7777 {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("files: target %q: mode %o out of range (want 0-07777)", target, f.Mode)}
		}
	}
	return nil
}

// validateMounts checks each mount's raw paths: the target must be absolute or
// ~-prefixed and free of ".." segments and control characters; bind-mount
// sources follow the same rule. Service-socket mounts carry no source (the
// socket comes from the service's exposes), so only their target is checked
// here; validateMountServices enforces the service/socket pairing.
func validateMounts(rc RawProfile) error {
	for target, m := range rc.Mounts {
		if err := checkPath(target, "mount target"); err != nil {
			return ProfileError{Path: rc.Path, Message: err.Error()}
		}
		if m.Service == "" && m.Socket == "" {
			if err := checkPath(m.Source, "mount source"); err != nil {
				return ProfileError{Path: rc.Path, Message: err.Error()}
			}
		}
	}
	return nil
}

// validateCaches checks each cache target: absolute or ~-prefixed, free of
// ".." segments and control characters.
func validateCaches(rc RawProfile) error {
	for _, paths := range rc.Caches {
		for _, p := range paths {
			if err := checkPath(p, "cache path"); err != nil {
				return ProfileError{Path: rc.Path, Message: err.Error()}
			}
		}
	}
	return nil
}

func validateTools(rc RawProfile) error {
	for name, tool := range rc.Tools {
		if !toolNameRe.MatchString(name) || containsControl(name) || tool.Version == "" || containsControl(tool.Version) {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("tools: invalid tool name/version %q", name)}
		}
		if strings.HasPrefix(name, "appimage:") {
			if tool.SHA256 != "" && !hexSHA256Re.MatchString(tool.SHA256) {
				return ProfileError{Path: rc.Path, Message: fmt.Sprintf("tools: %s: invalid universal sha256", name)}
			}
			for arch, sum := range tool.SHA256ByArch {
				if arch != "amd64" && arch != "aarch64" {
					return ProfileError{Path: rc.Path, Message: fmt.Sprintf("tools: %s: unknown arch %q (want amd64 or aarch64)", name, arch)}
				}
				if !hexSHA256Re.MatchString(sum) {
					return ProfileError{Path: rc.Path, Message: fmt.Sprintf("tools: %s: invalid sha256 for arch %q", name, arch)}
				}
			}
		}
	}
	return nil
}

func validateEnv(rc RawProfile) error {
	for k, v := range rc.Env {
		if !envKeyRe.MatchString(k) || containsControl(k) || containsControl(v) {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("environment: invalid key %q", k)}
		}
	}
	return nil
}

func validateNetwork(rc RawProfile) error {
	if rc.Network != "" && (!networkRe.MatchString(rc.Network) || containsControl(rc.Network)) {
		return ProfileError{Path: rc.Path, Message: fmt.Sprintf("network: invalid network name %q", rc.Network)}
	}
	if rc.Network == "host" && len(rc.Services) > 0 {
		return ProfileError{Path: rc.Path, Message: "network: host cannot be combined with services (a host-network consumer cannot join the service network)"}
	}
	return nil
}

func validateMeta(rc RawProfile) error {
	if rc.Meta == nil {
		return nil
	}
	if containsControl(rc.Meta.Description) {
		return ProfileError{Path: rc.Path, Message: "meta: description must not contain control characters"}
	}
	for _, tag := range rc.Meta.Tags {
		if containsControl(tag) || strings.TrimSpace(tag) == "" {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("meta: invalid tag %q", tag)}
		}
	}
	return nil
}

func validateResources(rc RawProfile) error {
	if rc.Resources == nil {
		return nil
	}
	if rc.Resources.Memory != "" {
		if _, err := ParseMemoryBytes(rc.Resources.Memory); err != nil {
			return ProfileError{Path: rc.Path, Message: "resources: memory: " + err.Error()}
		}
	}
	if rc.Resources.CPUs != "" {
		if _, err := ParseNanoCPUs(rc.Resources.CPUs); err != nil {
			return ProfileError{Path: rc.Path, Message: "resources: cpus: " + err.Error()}
		}
	}
	return nil
}

func validateServices(rc RawProfile) error {
	for name, svc := range rc.Services {
		if !profileNameRe.MatchString(name) {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: invalid service name (must match %s)", name, profileNameRe)}
		}
		if svc.Image == "" {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: image is required", name)}
		}
		if strings.ContainsAny(svc.Image, "\x00\n\r") {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: image: must not contain control characters", name)}
		}
		if _, err := reference.ParseNormalizedNamed(svc.Image); err != nil {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: image: invalid image reference %q: %v", name, svc.Image, err)}
		}
		if len(svc.Command) == 0 {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: command is required", name)}
		}
		if svc.Network != "" {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: network is always enabled for services; remove the network field", name)}
		}
		if svc.TTY != "" {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: must not set tty", name)}
		}
		if svc.Resources != nil {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: must not set resources", name)}
		}
		if len(svc.Tools) > 0 {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: must not set tools", name)}
		}
		if svc.Dbus != nil {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: must not set dbus", name)}
		}
		if len(svc.Ports) > 0 {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: must not set ports", name)}
		}
		if len(svc.Devices) > 0 {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: must not set devices", name)}
		}
		if len(svc.Services) > 0 {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: must not declare nested services", name)}
		}
		if svc.Version != 0 {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: must not set version", name)}
		}
		if len(svc.ExtendsList.Raw) > 0 {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: must not set extends", name)}
		}
		// Reuse the existing per-field validators so service packages/repos/env/files
		// get the same validation as the main profile. Wrap in a synthetic
		// RawProfile with the service's fields so the existing validators work
		// unchanged (they take RawProfile and use rc.Path for error messages).
		svcRC := RawProfile{Profile: Profile{
			Packages: svc.Packages,
			Repos:    svc.Repos,
			Env:      svc.Env,
			Files:    svc.Files,
		}, Path: rc.Path}
		if err := validatePackages(svcRC); err != nil {
			return err
		}
		if err := validateRepos(svcRC); err != nil {
			return err
		}
		if err := validateEnv(svcRC); err != nil {
			return err
		}
		if err := validateFiles(svcRC); err != nil {
			return err
		}
		for socketName, socketPath := range svc.Exposes {
			if !profileNameRe.MatchString(socketName) {
				return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: exposes %s: invalid socket name (must match %s)", name, socketName, profileNameRe)}
			}
			if containsControl(socketPath) {
				return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: exposes %s: path must not contain control characters", name, socketName)}
			}
			// A {{ }} template is exempt from the absolute/non-root-prefix rule
			// (it cannot be evaluated here) but still must be free of literal
			// ".."; ResolveTildes re-checks the rendered path.
			if !strings.Contains(socketPath, "{{") {
				if err := checkExposePath(socketPath); err != nil {
					return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: exposes %s: %v", name, socketName, err)}
				}
			}
			if hasDotDot(socketPath) {
				return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: exposes %s: path %q must not contain '..' segments", name, socketName, socketPath)}
			}
		}
		for mountTarget, m := range svc.Mounts {
			if m.Service != "" || m.Socket != "" {
				return ProfileError{Path: rc.Path, Message: fmt.Sprintf("services: %s: mount %s: must not use service/socket (no inter-service dependencies in v1)", name, mountTarget)}
			}
			if err := checkPath(mountTarget, fmt.Sprintf("services: %s: mount target", name)); err != nil {
				return ProfileError{Path: rc.Path, Message: err.Error()}
			}
			if err := checkPath(m.Source, fmt.Sprintf("services: %s: mount source", name)); err != nil {
				return ProfileError{Path: rc.Path, Message: err.Error()}
			}
		}
		for _, paths := range svc.Caches {
			for _, p := range paths {
				if err := checkPath(p, fmt.Sprintf("services: %s: cache path", name)); err != nil {
					return ProfileError{Path: rc.Path, Message: err.Error()}
				}
			}
		}
	}
	return nil
}

func validateMountServices(rc RawProfile) error {
	for target, m := range rc.Mounts {
		hasService := m.Service != ""
		hasSocket := m.Socket != ""
		hasSource := m.Source != ""
		if hasService && !hasSocket {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("mount %s: service requires socket", target)}
		}
		if hasSocket && !hasService {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("mount %s: socket requires service", target)}
		}
		if hasService && hasSource {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("mount %s: must not set source with service/socket", target)}
		}
		if hasService && m.Create {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("mount %s: must not set create with service/socket", target)}
		}
		if hasService && m.Optional {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("mount %s: must not set optional with service/socket", target)}
		}
		if hasService && m.ReadOnly {
			return ProfileError{Path: rc.Path, Message: fmt.Sprintf("mount %s: read_only must be false for service/socket mounts (connect fails on a read-only bind)", target)}
		}
		if hasService {
			svc, ok := rc.Services[m.Service]
			if !ok {
				return ProfileError{Path: rc.Path, Message: fmt.Sprintf("mount %s: service %q not declared", target, m.Service)}
			}
			if _, ok := svc.Exposes[m.Socket]; !ok {
				return ProfileError{Path: rc.Path, Message: fmt.Sprintf("mount %s: socket %q not exposed by service %q", target, m.Socket, m.Service)}
			}
		}
	}
	return nil
}

// ParseMemoryBytes converts a Docker-style memory string to bytes using
// docker/go-units, the same parser Docker's --memory uses. Rejects empty and
// unparseable values.
func ParseMemoryBytes(s string) (int64, error) {
	if strings.TrimSpace(s) == "" {
		return 0, errors.New("empty memory value")
	}
	b, err := units.RAMInBytes(s)
	if err != nil || b <= 0 {
		return 0, fmt.Errorf("invalid memory value %q", s)
	}
	return b, nil
}

// ParseNanoCPUs converts a CPU-count string ("2", "1.5") to nanos, matching
// Docker's --cpus semantics. Rejects NaN, infinities, values <= 0, and values
// that would overflow int64 after scaling (a fractional count above ~9.2e9).
func ParseNanoCPUs(s string) (int64, error) {
	s = strings.TrimSpace(s)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
		return 0, fmt.Errorf("invalid cpu count %q", s)
	}
	n := f * 1e9
	// float64(math.MaxInt64) rounds to 2^63, so `>` would let an exact 2^63
	// through and int64() wrap it negative; `>=` rejects the wrap boundary.
	if n >= math.MaxInt64 {
		return 0, fmt.Errorf("cpu count %q out of range", s)
	}
	return int64(n), nil
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
// rejects empty names, names unsafe for use as a file path (an invalid
// segment, ".."), a reserved-namespace first segment, and single-segment
// names reserved for subcommands. Fragment collisions are checked separately
// by the caller against the catalog.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("profile name is required")
	}
	segs := strings.Split(name, "/")
	for _, seg := range segs {
		if !profileNameRe.MatchString(seg) || strings.Contains(seg, "..") {
			return fmt.Errorf("invalid profile name %q: each segment must match %s and must not contain '..'", name, profileNameRe)
		}
	}
	// The reserved-namespace check applies to the first segment of a
	// hierarchical name only; a bare profile named "core" is allowed (it
	// doesn't collide with built-in "core/..." keys).
	if len(segs) > 1 && segs[0] == "core" {
		return fmt.Errorf("invalid profile name %q: %s is a reserved namespace prefix", name, "core")
	}
	if !strings.Contains(name, "/") && reservedNames[name] {
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
