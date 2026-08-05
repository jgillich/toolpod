package profile

import (
	"fmt"
	"sort"
	"strings"
)

// ParseRefForCatalog parses s against the catalog's registered namespaces.
func (c Catalog) ParseRefForCatalog(s string) (Ref, error) {
	return ParseRef(s, c.namespaces)
}

// ParseRef splits a reference string against the registered namespaces into a
// Ref. A string with no "/" is unqualified (Ref{Namespace: "", Name: s}). A
// string with "/" is matched against the longest registered namespace prefix
// at a segment boundary (ns + "/"); the remainder is the local name and may
// itself be multi-segment (lang/go). A slash string matching no registered
// prefix is an unqualified hierarchical name (user namespaces like lang/go
// parse this way), not an error. An empty local name ("core/") is rejected.
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
			// The local name may be multi-segment (lang/go, lang/js/node);
			// the namespace is a registered prefix, everything after it is name.
			return Ref{Namespace: ns, Name: local}, nil
		}
	}
	// No registered prefix matches, so the whole string is an unqualified
	// hierarchical name. This is how user namespaces (lang/go,
	// services/podman) parse: their directory is not a registered prefix.
	return Ref{Namespace: "", Name: s}, nil
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
