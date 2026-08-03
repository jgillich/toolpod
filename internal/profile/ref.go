package profile

import (
	"fmt"
	"sort"
	"strings"
)

// ParseRef splits a reference string against the registered namespaces into a
// Ref. A string with no "/" is unqualified (Ref{Namespace: "", Name: s}). A
// string with "/" is matched against the longest registered namespace prefix
// at a segment boundary (ns + "/"); the remainder is the local name. An
// unregistered prefix is a parse error (the "/" belongs to an unknown
// namespace); an empty local name ("core/") is rejected.
func ParseRef(s string, namespaces map[string]bool) (Ref, error) {
	if s == "" {
		return Ref{}, fmt.Errorf("empty reference")
	}
	if !strings.Contains(s, "/") {
		return Ref{Namespace: "", Name: s}, nil
	}
	// Longest-prefix match over registered namespaces. "" has no prefix and
	// never matches a qualified string, so only non-empty namespaces compete.
	prefixes := make([]string, 0, len(namespaces))
	for ns := range namespaces {
		if ns != "" {
			prefixes = append(prefixes, ns)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(prefixes)))
	for _, ns := range prefixes {
		if strings.HasPrefix(s, ns+"/") {
			local := s[len(ns)+1:]
			if local == "" {
				return Ref{}, fmt.Errorf("empty local name in extends: %s", s)
			}
			// Local names are single-segment file basenames; the namespace
			// carries any path prefix. A remaining "/" means the local name
			// has multiple segments, which is not allowed.
			if strings.Contains(local, "/") {
				return Ref{}, fmt.Errorf("invalid local name %q in extends: %s (must be a single segment)", local, s)
			}
			return Ref{Namespace: ns, Name: local}, nil
		}
	}
	return Ref{}, fmt.Errorf("unknown namespace in extends: %s", s)
}

// ResolveRef resolves a Ref to a canonical catalog FullName (an entries key).
// For unqualified names (ref.Namespace == ""), returns the user key (bare name)
// if present, else the core key ("core/"+name). For qualified names, returns
// the qualified key directly (no fallback). Returns ok=false if no entry
// matches.
func (c Catalog) ResolveRef(ref Ref) (string, bool) {
	if ref.Namespace == "" {
		if _, ok := c.entries[ref.Name]; ok {
			return ref.Name, true
		}
		coreKey := "core/" + ref.Name
		if _, ok := c.entries[coreKey]; ok {
			return coreKey, true
		}
		return "", false
	}
	key := ref.FullName()
	_, ok := c.entries[key]
	return key, ok
}
