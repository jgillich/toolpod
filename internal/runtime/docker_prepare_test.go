package runtime

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

// prepareBuildFake serves the endpoints Prepare needs for a spec without
// caches (subpath probe disabled by version) and a single derived-image build:
// /version, /images/<ref>/json (the base image always present, the derived
// tag appearing once a build completes), and /build.
type prepareBuildFake struct {
	mu          sync.Mutex
	builds      int
	derivedSeen bool
	buildDur    time.Duration
}

func (f *prepareBuildFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/version"):
		fmt.Fprint(w, `{"Version":"26.1.0"}`)
	case strings.HasSuffix(r.URL.Path, "/json"):
		f.mu.Lock()
		derived := strings.Contains(r.URL.Path, "tpd/packages")
		present := !derived || f.derivedSeen
		f.mu.Unlock()
		if !present {
			http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
			return
		}
		id := "sha256:baseid"
		if derived {
			id = "sha256:derivedid"
		}
		fmt.Fprintf(w, `{"Id":%q}`, id)
	case strings.HasSuffix(r.URL.Path, "/build"):
		f.mu.Lock()
		f.builds++
		f.mu.Unlock()
		io.Copy(io.Discard, r.Body)
		time.Sleep(f.buildDur)
		f.mu.Lock()
		f.derivedSeen = true
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"stream":"Successfully built derived\n"}`+"\n")
	default:
		http.NotFound(w, r)
	}
}

func TestPrepareSerializesConcurrentDerivedBuilds(t *testing.T) {
	fake := &prepareBuildFake{buildDur: 150 * time.Millisecond}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cli, err := client.NewClientWithOpts(
		client.WithHost("tcp://"+srv.Listener.Addr().String()),
		client.WithVersion("1.41"),
	)
	if err != nil {
		t.Fatal(err)
	}
	oldDir := buildLockDir
	buildLockDir = t.TempDir()
	t.Cleanup(func() { buildLockDir = oldDir })

	rt := &DockerRuntime{cli: cli}
	spec := Spec{Image: "debian:13-slim", Packages: []string{"git"}}
	var wg sync.WaitGroup
	refs := make([]string, 2)
	errs := make([]error, 2)
	for i := 0; i < len(errs); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			refs[i], errs[i] = rt.Prepare(context.Background(), spec, NoopProgressWriter{}, false)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
	derived := DerivedTag("sha256:baseid", []string{"git"}, nil)
	for i, ref := range refs {
		if ref != derived {
			t.Errorf("goroutine %d image ref = %q, want derived %q", i, ref, derived)
		}
	}
	if fake.builds != 1 {
		t.Errorf("concurrent Prepare for the same derived tag must build once, got %d", fake.builds)
	}
}
