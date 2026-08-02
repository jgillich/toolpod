package mise

import (
	"context"

	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
)

func EnsureVolume(ctx context.Context, cli *client.Client, name string) error {
	_, err := cli.VolumeCreate(ctx, volume.CreateOptions{Name: name})
	return err
}
