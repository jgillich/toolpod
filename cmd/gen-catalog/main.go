package main

import (
	"fmt"
	"os"

	"github.com/jgillich/tpd/internal/profile"
)

// gen-catalog regenerates the README built-in profiles table and
// docs/catalog.md from the embedded catalog. Run via `make docs`.
func main() {
	rows, err := profile.ProfilesTable()
	if err != nil {
		fail("generating README table: %v", err)
	}
	doc, err := profile.CatalogDoc()
	if err != nil {
		fail("generating catalog doc: %v", err)
	}

	readme, err := os.ReadFile("README.md")
	if err != nil {
		fail("reading README.md: %v", err)
	}
	patched, err := profile.PatchReadme(readme, rows)
	if err != nil {
		fail("%v", err)
	}
	if err := os.WriteFile("README.md", patched, 0o644); err != nil {
		fail("writing README.md: %v", err)
	}
	if err := os.WriteFile("docs/catalog.md", []byte(doc), 0o644); err != nil {
		fail("writing docs/catalog.md: %v", err)
	}
	fmt.Println("regenerated README.md and docs/catalog.md")
}

func fail(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "gen-catalog: "+format+"\n", args...)
	os.Exit(1)
}
