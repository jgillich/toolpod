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
	"github.com/jgillich/tpd/internal/profile"
	"github.com/jgillich/tpd/internal/runtime"
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
	All     bool // remove every tpd-managed resource regardless of liveness
	Volumes bool // scope to volumes only
	Images  bool // scope to images only
	Force   bool // skip confirmation prompt
}

type Result struct {
	VolumesRemoved []string
	ImagesRemoved  []string
}

// Run prunes tpd-managed Docker resources (volumes and derived images).
//
// Default (no flags): remove only catalog-unused resources — a volume is
// used if some resolvable profile declares it (tpd-mise if any profile
// exists, tpd-cache-<name> for any profile declaring caches.<name>); a
// tpd/packages:<hash> image is used if some resolvable profile's
// (base-image-id, merged-packages) hash matches. --all removes
// every tpd-managed resource regardless of liveness. --volumes / --images
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
		existing, err := listTpdVolumes(ctx, cli)
		if err != nil {
			return result, fmt.Errorf("list volumes: %w", err)
		}
		var remove []string
		for _, v := range existing {
			if opts.All || !volumeUsed(v.Name, usedVolumes) {
				remove = append(remove, v.Name)
			}
		}
		if len(remove) > 0 {
			if opts.Force || confirm("volumes", remove, os.Stdin) {
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
		existing, err := listTpdImages(ctx, cli)
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
			if opts.Force || confirm("images", remove, os.Stdin) {
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
// the set of tpd-managed volume names and tpd/packages:<hash> image refs
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

	for _, name := range names {
		cfg, err := profile.ResolveProfile(cat, name)
		if err != nil {
			// A profile that fails to resolve can't be launched, so it
			// contributes no used resources; skip it.
			continue
		}
		for cacheName := range cfg.Caches {
			usedVolumes["tpd-cache-"+cacheName] = true
		}
		if (len(cfg.Packages) == 0 && len(cfg.Repos) == 0) || cfg.Image == "" {
			continue
		}
		inspect, _, err := cli.ImageInspectWithRaw(ctx, cfg.Image)
		if err != nil {
			// Base image not present locally ⇒ no derived image yet.
			continue
		}
		repos := make(map[string]runtime.Repo, len(cfg.Repos))
		for name, r := range cfg.Repos {
			repos[name] = runtime.Repo{ExtRepo: r.ExtRepo, URL: r.URL, KeyURL: r.KeyURL, Suites: r.Suites, Components: r.Components}
		}
		if tag := runtime.DerivedTag(inspect.ID, cfg.Packages, repos); tag != "" {
			usedImages[tag] = true
		}
	}
	return usedVolumes, usedImages, nil
}

// volumeUsed reports whether name is a cache volume some profile uses: the
// shared subpath volume (exact match) or a per-target fallback volume
// (base-<hash> prefix, since the hash is computed over the expanded target
// path which prune does not see).
func volumeUsed(name string, usedVolumes map[string]bool) bool {
	for base := range usedVolumes {
		if name == base || strings.HasPrefix(name, base+"-") {
			return true
		}
	}
	return false
}

func listTpdVolumes(ctx context.Context, cli dockerClient) ([]*volume.Volume, error) {
	resp, err := cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return nil, err
	}
	var found []*volume.Volume
	for _, v := range resp.Volumes {
		if !isTpdVolume(v.Name) {
			continue
		}
		if v.Labels[runtime.OwnershipLabel] != "true" {
			fmt.Fprintf(os.Stderr, "warning: skipping unlabeled tpd-* resource %s (not tpd-owned)\n", v.Name)
			continue
		}
		found = append(found, v)
	}
	return found, nil
}

func listTpdImages(ctx context.Context, cli dockerClient) ([]string, error) {
	f := filters.NewArgs()
	f.Add("reference", "tpd/packages")
	images, err := cli.ImageList(ctx, image.ListOptions{Filters: f})
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, img := range images {
		if img.Labels[runtime.OwnershipLabel] != "true" {
			name := img.ID
			if len(img.RepoTags) > 0 {
				name = img.RepoTags[0]
			}
			fmt.Fprintf(os.Stderr, "warning: skipping unlabeled tpd-* resource %s (not tpd-owned)\n", name)
			continue
		}
		for _, tags := range img.RepoTags {
			if ref := runtime.DerivedRef(tags); ref != "" {
				refs = append(refs, ref)
			}
		}
	}
	sort.Strings(refs)
	return refs, nil
}

func isTpdVolume(name string) bool {
	return strings.HasPrefix(name, "tpd-")
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
