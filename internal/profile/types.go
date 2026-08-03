package profile

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Ref is a parsed-but-not-yet-resolved reference to a profile or fragment.
// Namespace == "" means unqualified (resolve via user-first-then-core fallback);
// any other value ("core", a future remote namespace) means qualified (direct
// lookup, no fallback).
type Ref struct {
	Namespace string
	Name      string
}

// FullName returns the canonical string form: "ns/name", or the bare name
// when Namespace is "".
func (r Ref) FullName() string {
	if r.Namespace == "" {
		return r.Name
	}
	return r.Namespace + "/" + r.Name
}

// ExtendsList is the yaml-decoded extends field. Raw holds the strings as
// written; Resolved is filled by Resolve splitting each Raw string against the
// registered namespaces. MarshalYAML emits Resolved (canonical strings) when
// available, else Raw (for round-tripping un-resolved lists).
type ExtendsList struct {
	Raw      []string `yaml:"-"`
	Resolved []Ref    `yaml:"-"`
}

// UnmarshalYAML decodes a scalar or list of strings into Raw. No namespace
// splitting happens here (yaml.v3 gives no context). Resolved stays nil.
func (e *ExtendsList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		e.Raw = []string{s}
		return nil
	}
	var list []string
	if err := value.Decode(&list); err != nil {
		return err
	}
	e.Raw = list
	return nil
}

// MarshalYAML emits Resolved (if non-empty) as canonical strings, else Raw.
func (e ExtendsList) MarshalYAML() (interface{}, error) {
	if len(e.Resolved) > 0 {
		out := make([]string, len(e.Resolved))
		for i, r := range e.Resolved {
			out[i] = r.FullName()
		}
		return out, nil
	}
	if len(e.Raw) > 0 {
		return e.Raw, nil
	}
	return nil, nil
}

// Resolve splits each Raw string against the registered namespaces into
// Resolved. Idempotent. An unregistered prefix or empty local name is an error.
func (e *ExtendsList) Resolve(namespaces map[string]bool) error {
	if len(e.Raw) == 0 {
		e.Resolved = nil
		return nil
	}
	resolved := make([]Ref, len(e.Raw))
	for i, s := range e.Raw {
		r, err := ParseRef(s, namespaces)
		if err != nil {
			return err
		}
		resolved[i] = r
	}
	e.Resolved = resolved
	return nil
}

// Profile is a resolved tpd profile (after extends-merge and validation).
// YAML tags match the schema in the design doc §4.1.
type Profile struct {
	Version     int                   `yaml:"version"`
	ExtendsList ExtendsList           `yaml:"extends,omitempty"`
	Image       string                `yaml:"image,omitempty"`
	Packages    []string              `yaml:"packages,omitempty"`
	Repos       map[string]Repo       `yaml:"repos,omitempty"`
	Files       map[string]File       `yaml:"files,omitempty"`
	Command     []string              `yaml:"command,omitempty"`
	Caches      map[string]CachePaths `yaml:"caches,omitempty"`
	Mounts      map[string]Mount      `yaml:"mounts,omitempty"`
	Env         map[string]string     `yaml:"environment,omitempty"`
	Labels      map[string]string     `yaml:"labels,omitempty"`
	Network     string                `yaml:"network,omitempty"`
	Resources   *Resources            `yaml:"resources,omitempty"`
	TTY         string                `yaml:"tty,omitempty"`
	Tools       map[string]Tool       `yaml:"tools,omitempty"`
	Ports       map[string]PortBind   `yaml:"ports,omitempty"`
	Devices     map[string]DeviceBind `yaml:"devices,omitempty"`
	Dbus        *DbusConfig           `yaml:"dbus,omitempty"`
}

// Tool is a single mise tool: the version plus optional verification
// metadata. SHA256 is a universal asset digest; SHA256ByArch keys are the
// schema's arch set ("amd64", "aarch64"), which the appimage backend maps its
// RUNTIME.archType to. Decodes from a YAML scalar (the version) or a map
// ({version, sha256}).
type Tool struct {
	Version      string
	SHA256       string
	SHA256ByArch map[string]string
}

func (t *Tool) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		return node.Decode(&t.Version)
	case yaml.MappingNode:
		var raw struct {
			Version string    `yaml:"version"`
			SHA256  yaml.Node `yaml:"sha256"`
		}
		if err := node.Decode(&raw); err != nil {
			return err
		}
		t.Version = raw.Version
		switch raw.SHA256.Kind {
		case 0:
			return nil
		case yaml.ScalarNode:
			return raw.SHA256.Decode(&t.SHA256)
		case yaml.MappingNode:
			return raw.SHA256.Decode(&t.SHA256ByArch)
		}
	}
	return fmt.Errorf("tools value must be a version string or a {version, sha256} map")
}

func (t Tool) MarshalYAML() (interface{}, error) {
	if len(t.SHA256ByArch) > 0 {
		return struct {
			Version string            `yaml:"version"`
			SHA256  map[string]string `yaml:"sha256"`
		}{t.Version, t.SHA256ByArch}, nil
	}
	if t.SHA256 == "" {
		return t.Version, nil
	}
	return struct {
		Version string `yaml:"version"`
		SHA256  string `yaml:"sha256"`
	}{t.Version, t.SHA256}, nil
}

