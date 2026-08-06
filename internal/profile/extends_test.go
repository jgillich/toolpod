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

func TestMergeChainAttributionAcrossExtends(t *testing.T) {
	// Simulate the extends chain myagent -> core/lang/typescript -> core/lang/javascript
	// by calling MergeProfiles twice, the way resolveChain does. Keys from
	// javascript should be attributed to core/lang/javascript, not to
	// typescript or myagent.
	js := RawProfile{
		Profile:   Profile{Env: map[string]string{"JS": "1"}},
		Namespace: "core", Name: "lang/javascript",
	}
	js.Provenance = initProvenance(js)
	ts := RawProfile{
		Profile:   Profile{Env: map[string]string{"TS": "1"}},
		Namespace: "core", Name: "lang/typescript",
	}
	ts.Provenance = initProvenance(ts)
	merged := MergeProfiles(RawProfile{}, js)
	merged = MergeProfiles(merged, ts)
	if merged.Provenance.Env["JS"] != (Contributor{FullName: "core/lang/javascript", Namespace: "core"}) {
		t.Errorf("JS should be attributed to core/lang/javascript, got %+v", merged.Provenance.Env["JS"])
	}
	if merged.Provenance.Env["TS"] != (Contributor{FullName: "core/lang/typescript", Namespace: "core"}) {
		t.Errorf("TS should be attributed to core/lang/typescript, got %+v", merged.Provenance.Env["TS"])
	}
}

func TestResolveChainAttributionAcrossExtends(t *testing.T) {
	// myagent extends core/lang/typescript extends core/lang/javascript.
	// Keys from javascript should be attributed to core/lang/javascript,
	// not to typescript or myagent.
	//
	// NewProfileCatalogForTest stamps Namespace="core"; Name=<map key>.
	// Use bare keys so FullName() == "core/" + key. The test is about
	// attribution across extends, not user-vs-core, so all three are
	// core entries.
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"lang/javascript": {
			Profile: Profile{
				Version: 1, Image: "img", Command: []string{"run"},
				Env: map[string]string{"JS": "1"},
			},
		},
		"lang/typescript": {
			Profile: Profile{
				Version: 1, Image: "img", Command: []string{"run"},
				ExtendsList: ExtendsList{Resolved: []Ref{{Namespace: "core", Name: "lang/javascript"}}},
				Env:         map[string]string{"TS": "1"},
			},
		},
		"myagent": {
			Profile: Profile{
				Version: 1, Image: "img", Command: []string{"run"},
				ExtendsList: ExtendsList{Resolved: []Ref{{Namespace: "core", Name: "lang/typescript"}}},
			},
		},
	})
	res, err := ResolveProfileWithProv(cat, "core/myagent")
	if err != nil {
		t.Fatalf("ResolveProfileWithProv: %v", err)
	}
	if res.Prov.Env["JS"] != (Contributor{FullName: "core/lang/javascript", Namespace: "core"}) {
		t.Errorf("JS should be attributed to core/lang/javascript, got %+v", res.Prov.Env["JS"])
	}
	if res.Prov.Env["TS"] != (Contributor{FullName: "core/lang/typescript", Namespace: "core"}) {
		t.Errorf("TS should be attributed to core/lang/typescript, got %+v", res.Prov.Env["TS"])
	}
	if res.FullName != "core/myagent" {
		t.Errorf("FullName = %q, want core/myagent", res.FullName)
	}
}
