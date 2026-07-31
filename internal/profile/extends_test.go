package profile

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestExtendsListUnmarshalString(t *testing.T) {
	var p struct {
		Extends ExtendsList `yaml:"extends"`
	}
	if err := yaml.Unmarshal([]byte("extends: opencode\n"), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Extends) != 1 || p.Extends[0] != "opencode" {
		t.Errorf("got %v, want [opencode]", p.Extends)
	}
}

func TestExtendsListUnmarshalList(t *testing.T) {
	var p struct {
		Extends ExtendsList `yaml:"extends"`
	}
	if err := yaml.Unmarshal([]byte("extends: [opencode, ssh]\n"), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Extends) != 2 || p.Extends[0] != "opencode" || p.Extends[1] != "ssh" {
		t.Errorf("got %v, want [opencode ssh]", p.Extends)
	}
}

func TestExtendsListUnmarshalEmpty(t *testing.T) {
	var p struct {
		Extends ExtendsList `yaml:"extends"`
	}
	if err := yaml.Unmarshal([]byte("extends: []\n"), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Extends) != 0 {
		t.Errorf("got %v, want empty", p.Extends)
	}
}

func TestExtendsListMarshalList(t *testing.T) {
	e := ExtendsList{"opencode", "ssh"}
	out, err := yaml.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "- opencode\n- ssh\n" {
		t.Errorf("marshaled = %q, want YAML list", string(out))
	}
}
