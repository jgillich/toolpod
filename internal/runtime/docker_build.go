package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/docker/docker/client"
)

// derivedTag returns the content-addressed tag tpod lays on a derived image
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

// resolveImageID returns the content-addressed image-config SHA of the
// locally-present image referenced by ref. Used as the hash input for
// derivedTag and for invalidating derived images when the local base image
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