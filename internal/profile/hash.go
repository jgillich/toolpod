package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

func computeServiceHash(svc Service) string {
	h := sha256.New()
	fmt.Fprintln(h, svc.Image)
	for _, p := range sortedStrings(svc.Packages) {
		fmt.Fprintln(h, p)
	}
	for _, name := range sortedKeys(svc.Repos) {
		r := svc.Repos[name]
		fmt.Fprintf(h, "repo %s %s %s %s %s %s\n", name, r.ExtRepo, r.URL, r.KeyURL, r.Suites, r.Components)
	}
	for _, target := range sortedKeys(svc.Files) {
		f := svc.Files[target]
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		fmt.Fprintf(h, "file %s %o %s\n", target, mode, f.Content)
	}
	for _, c := range svc.Command {
		fmt.Fprintln(h, c)
	}
	for _, k := range sortedKeys(svc.Env) {
		fmt.Fprintf(h, "env %s %s\n", k, svc.Env[k])
	}
	for _, k := range sortedKeys(svc.Exposes) {
		fmt.Fprintf(h, "expose %s %s\n", k, svc.Exposes[k])
	}
	for _, target := range sortedKeys(svc.Mounts) {
		m := svc.Mounts[target]
		fmt.Fprintf(h, "mount %s %s %s %s %v %v %v\n", target, m.Source, m.Service, m.Socket, m.ReadOnly, m.Optional, m.Create)
	}
	for _, name := range sortedKeys(svc.Caches) {
		paths := svc.Caches[name]
		fmt.Fprintf(h, "cache %s %v\n", name, []string(paths))
	}
	for _, k := range sortedKeys(svc.Labels) {
		fmt.Fprintf(h, "label %s %s\n", k, svc.Labels[k])
	}
	fmt.Fprintf(h, "privileged %v\n", svc.Privileged)
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:])[:12]
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
