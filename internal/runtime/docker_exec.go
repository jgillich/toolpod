package runtime

import (
	"context"
	"fmt"
	"io"
	"strings"

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

	// Same identity handling as the Run path (see containerIdentity).
	userns, rootUser, hostUID, hostGID := containerIdentity(d.podman)
	home := ""
	for _, e := range env {
		if strings.HasPrefix(e, "HOME=") {
			home = strings.TrimPrefix(e, "HOME=")
			break
		}
	}
	writable := make([]string, 0, len(volumes)+1)
	if home != "" {
		writable = append(writable, home)
	}
	for _, v := range volumes {
		writable = append(writable, v.Target)
	}
	bootstrap := ""
	if len(writable) > 0 {
		bootstrap = fmt.Sprintf("chown %d:%d %s", hostUID, hostGID, quoteJoin(writable))
		if home != "" {
			bootstrap = "mkdir -p " + shq(home) + " && " + bootstrap
		}
	}
	if bootstrap != "" {
		cmd = wrapAsUser(bootstrap, hostUID, hostGID, cmd)
	}

	resp, err := d.cli.ContainerCreate(ctx, &container.Config{
		Image:      image,
		Cmd:        cmd,
		Env:        env,
		User:       rootUser,
		Entrypoint: []string{},
	}, &container.HostConfig{
		Mounts:      mounts,
		UsernsMode:  userns,
		AutoRemove:  false,
		NetworkMode: "bridge",
	}, nil, nil, "")
	if err != nil {
		return -1, fmt.Errorf("create exec container: %w", err)
	}
	defer d.cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})

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
