package runtime

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

type recordingWriter struct {
	lines []string
}

func (r *recordingWriter) WriteProgress(line string) { r.lines = append(r.lines, line) }

// expectedTag mirrors DerivedTag's algorithm so tests assert the contract
// (determinism, sort-normalisation, namespace) rather than re-implement
// the function. If the algorithm intentionally changes, these tests
// should be updated alongside DerivedTag.
func expectedTag(baseID string, packages []string, repos map[string]Repo) string {
	if len(packages) == 0 && len(repos) == 0 {
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
	if cr := expectedCanonicalRepos(repos); cr != "" {
		fmt.Fprintf(h, "\x00%s", cr)
	}
	return "tpod/packages:" + hex.EncodeToString(h.Sum(nil)[:8])
}

func expectedCanonicalRepos(repos map[string]Repo) string {
	if len(repos) == 0 {
		return ""
	}
	names := make([]string, 0, len(repos))
	for name := range repos {
		names = append(names, name)
	}
	// sort
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j-1] > names[j]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
	entries := make([]string, 0, len(names))
	for _, name := range names {
		r := repos[name]
		entries = append(entries, name+"\x01"+r.ExtRepo+"\x00"+r.URL+"\x00"+r.KeyURL+"\x00"+r.Suites+"\x00"+r.Components)
	}
	return join(entries, "\x02")
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
		repos    map[string]Repo
	}{
		{"empty everything returns empty tag", baseID, nil, nil},
		{"empty base id with packages still hashes", "", []string{"git"}, nil},
		{"single package", baseID, []string{"git"}, nil},
		{"order independent: git then curl", baseID, []string{"git", "curl"}, nil},
		{"order independent: curl then git", baseID, []string{"curl", "git"}, nil},
		{"duplicates preserved", baseID, []string{"git", "git"}, nil},
		{"different base id different tag", "sha256:other", []string{"git"}, nil},
		{
			"repos-only profile hashes",
			baseID, nil,
			map[string]Repo{"mise": {ExtRepo: "mise"}},
		},
		{
			"packages plus repos",
			baseID, []string{"mise"},
			map[string]Repo{"mise": {ExtRepo: "mise"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DerivedTag(tt.baseID, tt.packages, tt.repos)
			want := expectedTag(tt.baseID, tt.packages, tt.repos)
			if got != want {
				t.Errorf("DerivedTag(%q, %v, %v) = %q, want %q", tt.baseID, tt.packages, tt.repos, got, want)
			}
			// Empty-everything invariant: literal "" return, no tag prefix.
			if len(tt.packages) == 0 && len(tt.repos) == 0 && got != "" {
				t.Errorf("empty packages+repos must return empty string, got %q", got)
			}
		})
	}
}

func TestDerivedTagOrderIndependent(t *testing.T) {
	const baseID = "sha256:abcdef"
	pkgs := []string{"libxml2-dev", "libicu-dev", "libonig-dev", "libzip-dev", "bison", "re2c"}
	a := DerivedTag(baseID, append([]string(nil), pkgs...), nil)
	b := DerivedTag(baseID, []string{"re2c", "bison", "libzip-dev", "libonig-dev", "libicu-dev", "libxml2-dev"}, nil)
	if a != b {
		t.Errorf("DerivedTag must be sort-normalised: got %q vs %q", a, b)
	}
}

func TestDerivedTagTagShape(t *testing.T) {
	tag := DerivedTag("sha256:abc", []string{"git"}, nil)
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
	// Cross-profile sharing invariant: identical (base, packages, repos) ⇒
	// identical tag. Profile name is not a hash input.
	const baseID = "sha256:same"
	pkgs := []string{"libssl-dev", "libxml2-dev"}
	if a, b := DerivedTag(baseID, pkgs, nil), DerivedTag(baseID, pkgs, nil); a != b {
		t.Errorf("identical (base, packages) must produce identical tags: %q vs %q", a, b)
	}
}

func TestDerivedTagReposSortNormalised(t *testing.T) {
	const baseID = "sha256:repo"
	pkgs := []string{"git"}
	// Go map iteration is randomized; reordered maps must hash identically.
	a := DerivedTag(baseID, pkgs, map[string]Repo{
		"mise":   {ExtRepo: "mise"},
		"nodejs": {ExtRepo: "nodejs"},
	})
	b := DerivedTag(baseID, pkgs, map[string]Repo{
		"nodejs": {ExtRepo: "nodejs"},
		"mise":   {ExtRepo: "mise"},
	})
	if a != b {
		t.Errorf("DerivedTag must be map-order-independent: %q vs %q", a, b)
	}
	// Different field values must change the tag.
	c := DerivedTag(baseID, pkgs, map[string]Repo{
		"mise":   {ExtRepo: "mise"},
		"nodejs": {ExtRepo: "nodejs"},
		"extra":  {URL: "https://deb.example.com", KeyURL: "https://key.example.com"},
	})
	if a == c {
		t.Error("repos with different entries must produce different tags")
	}
}

