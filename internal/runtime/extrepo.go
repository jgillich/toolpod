package runtime

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"gopkg.in/yaml.v3"
)

// extrepoCatalogBase is where extrepo's per-version catalog index and GPG
// keys are published. The codename of the base image selects the version
// (e.g. .../debian/trixie/index.yaml), so the fetch matches the user's
// image setting instead of a hardcoded distro.
var extrepoCatalogBase = "https://extrepo-team.pages.debian.net/extrepo-data/debian"

// extrepoEnabledPolicies mirrors the default /etc/extrepo/config.yaml of the
// extrepo package: only "main" is enabled (contrib/non-free are commented
// out). Repos whose policy isn't enabled are rejected at resolve time.
var extrepoEnabledPolicies = map[string]bool{"main": true}

// extrepoEntry is one repo in the catalog index.yaml. Only the fields needed
// to synthesize an apt source are parsed.
type extrepoEntry struct {
	Policy         string            `yaml:"policy"`
	Policies       map[string]string `yaml:"policies"`
	GPGKeyFile     string            `yaml:"gpg-key-file"`
	GPGKeyChecksum struct {
		SHA256 string `yaml:"sha256"`
	} `yaml:"gpg-key-checksum"`
	Source map[string]string `yaml:"source"`
}

// resolvedRepo is a profile repo resolved against the extrepo catalog into a
// concrete apt source: a deb822 .sources document and the signing key, both
// of which ride into the derived image via the build context.
type resolvedRepo struct {
	name    string
	sources string
	key     []byte
}

// resolveExtrepoRepos resolves every repo in the map against the extrepo
// catalog for the base image's Debian codename, fetching each repo's signing
// key and verifying its sha256 against the catalog checksum. Returns nil when
// repos is empty. Only called on a derived-image cache miss.
func resolveExtrepoRepos(ctx context.Context, cli *client.Client, baseRef string, repos map[string]Repo) ([]resolvedRepo, error) {
	if len(repos) == 0 {
		return nil, nil
	}
	codename, err := readImageCodename(ctx, cli, baseRef)
	if err != nil {
		return nil, err
	}
	return resolveReposForCodename(ctx, codename, repos)
}

// resolveReposForCodename is the docker-free half of resolveExtrepoRepos:
// given a codename it fetches the catalog, resolves each repo, and verifies
// its key checksum. Split out so it's unit-testable without a daemon.
func resolveReposForCodename(ctx context.Context, codename string, repos map[string]Repo) ([]resolvedRepo, error) {
	index, err := fetchExtrepoIndex(ctx, codename)
	if err != nil {
		return nil, err
	}
	var out []resolvedRepo
	for _, name := range sortedRepoKeys(repos) {
		r := repos[name]
		entry, ok := index[r.ExtRepo]
		if !ok {
			return nil, fmt.Errorf("extrepo repo %q not found in catalog for %s", r.ExtRepo, codename)
		}
		components, err := entry.components()
		if err != nil {
			return nil, fmt.Errorf("repo %s: %w", r.ExtRepo, err)
		}
		key, err := fetchExtrepoKey(ctx, codename, entry.GPGKeyFile)
		if err != nil {
			return nil, fmt.Errorf("repo %s: %w", r.ExtRepo, err)
		}
		if sum := sha256Hex(key); sum != entry.GPGKeyChecksum.SHA256 {
			return nil, fmt.Errorf("repo %s: gpg key checksum mismatch (got %s, catalog says %s)", r.ExtRepo, sum, entry.GPGKeyChecksum.SHA256)
		}
		keyPath := "/etc/apt/keyrings/" + name + ".asc"
		out = append(out, resolvedRepo{
			name:    name,
			sources: entry.renderSources(name, components, keyPath),
			key:     key,
		})
	}
	return out, nil
}

// components returns the apt components enabled for this repo under the
// default policy set. The extrepo catalog encodes components either as a
// scalar `policy: main` (components pass through verbatim from source) or as
// a `policies:` map keyed by component (only components whose policy is
// enabled survive, joined for <COMPONENTS> substitution). An unenabled repo
// is an error, mirroring extrepo's own "none of the license inclusion
// policies ... were enabled" refusal.
func (e extrepoEntry) components() (string, error) {
	if len(e.Policies) > 0 {
		var enabled []string
		for comp, pol := range e.Policies {
			if extrepoEnabledPolicies[pol] {
				enabled = append(enabled, comp)
			}
		}
		sort.Strings(enabled)
		if len(enabled) == 0 {
			return "", fmt.Errorf("none of the license inclusion policies in %q are enabled (only main is supported)", e.Policy)
		}
		return strings.Join(enabled, " "), nil
	}
	if !extrepoEnabledPolicies[e.Policy] {
		return "", fmt.Errorf("policy %q is not enabled (only main is supported)", e.Policy)
	}
	return "", nil
}

