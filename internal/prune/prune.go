package prune

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/jgillich/tpod/internal/profile"
	"github.com/jgillich/tpod/internal/runtime"
	"golang.org/x/term"
)

// dockerClient is the subset of the Docker/Podman API the prune path needs.
// The real *client.Client satisfies it; tests supply a fake.
type dockerClient interface {
	VolumeList(ctx context.Context, opts volume.ListOptions) (volume.ListResponse, error)
	VolumeRemove(ctx context.Context, name string, force bool) error
	ImageList(ctx context.Context, opts image.ListOptions) ([]image.Summary, error)
	ImageRemove(ctx context.Context, ref string, opts image.RemoveOptions) ([]image.DeleteResponse, error)
	ImageInspectWithRaw(ctx context.Context, ref string) (types.ImageInspect, []byte, error)
}

type Options struct {
	All     bool // remove every tpod-managed resource regardless of liveness
	Volumes bool // scope to volumes only
	Images  bool // scope to images only
	Force   bool // skip confirmation prompt
}

type Result struct {
	VolumesRemoved []string
	ImagesRemoved  []string
}

// Run prunes tpod-managed Docker resources (volumes and derived images).
//
// Default (no flags): remove only catalog-unused resources — a volume is
// used if some resolvable profile declares it (tpod-mise if any profile
// exists, tpod-cache-<name> for any profile declaring caches.<name>); a
// tpod/packages:<hash> image is used if some resolvable profile's
// (base-image-id, merged-packages) hash matches. --all removes
// every tpod-managed resource regardless of liveness. --volumes / --images
// scope to one resource type; without either, both are pruned.
func Run(ctx context.Context, opts Options) (Result, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return Result{}, fmt.Errorf("docker client: %w", err)
	}
	return run(ctx, cli, opts)
}

func run(ctx context.Context, cli dockerClient, opts Options) (Result, error) {
	scopeVolumes := !opts.Images || opts.Volumes
	scopeImages := !opts.Volumes || opts.Images

	var usedVolumes, usedImages map[string]bool
	if !opts.All {
		var err error
		usedVolumes, usedImages, err = computeUsed(ctx, cli)
		if err != nil {
			return Result{}, fmt.Errorf("compute used: %w", err)
		}
	}

	var result Result

	if scopeVolumes {
		existing, err := listTpodVolumes(ctx, cli)
		if err != nil {
			return result, fmt.Errorf("list volumes: %w", err)
		}
		var remove []string
		for _, v := range existing {
			if opts.All || !usedVolumes[v.Name] {
				remove = append(remove, v.Name)
			}
		}
		if len(remove) > 0 {
			if !opts.Force && !confirm("volumes", remove, os.Stdin) {
				// Skip volumes; continue to images if scoped.
			} else {
				for _, name := range remove {
					if err := cli.VolumeRemove(ctx, name, true); err != nil {
						fmt.Fprintf(os.Stderr, "  failed to remove volume %s: %v\n", name, err)
					} else {
						result.VolumesRemoved = append(result.VolumesRemoved, name)
					}
				}
			}
		}
	}

	if scopeImages {
		existing, err := listTpodImages(ctx, cli)
		if err != nil {
			return result, fmt.Errorf("list images: %w", err)
		}
		var remove []string
		for _, ref := range existing {
			if opts.All || !usedImages[ref] {
				remove = append(remove, ref)
			}
		}
		if len(remove) > 0 {
			if !opts.Force && !confirm("images", remove, os.Stdin) {
				// Skip images.
			} else {
				for _, ref := range remove {
					if _, err := cli.ImageRemove(ctx, ref, image.RemoveOptions{Force: true, PruneChildren: true}); err != nil {
						fmt.Fprintf(os.Stderr, "  failed to remove image %s: %v\n", ref, err)
					} else {
						result.ImagesRemoved = append(result.ImagesRemoved, ref)
					}
				}
			}
		}
	}

	return result, nil
}

// computeUsed walks every resolvable profile (built-in + user) and returns
// the set of tpod-managed volume names and tpod/packages:<hash> image refs
// that would be produced by some current profile. Profiles whose base image
// is not present locally contribute no derived-image hashes (no local base
// ⇒ no possible derived image).
func computeUsed(ctx context.Context, cli dockerClient) (map[string]bool, map[string]bool, error) {
	usedVolumes := map[string]bool{}
	usedImages := map[string]bool{}

	userDir := profile.DefaultProfileDir()
	cat, err := profile.LoadProfilesTolerant(userDir, func(msg string) {
		fmt.Fprintf(os.Stderr, "  warning: skipping %s\n", msg)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("load profiles: %w", err)
	}
	names := cat.ProfileNames()
	if len(names) == 0 {
		return usedVolumes, usedImages, nil
	}

	// tpod-mise is always mounted by every launch; consider it used if any
	// profile resolves.
	usedVolumes["tpod-mise"] = true

	for _, name := range names {
		cfg, err := profile.ResolveProfile(cat, name)
		if err != nil {
			// A profile that fails to resolve can't be launched, so it
			// contributes no used resources; skip it.
			continue
		}
		for cacheName := range cfg.Caches {
			usedVolumes["tpod-cache-"+cacheName] = true
		}
		if len(cfg.Packages) == 0 || cfg.Image == "" {
			continue
		}
		inspect, _, err := cli.ImageInspectWithRaw(ctx, cfg.Image)
		if err != nil {
			// Base image not present locally ⇒ no derived image yet.
			continue
		}
		if tag := runtime.DerivedTag(inspect.ID, cfg.Packages); tag != "" {
			usedImages[tag] = true
		}
	}
	return usedVolumes, usedImages, nil
}

func listTpodVolumes(ctx context.Context, cli dockerClient) ([]*volume.Volume, error) {
	resp, err := cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return nil, err
	}
	var found []*volume.Volume
	for _, v := range resp.Volumes {
		if isTpodVolume(v.Name) {
			found = append(found, v)
		}
	}
	return found, nil
}

func listTpodImages(ctx context.Context, cli dockerClient) ([]string, error) {
	f := filters.NewArgs()
	f.Add("reference", "tpod/packages")
	images, err := cli.ImageList(ctx, image.ListOptions{Filters: f})
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, img := range images {
		for _, tags := range img.RepoTags {
			if strings.HasPrefix(tags, "tpod/packages:") {
				refs = append(refs, tags)
			}
		}
	}
	sort.Strings(refs)
	return refs, nil
}

func isTpodVolume(name string) bool {
	return strings.HasPrefix(name, "tpod-")
}

func confirm(kind string, items []string, r io.Reader) bool {
	if f, ok := r.(*os.File); ok && !term.IsTerminal(int(f.Fd())) {
		fmt.Fprintln(os.Stderr, "Error: cannot prompt for confirmation in non-interactive shell. Use --force.")
		return false
	}
	fmt.Printf("The following %s will be removed:\n", kind)
	for _, item := range items {
		fmt.Printf("  %s\n", item)
	}
	fmt.Print("Proceed? [y/N] ")
	scanner := bufio.NewScanner(r)
	scanner.Scan()
	return strings.ToLower(strings.TrimSpace(scanner.Text())) == "y"
}