func TestDerivedRefNormalizesRepoTags(t *testing.T) {
	const hash = "abc123"
	cases := []struct {
		repoTag string
		want    string
	}{
		{"docker.io/tpod/packages:" + hash, "tpod/packages:" + hash},
		{"tpod/packages:" + hash, "tpod/packages:" + hash},
		{"localhost/tpod/packages:" + hash, "tpod/packages:" + hash},
		{"quay.io/tpod/packages:" + hash, "tpod/packages:" + hash},
		{"myregistry.internal/tpod/packages:" + hash, "tpod/packages:" + hash},
		// Not derived images → empty.
		{"docker.io/library/foo:latest", ""},
		{"tpod/packages", ""}, // no tag
		{"ghcr.io/jgillich/tpod-mise:latest", ""},
		{"not even a reference!", ""},
	}
	for _, tt := range cases {
		if got := DerivedRef(tt.repoTag); got != tt.want {
			t.Errorf("DerivedRef(%q) = %q, want %q", tt.repoTag, got, tt.want)
		}
	}
}

func TestDerivedTagEmptyReposEqualsPackagesOnly(t *testing.T) {
	// Back-compat invariant: a packages-only profile (empty repos map) must
	// keep the byte-identical pre-repos hash. nil and empty map must agree.
	const baseID = "sha256:compat"
	pkgs := []string{"libxml2-dev", "git"}
	a := DerivedTag(baseID, pkgs, nil)
	b := DerivedTag(baseID, pkgs, map[string]Repo{})
	if a != b {
		t.Errorf("empty repos map must equal nil: %q vs %q", a, b)
	}
	// And equal the pre-repos algorithm.
	want := expectedTag(baseID, pkgs, nil)
	if a != want {
		t.Errorf("DerivedTag with empty repos = %q, want pre-repos hash %q", a, want)
	}
}
func TestSynthesizeDockerfile(t *testing.T) {
	const baseRef = "debian:13-slim"
	got := synthesizeDockerfile(baseRef, nil, []string{"libxml2-dev", "git"})
	if !strings.Contains(got, "FROM "+baseRef+"\n") {
		t.Errorf("dockerfile must start with FROM baseRef:\n%s", got)
	}
	// Package list sorted, each shell-quoted, single apt invocation.
	wantInstall := "'git' \\\n        'libxml2-dev'"
	if !strings.Contains(got, wantInstall) {
		t.Errorf("dockerfile must contain sorted shell-quoted packages:\n%s\nwant substring:\n%s", got, wantInstall)
	}
	if !strings.Contains(got, "apt-get install -y --no-install-recommends") {
		t.Errorf("dockerfile must use --no-install-recommends:\n%s", got)
	}
	if !strings.Contains(got, "rm -rf /var/lib/apt/lists/*") {
		t.Errorf("dockerfile must clean apt lists:\n%s", got)
	}
	if strings.Contains(got, "ca-certificates") {
		t.Errorf("repo-less dockerfile must not bootstrap ca-certificates:\n%s", got)
	}
	if strings.Count(got, "apt-get update") != 1 {
		t.Errorf("repo-less dockerfile must have a single apt-get update:\n%s", got)
	}
}

func TestSynthesizeDockerfileRepos(t *testing.T) {
	const baseRef = "base:1"
	repos := []resolvedRepo{
		{name: "mise"},
		{name: "nodejs"},
		{name: "extrane"},
	}
	got := synthesizeDockerfile(baseRef, repos, []string{"mise"})
	if !strings.Contains(got, "FROM "+baseRef+"\n") {
		t.Errorf("dockerfile must start with FROM baseRef:\n%s", got)
	}
	// Each repo emits a COPY of its .sources and key, in sorted repo order,
	// before apt-get update.
	for _, want := range []string{"COPY extrepo/extrane.sources", "COPY extrepo/mise.sources", "COPY extrepo/nodejs.sources"} {
		if !strings.Contains(got, want) {
			t.Errorf("dockerfile must contain %q:\n%s", want, got)
		}
	}
	pos := make([]int, 3)
	for i, want := range []string{"extrane", "mise", "nodejs"} {
		pos[i] = strings.Index(got, "COPY extrepo/"+want+".sources")
		if pos[i] < 0 {
			t.Fatalf("missing %q in dockerfile:\n%s", want, got)
		}
	}
	if !(pos[0] < pos[1] && pos[1] < pos[2]) {
		t.Errorf("repo COPYs must be sorted by name: %v", pos)
	}
	if !strings.Contains(got, "COPY extrepo/mise.asc /etc/apt/keyrings/mise.asc") {
		t.Errorf("dockerfile must COPY the key to apt keyrings:\n%s", got)
	}
	if !strings.Contains(got, "apt-get update") {
		t.Errorf("dockerfile must run apt-get update after copying repos:\n%s", got)
	}
	// A bare base has no ca-certificates; the first RUN must install them so
	// apt can verify the https repos the COPYs add. It must precede both the
	// COPYs and the main apt-get update.
	certInstall := "apt-get install -y --no-install-recommends ca-certificates"
	if !strings.Contains(got, certInstall) {
		t.Errorf("dockerfile must bootstrap ca-certificates when repos present:\n%s", got)
	}
	firstRun := strings.Index(got, "RUN apt-get update")
	firstCopy := strings.Index(got, "COPY extrepo/extrane.sources")
	secondRun := strings.LastIndex(got, "RUN apt-get update")
	if !(firstRun < firstCopy && firstCopy < secondRun) {
		t.Errorf("bootstrap RUN must precede COPYs and main RUN:\n%s", got)
	}
}

