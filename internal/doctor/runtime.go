package doctor

import (
	"github.com/docker/docker/client"
)

type dockerRT struct {
	cli    *client.Client
	engine string // detected engine name ("docker" or "podman"), set by the runtime check
}

func newRuntime() (*dockerRT, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &dockerRT{cli: cli}, nil
}
