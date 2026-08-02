package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const testExtrepoIndex = `---
1password:
  policy: non-free
  gpg-key-file: 1password.asc
  gpg-key-checksum:
    sha256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  source:
    Architectures: amd64
    Components: main
    Suites: stable
    Types: deb
    URIs: https://downloads.1password.com/linux/debian/amd64
mise:
  policy: main
  gpg-key-file: mise.asc
  gpg-key-checksum:
    sha256: a765dfe3aaa609ef7e1f820ccd6f807cc96d522c5870c8d4073284799e3001cd
  source:
    Architectures: amd64 arm64
    Components: main
    Suites: stable
    Types: deb
    URIs: https://mise.jdx.dev/deb
bananas:
  policies:
    main: main
  gpg-key-file: bananas.asc
  gpg-key-checksum:
    sha256: 1593ebd78ca1beec1ac5bb59210ea03ce1ecc100be721135ece5db0992507f9c
  source:
    Architectures: arm64
    Components: <COMPONENTS>
    Suites: trixie-bananas
    Types: deb deb-src
    URIs: https://bananas-archive.debian.net/bananas-archive
`

// testMiseKey's sha256 must match the mise gpg-key-checksum above.
const testMiseKey = "fake mise key bytes"

func TestFetchExtrepoIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/trixie/index.yaml" {
			w.Write([]byte(testExtrepoIndex))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	old := extrepoCatalogBase
	extrepoCatalogBase = srv.URL
	defer func() { extrepoCatalogBase = old }()

	index, err := fetchExtrepoIndex(context.Background(), "trixie")
	if err != nil {
		t.Fatalf("fetchExtrepoIndex: %v", err)
	}
	mise, ok := index["mise"]
	if !ok {
		t.Fatal("catalog must contain mise")
	}
	if mise.Source["URIs"] != "https://mise.jdx.dev/deb" {
		t.Errorf("mise URIs = %q", mise.Source["URIs"])
	}
	if mise.GPGKeyChecksum.SHA256 != "a765dfe3aaa609ef7e1f820ccd6f807cc96d522c5870c8d4073284799e3001cd" {
		t.Errorf("mise key checksum = %q", mise.GPGKeyChecksum.SHA256)
	}
}

func TestFetchExtrepoIndexMissingVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	old := extrepoCatalogBase
	extrepoCatalogBase = srv.URL
	defer func() { extrepoCatalogBase = old }()

	if _, err := fetchExtrepoIndex(context.Background(), "bogus"); err == nil {
		t.Error("fetchExtrepoIndex must fail for missing catalog version")
	}
}

func TestExtrepoEntryComponentsPolicy(t *testing.T) {
	index, err := parseTestIndex(t)
	if err != nil {
		t.Fatal(err)
	}
	// Scalar policy: main → enabled, no component substitution needed.
	comps, err := index["mise"].components()
	if err != nil {
		t.Fatalf("mise components: %v", err)
	}
	if comps != "" {
		t.Errorf("mise components = %q, want empty", comps)
	}
	// Scalar policy: non-free → rejected.
	if _, err := index["1password"].components(); err == nil {
		t.Error("non-free repo must be rejected under main-only policy")
	}
	// policies map: only main components survive.
	comps, err = index["bananas"].components()
	if err != nil {
		t.Fatalf("bananas components: %v", err)
	}
	if comps != "main" {
		t.Errorf("bananas components = %q, want main", comps)
	}
}

func TestExtrepoEntryRenderSources(t *testing.T) {
	index, err := parseTestIndex(t)
	if err != nil {
		t.Fatal(err)
	}
	got := index["mise"].renderSources("mise", "", "/etc/apt/keyrings/mise.asc")
	for _, want := range []string{
		"Architectures: amd64 arm64",
		"Components: main",
		"URIs: https://mise.jdx.dev/deb",
		"Signed-By: /etc/apt/keyrings/mise.asc",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("renderSources must contain %q:\n%s", want, got)
		}
	}
	// <COMPONENTS> substitution in the policies-map variant.
	got = index["bananas"].renderSources("bananas", "main", "/etc/apt/keyrings/bananas.asc")
	if !strings.Contains(got, "Components: main") {
		t.Errorf("bananas render must substitute <COMPONENTS>:\n%s", got)
	}
}

func TestResolveExtrepoRepos(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/trixie/index.yaml":
			w.Write([]byte(testExtrepoIndex))
		case r.URL.Path == "/trixie/mise.asc":
			w.Write([]byte(testMiseKey))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := extrepoCatalogBase
	extrepoCatalogBase = srv.URL
	defer func() { extrepoCatalogBase = old }()

	resolved, err := resolveReposForCodename(context.Background(), "trixie", map[string]Repo{
		"mise": {ExtRepo: "mise"},
	})
	if err != nil {
		t.Fatalf("resolveReposForCodename: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("resolved %d repos, want 1", len(resolved))
	}
	if resolved[0].name != "mise" {
		t.Errorf("resolved name = %q, want mise", resolved[0].name)
	}
	if string(resolved[0].key) != testMiseKey {
		t.Errorf("resolved key mismatch: %q", resolved[0].key)
	}
}

func TestResolveExtrepoReposUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/trixie/index.yaml" {
			w.Write([]byte(testExtrepoIndex))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	old := extrepoCatalogBase
	extrepoCatalogBase = srv.URL
	defer func() { extrepoCatalogBase = old }()

	if _, err := resolveReposForCodename(context.Background(), "trixie", map[string]Repo{
		"nope": {ExtRepo: "nope"},
	}); err == nil {
		t.Error("resolveReposForCodename must fail for unknown repo")
	}
}

func TestResolveExtrepoReposKeyChecksumMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/trixie/index.yaml":
			w.Write([]byte(testExtrepoIndex))
		case r.URL.Path == "/trixie/mise.asc":
			w.Write([]byte("tampered key"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := extrepoCatalogBase
	extrepoCatalogBase = srv.URL
	defer func() { extrepoCatalogBase = old }()

	if _, err := resolveReposForCodename(context.Background(), "trixie", map[string]Repo{
		"mise": {ExtRepo: "mise"},
	}); err == nil {
		t.Error("resolveReposForCodename must fail on key checksum mismatch")
	}
}

func parseTestIndex(t *testing.T) (map[string]extrepoEntry, error) {
	t.Helper()
	var index map[string]extrepoEntry
	if err := yaml.Unmarshal([]byte(testExtrepoIndex), &index); err != nil {
		return nil, err
	}
	return index, nil
}

func TestParseOSReleaseCodename(t *testing.T) {
	got, err := parseOSReleaseCodename("ID=debian\nVERSION_ID=\"13\"\nVERSION=\"13 (trixie)\"\nVERSION_CODENAME=trixie\n")
	if err != nil {
		t.Fatalf("parseOSReleaseCodename: %v", err)
	}
	if got != "trixie" {
		t.Errorf("codename = %q, want trixie", got)
	}
}

func TestParseOSReleaseCodenameMissing(t *testing.T) {
	if _, err := parseOSReleaseCodename("ID=fedora\nVERSION_ID=\"40\"\n"); err == nil {
		t.Error("parseOSReleaseCodename must fail without VERSION_CODENAME")
	}
}
