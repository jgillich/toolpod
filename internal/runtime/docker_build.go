package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/distribution/reference"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
)

// DerivedTag returns the content-addressed tag tpod lays on a derived image
// built from baseID with the given packages and apt repos. The tag is the
// first 16 hex chars of a sha256 over baseID, the sorted package list, and
// (when non-empty) the sorted canonical repo descriptors:
//
//	no repos:   sha256(baseID \x00 sorted(packages).join(\x01))
//	with repos: sha256(baseID \x00 sorted(packages).join(\x01) \x00 sorted(canonical-repos).join(\x02))
//
// The repos segment is appended only when non-empty, so a packages-only
// profile keeps the byte-identical pre-repos hash and its cached derived
// image survives the upgrade. Pure; does not touch the Docker daemon. Package
// and repo order is canonicalised before hashing so catalog authors can't
// trigger rebuilds by reordering.
//
// Returns "" when both packages and repos are empty — the prepare path
// short-circuits and no derived image is built.
func DerivedTag(baseID string, packages []string, repos map[string]Repo) string {
	if len(packages) == 0 && len(repos) == 0 {
		return ""
	}
	sorted := sortedCopy(packages)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s", baseID, strings.Join(sorted, "\x01"))
	if cr := canonicalRepos(repos); cr != "" {
		fmt.Fprintf(h, "\x00%s", cr)
	}
	return "tpod/packages:" + hex.EncodeToString(h.Sum(nil)[:8])
}

// canonicalRepos serializes a repo map deterministically: entries sorted by
// map key, each as
// `name \x01 extrepo \x00 url \x00 key_url \x00 suites \x00 components`
// (empty fields as empty strings), joined with \x02 (distinct from the \x01
// inside an entry) so the serialization is injective even with arbitrary URL
// characters.
func canonicalRepos(repos map[string]Repo) string {
	if len(repos) == 0 {
		return ""
	}
	names := make([]string, 0, len(repos))
	for name := range repos {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]string, 0, len(names))
	for _, name := range names {
		r := repos[name]
		entries = append(entries, fmt.Sprintf("%s\x01%s\x00%s\x00%s\x00%s\x00%s",
			name, r.ExtRepo, r.URL, r.KeyURL, r.Suites, r.Components))
	}
	return strings.Join(entries, "\x02")
}

// DerivedRef normalizes a RepoTag from ImageList into the canonical
// tpod/packages:<hash> form, or "" if the tag doesn't belong to a derived
// image. Engines qualify RepoTags with their registry (docker.io/,
// localhost/, quay.io/, ...), so the match is on the reference path, not a
// string prefix — a locally built image keeps its tag even though its hash
// is what identifies it.
func DerivedRef(repoTag string) string {
	named, err := reference.ParseNormalizedNamed(repoTag)
	if err != nil || reference.Path(named) != "tpod/packages" || reference.IsNameOnly(named) {
		return ""
	}
	tagged, ok := named.(reference.NamedTagged)
	if !ok {
		return ""
	}
	return "tpod/packages:" + tagged.Tag()
}

// ResolveImageID returns the content-addressed image-config SHA of the
// locally-present image referenced by ref. Used as the hash input for
// DerivedTag and for invalidating derived images when the local base image
// changes. Callers handle a missing-local-image error by skipping the
// profile's contribution to the "used" derived-tag set (no local base ⇒
// no possible derived image).
func ResolveImageID(ctx context.Context, cli *client.Client, ref string) (string, error) {
	inspect, _, err := cli.ImageInspectWithRaw(ctx, ref)
	if err != nil {
		return "", err
	}
	if inspect.ID == "" {
		return "", fmt.Errorf("image %q has no ID", ref)
	}
	return inspect.ID, nil
}

