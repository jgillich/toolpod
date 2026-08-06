package profile

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/jgillich/tpd/internal/catalog"
)

// README markers delimiting the built-in profiles table. The generator
// replaces only the rows between them, leaving surrounding prose untouched.
const (
	ReadmeProfilesBegin = "<!-- BEGIN tpd profiles -->"
	ReadmeProfilesEnd   = "<!-- END tpd profiles -->"
)

// builtinCatalog loads the embedded built-in catalog without user overrides.
func builtinCatalog() (Catalog, error) {
	return LoadCatalog(catalog.Profiles, catalog.Fragments, "")
}

// ProfilesTable renders the README built-in profiles table (header + rows,
// profiles only, sorted by display name) from the embedded catalog.
func ProfilesTable() (string, error) {
	cat, err := builtinCatalog()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("| Profile | What it is |\n| --- | --- |\n")
	for _, dn := range cat.ProfileDisplayNames() {
		fmt.Fprintf(&b, "| [`%s`](internal/catalog/profiles/%s.yaml) | %s |\n", dn, dn, cat.Description(dn))
	}
	return b.String(), nil
}

// CatalogDoc renders docs/catalog.md: every built-in profile and fragment as
// a heading with its meta description and its full source in a <details>
// spoiler, fragments grouped by their top-level folder.
func CatalogDoc() (string, error) {
	cat, err := builtinCatalog()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("# Catalog\n\n")
	b.WriteString("The built-in profiles and fragments shipped in the tpd binary. Run `make docs` to regenerate this file and the README profiles table.\n\n")
	b.WriteString("## Profiles\n\n")
	for _, dn := range cat.ProfileDisplayNames() {
		if err := writeEntry(&b, catalog.Profiles, "profiles/"+dn+".yaml", dn, cat.Description(dn)); err != nil {
			return "", err
		}
	}
	b.WriteString("## Fragments\n\n")
	groups := map[string][]string{}
	for _, dn := range cat.FragmentDisplayNames() {
		group := "Other"
		if i := strings.Index(dn, "/"); i >= 0 {
			group = dn[:i]
		}
		groups[group] = append(groups[group], dn)
	}
	for _, group := range sortedKeys(groups) {
		fmt.Fprintf(&b, "### %s\n\n", group)
		for _, dn := range groups[group] {
			if err := writeEntry(&b, catalog.Fragments, "fragments/"+dn+".yaml", dn, cat.Description(dn)); err != nil {
				return "", err
			}
		}
	}
	return b.String(), nil
}

func writeEntry(b *strings.Builder, fsys fs.ReadFileFS, srcPath, name, desc string) error {
	data, err := fsys.ReadFile(srcPath)
	if err != nil {
		return err
	}
	src := strings.TrimRight(string(data), "\n")
	fmt.Fprintf(b, "### `%s`\n\n", name)
	if desc != "" {
		fmt.Fprintf(b, "%s\n\n", desc)
	}
	fmt.Fprintf(b, "<details><summary>Source</summary>\n\n```yaml\n%s\n```\n\n</details>\n\n", src)
	return nil
}

// PatchReadme replaces the profiles table between the README markers with
// rows (the full table including the header), returning the patched content.
func PatchReadme(data []byte, rows string) ([]byte, error) {
	s := string(data)
	begin := strings.Index(s, ReadmeProfilesBegin)
	end := strings.Index(s, ReadmeProfilesEnd)
	if begin < 0 || end < 0 || end < begin {
		return nil, fmt.Errorf("README is missing the %s markers", ReadmeProfilesBegin)
	}
	start := begin + len(ReadmeProfilesBegin)
	out := s[:start] + "\n\n" + rows + "\n" + s[end:]
	return []byte(out), nil
}
