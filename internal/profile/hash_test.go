package profile

import (
	"testing"
)

func TestComputeServiceHashStableAcrossEnvVariation(t *testing.T) {
	svc := Service{
		Image:   "debian:13-slim",
		Command: []string{"registry"},
		Env:     map[string]string{"X": "{{ .Env.HOME }}"},
		Caches:  map[string]CachePaths{"data": {"/var/lib/registry"}},
	}
	h1 := computeServiceHash(svc)
	h2 := computeServiceHash(svc)
	if h1 != h2 {
		t.Errorf("hash should be stable for identical unresolved definition: %q vs %q", h1, h2)
	}
}

func TestComputeServiceHashDiffersOnImageChange(t *testing.T) {
	svc := Service{Image: "debian:13-slim", Command: []string{"x"}}
	h1 := computeServiceHash(svc)
	svc.Image = "debian:14"
	h2 := computeServiceHash(svc)
	if h1 == h2 {
		t.Error("hash should differ when image changes")
	}
}

func TestComputeServiceHashDiffersOnCacheChange(t *testing.T) {
	svc := Service{
		Image:   "debian:13-slim",
		Command: []string{"x"},
		Caches:  map[string]CachePaths{"data": {"/var/lib/registry"}},
	}
	h1 := computeServiceHash(svc)
	svc.Caches["data"] = CachePaths{"/other/path"}
	h2 := computeServiceHash(svc)
	if h1 == h2 {
		t.Error("hash should differ when caches change")
	}
}

func TestComputeServiceHashDiffersOnLabelChange(t *testing.T) {
	svc := Service{
		Image:   "debian:13-slim",
		Command: []string{"x"},
		Labels:  map[string]string{"foo": "bar"},
	}
	h1 := computeServiceHash(svc)
	svc.Labels["foo"] = "baz"
	h2 := computeServiceHash(svc)
	if h1 == h2 {
		t.Error("hash should differ when labels change")
	}
}

func TestResolveProfileComputesServiceHash(t *testing.T) {
	dir := t.TempDir()
	mustWriteProfile(t, dir, "base.yaml", `version: 1
image: ubuntu
command: ["sh"]
services:
  registry:
    image: debian:13-slim
    command: ["registry"]
`)
	cat, err := LoadProfiles(dir)
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	prof, err := ResolveProfile(cat, "base")
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if prof.Services["registry"].Hash == "" {
		t.Error("ResolveProfile should set Service.Hash")
	}
}
