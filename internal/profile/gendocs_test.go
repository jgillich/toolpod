package profile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot returns the repository root by walking up from this test file.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

func TestBuiltinsHaveMetaDescriptions(t *testing.T) {
	cat, err := builtinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range cat.Names() {
		rc, _ := cat.Get(key)
		if rc.Meta == nil || strings.TrimSpace(rc.Meta.Description) == "" {
			t.Errorf("%s is missing a meta.description (add one, then run `make docs`)", key)
		}
	}
}

func TestProfilesTable(t *testing.T) {
	rows, err := ProfilesTable()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rows, "| Profile | What it is |\n| --- | --- |\n") {
		t.Errorf("table must start with the header, got:\n%s", rows)
	}
	if !strings.Contains(rows, "| [`mise`](internal/catalog/profiles/mise.yaml) |") {
		t.Errorf("table must list mise with a description, got:\n%s", rows)
	}
	// Profiles only — no fragment rows.
	if strings.Contains(rows, "fragments/") {
		t.Errorf("README table must not include fragments:\n%s", rows)
	}
}

func TestCatalogDocStructure(t *testing.T) {
	doc, err := CatalogDoc()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, "## Profiles") || !strings.Contains(doc, "## Fragments") {
		t.Errorf("doc must have Profiles and Fragments sections:\n%s", doc)
	}
	if !strings.Contains(doc, "### `mise`") {
		t.Errorf("doc must list the mise profile")
	}
	for _, group := range []string{"cloud", "gui", "infra", "toolchain", "service", "vcs"} {
		if !strings.Contains(doc, "### "+group) {
			t.Errorf("doc must group fragments under %s", group)
		}
	}
	if !strings.Contains(doc, "<details><summary>Source</summary>") {
		t.Errorf("doc must embed each entry's source in a spoiler")
	}
	if !strings.Contains(doc, "### `toolchain/go`") || !strings.Contains(doc, "Go toolchain with GOPATH cache") {
		t.Errorf("doc must list toolchain/go with its description")
	}
}

func TestDocsUpToDate(t *testing.T) {
	root := repoRoot(t)

	rows, err := ProfilesTable()
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	patched, err := PatchReadme(readme, rows)
	if err != nil {
		t.Fatal(err)
	}
	if string(patched) != string(readme) {
		t.Errorf("README.md built-in profiles table is stale; run `make docs`")
	}

	doc, err := CatalogDoc()
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(filepath.Join(root, "docs", "catalog.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(doc) != string(committed) {
		t.Errorf("docs/catalog.md is stale; run `make docs`")
	}
}
