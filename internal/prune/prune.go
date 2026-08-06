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
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
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
	ContainerList(ctx context.Context, opts container.ListOptions) ([]types.Container, error)
	ContainerInspectWithRaw(ctx context.Context, containerID string, getSize bool) (types.ContainerJSON, []byte, error)
	NetworkList(ctx context.Context, opts network.ListOptions) ([]network.Summary, error)
	NetworkRemove(ctx context.Context, networkID string) error
}

type Options struct {
	All      bool // remove every tpd-managed resource regardless of liveness
	Volumes  bool // scope to volumes only
	Images   bool // scope to images only
	Networks bool // scope to the tpd service network only
	Force    bool // skip confirmation prompt
}

type Result struct {
	VolumesRemoved  []string
	ImagesRemoved   []string
	NetworksRemoved []string
}

// Run prunes tpd-managed Docker resources (volumes, derived images, and the
// service network).
//
// Default (no type flags): remove only catalog-unused resources — a volume is
// used if some resolvable profile declares it (tpd-mise if any profile
// exists, tpd-cache-<name> for any profile declaring caches.<name>); a
// tpd/packages:<hash> image is used if some resolvable profile's
// (base-image-id, merged-packages) hash matches. The service network is never
// in scope by default. --all removes every tpd-managed resource regardless of
// liveness; it does not imply network scope. A type flag (--volumes / --images
// / --networks) scopes to exactly the requested types; without any, volumes
// and images are pruned.
func Run(ctx context.Context, opts Options) (Result, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return Result{}, fmt.Errorf("docker client: %w", err)
	}
	return run(ctx, cli, opts)
}

