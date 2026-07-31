package profile

import (
	"testing"
)

func TestMultiExtendsLeftToRight(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"base":     {Profile: Profile{Version: 1, Image: "base:latest", Command: []string{"sh"}}},
		"ssh":      {Profile: Profile{Mounts: map[string]Mount{"~/.ssh": {Source: "~/.ssh"}}}},
		"npm":      {Profile: Profile{Caches: map[string]string{"npm": "~/.npm"}, Tools: map[string]string{"node": "latest"}}},
		"myprofile": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{"base", "ssh", "npm"},
		}},
	})
	resolved, err := ResolveProfile(cat, "myprofile")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Image != "base:latest" {
		t.Errorf("Image = %q, want base:latest", resolved.Image)
	}
	if _, ok := resolved.Mounts["~/.ssh"]; !ok {
		t.Error("missing ~/.ssh mount from ssh fragment")
	}
	if _, ok := resolved.Caches["npm"]; !ok {
		t.Error("missing npm cache from npm fragment")
	}
	if resolved.Tools["node"] != "latest" {
		t.Error("missing node tool from npm fragment")
	}
}

func TestMultiExtendsLaterOverridesEarlier(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"a": {Profile: Profile{Image: "a:latest", Network: "none"}},
		"b": {Profile: Profile{Image: "b:latest"}},
		"myprofile": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{"a", "b"},
			Command:     []string{"sh"},
		}},
	})
	resolved, err := ResolveProfile(cat, "myprofile")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Image != "b:latest" {
		t.Errorf("Image = %q, want b:latest (later extends wins)", resolved.Image)
	}
	if resolved.Network != "none" {
		t.Errorf("Network = %q, want none", resolved.Network)
	}
}

func TestMultiExtendsBodyWinsLast(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"a": {Profile: Profile{Image: "a:latest"}},
		"myprofile": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{"a"},
			Image:       "myimage:latest",
			Command:     []string{"sh"},
		}},
	})
	resolved, err := ResolveProfile(cat, "myprofile")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Image != "myimage:latest" {
		t.Errorf("Image = %q, want myimage:latest (body wins)", resolved.Image)
	}
}

func TestMultiExtendsWithNestedDepthFirst(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"c": {Profile: Profile{Image: "c:latest", Network: "none"}},
		"a": {Profile: Profile{ExtendsList: ExtendsList{"c"}, Network: "bridge"}},
		"b": {Profile: Profile{Image: "b:latest"}},
		"myprofile": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{"a", "b"},
			Command:     []string{"sh"},
		}},
	})
	resolved, err := ResolveProfile(cat, "myprofile")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Network != "bridge" {
		t.Errorf("Network = %q, want bridge (a overrides c)", resolved.Network)
	}
	if resolved.Image != "b:latest" {
		t.Errorf("Image = %q, want b:latest", resolved.Image)
	}
}

func TestMultiExtendsDuplicateIgnored(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"ssh": {Profile: Profile{Mounts: map[string]Mount{"~/.ssh": {Source: "~/.ssh"}}}},
		"myprofile": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{"ssh", "ssh"},
			Command:     []string{"sh"},
			Image:       "base:latest",
		}},
	})
	resolved, err := ResolveProfile(cat, "myprofile")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Mounts) != 1 {
		t.Errorf("Mounts = %d entries, want 1 (duplicate ignored)", len(resolved.Mounts))
	}
}

func TestMultiExtendsCycleRejected(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"a": {Profile: Profile{ExtendsList: ExtendsList{"b"}, Image: "a:latest", Command: []string{"sh"}}},
		"b": {Profile: Profile{ExtendsList: ExtendsList{"a"}}},
	})
	_, err := ResolveProfile(cat, "a")
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}

func TestSingleStringExtendsStillWorks(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"base": {Profile: Profile{Version: 1, Image: "base:latest", Command: []string{"sh"}}},
		"child": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{"base"}, // normalized from string
		}},
	})
	resolved, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Image != "base:latest" {
		t.Errorf("Image = %q, want base:latest", resolved.Image)
	}
}

func TestOldInlinedProfileStillResolves(t *testing.T) {
	cat := NewProfileCatalogForTest(map[string]RawProfile{
		"base": {Profile: Profile{Version: 1, Image: "base:latest", Command: []string{"sh"}}},
		"child": {Profile: Profile{
			Version:     1,
			ExtendsList: ExtendsList{"base"},
			Mounts:      map[string]Mount{"~/.ssh": {Source: "~/.ssh", ReadOnly: true}},
		}},
	})
	resolved, err := ResolveProfile(cat, "child")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Image != "base:latest" {
		t.Errorf("Image = %q, want base:latest (inherited)", resolved.Image)
	}
	if m, ok := resolved.Mounts["~/.ssh"]; !ok {
		t.Errorf("expected inlined ~/.ssh mount to survive merge, got mounts: %v", resolved.Mounts)
	} else if m.Source != "~/.ssh" {
		t.Errorf("Mount source = %q, want ~/.ssh", m.Source)
	}
}
