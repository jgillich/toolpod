package profile

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"sort"
	"strconv"
)

func computeServiceHash(svc Service) string {
	h := sha256.New()
	field := func(tag string, vals ...string) {
		writeFramed(h, tag)
		for _, v := range vals {
			writeFramed(h, v)
		}
	}
	field("image", svc.Image)
	for _, p := range sortedStrings(svc.Packages) {
		field("package", p)
	}
	for _, name := range sortedKeys(svc.Repos) {
		r := svc.Repos[name]
		field("repo", name, r.ExtRepo, r.URL, r.KeyURL, r.Suites, r.Components)
	}
	for _, target := range sortedKeys(svc.Files) {
		f := svc.Files[target]
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		field("file", target, strconv.FormatUint(uint64(mode), 8), f.Content)
	}
	for _, c := range svc.Command {
		field("command", c)
	}
	for _, k := range sortedKeys(svc.Env) {
		field("env", k, svc.Env[k])
	}
	for _, k := range sortedKeys(svc.Exposes) {
		field("expose", k, svc.Exposes[k])
	}
	for _, target := range sortedKeys(svc.Mounts) {
		m := svc.Mounts[target]
		field("mount", target, m.Source, m.Service, m.Socket,
			strconv.FormatBool(m.ReadOnly), strconv.FormatBool(m.Optional), strconv.FormatBool(m.Create))
	}
	for _, name := range sortedKeys(svc.Caches) {
		for _, p := range svc.Caches[name] {
			field("cache", name, p)
		}
	}
	for _, k := range sortedKeys(svc.Labels) {
		field("label", k, svc.Labels[k])
	}
	field("privileged", strconv.FormatBool(svc.Privileged))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:])[:12]
}

// writeFramed writes v to h as a 4-byte big-endian length prefix followed by
// the raw bytes, so a value containing newlines or spaces cannot be mistaken
// for a field separator or an adjacent field.
func writeFramed(h io.Writer, v string) {
	var lenBytes [4]byte
	binary.BigEndian.PutUint32(lenBytes[:], uint32(len(v)))
	h.Write(lenBytes[:])
	io.WriteString(h, v)
}

func sortedStrings(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
