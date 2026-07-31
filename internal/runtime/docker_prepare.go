package runtime

import (
	"context"
	"fmt"

	"github.com/jgillich/toolpod/internal/build"
	"github.com/jgillich/toolpod/internal/mise"
)

func (d *DockerRuntime) Prepare(ctx context.Context, spec Spec, w ProgressWriter) (string, error) {
	runtimeHome := spec.RuntimeHome

	buildSpec := build.Spec{
		ProfileName: spec.ProfileName,
		Image:       spec.Image,
	}
	if spec.Build != nil {
		buildSpec.Build = &build.BuildSpec{
			Dockerfile: spec.Build.Dockerfile,
			Context:    spec.Build.Context,
			DependsOn:  spec.Build.DependsOn,
		}
	}

	imageRef, err := build.EnsureImage(ctx, d.cli, buildSpec, w, d.Rebuild)
	if err != nil {
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

	toolsSpec := mise.ToolsSpec{
		Image: spec.Image,
		Tools: spec.Tools,
	}
	if err := mise.EnsureTools(ctx, d, toolsSpec, runtimeHome, w); err != nil {
		return "", fmt.Errorf("mise tools: %w", err)
	}

	return imageRef, nil
}

var _ mise.ContainerRunner = (*DockerRuntime)(nil)
