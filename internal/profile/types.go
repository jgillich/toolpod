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

// Profile is a resolved toolpod profile (after extends-merge and validation).
// YAML tags match the schema in the design doc §4.1.
type Profile struct {
	Version     int                   `yaml:"version"`
	ExtendsList ExtendsList           `yaml:"extends,omitempty"`
	Image       string                `yaml:"image,omitempty"`
	Build       *Build                `yaml:"build,omitempty"`
	Command     []string              `yaml:"command,omitempty"`
	ArgsIfNone  []string              `yaml:"args_if_none,omitempty"`
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
}

// Build is the escape-hatch image source: a Dockerfile + optional depends_on.
type Build struct {
	Dockerfile string   `yaml:"dockerfile"`
	Context    string   `yaml:"context,omitempty"`
	DependsOn  []string `yaml:"depends_on,omitempty"`
}

// Mount is a single bind mount, keyed by container target path.
type Mount struct {
	Source   string `yaml:"source"`
	ReadOnly bool   `yaml:"read_only"`
	Optional bool   `yaml:"optional"`
}

// PortBind publishes a container port to the host. Empty Host means the
// host port is auto-allocated at launch.
type PortBind struct {
	Host     string `yaml:"host,omitempty"`
	HostIP   string `yaml:"host_ip,omitempty"`
	Protocol string `yaml:"protocol,omitempty"`
}

// DeviceBind attaches a host device node into the container.
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
