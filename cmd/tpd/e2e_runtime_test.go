package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/jgillich/tpd/internal/runtime"
)

func runTpd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildTpd(t), args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func runTpdEnvTimeout(t *testing.T, env []string, timeout time.Duration, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, buildTpd(t), args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func dockerAvailable() bool {
	if os.Getenv("DOCKER_HOST") != "" {
		return true
	}
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		return true
	}
	if _, err := os.Stat("/run/user/" + fmt.Sprint(os.Getuid()) + "/podman/podman.sock"); err == nil {
		return true
	}
	return false
}

func TestE2EDoctor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if !dockerAvailable() {
		t.Skip("docker/podman not available")
	}
	out, _ := runTpd(t, "doctor")
	if !strings.Contains(out, "runtime:") {
		t.Errorf("doctor output missing runtime check; got:\n%s", out)
	}
	if !strings.Contains(out, "all checks passed") && !strings.Contains(out, "failure") && !strings.Contains(out, "warning") {
		t.Errorf("doctor output missing summary; got:\n%s", out)
	}
}

func TestE2EPruneForce(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if !dockerAvailable() {
		t.Skip("docker/podman not available")
	}
	out, err := runTpd(t, "prune", "--force", "--volumes")
	if err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Nothing to prune") && !strings.Contains(out, "Removed") {
		t.Errorf("prune output should say 'Nothing to prune' or 'Removed'; got:\n%s", out)
	}
}

// TestE2EServiceNetworkAliases proves stable aliases, same-port services, and
// consumer attachment on a real engine: two services listen on the same
// container port and a consumer reaches each through its unchanged alias, and
// recreating a service keeps the alias resolving to the replacement. Nested
// child dynamic-port lookup is deliberately out of scope — it belongs to the
// nested engine API, not this suite (privileged nested Podman is not embedded
// here).
func TestE2EServiceNetworkAliases(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if !dockerAvailable() {
		t.Skip("docker/podman not available")
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() { cli.Close() })

	// The shared network is persistent; only remove it if this test created it
	// and nothing still references it, so a pre-existing tpd-services survives.
	preexisting := networkExists(cli, runtime.ServiceNetworkName)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		removeContainersByPrefix(ctx, cli, "tpd-svc-svc-")
		removeContainersByPrefix(ctx, cli, "tpd-consumer-")
		if !preexisting {
			removeNetworkIfUnused(ctx, cli, runtime.ServiceNetworkName)
		}
	})

	cfg := t.TempDir()
	profilesDir := filepath.Join(cfg, "tpd", "profiles")
	if err := os.MkdirAll(profilesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(profilesDir, "consumer.yaml")
	writeConsumerProfile := func(t *testing.T, svcOneBody string) {
		t.Helper()
		if err := os.WriteFile(profilePath, []byte(fmt.Sprintf(consumerProfileTmpl, svcOneBody)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	env := []string{"XDG_CONFIG_HOME=" + cfg}
	const launchTimeout = 5 * time.Minute

	writeConsumerProfile(t, "one")
	out, err := runTpdEnvTimeout(t, env, launchTimeout, "consumer")
	if err != nil {
		t.Fatalf("first launch: %v\n%s", err, out)
	}
	for _, want := range []string{"svc-one=one", "svc-two=two"} {
		if !strings.Contains(out, want) {
			t.Errorf("first launch should print %q; got:\n%s", want, out)
		}
	}

	writeConsumerProfile(t, "one-recreated")
	out, err = runTpdEnvTimeout(t, env, launchTimeout, "consumer")
	if err != nil {
		t.Fatalf("second launch after service change: %v\n%s", err, out)
	}
	if !strings.Contains(out, "svc-one=one-recreated") {
		t.Errorf("recreated service should answer through the unchanged alias; got:\n%s", out)
	}
	if !strings.Contains(out, "svc-two=two") {
		t.Errorf("unchanged service should still answer; got:\n%s", out)
	}
}

// consumerProfileTmpl declares two services on the same container port 8080
// and a consumer that fetches each through TPD_SERVICE_*_HOST. The services
// install python3 via packages, so all three containers share one derived
// image. %s is the svc-one response body, changed between launches to force a
// different service hash (and thus a replacement container).
const consumerProfileTmpl = `version: 1
image: debian:13-slim
packages:
  - python3
command:
  - python3
  - -c
  - |
    import os, time, urllib.request
    def fetch(url):
        for _ in range(20):
            try:
                return urllib.request.urlopen(url, timeout=3).read().decode()
            except Exception:
                time.sleep(1)
        return "TIMEOUT"
    one = fetch("http://" + os.environ["TPD_SERVICE_SVC_ONE_HOST"] + ":8080")
    two = fetch("http://" + os.environ["TPD_SERVICE_SVC_TWO_HOST"] + ":8080")
    print("svc-one=" + one)
    print("svc-two=" + two)
services:
  svc-one:
    image: debian:13-slim
    packages:
      - python3
    command:
      - python3
      - -c
      - |
        import http.server
        class H(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                body = b"%s"
                self.send_response(200)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
            def log_message(self, *args):
                pass
        http.server.HTTPServer(("0.0.0.0", 8080), H).serve_forever()
  svc-two:
    image: debian:13-slim
    packages:
      - python3
    command:
      - python3
      - -c
      - |
        import http.server
        class H(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                body = b"two"
                self.send_response(200)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
            def log_message(self, *args):
                pass
        http.server.HTTPServer(("0.0.0.0", 8080), H).serve_forever()
`

func networkExists(cli *client.Client, name string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	networks, err := cli.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return false
	}
	for _, n := range networks {
		if n.Name == name {
			return true
		}
	}
	return false
}

func removeContainersByPrefix(ctx context.Context, cli *client.Client, prefix string) {
	list, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return
	}
	for _, c := range list {
		for _, n := range c.Names {
			if strings.HasPrefix(strings.TrimPrefix(n, "/"), prefix) {
				cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
				break
			}
		}
	}
}

// removeNetworkIfUnused removes the network only when it still exists and no
// container is attached, so cleanup never tears down a network in use.
func removeNetworkIfUnused(ctx context.Context, cli *client.Client, name string) {
	inspected, err := cli.NetworkInspect(ctx, name, network.InspectOptions{})
	if err != nil {
		return
	}
	if len(inspected.Containers) > 0 {
		return
	}
	cli.NetworkRemove(ctx, name)
}