// DbusConfig is a flatpak-style session-bus allowlist. Talk names may be
// called; Own names may be acquired. Each name maps to an empty object; a
// null value drops an inherited name (the pointer distinguishes allow from
// remove). Values are maps (not lists) so profiles extending a base merge
// their names key-by-key.
type DbusConfig struct {
	Talk map[string]*struct{} `yaml:"talk,omitempty"`
	Own  map[string]*struct{} `yaml:"own,omitempty"`
}

type Mount struct {
	Source   string `yaml:"source"`
	ReadOnly bool   `yaml:"read_only"`
	Optional bool   `yaml:"optional"`
	Create   bool   `yaml:"create,omitempty"` // mkdir the source if missing (directories only)
}

// UnmarshalYAML defaults read_only to true, so a mount without an explicit
// read_only key is read-only rather than read-write.
func (m *Mount) UnmarshalYAML(value *yaml.Node) error {
	type plain Mount
	var raw plain
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*m = Mount(raw)
	m.ReadOnly = true
	if value.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(value.Content); i += 2 {
			if value.Content[i].Value == "read_only" {
				if err := value.Content[i+1].Decode(&m.ReadOnly); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}

// MarshalYAML omits the default read_only: true, keeping only explicit
// read_only: false for writable mounts.
func (m Mount) MarshalYAML() (interface{}, error) {
	type plain Mount
	out := struct {
		Source   string `yaml:"source"`
		ReadOnly *bool  `yaml:"read_only,omitempty"`
		Optional bool   `yaml:"optional"`
		Create   bool   `yaml:"create,omitempty"`
	}{
		Source:   m.Source,
		Optional: m.Optional,
		Create:   m.Create,
	}
	if !m.ReadOnly {
		ro := false
		out.ReadOnly = &ro
	}
	return out, nil
}

// PortBind publishes a container port to the host. Empty Host means the
// host port is auto-allocated at launch.
type PortBind struct {
	Host     string `yaml:"host,omitempty"`
	HostIP   string `yaml:"host_ip,omitempty"`
	Protocol string `yaml:"protocol,omitempty"`
}

// Repo is a single extra apt source, keyed by its merge identity (a logical
// repo name). Either ExtRepo (an extrepo catalog name) or a fully inline
// custom repo (URL/KeyURL/Suites/Components) must be set.
type Repo struct {
	ExtRepo    string `yaml:"extrepo,omitempty"`
	URL        string `yaml:"url,omitempty"`
	KeyURL     string `yaml:"key_url,omitempty"`
	Suites     string `yaml:"suites,omitempty"`
	Components string `yaml:"components,omitempty"`
}

// File is a single file written into the container at launch, keyed by its
// target path. Content is embedded inline and rendered as a {{ }} template;
// Mode is the raw permission bits (default 0644).
type File struct {
	Content string `yaml:"content"`
	Mode    uint32 `yaml:"mode,omitempty"`
}

type DeviceBind struct {
	Source      string `yaml:"source,omitempty"`
	Permissions string `yaml:"permissions,omitempty"`
	Cgroup      bool   `yaml:"cgroup,omitempty"`
}

// CachePaths is the set of container paths a single cache volume backs. A
// scalar ("caches: {foo: ~/.foo}") and a list both decode; a single path
// marshals back as a scalar so single-path caches read naturally in show.
type CachePaths []string

func (c *CachePaths) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		*c = CachePaths{s}
		return nil
	}
	var list []string
	if err := value.Decode(&list); err != nil {
		return err
	}
	*c = CachePaths(list)
	return nil
}

func (c CachePaths) MarshalYAML() (interface{}, error) {
	if len(c) == 1 {
		return c[0], nil
	}
	return []string(c), nil
}

// Resources are optional resource hints (best-effort; runtime may ignore).
type Resources struct {
	Memory string `yaml:"memory,omitempty"`
	CPUs   string `yaml:"cpus,omitempty"`
}

// RawProfile is a profile as loaded from disk, before extends-merge. It
// carries its source identity (Namespace + Name) and file path. Namespace is
// "core" for embedded built-ins, "" for user files, or a future remote
// namespace ("github.com/user/project"). Name is the local single-segment name
// (file basename). FullName is the canonical catalog key; DisplayName is the
// unqualified name used in user-facing output.
type RawProfile struct {
	Profile
	Namespace string                    `yaml:"-"` // source identity, stamped by loaders
	Name      string                    `yaml:"-"` // local single-segment name (file basename)
	Path      string                    `yaml:"-"` // file path for error reporting
	NullKeys  map[string]map[string]bool `yaml:"-"` // field → set of keys that are explicitly null (delete-on-inherit)
}

// FullName is the canonical catalog key and the qualified YAML/string form.
func (rc RawProfile) FullName() string {
	if rc.Namespace == "" {
		return rc.Name
	}
	return rc.Namespace + "/" + rc.Name
}

// DisplayName is the unqualified name used in user-facing output (list, wizard).
func (rc RawProfile) DisplayName() string {
	return rc.Name
}
