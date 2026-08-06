package profile

import "testing"

func TestContributorTrusted(t *testing.T) {
	cases := []struct {
		c    Contributor
		want bool
	}{
		{Contributor{FullName: "myagent", Namespace: ""}, true},
		{Contributor{FullName: "core/creds/ssh", Namespace: "core"}, false},
		{Contributor{FullName: "github.com/foo/bar", Namespace: "github.com/foo"}, false},
		{Contributor{}, true}, // zero value: unset == trusted
	}
	for _, tc := range cases {
		if got := tc.c.Trusted(); got != tc.want {
			t.Errorf("Contributor%+v.Trusted() = %v, want %v", tc.c, got, tc.want)
		}
	}
}

func TestInitProvenanceStampsLeafContributor(t *testing.T) {
	rc := RawProfile{
		Profile: Profile{
			Mounts: map[string]Mount{
				"~/.ssh": {Source: "~/.ssh"},
			},
			Env:     map[string]string{"DOCKER_HOST": "unix:///var/run/docker.sock"},
			Network: "host",
			Services: map[string]Service{
				"podman": {Image: "img", Command: []string{"run"}},
			},
		},
		Namespace: "core",
		Name:      "creds/ssh",
	}
	prov := initProvenance(rc)
	want := Contributor{FullName: "core/creds/ssh", Namespace: "core"}
	if got := prov.Mounts["~/.ssh"]; got != want {
		t.Errorf("Mounts[~/.ssh] = %+v, want %+v", got, want)
	}
	if got := prov.Env["DOCKER_HOST"]; got != want {
		t.Errorf("Env[DOCKER_HOST] = %+v, want %+v", got, want)
	}
	if prov.Network != want {
		t.Errorf("Network = %+v, want %+v", prov.Network, want)
	}
	if got := prov.Services["podman"]; got != want {
		t.Errorf("Services[podman] = %+v, want %+v", got, want)
	}
}

func TestInitProvenanceUserEntryIsTrusted(t *testing.T) {
	rc := RawProfile{
		Profile:   Profile{Mounts: map[string]Mount{"~/x": {Source: "~/x"}}},
		Namespace: "",
		Name:      "myagent",
	}
	prov := initProvenance(rc)
	if !prov.Mounts["~/x"].Trusted() {
		t.Errorf("user entry should be trusted, got %+v", prov.Mounts["~/x"])
	}
}

func TestInitProvenanceEmptyProfileIsEmpty(t *testing.T) {
	prov := initProvenance(RawProfile{})
	if len(prov.Mounts) != 0 || len(prov.Env) != 0 || len(prov.Services) != 0 {
		t.Errorf("empty profile should have empty provenance, got %+v", prov)
	}
}

func TestInitProvenanceStampsAllFields(t *testing.T) {
	rc := RawProfile{
		Profile: Profile{
			Tools:     map[string]Tool{"kubectl": {Version: "latest"}},
			Caches:    map[string]CachePaths{"go": {"~/go"}},
			Repos:     map[string]Repo{"mise": {ExtRepo: "mise"}},
			Files:     map[string]File{"/etc/x": {Content: "x"}},
			Labels:    map[string]string{"a": "b"},
			Packages:  []string{"git"},
			Image:     "debian:13-slim",
			Command:   []string{"sh"},
			TTY:       "true",
			Resources: &Resources{Memory: "1g", CPUs: "2"},
		},
		Namespace: "core",
		Name:      "mise",
	}
	prov := initProvenance(rc)
	c := Contributor{FullName: "core/mise", Namespace: "core"}
	if prov.Tools["kubectl"] != c {
		t.Errorf("Tools provenance = %+v, want %+v", prov.Tools["kubectl"], c)
	}
	if prov.Caches["go"] != c {
		t.Errorf("Caches provenance = %+v, want %+v", prov.Caches["go"], c)
	}
	if prov.Repos["mise"] != c {
		t.Errorf("Repos provenance = %+v, want %+v", prov.Repos["mise"], c)
	}
	if prov.Files["/etc/x"] != c {
		t.Errorf("Files provenance = %+v, want %+v", prov.Files["/etc/x"], c)
	}
	if prov.Labels["a"] != c {
		t.Errorf("Labels provenance = %+v, want %+v", prov.Labels["a"], c)
	}
	if prov.Packages["git"] != c {
		t.Errorf("Packages provenance = %+v, want %+v", prov.Packages["git"], c)
	}
	if prov.Image != c || prov.Command != c || prov.TTY != c {
		t.Errorf("scalar provenance = {image:%+v command:%+v tty:%+v}, want %+v for all", prov.Image, prov.Command, prov.TTY, c)
	}
	if prov.Resources.Memory != c || prov.Resources.CPUs != c {
		t.Errorf("resources provenance = %+v, want %+v", prov.Resources, c)
	}
}
