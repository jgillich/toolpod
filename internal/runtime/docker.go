package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/client"
	"github.com/jgillich/tpd/internal/workspace"
)

const (
	// defaultDaemonHost is the Unix socket used when the daemon host is empty
	// or given as a bare "unix://".
	defaultDaemonHost = "unix:///var/run/docker.sock"
)

// daemonHTTPTimeout bounds daemon /info queries: it is the http.Client.Timeout
// on the raw QueryRootless client and the context bound DaemonInfo applies to
// the SDK's Info call. http.Client.Timeout is derived from the request context,
// so caller cancellation still wins over the bound. A var so tests can shrink
// it instead of waiting out the real bound.
var daemonHTTPTimeout = 10 * time.Second

var _ Runtime = (*DockerRuntime)(nil)

type DockerRuntime struct {
	cli         *client.Client
	podman      bool // rootless Podman: containers need --userns=keep-id
	selinux     bool // SELinux enforcing: containers need --security-opt label=disable
	subpathOnce sync.Once
	subpath     bool // cached VolumeOptions.Subpath support probe
}

// subpathSupported reports whether this engine's Docker-compatible API honors
// volume subpaths, detected once and cached for the runtime's lifetime.
func (d *DockerRuntime) subpathSupported(ctx context.Context) bool {
	d.subpathOnce.Do(func() {
		d.subpath = supportsVolumeSubpath(ctx, d.cli)
	})
	return d.subpath
}

func NewDockerRuntime() (*DockerRuntime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	// Best guess until DetectMode queries /info; also used by callers that
	// skip DetectMode (e.g. integration tests).
	return &DockerRuntime{
		cli:     cli,
		podman:  isLikelyRootlessSocket(cli.DaemonHost()),
		selinux: SELinuxEnforcing(),
	}, nil
}

// DetectMode queries the engine's /info endpoint and checks for the Podman
// "rootless" field. The Docker SDK's types.Info does not map this field, so
// we make a raw HTTP request and parse the JSON ourselves. Spec §5.4.
func (d *DockerRuntime) DetectMode(ctx context.Context) (workspace.Mode, error) {
	info, err := DaemonInfo(ctx, d.cli)
	if err != nil {
		return workspace.ModeRootful, fmt.Errorf("docker info: %w", err)
	}

	rootless, err := QueryRootless(ctx, d.cli)
	if err != nil {
		return workspace.ModeRootful, fmt.Errorf("query engine mode: %w", err)
	}
	d.podman = rootless

	if rootless || strings.Contains(info.Name, "podman") {
		return workspace.ModeRootless, nil
	}
	return workspace.ModeRootful, nil
}

// DaemonInfo returns the engine's /info, bounded by daemonHTTPTimeout so a
// reachable-but-hung daemon cannot block the caller indefinitely. Exported
// for tpd doctor. The SDK client itself is unbounded: a client-level timeout
// would cut off its long-running streams (image pulls, container waits).
func DaemonInfo(ctx context.Context, cli *client.Client) (system.Info, error) {
	ctx, cancel := context.WithTimeout(ctx, daemonHTTPTimeout)
	defer cancel()
	return cli.Info(ctx)
}

// QueryRootless makes a raw GET to /info and parses the "rootless" field
// from the JSON response. Podman's Docker-compatible API includes this field;
// Docker's does not (it's absent, so json.Unmarshal leaves it as false).
// Exported so the doctor package can reuse it (Plan 3) without duplication.
func QueryRootless(ctx context.Context, cli *client.Client) (bool, error) {
	httpClient, base, err := NewDaemonHTTPClient(cli.DaemonHost())
	if err != nil {
		return false, err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", base+"/info", nil)
	if err != nil {
		return false, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("GET /info: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10+1))
	if err != nil {
		return false, err
	}
	if len(body) > 64<<10 {
		return false, errors.New("GET /info: response exceeds 64 KiB")
	}
	var info struct {
		Rootless bool `json:"rootless"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return false, err
	}
	return info.Rootless, nil
}

// NewDaemonHTTPClient returns an HTTP client and the base URL of the Docker
// API for the daemon host string. Unix sockets (including a bare "unix://",
// which means defaultDaemonHost) get a Unix transport; "tcp://" is rewritten
// to "http://"; "http://"/"https://" pass through unchanged. "ssh://" and
// "npipe://" are rejected with a clear error: ssh needs the CLI's connection
// proxy and npipe is Windows-only. Every request is bounded by
// daemonHTTPTimeout.
func NewDaemonHTTPClient(host string) (*http.Client, string, error) {
	if host == "" {
		host = defaultDaemonHost
	}

	switch {
	case strings.HasPrefix(host, "unix://"):
		sock := unixSocketPath(host)
		dialer := &net.Dialer{}
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", sock)
			},
		}
		return &http.Client{Transport: transport, Timeout: daemonHTTPTimeout}, "http://localhost", nil
	case strings.HasPrefix(host, "tcp://"):
		host = "http://" + strings.TrimPrefix(host, "tcp://")
	case strings.HasPrefix(host, "http://"), strings.HasPrefix(host, "https://"):
	case strings.HasPrefix(host, "ssh://"), strings.HasPrefix(host, "npipe://"):
		return nil, "", fmt.Errorf("unsupported daemon host %q (want unix://, tcp://, http://, or https://)", host)
	default:
		return nil, "", fmt.Errorf("unsupported daemon host %q (want unix://, tcp://, http://, or https://)", host)
	}
	return &http.Client{Timeout: daemonHTTPTimeout}, host, nil
}

// unixSocketPath returns the socket a "unix://" daemon host dials, defaulting
// a bare "unix://" to the default socket path.
func unixSocketPath(host string) string {
	sock := strings.TrimPrefix(host, "unix://")
	if sock == "" {
		sock = strings.TrimPrefix(defaultDaemonHost, "unix://")
	}
	return sock
}

func isLikelyRootlessSocket(host string) bool {
	return strings.Contains(host, "/run/user/") && strings.Contains(host, "podman")
}