func run(ctx context.Context, cli dockerClient, opts Options) (Result, error) {
	anyTypeFlag := opts.Volumes || opts.Images || opts.Networks
	scopeVolumes := !anyTypeFlag || opts.Volumes
	scopeImages := !anyTypeFlag || opts.Images
	scopeNetworks := opts.Networks

	var usedVolumes, usedImages map[string]bool
	if !opts.All && (scopeVolumes || scopeImages) {
		var err error
		usedVolumes, usedImages, err = computeUsed(ctx, cli)
		if err != nil {
			return Result{}, fmt.Errorf("compute used: %w", err)
		}
	}

	var removeVolumes, removeImages []string
	var removeNetworks []network.Summary
	if scopeVolumes {
		existing, err := listTpdVolumes(ctx, cli)
		if err != nil {
			return Result{}, fmt.Errorf("list volumes: %w", err)
		}
		for _, v := range existing {
			if opts.All || !volumeUsed(v.Name, usedVolumes) {
				removeVolumes = append(removeVolumes, v.Name)
			}
		}
	}
	if scopeImages {
		existing, err := listTpdImages(ctx, cli)
		if err != nil {
			return Result{}, fmt.Errorf("list images: %w", err)
		}
		for _, ref := range existing {
			if opts.All || !usedImages[ref] {
				removeImages = append(removeImages, ref)
			}
		}
	}
	if scopeNetworks {
		existing, err := listTpdNetworks(ctx, cli)
		if err != nil {
			return Result{}, fmt.Errorf("list networks: %w", err)
		}
		removeNetworks = append(removeNetworks, existing...)
	}

	if len(removeVolumes) > 0 || len(removeImages) > 0 {
		// A resource referenced by a running container must never be removed,
		// even under --all (which only relaxes the catalog-liveness check).
		volumesInUse, imagesInUse, err := runningContainerRefs(ctx, cli)
		if err != nil {
			return Result{}, fmt.Errorf("list running containers: %w", err)
		}
		var keptV, keptI []string
		for _, name := range removeVolumes {
			if volumesInUse[name] {
				fmt.Fprintf(os.Stderr, "skipping %s: in use by a running container\n", name)
				continue
			}
			keptV = append(keptV, name)
		}
		removeVolumes = keptV
		for _, ref := range removeImages {
			inUse, err := imageInUse(ctx, cli, ref, imagesInUse)
			if err != nil {
				return Result{}, fmt.Errorf("re-check image %s: %w", ref, err)
			}
			if inUse {
				fmt.Fprintf(os.Stderr, "skipping %s: in use by a running container\n", ref)
				continue
			}
			keptI = append(keptI, ref)
		}
		removeImages = keptI
	}
	if len(removeNetworks) > 0 {
		// Networks have no catalog-liveness notion; the only protection is a
		// running container attached to them.
		networksInUse, err := runningContainerNetworks(ctx, cli)
		if err != nil {
			return Result{}, fmt.Errorf("list running containers: %w", err)
		}
		var keptN []network.Summary
		for _, n := range removeNetworks {
			if networksInUse[n.Name] || networksInUse[n.ID] {
				fmt.Fprintf(os.Stderr, "skipping %s: in use by a running container\n", n.Name)
				continue
			}
			keptN = append(keptN, n)
		}
		removeNetworks = keptN
	}

	var result Result

	if scopeVolumes && len(removeVolumes) > 0 {
		if opts.Force || confirm("volumes", removeVolumes, os.Stdin) {
			for _, name := range removeVolumes {
				inUse, err := volumeInUse(ctx, cli, name)
				if err != nil {
					return result, fmt.Errorf("re-check volume %s: %w", name, err)
				}
				if inUse {
					fmt.Fprintf(os.Stderr, "skipping %s: in use by a running container\n", name)
					continue
				}
				if err := cli.VolumeRemove(ctx, name, true); err != nil {
					fmt.Fprintf(os.Stderr, "  failed to remove volume %s: %v\n", name, err)
				} else {
					result.VolumesRemoved = append(result.VolumesRemoved, name)
				}
			}
		}
	}

	if scopeImages && len(removeImages) > 0 {
		if opts.Force || confirm("images", removeImages, os.Stdin) {
			for _, ref := range removeImages {
				inUse, err := imageInUseNow(ctx, cli, ref)
				if err != nil {
					return result, fmt.Errorf("re-check image %s: %w", ref, err)
				}
				if inUse {
					fmt.Fprintf(os.Stderr, "skipping %s: in use by a running container\n", ref)
					continue
				}
				if _, err := cli.ImageRemove(ctx, ref, image.RemoveOptions{Force: true, PruneChildren: true}); err != nil {
					fmt.Fprintf(os.Stderr, "  failed to remove image %s: %v\n", ref, err)
				} else {
					result.ImagesRemoved = append(result.ImagesRemoved, ref)
				}
			}
		}
	}

	if scopeNetworks && len(removeNetworks) > 0 {
		names := make([]string, len(removeNetworks))
		for i, n := range removeNetworks {
			names[i] = n.Name
		}
		if opts.Force || confirm("networks", names, os.Stdin) {
			for _, n := range removeNetworks {
				// Re-scan so a container attached while the prompt was open
				// protects the network.
				inUse, err := runningContainerNetworks(ctx, cli)
				if err != nil {
					return result, fmt.Errorf("re-check network %s: %w", n.Name, err)
				}
				if inUse[n.Name] || inUse[n.ID] {
					fmt.Fprintf(os.Stderr, "skipping %s: in use by a running container\n", n.Name)
					continue
				}
				if err := cli.NetworkRemove(ctx, n.ID); err != nil {
					fmt.Fprintf(os.Stderr, "  failed to remove network %s: %v\n", n.Name, err)
				} else {
					result.NetworksRemoved = append(result.NetworksRemoved, runtime.ServiceNetworkName)
				}
			}
		}
	}

	return result, nil
}

// runningContainerRefs returns the tpd-managed resources referenced by running
// containers: volume names by mount, and image IDs by inspect. Only tpd's own
// containers (ownership label) are considered.
func runningContainerRefs(ctx context.Context, cli dockerClient) (volumes map[string]bool, images map[string]bool, err error) {
	f := filters.NewArgs()
	f.Add("label", runtime.OwnershipLabel+"=true")
	containers, err := cli.ContainerList(ctx, container.ListOptions{Filters: f})
	if err != nil {
		return nil, nil, err
	}
	volumes, images = map[string]bool{}, map[string]bool{}
	for _, c := range containers {
		insp, _, err := cli.ContainerInspectWithRaw(ctx, c.ID, false)
		if err != nil {
			return nil, nil, fmt.Errorf("inspect container %s: %w", c.ID, err)
		}
		for _, m := range insp.Mounts {
			if m.Type == mount.TypeVolume {
				volumes[m.Name] = true
			}
		}
		images[insp.Image] = true
	}
	return volumes, images, nil
}

