package scaffold

import (
	"bytes"
	"strings"

	"gopkg.in/yaml.v3"
)

// mergeYAML merges generated content into an existing profile file at the node
// level so the file's comments, key order, and value styles survive the
// round-trip. extends is unioned (existing entries keep their position, new
// ones appended, deduplicated); every other generated key is only added when
// missing, so a user's own values are never clobbered. An empty or
// non-mapping existing file falls back to the generated content unchanged.
func mergeYAML(existing, generated []byte) (string, error) {
	var ex yaml.Node
	if err := yaml.Unmarshal(existing, &ex); err != nil {
		return "", err
	}
	exMap := mappingNode(&ex)
	if exMap == nil {
		return string(generated), nil
	}

	var gen yaml.Node
	if err := yaml.Unmarshal(generated, &gen); err != nil {
		return "", err
	}
	genMap := mappingNode(&gen)
	if genMap == nil {
		return string(existing), nil
	}
	mergeMap(exMap, genMap)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(detectIndent(existing))
	if err := enc.Encode(&ex); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// mappingNode returns the top-level mapping node of a parsed document, or nil.
func mappingNode(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	if doc != nil && doc.Kind == yaml.MappingNode {
		return doc
	}
	return nil
}

// mergeMap merges src's keys into dst. A key already in dst keeps its node —
// except extends, which is unioned — so generated content only fills gaps.
func mergeMap(dst, src *yaml.Node) {
	for i := 0; i+1 < len(src.Content); i += 2 {
		key := src.Content[i]
		val := src.Content[i+1]
		existing := mappingValue(dst, key.Value)
		if existing == nil {
			dst.Content = append(dst.Content, cloneNode(key), cloneNode(val))
			continue
		}
		if key.Value == "extends" {
			mergeExtends(existing, val)
		}
	}
}

// mappingValue returns the value node for a mapping key, or nil.
func mappingValue(m *yaml.Node, name string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == name {
			return m.Content[i+1]
		}
	}
	return nil
}

// mergeExtends unions the generated extends values into the existing sequence,
// keeping existing entries first and deduplicating by value.
func mergeExtends(existing, generated *yaml.Node) {
	if existing.Kind != yaml.SequenceNode || generated.Kind != yaml.SequenceNode {
		return
	}
	seen := make(map[string]bool, len(existing.Content)+len(generated.Content))
	for _, item := range existing.Content {
		seen[item.Value] = true
	}
	for _, item := range generated.Content {
		if seen[item.Value] {
			continue
		}
		seen[item.Value] = true
		existing.Content = append(existing.Content, cloneNode(item))
	}
}

// detectIndent returns the indentation width used by the file, derived from the
// smallest leading-space run on any non-comment line, so re-encoding keeps the
// file's existing layout. Defaults to 4 (yaml.v3's encoder default) when the
// file has no nested content.
func detectIndent(data []byte) int {
	indent := 4
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if leading := len(line) - len(trimmed); leading > 0 && leading < indent {
			indent = leading
		}
	}
	return indent
}

func cloneNode(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	c := *n
	if n.Content != nil {
		c.Content = make([]*yaml.Node, len(n.Content))
		for i, child := range n.Content {
			c.Content[i] = cloneNode(child)
		}
	}
	return &c
}
