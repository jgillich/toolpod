package profile

import (
	"reflect"
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
	if len(p.Extends.Raw) != 1 || p.Extends.Raw[0] != "opencode" {
		t.Errorf("Raw = %v, want [opencode]", p.Extends.Raw)
	}
	if p.Extends.Resolved != nil {
		t.Errorf("Resolved should be nil before Resolve(), got %v", p.Extends.Resolved)
	}
}

func TestExtendsListUnmarshalList(t *testing.T) {
	var p struct {
		Extends ExtendsList `yaml:"extends"`
	}
	if err := yaml.Unmarshal([]byte("extends: [opencode, ssh]\n"), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Extends.Raw) != 2 || p.Extends.Raw[0] != "opencode" || p.Extends.Raw[1] != "ssh" {
		t.Errorf("Raw = %v, want [opencode ssh]", p.Extends.Raw)
	}
}

func TestExtendsListUnmarshalEmpty(t *testing.T) {
	var p struct {
		Extends ExtendsList `yaml:"extends"`
	}
	if err := yaml.Unmarshal([]byte("extends: []\n"), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Extends.Raw) != 0 {
		t.Errorf("Raw = %v, want empty", p.Extends.Raw)
	}
}

func TestExtendsListResolve(t *testing.T) {
	ns := map[string]bool{"": true, "core": true}
	el := ExtendsList{Raw: []string{"core/mise", "javascript"}}
	if err := el.Resolve(ns); err != nil {
		t.Fatal(err)
	}
	want := []Ref{{Namespace: "core", Name: "mise"}, {Namespace: "", Name: "javascript"}}
	if !reflect.DeepEqual(el.Resolved, want) {
		t.Errorf("Resolved = %+v, want %+v", el.Resolved, want)
	}
}

func TestExtendsListResolveHierarchicalFallback(t *testing.T) {
	ns := map[string]bool{"": true, "core": true}
	el := ExtendsList{Raw: []string{"corexy/foo"}}
	if err := el.Resolve(ns); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := []Ref{{Namespace: "", Name: "corexy/foo"}}
	if !reflect.DeepEqual(el.Resolved, want) {
		t.Errorf("Resolved = %+v, want %+v", el.Resolved, want)
	}
}

func TestExtendsListMarshalResolved(t *testing.T) {
	el := ExtendsList{Resolved: []Ref{{Namespace: "core", Name: "mise"}, {Namespace: "", Name: "javascript"}}}
	out, err := yaml.Marshal(el)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "- core/mise\n- javascript\n" {
		t.Errorf("marshaled = %q, want YAML list of canonical names", string(out))
	}
}

func TestExtendsListMarshalRawFallback(t *testing.T) {
	el := ExtendsList{Raw: []string{"opencode", "ssh"}}
	out, err := yaml.Marshal(el)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "- opencode\n- ssh\n" {
		t.Errorf("marshaled = %q, want raw fallback", string(out))
	}
}
