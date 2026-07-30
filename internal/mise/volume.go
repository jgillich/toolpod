package mise

import (
	"context"
	"strings"

	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
)

type NamedVolume struct {
	Name   string
	Target string
}

func MiseVolume(runtimeHome string) NamedVolume {
	return NamedVolume{
		Name:   "toolpod-mise",
		Target: "/mise",
	}
}

func CacheVolume(name, target string) NamedVolume {
	return NamedVolume{
		Name:   "toolpod-cache-" + name,
		Target: target,
	}
}

func EnsureVolume(ctx context.Context, cli *client.Client, name string) error {
	_, err := cli.VolumeCreate(ctx, volume.CreateOptions{Name: name})
	return err
}

func VolumeExists(ctx context.Context, cli *client.Client, name string) (bool, error) {
	_, err := cli.VolumeInspect(ctx, name)
	if err != nil {
		if strings.Contains(err.Error(), "no such volume") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
