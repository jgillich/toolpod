package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/docker/docker/client"
)

var _ Runtime = (*DockerRuntime)(nil)

type DockerRuntime struct {
	cli    *client.Client
	podman bool // rootless Podman: containers need --userns=keep-id
}

func NewDockerRuntime() (*DockerRuntime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	// Best guess until DetectMode queries /info; also used by callers that
	// skip DetectMode (e.g. integration tests).
	return &DockerRuntime{cli: cli, podman: isLikelyRootlessSocket(cli.DaemonHost())}, nil
}

// DetectMode queries the engine's /info endpoint and checks for the Podman
// "rootless" field. The Docker SDK's types.Info does not map this field, so
// we make a raw HTTP request and parse the JSON ourselves. Spec §5.4.
func (d *DockerRuntime) DetectMode(ctx context.Context) (string, error) {
	info, err := d.cli.Info(ctx)
	if err != nil {
		return "B", fmt.Errorf("docker info: %w", err)
	}

	rootless, err := QueryRootless(ctx, d.cli)
	if err != nil {
		rootless = isLikelyRootlessSocket(d.cli.DaemonHost())
	}
	d.podman = rootless

	if rootless || strings.Contains(info.Name, "podman") {
		return "A", nil
	}
	return "B", nil
}

// QueryRootless makes a raw GET to /info and parses the "rootless" field
// from the JSON response. Podman's Docker-compatible API includes this field;
// Docker's does not (it's absent, so json.Unmarshal leaves it as false).
// Exported so the doctor package can reuse it (Plan 3) without duplication.
func QueryRootless(ctx context.Context, cli *client.Client) (bool, error) {
	host := cli.DaemonHost()
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}

	// For unix sockets, use http.Client with a custom transport.
	var httpClient *http.Client
	var url string
	if len(host) > 7 && host[:7] == "unix://" {
		httpClient = &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", host[7:])
				},
			},
		}
		url = "http://localhost/info"
	} else {
		httpClient = http.DefaultClient
		url = host + "/info"
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	var info struct {
		Rootless bool `json:"rootless"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return false, err
	}
	return info.Rootless, nil
}

// isLikelyRootlessSocket checks whether the DOCKER_HOST socket path matches
// known rootless Podman locations (e.g. /run/user/<uid>/podman/podman.sock).
func isLikelyRootlessSocket(host string) bool {
	return strings.Contains(host, "/run/user/") && strings.Contains(host, "podman")
}
