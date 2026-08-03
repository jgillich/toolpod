package runtime

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/jgillich/tpod/internal/mise"
)

func (d *DockerRuntime) Prepare(ctx context.Context, spec Spec, w ProgressWriter) (string, error) {
	baseRef := spec.Image
	if err := ensureImagePulled(ctx, d.cli, baseRef, w); err != nil {
		return "", fmt.Errorf("ensure base image: %w", err)
	}
	baseID, err := ResolveImageID(ctx, d.cli, baseRef)
	if err != nil {
		return "", fmt.Errorf("resolve base image ID: %w", err)
	}

	imageRef := baseRef
	if len(spec.Packages) > 0 || len(spec.Repos) > 0 {
		if err := checkExtrepoOnly(spec.Repos); err != nil {
			return "", err
		}
		derivedRef := DerivedTag(baseID, spec.Packages, spec.Repos)
		exists, err := imageExists(ctx, d.cli, derivedRef)
		if err != nil {
			return "", err
		}
		if !exists {
			if err := buildDerivedImage(ctx, d.cli, derivedRef, baseRef, spec.Repos, spec.Packages, w); err != nil {
				return "", fmt.Errorf("build derived image: %w", err)
			}
		}
		imageRef = derivedRef
	}

	// Cache volumes and (when the engine honors subpaths) their subdirectories.
	// volume-subpath requires the subdirectory to already exist in the volume;
	// create it through the volume's host mountpoint before the container runs.
	subpath := d.subpathSupported(ctx)
	volumes := map[string][]string{}
	for _, c := range spec.Caches {
		if subpath {
			volumes[c.Name] = append(volumes[c.Name], c.Subpath)
		} else {
			volumes[c.Name+"-"+c.Subpath] = nil
		}
	}
	for name, subpaths := range volumes {
		if err := mise.EnsureVolume(ctx, d.cli, name); err != nil {
			return "", fmt.Errorf("cache volume %s: %w", name, err)
		}
		if subpath && len(subpaths) > 0 {
			insp, _, err := d.cli.VolumeInspectWithRaw(ctx, name)
			if err != nil {
				return "", fmt.Errorf("inspect cache volume %s: %w", name, err)
			}
			for _, sp := range subpaths {
				if err := os.MkdirAll(filepath.Join(insp.Mountpoint, sp), 0o755); err != nil {
					return "", fmt.Errorf("create cache subpath %s in %s: %w", sp, name, err)
				}
			}
		}
	}

	return imageRef, nil
}

// checkExtrepoOnly rejects custom (URL-based) repos, which are schema-ready
// but not yet synthesizable. Extrepo repos pass through untouched.
func checkExtrepoOnly(repos map[string]Repo) error {
	for name, repo := range repos {
		if repo.ExtRepo == "" {
			return fmt.Errorf("custom apt repos not yet supported; use extrepo: <name> (repo %q)", name)
		}
	}
	return nil
}

func ensureImagePulled(ctx context.Context, cli *client.Client, ref string, w ProgressWriter) error {
	exists, err := imageExists(ctx, cli, ref)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	w.WriteProgress("pull: " + ref)
	reader, err := cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer reader.Close()
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return fmt.Errorf("drain pull response: %w", err)
	}
	return nil
}

func imageExists(ctx context.Context, cli *client.Client, ref string) (bool, error) {
	_, _, err := cli.ImageInspectWithRaw(ctx, ref)
	if err != nil {
		if client.IsErrNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
