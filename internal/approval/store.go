package approval

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Store persists per-profile approval choices.
type Store interface {
	Load(profileName string) (State, error)
	Save(profileName string, s State) error
}

type State struct {
	Hash     string
	Approved map[string]ApprovedField
}

// ApprovedField represents one field's approved set. Map fields use Keys;
// the scalar network uses Network (nil = never decided, true = approved,
// false = denied).
type ApprovedField struct {
	Keys    []string
	Network *bool
}

var nameSegRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// FSStore writes state files under <root>/approvals/<FullName>.yaml.
type FSStore struct {
	root string
}

func NewFSStore(root string) *FSStore {
	return &FSStore{root: root}
}

func (s *FSStore) pathFor(fullName string) (string, error) {
	segs := splitPath(fullName)
	for _, seg := range segs {
		if seg == "" || seg == ".." || strings.Contains(seg, "..") || !nameSegRe.MatchString(seg) {
			return "", fmt.Errorf("invalid profile name segment %q in %q", seg, fullName)
		}
	}
	return filepath.Join(s.root, "approvals", filepath.FromSlash(fullName)+".yaml"), nil
}

func splitPath(p string) []string {
	var segs []string
	cur := ""
	for _, r := range p {
		if r == '/' {
			segs = append(segs, cur)
			cur = ""
		} else {
			cur += string(r)
		}
	}
	segs = append(segs, cur)
	return segs
}

func (s *FSStore) Load(fullName string) (State, error) {
	path, err := s.pathFor(fullName)
	if err != nil {
		return State{}, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var st State
	if err := yaml.Unmarshal(data, &st); err != nil {
		return State{}, err
	}
	return st, nil
}

func (s *FSStore) Save(fullName string, st State) error {
	path, err := s.pathFor(fullName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// yamlState is the on-disk representation. A field present in Approved
// with nil Keys means "field present, all denied"; a field absent from
// the map means "never decided". This distinction must survive
// round-trip, so State has custom marshal/unmarshal.
//
// The on-disk shape nests dbus sub-fields per spec §4:
//
//	dbus: { talk: [...], own: [...] }
//
// services is a flat list of approved service names (coarse model: one
// item per service, Key = service name), handled by the generic flat
// path like mounts/env/ports. Only dbus retains nested marshaling. The
// Go State.Approved map keys are flat (e.g. "dbus.talk", "services").
type yamlState struct {
	Hash     string       `yaml:"hash"`
	Approved yamlApproved `yaml:"approved,omitempty"`
}

// yamlApproved is the top-level "approved:" block. Top-level scalar/map
// fields are keyed directly; dbus is the only nested sub-object.
type yamlApproved struct {
	Mounts   *yamlField `yaml:"mounts,omitempty"`
	Devices  *yamlField `yaml:"devices,omitempty"`
	Env      *yamlField `yaml:"env,omitempty"`
	Ports    *yamlField `yaml:"ports,omitempty"`
	Network  *bool      `yaml:"network,omitempty"`
	Dbus     *yamlDbus  `yaml:"dbus,omitempty"`
	Services *yamlField `yaml:"services,omitempty"`
}

// yamlField is a pointer-wrapped []string so the three-state distinction
// survives round-trip: a nil pointer (the field is absent from yamlApproved)
// means "never decided"; a non-nil pointer with an empty slice means
// "field present, all denied"; a non-nil pointer with items means
// "approved". It marshals as a bare YAML list (not nested under keys:) to
// match the spec's human-readable on-disk shape (mounts: [~/.ssh], not
// mounts: {keys: [~/.ssh]}).
type yamlField []string

// ptrField wraps an ApprovedField's Keys in a non-nil *yamlField so
// "present, all denied" (empty slice) emits an explicit key rather than
// being omitted by omitempty on the parent pointer.
func ptrField(af ApprovedField) *yamlField {
	f := yamlField(af.Keys)
	return &f
}

func (f *yamlField) UnmarshalYAML(unmarshal func(interface{}) error) error {
	return unmarshal((*[]string)(f))
}

type yamlDbus struct {
	Talk *yamlField `yaml:"talk,omitempty"`
	Own  *yamlField `yaml:"own,omitempty"`
}

func (s State) MarshalYAML() (interface{}, error) {
	out := yamlState{Hash: s.Hash}
	a := yamlApproved{}
	for k, v := range s.Approved {
		switch k {
		case "mounts":
			a.Mounts = ptrField(v)
		case "devices":
			a.Devices = ptrField(v)
		case "env":
			a.Env = ptrField(v)
		case "ports":
			a.Ports = ptrField(v)
		case "network":
			a.Network = v.Network
		case "services":
			a.Services = ptrField(v)
		case "dbus.talk":
			if a.Dbus == nil {
				a.Dbus = &yamlDbus{}
			}
			a.Dbus.Talk = ptrField(v)
		case "dbus.own":
			if a.Dbus == nil {
				a.Dbus = &yamlDbus{}
			}
			a.Dbus.Own = ptrField(v)
		}
	}
	out.Approved = a
	return out, nil
}

func (s *State) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var y yamlState
	if err := unmarshal(&y); err != nil {
		return err
	}
	s.Hash = y.Hash
	s.Approved = map[string]ApprovedField{}
	a := y.Approved
	if a.Mounts != nil {
		s.Approved["mounts"] = ApprovedField{Keys: *a.Mounts}
	}
	if a.Devices != nil {
		s.Approved["devices"] = ApprovedField{Keys: *a.Devices}
	}
	if a.Env != nil {
		s.Approved["env"] = ApprovedField{Keys: *a.Env}
	}
	if a.Ports != nil {
		s.Approved["ports"] = ApprovedField{Keys: *a.Ports}
	}
	if a.Network != nil {
		s.Approved["network"] = ApprovedField{Network: a.Network}
	}
	if a.Services != nil {
		s.Approved["services"] = ApprovedField{Keys: *a.Services}
	}
	if a.Dbus != nil {
		if a.Dbus.Talk != nil {
			s.Approved["dbus.talk"] = ApprovedField{Keys: *a.Dbus.Talk}
		}
		if a.Dbus.Own != nil {
			s.Approved["dbus.own"] = ApprovedField{Keys: *a.Dbus.Own}
		}
	}
	return nil
}
