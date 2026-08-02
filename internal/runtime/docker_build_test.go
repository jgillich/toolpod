package runtime

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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
func expectedTag(baseID string, packages []string) string {
	if len(packages) == 0 {
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
	return "tpod/packages:" + hex.EncodeToString(h.Sum(nil)[:8])
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
	}{
		{"empty packages slice returns empty tag", baseID, nil},
		{"empty base id with packages still hashes", "", []string{"git"}},
		{"single package", baseID, []string{"git"}},
		{"order independent: git then curl", baseID, []string{"git", "curl"}},
		{"order independent: curl then git", baseID, []string{"curl", "git"}},
		{"duplicates preserved", baseID, []string{"git", "git"}},
		{"different base id different tag", "sha256:other", []string{"git"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DerivedTag(tt.baseID, tt.packages)
			want := expectedTag(tt.baseID, tt.packages)
			if got != want {
				t.Errorf("DerivedTag(%q, %v) = %q, want %q", tt.baseID, tt.packages, got, want)
			}
			// Empty-packages invariant: literal "" return, no tag prefix.
			if len(tt.packages) == 0 && got != "" {
				t.Errorf("empty packages must return empty string, got %q", got)
			}
		})
	}
}

func TestDerivedTagOrderIndependent(t *testing.T) {
	const baseID = "sha256:abcdef"
	pkgs := []string{"libxml2-dev", "libicu-dev", "libonig-dev", "libzip-dev", "bison", "re2c"}
	a := DerivedTag(baseID, append([]string(nil), pkgs...))
	b := DerivedTag(baseID, []string{"re2c", "bison", "libzip-dev", "libonig-dev", "libicu-dev", "libxml2-dev"})
	if a != b {
		t.Errorf("DerivedTag must be sort-normalised: got %q vs %q", a, b)
	}
}

func TestDerivedTagTagShape(t *testing.T) {
	tag := DerivedTag("sha256:abc", []string{"git"})
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
	// Cross-profile sharing invariant: identical (base, packages) ⇒
	// identical tag. Profile name is not a hash input.
	const baseID = "sha256:same"
	pkgs := []string{"libssl-dev", "libxml2-dev"}
	if a, b := DerivedTag(baseID, pkgs), DerivedTag(baseID, pkgs); a != b {
		t.Errorf("identical (base, packages) must produce identical tags: %q vs %q", a, b)
	}
}
func TestSynthesizeDockerfile(t *testing.T) {
	const baseRef = "ghcr.io/jgillich/tpod-mise:latest"
	got := synthesizeDockerfile(baseRef, []string{"libxml2-dev", "git"})
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
}

func TestSynthesizeDockerfileOrderIndependent(t *testing.T) {
	const baseRef = "base:1"
	a := synthesizeDockerfile(baseRef, []string{"git", "curl"})
	b := synthesizeDockerfile(baseRef, []string{"curl", "git"})
	if a != b {
		t.Errorf("dockerfile synthesis must be sort-normalised:\nA:\n%s\nB:\n%s", a, b)
	}
}

func TestSynthesizeDockerfileShellQuotesPackages(t *testing.T) {
	// A hostile package name must not break out of the RUN step. Validation
	// rejects these, but the emission path is defense-in-depth.
	got := synthesizeDockerfile("base:1", []string{"libxml2-dev;rm -rf /"})
	if strings.Contains(got, "libxml2-dev;rm -rf /") && !strings.Contains(got, "'libxml2-dev;rm -rf /'") {
		t.Errorf("package name must be shell-quoted:\n%s", got)
	}
}

func TestTarDockerfileProducesUsableTar(t *testing.T) {
	body := []byte("FROM base:1\n")
	r, err := tarDockerfile(body)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(r)
	hdr, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Name != "Dockerfile" {
		t.Errorf("tar entry name = %q, want Dockerfile", hdr.Name)
	}
	got, err := io.ReadAll(tr)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Errorf("tar content = %q, want %q", got, body)
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Errorf("expected only one tar entry, got %v", err)
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
