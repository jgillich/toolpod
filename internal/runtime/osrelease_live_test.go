package runtime

import (
	"context"
	"os"
	"testing"
)

func TestLiveReadImageCodenameSymlink(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if os.Getenv("DOCKER_HOST") == "" {
		t.Skip("DOCKER_HOST not set")
	}
	rt, err := NewDockerRuntime()
	if err != nil {
		t.Fatalf("NewDockerRuntime: %v", err)
	}
	// debian:13-slim's /etc/os-release is a symlink to ../usr/lib/os-release;
	// resolving the codename exercises the symlink-following path.
	got, err := readImageCodename(context.Background(), rt.cli, "debian:13-slim")
	if err != nil {
		t.Fatalf("readImageCodename: %v", err)
	}
	if got != "trixie" {
		t.Errorf("codename = %q, want trixie", got)
	}
}
