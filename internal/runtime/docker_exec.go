package runtime

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/jgillich/toolpod/internal/mise"
)

func (d *DockerRuntime) RunInContainer(ctx context.Context, image string, volumes []mise.VolumeMount, env []string, cmd []string) (int, error) {
	mounts := make([]mount.Mount, len(volumes))
	for i, v := range volumes {
		mounts[i] = mount.Mount{
			Type:   mount.TypeVolume,
			Source: v.Name,
			Target: v.Target,
		}
	}
	resp, err := d.cli.ContainerCreate(ctx, &container.Config{
		Image: image,
		Cmd:   cmd,
		Env:   env,
	}, &container.HostConfig{
		Mounts:     mounts,
		AutoRemove:  true,
		NetworkMode: "none",
	}, nil, nil, "")
	if err != nil {
		return -1, fmt.Errorf("create exec container: %w", err)
	}
	if err := d.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return -1, fmt.Errorf("start exec container: %w", err)
	}
	statusCh, errCh := d.cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		return -1, fmt.Errorf("exec container wait: %w", err)
	case status := <-statusCh:
		if status.StatusCode != 0 {
			logs, _ := d.cli.ContainerLogs(ctx, resp.ID, container.LogsOptions{
				ShowStdout: true,
				ShowStderr: true,
			})
			if logs != nil {
				buf, _ := io.ReadAll(logs)
				logs.Close()
				return int(status.StatusCode), fmt.Errorf("exec failed (exit %d): %s", status.StatusCode, string(buf))
			}
		}
		return int(status.StatusCode), nil
	}
}
