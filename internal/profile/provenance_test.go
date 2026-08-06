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
