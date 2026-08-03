package runtime

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
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

	// Cache volumes and (when the engine honors subpaths) their subdirectories.
	// volume-subpath requires the subdirectory to already exist in the volume;
	// create it from a helper container so nothing writes to the host.
	subpath := d.subpathSupported(ctx)
	volumes := map[string][]string{}
	for _, c := range spec.Caches {
		if subpath {
			volumes[c.Name] = append(volumes[c.Name], c.Subpath)
		} else {
			volumes[c.Name+"-"+c.Subpath] = nil
		}
	}
	for name := range volumes {
		if err := EnsureVolume(ctx, d.cli, name); err != nil {
			return "", fmt.Errorf("cache volume %s: %w", name, err)
		}
	}

	// The helper container only needs the base image (already pulled), so run
	// it while the derived image builds instead of serially before it. All
	// cache volumes are prepared by one container; wg.Wait() on return keeps
	// a failed build from leaving a stray helper container behind.
	subpathErr := make(chan error, 1)
	var wg sync.WaitGroup
	if subpath {
		wg.Add(1)
		go func() {
			defer wg.Done()
			subpathErr <- d.ensureCacheSubpaths(ctx, baseRef, volumes)
		}()
	} else {
		subpathErr <- nil
	}
	defer wg.Wait()

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

	if err := <-subpathErr; err != nil {
		return "", fmt.Errorf("create cache subpaths: %w", err)
	}
	return imageRef, nil
}

// ensureCacheSubpaths creates the subpath directories inside a cache volume
// from a helper container. The volume's host mountpoint is root-owned on
// rootful engines and writes engine-internal storage paths directly, so the
// mkdir runs inside a container as root instead.
func (d *DockerRuntime) ensureCacheSubpaths(ctx context.Context, image string, volumes map[string][]string) error {
	mounts, mkdirs := subpathVolumeSpecs(volumes)
	if len(mkdirs) == 0 {
		return nil
	}
	resp, err := d.cli.ContainerCreate(ctx, &container.Config{
		Image:      image,
		Cmd:        append([]string{"mkdir", "-p"}, mkdirs...),
		User:       containerUser,
		Entrypoint: []string{},
	}, &container.HostConfig{
		Mounts:      mounts,
		NetworkMode: container.NetworkMode("none"),
		SecurityOpt: d.securityOpts(),
	}, nil, nil, "")
	if err != nil {
		return fmt.Errorf("create helper container: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = d.cli.ContainerRemove(cleanupCtx, resp.ID, container.RemoveOptions{Force: true})
	}()
	if err := d.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start helper container: %w", err)
	}
	statusCh, errCh := d.cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		return fmt.Errorf("wait helper container: %w", err)
	case status := <-statusCh:
		if status.StatusCode != 0 {
			return fmt.Errorf("mkdir in cache volume exited %d", status.StatusCode)
		}
	}
	return nil
}

// subpathVolumeSpecs maps each cache volume to a helper-container mount
// target and the mkdir arguments creating its subpath directories.
func subpathVolumeSpecs(volumes map[string][]string) ([]mount.Mount, []string) {
	names := make([]string, 0, len(volumes))
	for name := range volumes {
		names = append(names, name)
	}
	sort.Strings(names)
	var mounts []mount.Mount
	var mkdirs []string
	for i, name := range names {
		target := filepath.Join("/data", strconv.Itoa(i))
		mounts = append(mounts, mount.Mount{Type: mount.TypeVolume, Source: name, Target: target})
		for _, sp := range volumes[name] {
			mkdirs = append(mkdirs, filepath.Join(target, sp))
		}
	}
	return mounts, mkdirs
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

// EnsureVolume creates name if it does not already exist, tagging it with the
// ownership label so prune only ever removes volumes tpd created.
func EnsureVolume(ctx context.Context, cli *client.Client, name string) error {
	_, err := cli.VolumeCreate(ctx, volume.CreateOptions{
		Name:   name,
		Labels: OwnershipLabels(),
	})
	return err
}