// runningContainerNetworks returns every network a running container is
// attached to, keyed by name and ID. Unlike runningContainerRefs it is
// deliberately unfiltered: an unlabeled container attached to the managed
// network protects it as strongly as a tpd container would.
func runningContainerNetworks(ctx context.Context, cli dockerClient) (map[string]bool, error) {
	containers, err := cli.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return nil, err
	}
	inUse := map[string]bool{}
	for _, c := range containers {
		insp, _, err := cli.ContainerInspectWithRaw(ctx, c.ID, false)
		if err != nil {
			return nil, fmt.Errorf("inspect container %s: %w", c.ID, err)
		}
		if insp.NetworkSettings == nil {
			continue
		}
		for name, ep := range insp.NetworkSettings.Networks {
			inUse[name] = true
			if ep != nil && ep.NetworkID != "" {
				inUse[ep.NetworkID] = true
			}
		}
	}
	return inUse, nil
}

// imageInUse reports whether the derived image ref is referenced by a running
// container, resolved by comparing the ref's image ID against the used set.
// Inspection failures are returned so prune fails closed instead of treating an
// unresolvable image as unused.
func imageInUse(ctx context.Context, cli dockerClient, ref string, usedImages map[string]bool) (bool, error) {
	inspect, _, err := cli.ImageInspectWithRaw(ctx, ref)
	if err != nil {
		return false, fmt.Errorf("inspect image %s: %w", ref, err)
	}
	return usedImages[inspect.ID], nil
}

func volumeInUse(ctx context.Context, cli dockerClient, name string) (bool, error) {
	volumes, _, err := runningContainerRefs(ctx, cli)
	return volumes[name], err
}

func imageInUseNow(ctx context.Context, cli dockerClient, ref string) (bool, error) {
	_, images, err := runningContainerRefs(ctx, cli)
	if err != nil {
		return false, err
	}
	return imageInUse(ctx, cli, ref, images)
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
		for _, svc := range cfg.Services {
			for cacheName := range svc.Caches {
				usedVolumes["tpd-cache-"+cacheName] = true
			}
			if len(svc.Packages) > 0 || len(svc.Repos) > 0 {
				if svc.Image == "" {
					continue
				}
				inspect, _, err := cli.ImageInspectWithRaw(ctx, svc.Image)
				if err != nil {
					continue
				}
				svcRepos := make(map[string]runtime.Repo, len(svc.Repos))
				for rname, r := range svc.Repos {
					svcRepos[rname] = runtime.Repo{ExtRepo: r.ExtRepo, URL: r.URL, KeyURL: r.KeyURL, Suites: r.Suites, Components: r.Components}
				}
				if tag := runtime.DerivedTag(inspect.ID, svc.Packages, svcRepos); tag != "" {
					usedImages[tag] = true
				}
			}
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

// listTpdNetworks selects the managed service network: only the canonical name
// carrying both the ownership and services-role labels. A same-name network
// missing either label is reported and never removed.
func listTpdNetworks(ctx context.Context, cli dockerClient) ([]network.Summary, error) {
	networks, err := cli.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil, err
	}
	var found []network.Summary
	for _, n := range networks {
		if n.Name != runtime.ServiceNetworkName {
			continue
		}
		if n.Labels[runtime.OwnershipLabel] != "true" {
			fmt.Fprintf(os.Stderr, "warning: skipping unlabeled %s network (not tpd-owned)\n", n.Name)
			continue
		}
		if n.Labels[runtime.NetworkRoleLabel] != runtime.NetworkRoleServices {
			fmt.Fprintf(os.Stderr, "warning: skipping %s (not a tpd services network)\n", n.Name)
			continue
		}
		found = append(found, n)
	}
	return found, nil
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
