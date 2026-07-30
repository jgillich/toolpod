package build

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
	"github.com/jgillich/toolpod/internal/profile"
)

// Spec is the subset of the container spec needed for image preparation.
// Defined here (not imported from runtime) to avoid an import cycle:
// runtime.DockerRuntime.Prepare calls build.EnsureImage, and build must not
// import runtime.
type Spec struct {
	ProfileName string
	Image       string
	Build       *BuildSpec
}

type BuildSpec struct {
	Dockerfile string
	Context    string
	DependsOn  []string
}

// ProgressWriter is structurally identical to runtime.ProgressWriter.
// Defined locally to avoid importing runtime.
type ProgressWriter interface {
	WriteProgress(line string)
}

// LocalTag returns the local image tag for a built profile.
func LocalTag(name string) string {
	return "toolpod/" + name + ":latest"
}

// EnsureImage ensures the image for spec is available. For image: specs it
// pulls the referenced image if missing; for build: specs it builds the local
// image (when missing, or always when rebuild is true). Returns the image
// reference to use for the container. Spec §3.4.
func EnsureImage(ctx context.Context, cli *client.Client, spec Spec, w ProgressWriter, rebuild bool) (string, error) {
	if spec.Build == nil {
		return ensurePull(ctx, cli, spec.Image, w)
	}
	tag := LocalTag(spec.ProfileName)
	if rebuild {
		w.WriteProgress("build: rebuilding " + tag)
		return tag, buildImage(ctx, cli, spec, w)
	}
	exists, err := imageExists(ctx, cli, tag)
	if err != nil {
		return "", err
	}
	if exists {
		return tag, nil
	}
	w.WriteProgress("build: building " + tag)
	return tag, buildImage(ctx, cli, spec, w)
}

func ensurePull(ctx context.Context, cli *client.Client, ref string, w ProgressWriter) (string, error) {
	exists, err := imageExists(ctx, cli, ref)
	if err != nil {
		return "", err
	}
	if exists {
		return ref, nil
	}
	w.WriteProgress("pull: " + ref)
	reader, err := cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return "", fmt.Errorf("pull %s: %w", ref, err)
	}
	defer reader.Close()
	io.Copy(io.Discard, reader)
	return ref, nil
}

// buildImage builds a Docker image from the spec's build spec and tags it
// with the local tag. Spec §3.4.
func buildImage(ctx context.Context, cli *client.Client, spec Spec, w ProgressWriter) error {
	dockerfilePath := spec.Build.Dockerfile
	contextDir := spec.Build.Context
	if contextDir == "" {
		contextDir = filepath.Dir(dockerfilePath)
	}

	buildCtx, err := createBuildContext(contextDir)
	if err != nil {
		return fmt.Errorf("build context: %w", err)
	}
	defer buildCtx.Close()

	tag := LocalTag(spec.ProfileName)
	resp, err := cli.ImageBuild(ctx, buildCtx, types.ImageBuildOptions{
		Dockerfile: filepath.Base(dockerfilePath),
		Tags:       []string{tag},
		Remove:     true,
	})
	if err != nil {
		if strings.Contains(err.Error(), "toolpod/") && !isInDependsOn(spec, err.Error()) {
			return fmt.Errorf("image build: %w\nhint: this Dockerfile references a toolpod/* image — add it to build.depends_on", err)
		}
		return fmt.Errorf("image build: %w", err)
	}
	defer resp.Body.Close()

	io.Copy(io.Discard, resp.Body)

	exists, err := imageExists(ctx, cli, tag)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("build completed but image %s not found", tag)
	}
	return nil
}

// createBuildContext creates a tar archive of dir for use as a Docker build
// context. Uses the Docker SDK's archive.Tar for correct, portable tarring.
func createBuildContext(dir string) (io.ReadCloser, error) {
	return archive.Tar(dir, archive.Uncompressed)
}

// isInDependsOn reports whether errStr references a toolpod/* tag matching a
// dependency already declared in the spec's depends_on.
func isInDependsOn(spec Spec, errStr string) bool {
	if spec.Build == nil {
		return false
	}
	for _, dep := range spec.Build.DependsOn {
		if strings.Contains(errStr, "toolpod/"+dep) {
			return true
		}
	}
	return false
}

func imageExists(ctx context.Context, cli *client.Client, ref string) (bool, error) {
	_, _, err := cli.ImageInspectWithRaw(ctx, ref)
	if err != nil {
		if client.IsErrNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ResolveDependencies returns the build order (topological sort) of the
// depends_on entries reachable from name — i.e. the dependencies that must be
// built before name's own image. The target itself is excluded.
func ResolveDependencies(cat profile.Catalog, name string) ([]string, error) {
	visited := map[string]bool{}
	inProgress := map[string]bool{}
	var order []string

	var visit func(n string) error
	visit = func(n string) error {
		if visited[n] {
			return nil
		}
		if inProgress[n] {
			return fmt.Errorf("depends_on cycle detected at: %s", n)
		}
		inProgress[n] = true
		rc, ok := cat.Get(n)
		if !ok {
			return fmt.Errorf("depends_on references unknown profile: %s", n)
		}
		if rc.Build != nil {
			for _, dep := range rc.Build.DependsOn {
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		inProgress[n] = false
		visited[n] = true
		order = append(order, n)
		return nil
	}

	rc, ok := cat.Get(name)
	if !ok {
		return nil, fmt.Errorf("unknown profile: %s", name)
	}
	if rc.Build != nil {
		for _, dep := range rc.Build.DependsOn {
			if err := visit(dep); err != nil {
				return nil, err
			}
		}
	}
	return order, nil
}