// buildDerivedImage builds and tags a derived image (base + repos + packages)
// as derivedRef. The Dockerfile is synthesised in-memory: FROM <baseRef> (the
// base image is already pulled), a COPY per resolved repo (the deb822 .sources
// and signing key resolved from the extrepo catalog at build time), then a
// single apt-get install of the sorted, shell-quoted package list. Build
// output is streamed through w.
func buildDerivedImage(ctx context.Context, cli *client.Client, derivedRef, baseRef string, repos map[string]Repo, packages []string, w ProgressWriter) error {
	resolved, err := resolveExtrepoRepos(ctx, cli, baseRef, repos)
	if err != nil {
		return fmt.Errorf("resolve repos: %w", err)
	}
	dockerfile := []byte(synthesizeDockerfile(baseRef, resolved, packages))
	buildContext, err := tarBuildContext(dockerfile, repoFiles(resolved))
	if err != nil {
		return fmt.Errorf("build context: %w", err)
	}

	w.WriteProgress("build: " + derivedRef)
	resp, err := cli.ImageBuild(ctx, buildContext, types.ImageBuildOptions{
		Tags:       []string{derivedRef},
		Dockerfile: "Dockerfile",
		Remove:     true,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := drainBuildStream(resp.Body, w); err != nil {
		return err
	}
	return nil
}

// drainBuildStream reads the JSON-message stream emitted by ImageBuild,
// forwarding build output through w and surfacing embedded build errors.
//
// ImageBuild returns HTTP 200 even when a RUN step fails: the daemon writes
// the error into the response body as a JSONMessage with an Error field and
// returns nil from the API call. Discarding the body (as the original
// io.Copy(io.Discard,...) did) silently swallowed build failures, leaving an
// untagged derived image and an opaque "No such image" later at launch.
//
// On apt "Unable to locate package" errors we synthesise a cleaner message
// naming the offending entries, per spec failure-mode #3.
func drainBuildStream(body io.Reader, w ProgressWriter) error {
	dec := json.NewDecoder(body)
	var unknownPackages []string
	var buildErr error
	for {
		var msg jsonmessage.JSONMessage
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("read build stream: %w", err)
		}
		if msg.Error != nil && buildErr == nil {
			buildErr = fmt.Errorf("build: %s", msg.Error.Message)
		}
		if msg.ErrorMessage != "" && buildErr == nil {
			buildErr = fmt.Errorf("build: %s", msg.ErrorMessage)
		}
		if msg.Stream != "" {
			for _, line := range strings.Split(msg.Stream, "\n") {
				if line == "" {
					continue
				}
				if p := missingPackage(line); p != "" {
					unknownPackages = appendUnique(unknownPackages, p)
				}
				w.WriteProgress(line)
			}
		}
	}
	if len(unknownPackages) > 0 {
		return fmt.Errorf("build: apt could not locate package(s): %s (check the packages: entries)", strings.Join(unknownPackages, ", "))
	}
	return buildErr
}

var missingPackageRE = regexp.MustCompile(`E: Unable to locate package ([^\s]+)`)

func missingPackage(line string) string {
	m := missingPackageRE.FindStringSubmatch(line)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// sortedCopy returns a lexicographically sorted copy of s. Used by both
// DerivedTag (hash input) and synthesizeDockerfile (emitted apt line) so the
// "identical list for hashing and emission" invariant can't drift.
func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// synthesizeDockerfile renders the derived-image Dockerfile. The package list
// is sorted (so reordering catalog entries can't change the emitted file) and
// each name is shell-quoted (defense-in-depth; validation already rejects
// metacharacters). Each resolved repo emits a COPY of its .sources and key
// (sorted by map key) into the image before apt-get update, replacing the
// old `extrepo enable <name>` chain. The context file names are
// extrepo/<name>.sources and extrepo/<name>.asc.
//
// When repos are present, a bootstrap RUN installs ca-certificates first: the
// base image is bare (no certs), and apt-get update needs them to verify the
// https repos the COPYs add. The Debian archive itself is http, so the
// bootstrap works without certificates.
func synthesizeDockerfile(baseRef string, repos []resolvedRepo, packages []string) string {
	sorted := sortedCopy(packages)
	quoted := make([]string, len(sorted))
	for i, p := range sorted {
		quoted[i] = shellQuote([]string{p})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\n", baseRef)
	if len(repos) > 0 {
		b.WriteString("RUN apt-get update \\\n")
		b.WriteString("    && apt-get install -y --no-install-recommends ca-certificates \\\n")
		b.WriteString("    && rm -rf /var/lib/apt/lists/*\n")
	}
	for _, r := range sortedResolvedRepos(repos) {
		fmt.Fprintf(&b, "COPY extrepo/%s.sources /etc/apt/sources.list.d/extrepo_%s.sources\n", r.name, r.name)
		fmt.Fprintf(&b, "COPY extrepo/%s.asc /etc/apt/keyrings/%s.asc\n", r.name, r.name)
	}
	b.WriteString("RUN apt-get update \\\n")
	b.WriteString("    && apt-get install -y --no-install-recommends \\\n")
	b.WriteString("        " + strings.Join(quoted, " \\\n        ") + " \\\n")
	b.WriteString("    && rm -rf /var/lib/apt/lists/*\n")
	return b.String()
}

func repoFiles(repos []resolvedRepo) map[string][]byte {
	if len(repos) == 0 {
		return nil
	}
	files := make(map[string][]byte, 2*len(repos))
	for _, r := range repos {
		files["extrepo/"+r.name+".sources"] = []byte(r.sources)
		files["extrepo/"+r.name+".asc"] = r.key
	}
	return files
}

func sortedRepoKeys(repos map[string]Repo) []string {
	names := make([]string, 0, len(repos))
	for name := range repos {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// sortedResolvedRepos returns a name-sorted copy of resolved repos so the
// emitted COPY lines are deterministic regardless of input order.
func sortedResolvedRepos(repos []resolvedRepo) []resolvedRepo {
	out := append([]resolvedRepo(nil), repos...)
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func tarBuildContext(dockerfile []byte, files map[string][]byte) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := writeTarEntry(tw, "Dockerfile", 0o644, dockerfile); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := writeTarEntry(tw, name, 0o644, files[name]); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

func writeTarEntry(tw *tar.Writer, name string, mode int64, content []byte) error {
	if err := tw.WriteHeader(&tar.Header{
		Name: name,
		Mode: mode,
		Size: int64(len(content)),
	}); err != nil {
		return err
	}
	_, err := tw.Write(content)
	return err
}
