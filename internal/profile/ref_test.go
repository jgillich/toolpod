package profile

import (
	"testing"
)

func TestParseRefUnqualified(t *testing.T) {
	r, err := ParseRef("mise", map[string]bool{"": true, "core": true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Namespace != "" || r.Name != "mise" {
		t.Errorf("got %+v, want {Namespace: \"\", Name: \"mise\"}", r)
	}
}

func TestParseRefQualifiedCore(t *testing.T) {
	r, err := ParseRef("core/mise", map[string]bool{"": true, "core": true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Namespace != "core" || r.Name != "mise" {
		t.Errorf("got %+v, want {Namespace: \"core\", Name: \"mise\"}", r)
	}
}

func TestParseRefRejectsEmptyLocalName(t *testing.T) {
	_, err := ParseRef("core/", map[string]bool{"": true, "core": true})
	if err == nil {
		t.Fatal("expected error for empty local name")
	}
}

func TestParseRefRejectsMultiSegmentLocalName(t *testing.T) {
	// Local names are single-segment; core/foo/bar is not a valid ref even
	// though "core" is a registered namespace.
	_, err := ParseRef("core/foo/bar", map[string]bool{"": true, "core": true})
	if err == nil {
		t.Fatal("expected error for multi-segment local name")
	}
}

func TestParseRefRejectsUnknownNamespace(t *testing.T) {
	_, err := ParseRef("corexy/foo", map[string]bool{"": true, "core": true})
	if err == nil {
		t.Fatal("expected error for unknown namespace corexy (segment-boundary check)")
	}
}

func TestParseRefLongestPrefix(t *testing.T) {
	// Synthetic multi-segment namespace; longest prefix must win.
	ns := map[string]bool{"": true, "core": true, "github.com/user/project": true}
	r, err := ParseRef("github.com/user/project/foo", ns)
	if err != nil {
		t.Fatal(err)
	}
	if r.Namespace != "github.com/user/project" || r.Name != "foo" {
		t.Errorf("got %+v, want {Namespace: \"github.com/user/project\", Name: \"foo\"}", r)
	}
}

func TestParseRefEmptyString(t *testing.T) {
	_, err := ParseRef("", map[string]bool{"": true, "core": true})
	if err == nil {
		t.Fatal("expected error for empty reference")
	}
}

func TestResolveRefUnqualifiedUserShadowsCore(t *testing.T) {
	cat := Catalog{
		entries: map[string]RawProfile{
			"core/mise": {Profile: Profile{Image: "builtin"}, Namespace: "core", Name: "mise"},
			"mise":      {Profile: Profile{Image: "user"}, Namespace: "", Name: "mise"},
		},
		namespaces: map[string]bool{"": true, "core": true},
	}
	got, ok := cat.ResolveRef(Ref{Name: "mise"})
	if !ok {
		t.Fatal("expected ok")
	}
	if got != "mise" {
		t.Errorf("ResolveRef unqualified = %q, want %q (user wins)", got, "mise")
	}
}

func TestResolveRefUnqualifiedFallsBackToCore(t *testing.T) {
	cat := Catalog{
		entries: map[string]RawProfile{
			"core/mise": {Profile: Profile{Image: "builtin"}, Namespace: "core", Name: "mise"},
		},
		namespaces: map[string]bool{"": true, "core": true},
	}
	got, ok := cat.ResolveRef(Ref{Name: "mise"})
	if !ok {
		t.Fatal("expected ok")
	}
	if got != "core/mise" {
		t.Errorf("ResolveRef unqualified fallback = %q, want %q", got, "core/mise")
	}
}

func TestResolveRefQualifiedBypassesUser(t *testing.T) {
	cat := Catalog{
		entries: map[string]RawProfile{
			"core/mise": {Profile: Profile{Image: "builtin"}, Namespace: "core", Name: "mise"},
			"mise":      {Profile: Profile{Image: "user"}, Namespace: "", Name: "mise"},
		},
		namespaces: map[string]bool{"": true, "core": true},
	}
	got, ok := cat.ResolveRef(Ref{Namespace: "core", Name: "mise"})
	if !ok {
		t.Fatal("expected ok")
	}
	if got != "core/mise" {
		t.Errorf("ResolveRef qualified = %q, want %q", got, "core/mise")
	}
}

func TestResolveRefNotFound(t *testing.T) {
	cat := Catalog{
		entries:    map[string]RawProfile{},
		namespaces: map[string]bool{"": true, "core": true},
	}
	if _, ok := cat.ResolveRef(Ref{Name: "nope"}); ok {
		t.Error("expected ok=false for missing name")
	}
	if _, ok := cat.ResolveRef(Ref{Namespace: "core", Name: "nope"}); ok {
		t.Error("expected ok=false for missing qualified name")
	}
}

func TestParseRefRoundTrip(t *testing.T) {
	cases := []string{"mise", "core/mise", "github.com/u/p/mise"}
	ns := map[string]bool{"": true, "core": true, "github.com/u/p": true}
	for _, s := range cases {
		r, err := ParseRef(s, ns)
		if err != nil {
			t.Fatalf("ParseRef(%q): %v", s, err)
		}
		if got := r.FullName(); got != s {
			t.Errorf("ParseRef(%q).FullName() = %q, want %q", s, got, s)
		}
	}
}