func TestSynthesizeDockerfileOrderIndependent(t *testing.T) {
	const baseRef = "base:1"
	a := synthesizeDockerfile(baseRef, nil, []string{"git", "curl"})
	b := synthesizeDockerfile(baseRef, nil, []string{"curl", "git"})
	if a != b {
		t.Errorf("dockerfile synthesis must be sort-normalised:\nA:\n%s\nB:\n%s", a, b)
	}
}

func TestSynthesizeDockerfileShellQuotesPackages(t *testing.T) {
	// A hostile package name must not break out of the RUN step. Validation
	// rejects these, but the emission path is defense-in-depth.
	got := synthesizeDockerfile("base:1", nil, []string{"libxml2-dev;rm -rf /"})
	if strings.Contains(got, "libxml2-dev;rm -rf /") && !strings.Contains(got, "'libxml2-dev;rm -rf /'") {
		t.Errorf("package name must be shell-quoted:\n%s", got)
	}
}

func TestTarBuildContextProducesUsableTar(t *testing.T) {
	body := []byte("FROM base:1\n")
	files := map[string][]byte{
		"extrepo/mise.sources": []byte("Types: deb\nURIs: https://mise.jdx.dev/deb\n"),
		"extrepo/mise.asc":     []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----\n"),
	}
	r, err := tarBuildContext(body, files)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(r)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
		if got, _ := io.ReadAll(tr); !bytes.Equal(got, files[hdr.Name]) && hdr.Name != "Dockerfile" {
			t.Errorf("tar entry %q content mismatch", hdr.Name)
		}
	}
	want := []string{"Dockerfile", "extrepo/mise.asc", "extrepo/mise.sources"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("tar entry names = %v, want %v", names, want)
	}
}

func TestDrainBuildStreamSuccess(t *testing.T) {
	body := strings.NewReader(
		`{"stream":"Step 1/2 : FROM base:1\n"}` + "\n" +
			`{"stream":" ---> abc123\n"}` + "\n" +
			`{"stream":"Successfully built abc123\n"}` + "\n",
	)
	var w recordingWriter
	if err := drainBuildStream(body, &w); err != nil {
		t.Fatalf("drainBuildStream: %v", err)
	}
	if len(w.lines) == 0 {
		t.Errorf("expected build output forwarded to writer, got none")
	}
}

func TestDrainBuildStreamEmbeddedError(t *testing.T) {
	body := strings.NewReader(`{"errorDetail":{"message":"RUN failed: exit code 1"}}` + "\n")
	err := drainBuildStream(body, &recordingWriter{})
	if err == nil {
		t.Fatal("expected error from embedded build failure, got nil")
	}
	if !strings.Contains(err.Error(), "RUN failed") {
		t.Errorf("error should surface the embedded message; got %q", err.Error())
	}
}

func TestDrainBuildStreamMissingPackage(t *testing.T) {
	body := strings.NewReader(
		`{"stream":"E: Unable to locate package libxml2-dev\n"}` + "\n" +
			`{"stream":"E: Unable to locate package foodev\n"}` + "\n" +
			`{"errorDetail":{"message":"The command '/bin/sh -c apt-get install ...' returned a non-zero code: 100"}}` + "\n",
	)
	err := drainBuildStream(body, &recordingWriter{})
	if err == nil {
		t.Fatal("expected error listing missing packages, got nil")
	}
	for _, want := range []string{"libxml2-dev", "foodev"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name missing package %q; got %q", want, err.Error())
		}
	}
}

func TestDrainBuildStreamMissingPackageDedup(t *testing.T) {
	body := strings.NewReader(
		`{"stream":"E: Unable to locate package libxml2-dev\n"}` + "\n" +
			`{"stream":"E: Unable to locate package libxml2-dev\n"}` + "\n",
	)
	err := drainBuildStream(body, &recordingWriter{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if c := strings.Count(err.Error(), "libxml2-dev"); c != 1 {
		t.Errorf("missing package should be reported once, got %d in %q", c, err.Error())
	}
}
