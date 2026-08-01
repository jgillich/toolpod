package runtime

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/jgillich/toolpod/internal/mise"
)

func (d *DockerRuntime) Prepare(ctx context.Context, spec Spec, w ProgressWriter) (string, error) {
	runtimeHome := spec.RuntimeHome

	if err := ensureImagePulled(ctx, d.cli, spec.Image, w); err != nil {
		return "", fmt.Errorf("ensure image: %w", err)
	}

	miseVol := mise.MiseVolume(runtimeHome)
	if err := mise.EnsureVolume(ctx, d.cli, miseVol.Name); err != nil {
		return "", fmt.Errorf("mise volume: %w", err)
	}
	for _, cache := range spec.Caches {
		if err := mise.EnsureVolume(ctx, d.cli, cache.Name); err != nil {
			return "", fmt.Errorf("cache volume %s: %w", cache.Name, err)
		}
	}

	return spec.Image, nil
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
