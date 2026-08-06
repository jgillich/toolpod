package approval

import (
	"testing"

	"github.com/jgillich/tpd/internal/profile"
)

func makeResolvedWithMounts(mounts map[string]profile.Mount, c profile.Contributor) profile.Resolved {
	p := profile.Profile{Mounts: mounts}
	prov := profile.Provenance{Mounts: map[string]profile.Contributor{}}
	for k := range mounts {
		prov.Mounts[k] = c
	}
	return profile.Resolved{Profile: p, Prov: prov}
}

func TestHashStableForSameContent(t *testing.T) {
	res := makeResolvedWithMounts(map[string]profile.Mount{
		"~/.ssh": {Source: "~/.ssh"},
	}, profile.Contributor{FullName: "core/creds/ssh", Namespace: "core"})
	h1 := ComputeApprovalHash(res)
	h2 := ComputeApprovalHash(res)
	if h1 != h2 {
		t.Errorf("hash not stable: %q vs %q", h1, h2)
	}
	if len(h1) != 12 {
		t.Errorf("hash length = %d, want 12", len(h1))
	}
}

func TestHashChangesOnContributorSwap(t *testing.T) {
	mounts := map[string]profile.Mount{"~/.ssh": {Source: "~/.ssh"}}
	a := makeResolvedWithMounts(mounts, profile.Contributor{FullName: "core/creds/ssh", Namespace: "core"})
	b := makeResolvedWithMounts(mounts, profile.Contributor{FullName: "github.com/foo/ssh", Namespace: "github.com/foo"})
	if ComputeApprovalHash(a) == ComputeApprovalHash(b) {
		t.Error("hash should differ when contributor identity differs")
	}
}

func TestHashExcludesUserContributions(t *testing.T) {
	res := makeResolvedWithMounts(map[string]profile.Mount{"~/x": {Source: "~/x"}}, profile.Contributor{FullName: "myagent", Namespace: ""})
	h := ComputeApprovalHash(res)
	// No non-user sensitive fields → empty hash input → deterministic.
	want := ComputeApprovalHash(profile.Resolved{})
	if h != want {
		t.Errorf("user-only contributions should not affect hash; got %q, want %q", h, want)
	}
}

func TestHashPreservesTemplateLiterals(t *testing.T) {
	res := makeResolvedWithMounts(map[string]profile.Mount{
		"{{ .Env.X }}": {Source: "{{ .Env.X }}"},
	}, profile.Contributor{FullName: "core/gui", Namespace: "core"})
	h := ComputeApprovalHash(res)
	if h == ComputeApprovalHash(profile.Resolved{}) {
		t.Error("templated mount should produce a non-empty hash")
	}
}

func TestHashServiceDefinitionChangeRePrompts(t *testing.T) {
	core := profile.Contributor{FullName: "core/services/podman", Namespace: "core"}
	base := profile.Resolved{
		Profile: profile.Profile{Services: map[string]profile.Service{
			"podman": {
				Image: "img", Command: []string{"run"},
				Privileged: true,
				Exposes:    map[string]string{"podman": "/run/podman/podman.sock"},
			},
		}},
		Prov: profile.Provenance{Services: map[string]profile.Contributor{"podman": core}},
	}
	hBase := ComputeApprovalHash(base)

	// privileged flip → different hash.
	flip := base
	flip.Services = map[string]profile.Service{
		"podman": {Image: "img", Command: []string{"run"}, Exposes: map[string]string{"podman": "/run/podman/podman.sock"}},
	}
	if ComputeApprovalHash(flip) == hBase {
		t.Error("hash should change when service privileged flips")
	}

	// new expose socket → different hash.
	newExp := base
	newExp.Services = map[string]profile.Service{
		"podman": {Image: "img", Command: []string{"run"}, Privileged: true, Exposes: map[string]string{
			"podman":   "/run/podman/podman.sock",
			"registry": "/run/podman/registry.sock",
		}},
	}
	if ComputeApprovalHash(newExp) == hBase {
		t.Error("hash should change when a service expose socket is added")
	}

	// new service mount key → different hash.
	newMnt := base
	newMnt.Services = map[string]profile.Service{
		"podman": {Image: "img", Command: []string{"run"}, Privileged: true,
			Exposes: map[string]string{"podman": "/run/podman/podman.sock"},
			Mounts:  map[string]profile.Mount{"/var/lib/containers": {Source: "/var/lib/containers"}},
		},
	}
	if ComputeApprovalHash(newMnt) == hBase {
		t.Error("hash should change when a service mount key is added")
	}

	// new service env key → different hash.
	newEnv := base
	newEnv.Services = map[string]profile.Service{
		"podman": {Image: "img", Command: []string{"run"}, Privileged: true,
			Exposes: map[string]string{"podman": "/run/podman/podman.sock"},
			Env:     map[string]string{"PODMAN": "1"},
		},
	}
	if ComputeApprovalHash(newEnv) == hBase {
		t.Error("hash should change when a service env key is added")
	}
}

func TestHashServiceMountReadOnlyFlipChangesHash(t *testing.T) {
	core := profile.Contributor{FullName: "core/services/podman", Namespace: "core"}
	base := profile.Resolved{
		Profile: profile.Profile{Services: map[string]profile.Service{
			"podman": {
				Image: "img", Command: []string{"run"},
				Mounts: map[string]profile.Mount{"/var/lib/containers": {Source: "/var/lib/containers", ReadOnly: true}},
			},
		}},
		Prov: profile.Provenance{Services: map[string]profile.Contributor{"podman": core}},
	}
	hBase := ComputeApprovalHash(base)

	flip := base
	flip.Services = map[string]profile.Service{
		"podman": {Image: "img", Command: []string{"run"},
			Mounts: map[string]profile.Mount{"/var/lib/containers": {Source: "/var/lib/containers", ReadOnly: false}},
		},
	}
	if ComputeApprovalHash(flip) == hBase {
		t.Error("hash should change when a service mount read_only flips")
	}
}
