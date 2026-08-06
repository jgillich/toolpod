package profile

import "testing"

func TestChainEntriesInPreOrderDeduped(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"base": {Profile: Profile{Image: "base", Packages: []string{"git"}}},
		"a": {Profile: Profile{
			ExtendsList: ExtendsList{Raw: []string{"base"}},
			Tools:       map[string]Tool{"a": {Version: "1"}},
		}},
		"b": {Profile: Profile{
			ExtendsList: ExtendsList{Raw: []string{"base"}},
			Tools:       map[string]Tool{"b": {Version: "1"}},
		}},
		"myapp": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{Raw: []string{"a", "b"}},
			Command:     []string{"myapp"},
		}},
	})
	res, err := ResolveProfileWithProv(cat, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(res.Chain))
	for i, e := range res.Chain {
		names[i] = e.FullName
	}
	want := []string{"core/myapp", "core/a", "core/base", "core/b"}
	if len(names) != len(want) {
		t.Fatalf("chain = %v, want %v (shared parent once)", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("chain[%d] = %q, want %q (full chain %v)", i, names[i], want[i], names)
		}
	}
	if len(res.Chain[0].Extends) == 0 {
		t.Errorf("root ChainEntry should record its own extends: %+v", res.Chain[0])
	}
}
