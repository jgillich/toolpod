package profile

import "gopkg.in/yaml.v3"

// ExtendsList is a list of profile/fragment names to extend.
// It unmarshals from both a string ("extends: foo") and a list
// ("extends: [foo, bar]"). A single string is normalized to a
// one-element slice.
type ExtendsList []string

func (e *ExtendsList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		*e = ExtendsList{s}
		return nil
	}
	var list []string
	if err := value.Decode(&list); err != nil {
		return err
	}
	*e = ExtendsList(list)
	return nil
}

// Profile is a resolved tpod profile (after extends-merge and validation).
// YAML tags match the schema in the design doc §4.1.
type Profile struct {
	Version     int                   `yaml:"version"`
	ExtendsList ExtendsList           `yaml:"extends,omitempty"`
	Image       string                `yaml:"image,omitempty"`
	Packages    []string              `yaml:"packages,omitempty"`
	Repos       map[string]Repo       `yaml:"repos,omitempty"`
	Files       map[string]File       `yaml:"files,omitempty"`
	Command     []string              `yaml:"command,omitempty"`
	Caches      map[string]string     `yaml:"caches,omitempty"`
	Mounts      map[string]Mount      `yaml:"mounts,omitempty"`
	Env         map[string]string     `yaml:"environment,omitempty"`
	Labels      map[string]string     `yaml:"labels,omitempty"`
	Network     string                `yaml:"network,omitempty"`
	Resources   *Resources            `yaml:"resources,omitempty"`
	TTY         string                `yaml:"tty,omitempty"`
	Tools       map[string]string     `yaml:"tools,omitempty"`
	Ports       map[string]PortBind   `yaml:"ports,omitempty"`
	Devices     map[string]DeviceBind `yaml:"devices,omitempty"`
	Dbus        *DbusConfig           `yaml:"dbus,omitempty"`
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

// Resources are optional resource hints (best-effort; runtime may ignore).
type Resources struct {
	Memory string `yaml:"memory,omitempty"`
	CPUs   string `yaml:"cpus,omitempty"`
}

// RawProfile is a profile as loaded from disk, before extends-merge.
// It carries the source file path for error reporting.
type RawProfile struct {
	Profile
	Path     string                     `yaml:"-"` // file path for error reporting
	NullKeys map[string]map[string]bool `yaml:"-"` // field → set of keys that are explicitly null (delete-on-inherit)
}
