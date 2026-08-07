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

// These pairs all produced identical hashes under the old newline/space-delimited
// framing; length-prefixed fields must keep them distinct.

func TestComputeServiceHashFileContentNewlineDistinctFromCommand(t *testing.T) {
	svcNewline := Service{
		Image: "debian:13-slim",
		Files: map[string]File{"/f": {Content: "a\nb"}},
	}
	svcSplit := Service{
		Image:   "debian:13-slim",
		Files:   map[string]File{"/f": {Content: "a"}},
		Command: []string{"b"},
	}
	if computeServiceHash(svcNewline) == computeServiceHash(svcSplit) {
		t.Error("newline in file content must not forge a command arg")
	}
}

func TestComputeServiceHashEnvValueNewlineDistinctFromSplitEntries(t *testing.T) {
	svcNewline := Service{
		Image: "debian:13-slim",
		Env:   map[string]string{"A": "x\nenv B y"},
	}
	svcSplit := Service{
		Image: "debian:13-slim",
		Env:   map[string]string{"A": "x", "B": "y"},
	}
	if computeServiceHash(svcNewline) == computeServiceHash(svcSplit) {
		t.Error("newline in an env value must not forge another env entry")
	}
}

func TestComputeServiceHashEnvEntryDistinctFromCommandArg(t *testing.T) {
	svcEnv := Service{
		Image: "debian:13-slim",
		Env:   map[string]string{"foo": "bar"},
	}
	svcCommand := Service{
		Image:   "debian:13-slim",
		Command: []string{"env foo bar"},
	}
	if computeServiceHash(svcEnv) == computeServiceHash(svcCommand) {
		t.Error("an env entry must not hash the same as a command arg shaped like its line")
	}
}

func TestComputeServiceHashMountFieldSpaceSplitDistinct(t *testing.T) {
	svcTargetShort := Service{
		Image:  "debian:13-slim",
		Mounts: map[string]Mount{"a": {Source: "b c"}},
	}
	svcTargetLong := Service{
		Image:  "debian:13-slim",
		Mounts: map[string]Mount{"a b": {Source: "c"}},
	}
	if computeServiceHash(svcTargetShort) == computeServiceHash(svcTargetLong) {
		t.Error("space-ambiguous mount target/source split must not collide")
	}
}

func TestComputeServiceHashMountSourceNewlineDistinct(t *testing.T) {
	svcNewline := Service{
		Image:  "debian:13-slim",
		Mounts: map[string]Mount{"/m": {Source: "a\nb"}},
	}
	svcSplit := Service{
		Image:   "debian:13-slim",
		Mounts:  map[string]Mount{"/m": {Source: "a"}},
		Command: []string{"b"},
	}
	if computeServiceHash(svcNewline) == computeServiceHash(svcSplit) {
		t.Error("newline in a mount source must not be confused with a command arg")
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
