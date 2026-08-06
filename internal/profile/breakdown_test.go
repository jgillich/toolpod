package profile

import (
	"strings"
	"testing"
)

func TestProvenanceYAMLShadowing(t *testing.T) {
	cat := Catalog{
		entries: map[string]RawProfile{
			"core/t3code": {Profile: Profile{
				Version:     1,
				ExtendsList: ExtendsList{Raw: []string{"claude"}},
				Image:       "debian:13-slim",
				Command:     []string{"t3code"},
			}, Namespace: "core", Name: "t3code", Path: "built-in:profiles/t3code.yaml"},
			"claude": {Profile: Profile{
				ExtendsList: ExtendsList{Raw: []string{"core/claude", "core/infra/kubernetes"}},
			}, Namespace: "", Name: "claude", Path: "/home/u/.config/tpd/profiles/claude.yaml"},
			"core/claude": {Profile: Profile{
				Tools: map[string]Tool{"claude": {Version: "latest"}},
			}, Namespace: "core", Name: "claude", Path: "built-in:profiles/claude.yaml"},
			"core/infra/kubernetes": {Profile: Profile{
				Tools: map[string]Tool{"kubectl": {Version: "latest"}},
			}, Namespace: "core", Name: "infra/kubernetes", Path: "built-in-fragment:fragments/infra/kubernetes.yaml"},
		},
		namespaces: map[string]bool{"": true, "core": true},
		fragments:  map[string]bool{"core/infra/kubernetes": true},
	}
	for _, k := range []string{"core/t3code", "claude", "core/claude", "core/infra/kubernetes"} {
		e := cat.entries[k]
		if err := e.ExtendsList.Resolve(map[string]bool{"": true, "core": true}); err != nil {
			t.Fatal(err)
		}
		cat.entries[k] = e
	}
	res, err := ResolveProfileWithProv(cat, "core/t3code")
	if err != nil {
		t.Fatal(err)
	}
	out, err := res.ProvenanceYAML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "# core/t3code  (built-in:profiles/t3code.yaml)") {
		t.Errorf("missing root header:\n%s", out)
	}
	if !strings.Contains(out, "# claude  (/home/u/.config/tpd/profiles/claude.yaml)") {
		t.Errorf("missing user shadow header (root cause):\n%s", out)
	}
	if !strings.Contains(out, "# core/infra/kubernetes  (built-in-fragment:fragments/infra/kubernetes.yaml)") {
		t.Errorf("missing fragment header:\n%s", out)
	}
	if !strings.Contains(out, "kubectl: latest") {
		t.Errorf("kubectl should appear under its declaring fragment:\n%s", out)
	}
	if strings.Count(out, "claude: latest") != 1 {
		t.Errorf("claude: latest should appear exactly once (core/claude, not overridden):\n%s", out)
	}
	if !strings.HasPrefix(out, "# ") {
		t.Errorf("output should start with the root header:\n%s", out)
	}
}

func TestProvenanceYAMLOverrideHidesParent(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"base": {Profile: Profile{
			Image:  "x",
			Tools:  map[string]Tool{"kubectl": {Version: "1.0"}},
		}},
		"myapp": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{Raw: []string{"base"}},
			Command:     []string{"myapp"},
			Tools:       map[string]Tool{"kubectl": {Version: "1.1"}},
		}},
	})
	res, err := ResolveProfileWithProv(cat, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	out, err := res.ProvenanceYAML()
	if err != nil {
		t.Fatal(err)
	}
	base := `kubectl: "1.0"`
	if strings.Contains(out, base) {
		t.Errorf("parent's overridden kubectl must not appear:\n%s", out)
	}
	if !strings.Contains(out, `kubectl: "1.1"`) {
		t.Errorf("child's kubectl override should appear:\n%s", out)
	}
}