// renderSources emits the deb822 .sources document for this repo. Source
// fields are written in sorted key order with <COMPONENTS> substituted, and
// Signed-By points at the key file the build context copies in.
func (e extrepoEntry) renderSources(name, components, keyPath string) string {
	var b strings.Builder
	for _, k := range sortedKeys(e.Source) {
		v := strings.ReplaceAll(e.Source[k], "<COMPONENTS>", components)
		fmt.Fprintf(&b, "%s: %s\n", k, v)
	}
	fmt.Fprintf(&b, "Signed-By: %s\n", keyPath)
	return b.String()
}

// fetchExtrepoIndex downloads and parses the per-version catalog index.yaml.
func fetchExtrepoIndex(ctx context.Context, codename string) (map[string]extrepoEntry, error) {
	data, err := httpGet(ctx, extrepoCatalogBase+"/"+codename+"/index.yaml")
	if err != nil {
		return nil, fmt.Errorf("fetch extrepo catalog for %s: %w", codename, err)
	}
	var index map[string]extrepoEntry
	if err := yaml.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("parse extrepo catalog for %s: %w", codename, err)
	}
	return index, nil
}

// fetchExtrepoKey downloads the armored signing key for a repo.
func fetchExtrepoKey(ctx context.Context, codename, keyFile string) ([]byte, error) {
	data, err := httpGet(ctx, extrepoCatalogBase+"/"+codename+"/"+keyFile)
	if err != nil {
		return nil, fmt.Errorf("fetch gpg key %s: %w", keyFile, err)
	}
	return data, nil
}

func httpGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// readImageCodename returns the Debian codename of a locally-present base
// image (e.g. "trixie") from its /etc/os-release, without starting the
// image: a container is created but never run, and the file is copied out.
func readImageCodename(ctx context.Context, cli *client.Client, imageRef string) (string, error) {
	content, err := readImageFile(ctx, cli, imageRef, "/etc/os-release")
	if err != nil {
		return "", err
	}
	codename, err := parseOSReleaseCodename(string(content))
	if err != nil {
		return "", fmt.Errorf("%s: %w", imageRef, err)
	}
	return codename, nil
}

// parseOSReleaseCodename extracts VERSION_CODENAME from an /etc/os-release
// file. Split out of readImageCodename so it's unit-testable without a daemon.
func parseOSReleaseCodename(content string) (string, error) {
	for _, line := range strings.Split(content, "\n") {
		if v, ok := strings.CutPrefix(line, "VERSION_CODENAME="); ok {
			if v = strings.TrimSpace(v); v != "" {
				return v, nil
			}
		}
	}
	return "", fmt.Errorf("no VERSION_CODENAME in /etc/os-release (not a Debian base image?)")
}

// readImageFile copies a file out of a locally-present image. The container
// is created (which materializes the merged layer) but never started, so the
// operation needs no runtime.
//
// Engines differ on symlinks: Docker returns the symlink itself (TypeSymlink,
// body = the target path), while Podman dereferences it. If the entry is a
// symlink, resolve its target relative to the source path and read that path
// from the same container, tracking visited paths to catch cycles.
func readImageFile(ctx context.Context, cli *client.Client, imageRef, path string) ([]byte, error) {
	created, err := cli.ContainerCreate(ctx, &container.Config{Image: imageRef, Cmd: []string{"true"}}, nil, nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("create probe container from %s: %w", imageRef, err)
	}
	defer func() {
		_ = cli.ContainerRemove(context.WithoutCancel(ctx), created.ID, container.RemoveOptions{Force: true})
	}()

	seen := map[string]bool{}
	for {
		if seen[path] {
			return nil, fmt.Errorf("read %s from %s: symlink cycle", path, imageRef)
		}
		seen[path] = true

		content, target, err := readImageFileEntry(ctx, cli, created.ID, imageRef, path)
		if err != nil {
			return nil, err
		}
		if target == "" {
			return content, nil
		}
		path = target
	}
}

// readImageFileEntry copies one file out of the given container. It returns
// the file content, or a resolved symlink target when the entry is a symlink.
func readImageFileEntry(ctx context.Context, cli *client.Client, containerID, imageRef, path string) ([]byte, string, error) {
	rc, _, err := cli.CopyFromContainer(ctx, containerID, path)
	if err != nil {
		return nil, "", fmt.Errorf("copy %s out of %s: %w", path, imageRef, err)
	}
	defer rc.Close()
	tr := tar.NewReader(rc)
	hdr, err := tr.Next()
	if err != nil {
		return nil, "", fmt.Errorf("read %s from %s: %w", path, imageRef, err)
	}
	if hdr.Typeflag == tar.TypeSymlink {
		return nil, resolveLinkTarget(path, hdr.Linkname), nil
	}
	content, err := io.ReadAll(tr)
	if err != nil {
		return nil, "", fmt.Errorf("read %s from %s: %w", path, imageRef, err)
	}
	return content, "", nil
}

// resolveLinkTarget resolves a symlink's Linkname against the symlink's own
// path: absolute targets stay absolute, relative targets resolve against the
// containing directory.
func resolveLinkTarget(path, linkname string) string {
	if filepath.IsAbs(linkname) {
		return filepath.Clean(linkname)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(path), linkname))
}
