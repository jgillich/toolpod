package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
)

// expectedTag mirrors DerivedTag's algorithm so tests assert the contract
// (determinism, sort-normalisation, namespace) rather than re-implement
// the function. If the algorithm intentionally changes, these tests
// should be updated alongside DerivedTag.
func expectedTag(baseID string, packages []string) string {
	if len(packages) == 0 {
		return ""
	}
	sorted := append([]string(nil), packages...)
	// sort
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j-1] > sorted[j]; j-- {
			sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
		}
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s", baseID, join(sorted, "\x01"))
	return "tpod/packages:" + hex.EncodeToString(h.Sum(nil)[:8])
}

func join(ss []string, sep string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}

func TestDerivedTag(t *testing.T) {
	const baseID = "sha256:abc123"
	tests := []struct {
		name     string
		baseID   string
		packages []string
	}{
		{"empty packages slice returns empty tag", baseID, nil},
		{"empty base id with packages still hashes", "", []string{"git"}},
		{"single package", baseID, []string{"git"}},
		{"order independent: git then curl", baseID, []string{"git", "curl"}},
		{"order independent: curl then git", baseID, []string{"curl", "git"}},
		{"duplicates preserved", baseID, []string{"git", "git"}},
		{"different base id different tag", "sha256:other", []string{"git"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DerivedTag(tt.baseID, tt.packages)
			want := expectedTag(tt.baseID, tt.packages)
			if got != want {
				t.Errorf("DerivedTag(%q, %v) = %q, want %q", tt.baseID, tt.packages, got, want)
			}
			// Empty-packages invariant: literal "" return, no tag prefix.
			if len(tt.packages) == 0 && got != "" {
				t.Errorf("empty packages must return empty string, got %q", got)
			}
		})
	}
}

func TestDerivedTagOrderIndependent(t *testing.T) {
	const baseID = "sha256:abcdef"
	pkgs := []string{"libxml2-dev", "libicu-dev", "libonig-dev", "libzip-dev", "bison", "re2c"}
	a := DerivedTag(baseID, append([]string(nil), pkgs...))
	b := DerivedTag(baseID, []string{"re2c", "bison", "libzip-dev", "libonig-dev", "libicu-dev", "libxml2-dev"})
	if a != b {
		t.Errorf("DerivedTag must be sort-normalised: got %q vs %q", a, b)
	}
}

func TestDerivedTagTagShape(t *testing.T) {
	tag := DerivedTag("sha256:abc", []string{"git"})
	const prefix = "tpod/packages:"
	if len(tag) != len(prefix)+16 {
		t.Errorf("tag hash must be 16 hex chars: %q (suffix len %d)", tag, len(tag)-len(prefix))
	}
	for i := len(prefix); i < len(tag); i++ {
		c := tag[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("tag suffix must be lowercase hex: %q (char %q at %d)", tag, c, i)
		}
	}
}

func TestDerivedTagNoProfileName(t *testing.T) {
	// Cross-profile sharing invariant: identical (base, packages) ⇒
	// identical tag. Profile name is not a hash input.
	const baseID = "sha256:same"
	pkgs := []string{"libssl-dev", "libxml2-dev"}
	if a, b := DerivedTag(baseID, pkgs), DerivedTag(baseID, pkgs); a != b {
		t.Errorf("identical (base, packages) must produce identical tags: %q vs %q", a, b)
	}
}