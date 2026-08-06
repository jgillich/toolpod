package profile

import "testing"

func TestMergeAttributionMapFieldsLastWriter(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"base": {Profile: Profile{
			Tools:  map[string]Tool{"kubectl": {Version: "1.0"}, "k9s": {Version: "0.5"}},
			Caches: map[string]CachePaths{"go": {"~/go"}},
		}},
		"myapp": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{Raw: []string{"base"}},
			Image:       "x",
			Command:     []string{"myapp"},
			Tools:       map[string]Tool{"kubectl": {Version: "1.1"}},
		}},
	})
	res, err := ResolveProfileWithProv(cat, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	base := Contributor{FullName: "core/base", Namespace: "core"}
	app := Contributor{FullName: "core/myapp", Namespace: "core"}
	if got := res.Prov.Tools["kubectl"]; got != app {
		t.Errorf("kubectl attributed to %+v, want %+v (child override)", got, app)
	}
	if got := res.Prov.Tools["k9s"]; got != base {
		t.Errorf("k9s attributed to %+v, want %+v (parent key preserved)", got, base)
	}
	if got := res.Prov.Caches["go"]; got != base {
		t.Errorf("go cache attributed to %+v, want %+v", got, base)
	}
}

func TestMergeAttributionScalarsLastWriter(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"base": {Profile: Profile{
			Image:     "a:latest",
			Command:   []string{"a"},
			TTY:       "true",
			Network:   "none",
			Resources: &Resources{Memory: "512m", CPUs: "1"},
		}},
		"mid": {Profile: Profile{
			ExtendsList: ExtendsList{Raw: []string{"base"}},
		}},
		"myapp": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{Raw: []string{"mid"}},
			Command:     []string{"myapp"},
			Resources:   &Resources{Memory: "1g"},
		}},
	})
	res, err := ResolveProfileWithProv(cat, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	base := Contributor{FullName: "core/base", Namespace: "core"}
	app := Contributor{FullName: "core/myapp", Namespace: "core"}
	if res.Prov.Image != base {
		t.Errorf("image attributed to %+v, want %+v (base, only declarer through mid)", res.Prov.Image, base)
	}
	if res.Prov.Command != app {
		t.Errorf("command attributed to %+v, want %+v (myapp override)", res.Prov.Command, app)
	}
	if res.Prov.TTY != base {
		t.Errorf("tty attributed to %+v, want %+v", res.Prov.TTY, base)
	}
	if res.Prov.Network != base {
		t.Errorf("network attributed to %+v, want %+v", res.Prov.Network, base)
	}
	if res.Prov.Resources.Memory != app {
		t.Errorf("resources.memory attributed to %+v, want %+v (myapp override)", res.Prov.Resources.Memory, app)
	}
	if res.Prov.Resources.CPUs != base {
		t.Errorf("resources.cpus attributed to %+v, want %+v (base, not overridden)", res.Prov.Resources.CPUs, base)
	}
}

func TestMergeAttributionPackagesFirstDeclarer(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"base": {Profile: Profile{Packages: []string{"git", "curl"}}},
		"mid": {Profile: Profile{
			ExtendsList: ExtendsList{Raw: []string{"base"}},
			Packages:    []string{"curl", "vim"},
		}},
		"myapp": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{Raw: []string{"mid"}},
			Image:       "x",
			Command:     []string{"myapp"},
			Packages:    []string{"vim"},
		}},
	})
	res, err := ResolveProfileWithProv(cat, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	base := Contributor{FullName: "core/base", Namespace: "core"}
	mid := Contributor{FullName: "core/mid", Namespace: "core"}
	if res.Prov.Packages["git"] != base {
		t.Errorf("git attributed to %+v, want %+v", res.Prov.Packages["git"], base)
	}
	if res.Prov.Packages["curl"] != base {
		t.Errorf("curl attributed to %+v, want %+v (first declarer wins over later dup)", res.Prov.Packages["curl"], base)
	}
	if res.Prov.Packages["vim"] != mid {
		t.Errorf("vim attributed to %+v, want %+v", res.Prov.Packages["vim"], mid)
	}
}

func TestMergeAttributionPackagesNullResets(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"base": {Profile: Profile{Packages: []string{"git"}}},
		"mid": {
			Profile: Profile{
				ExtendsList: ExtendsList{Raw: []string{"base"}},
			},
			NullKeys: map[string]map[string]bool{"packages": {"*": true}},
		},
		"myapp": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{Raw: []string{"mid"}},
			Image:       "x",
			Command:     []string{"myapp"},
			Packages:    []string{"git"},
		}},
	})
	res, err := ResolveProfileWithProv(cat, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	app := Contributor{FullName: "core/myapp", Namespace: "core"}
	if len(res.Packages) != 1 || res.Packages[0] != "git" {
		t.Fatalf("packages = %v, want [git]", res.Packages)
	}
	if got := res.Prov.Packages["git"]; got != app {
		t.Errorf("after mid's packages: null, re-declared git attributed to %+v, want %+v", got, app)
	}
}
