package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

// DerivedTag returns the content-addressed tag tpod lays on a derived image
// built from baseID with the given packages. The tag is the first 16 hex
// chars of sha256(baseID + \u0000 + sorted(packages).join(\u0001)). Pure;
// does not touch the Docker daemon. Package order is canonicalised before
// hashing so catalog authors can't trigger rebuilds by reordering.
//
// Returns "" when packages is empty — the prepare path short-circuits and
// no derived image is built.
func DerivedTag(baseID string, packages []string) string {
	if len(packages) == 0 {
		return ""
	}
	sorted := append([]string(nil), packages...)
	sort.Strings(sorted)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s", baseID, strings.Join(sorted, "\x01"))
	return "tpod/packages:" + hex.EncodeToString(h.Sum(nil)[:8])
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

// buildDerivedImage builds and tags a derived image (base + packages) as
// derivedRef. The Dockerfile is synthesised in-memory: FROM <baseRef> (the
// base image is already pulled), then a single apt-get install of the sorted,
// shell-quoted package list. Build output is streamed through w.
func buildDerivedImage(ctx context.Context, cli *client.Client, derivedRef, baseRef string, packages []string, w ProgressWriter) error {
	dockerfile := []byte(synthesizeDockerfile(baseRef, packages))
	buildContext, err := tarDockerfile(dockerfile)
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
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return fmt.Errorf("drain build response: %w", err)
	}
	return nil
}

// synthesizeDockerfile renders the derived-image Dockerfile. The package list
// is sorted (so reordering catalog entries can't change the emitted file) and
// each name is shell-quoted (defense-in-depth; validation already rejects
// metacharacters).
func synthesizeDockerfile(baseRef string, packages []string) string {
	sorted := append([]string(nil), packages...)
	sort.Strings(sorted)
	quoted := make([]string, len(sorted))
	for i, p := range sorted {
		quoted[i] = shellQuote([]string{p})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\n", baseRef)
	b.WriteString("RUN apt-get update \\\n")
	b.WriteString("    && apt-get install -y --no-install-recommends \\\n")
	b.WriteString("        " + strings.Join(quoted, " \\\n        ") + " \\\n")
	b.WriteString("    && rm -rf /var/lib/apt/lists/*\n")
	return b.String()
}

// tarDockerfile wraps a Dockerfile body into a tar archive stream suitable as
// an ImageBuild build context.
func tarDockerfile(dockerfile []byte) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: "Dockerfile",
		Mode: 0o644,
		Size: int64(len(dockerfile)),
	}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(dockerfile); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}