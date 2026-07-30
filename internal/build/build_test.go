package build

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/jgillich/toolpod/internal/config"
)

func TestResolveDependenciesNoDeps(t *testing.T) {
	cat := config.NewCatalogForTest(map[string]config.RawConfig{
		"a": {Config: config.Config{Image: "a:1", Command: []string{"x"}}},
	})
	deps, err := ResolveDependencies(cat, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 0 {
		t.Errorf("deps = %v, want empty", deps)
	}
}

func TestResolveDependenciesChain(t *testing.T) {
	cat := config.NewCatalogForTest(map[string]config.RawConfig{
		"a": {Config: config.Config{Build: &config.Build{Dockerfile: "D", DependsOn: []string{"b"}}}},
		"b": {Config: config.Config{Build: &config.Build{Dockerfile: "D", DependsOn: []string{"c"}}}},
		"c": {Config: config.Config{Image: "c:1", Command: []string{"x"}}},
	})
	deps, err := ResolveDependencies(cat, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 2 || deps[0] != "c" || deps[1] != "b" {
		t.Errorf("deps = %v, want [c, b] (build order)", deps)
	}
}

func TestResolveDependenciesDiamond(t *testing.T) {
	cat := config.NewCatalogForTest(map[string]config.RawConfig{
		"a": {Config: config.Config{Build: &config.Build{Dockerfile: "D", DependsOn: []string{"b", "c"}}}},
		"b": {Config: config.Config{Build: &config.Build{Dockerfile: "D", DependsOn: []string{"d"}}}},
		"c": {Config: config.Config{Build: &config.Build{Dockerfile: "D", DependsOn: []string{"d"}}}},
		"d": {Config: config.Config{Image: "d:1", Command: []string{"x"}}},
	})
	deps, err := ResolveDependencies(cat, "a")
	if err != nil {
		t.Fatal(err)
	}
	// d is the shared leaf dependency: it must be built before b and c.
	// The target "a" is built last by the caller, so it is excluded from
	// its own dependency order (spec §3.4).
	if len(deps) != 3 {
		t.Fatalf("deps = %v, want 3 transitive dependencies (b, c, d)", deps)
	}
	if deps[0] != "d" {
		t.Errorf("d must be first, got %v", deps)
	}
	for _, d := range deps {
		if d == "a" {
			t.Error("target 'a' must not appear in its own dependency order")
		}
	}
}

func TestResolveDependenciesCycle(t *testing.T) {
	cat := config.NewCatalogForTest(map[string]config.RawConfig{
		"a": {Config: config.Config{Build: &config.Build{Dockerfile: "D", DependsOn: []string{"b"}}}},
		"b": {Config: config.Config{Build: &config.Build{Dockerfile: "D", DependsOn: []string{"a"}}}},
	})
	_, err := ResolveDependencies(cat, "a")
	if err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestResolveDependenciesMissing(t *testing.T) {
	cat := config.NewCatalogForTest(map[string]config.RawConfig{
		"a": {Config: config.Config{Build: &config.Build{Dockerfile: "D", DependsOn: []string{"nope"}}}},
	})
	_, err := ResolveDependencies(cat, "a")
	if err == nil {
		t.Fatal("expected missing-dependency error")
	}
}

func TestLocalTag(t *testing.T) {
	if got := LocalTag("myprof"); got != "toolpod/myprof:latest" {
		t.Errorf("LocalTag = %q, want toolpod/myprof:latest", got)
	}
}

func TestCreateBuildContext(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, err := createBuildContext(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	tr := tar.NewReader(rc)
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == "Dockerfile" {
			found = true
		}
	}
	if !found {
		t.Error("Dockerfile not found in build context tar")
	}
}
