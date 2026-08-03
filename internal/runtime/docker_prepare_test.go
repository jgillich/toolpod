package runtime

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/client"
)

// fakeImageDaemon serves the Docker image endpoints ensureImagePulled uses:
// GET /images/<ref>/json for the presence check and POST /images/create for
// the pull. present controls whether the image already exists.
type fakeImageDaemon struct {
	present bool
	pulls   int
}

func (d *fakeImageDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/json"):
		if !d.present {
			http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
			return
		}
		fmt.Fprint(w, `{}`)
	case strings.HasSuffix(r.URL.Path, "/images/create"):
		d.pulls++
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"Pull complete"}`+"\n")
	default:
		http.NotFound(w, r)
	}
}

func testPullClient(t *testing.T, d *fakeImageDaemon) *client.Client {
	t.Helper()
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	cli, err := client.NewClientWithOpts(
		client.WithHost("tcp://"+srv.Listener.Addr().String()),
		client.WithVersion("1.41"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return cli
}

func TestEnsureImagePulledSkipsPresentImage(t *testing.T) {
	daemon := &fakeImageDaemon{present: true}
	cli := testPullClient(t, daemon)
	if err := ensureImagePulled(context.Background(), cli, "debian:13-slim", NoopProgressWriter{}, false); err != nil {
		t.Fatalf("ensureImagePulled: %v", err)
	}
	if daemon.pulls != 0 {
		t.Errorf("present image must not be pulled, got %d pull(s)", daemon.pulls)
	}
}

func TestEnsureImagePulledForcesPullWhenRequested(t *testing.T) {
	daemon := &fakeImageDaemon{present: true}
	cli := testPullClient(t, daemon)
	var w recordingWriter
	if err := ensureImagePulled(context.Background(), cli, "debian:13-slim", &w, true); err != nil {
		t.Fatalf("ensureImagePulled: %v", err)
	}
	if daemon.pulls != 1 {
		t.Errorf("force=true with image present must still pull, got %d pull(s)", daemon.pulls)
	}
	if len(w.lines) != 1 || w.lines[0] != "pull: debian:13-slim" {
		t.Errorf("progress lines = %q, want [\"pull: debian:13-slim\"]", w.lines)
	}
}

func TestEnsureImagePulledPullsMissingImage(t *testing.T) {
	daemon := &fakeImageDaemon{present: false}
	cli := testPullClient(t, daemon)
	if err := ensureImagePulled(context.Background(), cli, "debian:13-slim", NoopProgressWriter{}, false); err != nil {
		t.Fatalf("ensureImagePulled: %v", err)
	}
	if daemon.pulls != 1 {
		t.Errorf("missing image must be pulled, got %d pull(s)", daemon.pulls)
	}
}
