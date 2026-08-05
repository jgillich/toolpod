package doctor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/jgillich/tpd/internal/profile"
	"github.com/jgillich/tpd/internal/runtime"
	"golang.org/x/sys/unix"
)

func runChecks(ctx context.Context, rt *dockerRT, opts Options) Result {
	var checks []Check

	checks = append(checks, checkRuntimeReachable(ctx, rt))
	checks = append(checks, checkRootless(ctx, rt))
	checks = append(checks, checkSELinux(runtime.SELinuxEnforcing()))
	checks = append(checks, checkMiseBaseImage(ctx, rt))
	checks = append(checks, checkDerivedImages(ctx, rt))
	checks = append(checks, checkVolumes(ctx, rt))
	checks = append(checks, checkUnlabeledLegacyResources(ctx, rt))
	checks = append(checks, checkPermissions(ctx, rt))
	checks = append(checks, checkLeakedContainers(ctx, rt))
	checks = append(checks, checkStaleBusSockets())

	userDir := opts.ProfileDir
	if userDir == "" {
		userDir = profile.DefaultProfileDir()
	}
	checks = append(checks, checkProfileValidity(userDir))
	checks = append(checks, checkUserOverrides(userDir)...)

	ws := opts.Workspace
	if ws == "" {
		ws, _ = os.Getwd()
	}
	checks = append(checks, checkProjectTools(ctx, ws))
	checks = append(checks, checkWorkspaceWritable(ctx, ws))

	return Result{Checks: checks}
}

func checkRuntimeReachable(ctx context.Context, rt *dockerRT) Check {
	info, err := rt.cli.Info(ctx)
	if err != nil {
		return Check{Name: "runtime", Status: Fail, Message: "unreachable: " + err.Error()}
	}
	engine := "docker"
	if info.OSType == "" || strings.Contains(info.Name, "podman") {
		engine = "podman"
	}
	return Check{Name: "runtime", Status: Pass, Message: fmt.Sprintf("%s at %s", engine, rt.cli.DaemonHost())}
}

func checkRootless(ctx context.Context, rt *dockerRT) Check {
	rootless, err := runtime.QueryRootless(ctx, rt.cli)
	if err != nil {
		return Check{Name: "rootless", Status: Fail, Message: err.Error()}
	}
	if !rootless {
		return Check{Name: "rootless", Status: Pass, Message: "no → Mode B (/workspace fallback)"}
	}
	return Check{Name: "rootless", Status: Pass, Message: "yes → Mode A (full mirroring)"}
}

func checkSELinux(enforcing bool) Check {
	if enforcing {
		return Check{Name: "selinux", Status: Pass, Message: "enforcing → containers run with label=disable"}
	}
	return Check{Name: "selinux", Status: Pass, Message: "not enforcing (label separation left on)"}
}

func checkMiseBaseImage(ctx context.Context, rt *dockerRT) Check {
	cat, err := profile.LoadProfiles("")
	if err != nil {
		return Check{Name: "mise base image", Status: Warn, Message: err.Error()}
	}
	base, ok := cat.Get("core/mise")
	if !ok {
		return Check{Name: "mise base image", Status: Warn, Message: "built-in mise profile not found"}
	}
	image := base.Image
	_, _, err = rt.cli.ImageInspectWithRaw(ctx, image)
	if err != nil {
		if client.IsErrNotFound(err) {
			return Check{Name: "mise base image", Status: Info, Message: "not present (will pull on first launch)"}
		}
		return Check{Name: "mise base image", Status: Warn, Message: err.Error()}
	}
	return Check{Name: "mise base image", Status: Pass, Message: "present"}
}

func checkDerivedImages(ctx context.Context, rt *dockerRT) Check {
	f := filters.NewArgs()
	f.Add("reference", "tpd/packages")
	images, err := rt.cli.ImageList(ctx, image.ListOptions{Filters: f})
	if err != nil {
		return Check{Name: "derived images", Status: Warn, Message: err.Error()}
	}
	var tags []string
	var total int64
	for _, img := range images {
		if img.Labels[runtime.OwnershipLabel] != "true" {
			continue
		}
		hasTpdTag := false
		for _, t := range img.RepoTags {
			if ref := runtime.DerivedRef(t); ref != "" {
				tags = append(tags, ref)
				hasTpdTag = true
			}
		}
		if hasTpdTag && img.Size > 0 {
			total += img.Size
		}
	}
	sort.Strings(tags)
	if len(tags) == 0 {
		return Check{Name: "derived images", Status: Pass, Message: "none yet (built on first launch of a profile with packages)"}
	}
	return Check{Name: "derived images", Status: Pass, Message: fmt.Sprintf("%d cached (%s reclaimable): %s", len(tags), humanSize(total), strings.Join(tags, ", "))}
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func checkVolumes(ctx context.Context, rt *dockerRT) Check {
	volumes, err := rt.cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return Check{Name: "volumes", Status: Warn, Message: err.Error()}
	}
	var found []string
	for _, v := range volumes.Volumes {
		if strings.HasPrefix(v.Name, "tpd-") && v.Labels[runtime.OwnershipLabel] == "true" {
			found = append(found, v.Name)
		}
	}
	if len(found) == 0 {
		return Check{Name: "volumes", Status: Pass, Message: "none yet (will create on first launch)"}
	}
	return Check{Name: "volumes", Status: Pass, Message: strings.Join(found, ", ")}
}

func checkPermissions(ctx context.Context, rt *dockerRT) Check {
	suffix, err := randomSuffix(8)
	if err != nil {
		return Check{Name: "permissions", Status: Fail, Message: "cannot generate a safe probe name: " + err.Error()}
	}
	probe := "tpd-diag-" + suffix
	if _, err := rt.cli.VolumeCreate(ctx, volume.CreateOptions{Name: probe}); err != nil {
		return Check{Name: "permissions", Status: Fail, Message: "cannot create volumes: " + err.Error()}
	}
	if err := rt.cli.VolumeRemove(ctx, probe, true); err != nil {
		return Check{Name: "permissions", Status: Warn, Message: "created probe volume but could not remove " + probe + " (remove manually): " + err.Error()}
	}

	images, err := rt.cli.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return Check{Name: "permissions", Status: Fail, Message: "cannot list images: " + err.Error()}
	}
	ref := firstRunableImage(images) // first image with a non-empty ID
	if ref == "" {
		return Check{Name: "permissions", Status: Info, Message: "volume creation OK; container-creation probe skipped (no local image)"}
	}
	resp, err := rt.cli.ContainerCreate(ctx, &container.Config{Image: ref, Cmd: []string{"true"}}, nil, nil, nil, "")
	if err != nil {
		return Check{Name: "permissions", Status: Fail, Message: "cannot create containers: " + err.Error()}
	}
	if err := rt.cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true}); err != nil {
		return Check{Name: "permissions", Status: Warn, Message: "created container but could not remove probe: " + err.Error()}
	}
	return Check{Name: "permissions", Status: Pass, Message: "can create containers and volumes"}
}

// firstRunableImage returns a create-time reference for the first image with
// an ID; image IDs work as an image reference without any tag.
func firstRunableImage(images []image.Summary) string {
	for _, img := range images {
		if img.ID != "" {
			return img.ID
		}
	}
	return ""
}

func randomSuffix(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func checkUnlabeledLegacyResources(ctx context.Context, rt *dockerRT) Check {
	var unlabeled []string

	volumes, err := rt.cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return Check{Name: "legacy resources", Status: Warn, Message: err.Error()}
	}
	for _, v := range volumes.Volumes {
		if strings.HasPrefix(v.Name, "tpd-") && !strings.HasPrefix(v.Name, "tpd-diag-") && v.Labels[runtime.OwnershipLabel] != "true" {
			unlabeled = append(unlabeled, v.Name)
		}
	}

	f := filters.NewArgs()
	f.Add("reference", "tpd/packages")
	images, err := rt.cli.ImageList(ctx, image.ListOptions{Filters: f})
	if err != nil {
		return Check{Name: "legacy resources", Status: Warn, Message: err.Error()}
	}
	for _, img := range images {
		if img.Labels[runtime.OwnershipLabel] == "true" {
			continue
		}
		ref := ""
		for _, t := range img.RepoTags {
			if d := runtime.DerivedRef(t); d != "" {
				ref = d
				break
			}
		}
		if ref == "" && len(img.RepoTags) == 0 {
			ref = img.ID
		}
		if ref != "" {
			unlabeled = append(unlabeled, ref)
		}
	}

	if len(unlabeled) == 0 {
		return Check{Name: "legacy resources", Status: Pass, Message: "none (all tpd resources carry the ownership label)"}
	}
	sort.Strings(unlabeled)
	return Check{Name: "legacy resources", Status: Info, Message: "may not be tpd-owned; not pruned automatically: " + strings.Join(unlabeled, ", ")}
}

func checkStaleBusSockets() Check {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return Check{Name: "stale dbus sockets", Status: Pass, Message: "no XDG_RUNTIME_DIR"}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "tpd-bus-*.sock"))
	if len(matches) == 0 {
		return Check{Name: "stale dbus sockets", Status: Pass, Message: "none"}
	}
	return Check{Name: "stale dbus sockets", Status: Warn, Message: strings.Join(matches, ", ")}
}

func checkLeakedContainers(ctx context.Context, rt *dockerRT) Check {
	f := filters.NewArgs()
	f.Add("label", runtime.OwnershipLabel+"=true")
	containers, err := rt.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return Check{Name: "leaked containers", Status: Warn, Message: err.Error()}
	}

	consumedServices := map[string]bool{}
	for _, c := range containers {
		// Count non-exited containers (running/created/paused) as consumers —
		// a created-state main container is a real consumer (same fix as
		// StopServices). This prevents doctor from flagging a healthy
		// sidecar during the concurrent-create window.
		if c.State == "exited" || c.State == "dead" {
			continue
		}
		if c.Labels[runtime.ServiceRoleLabel] == runtime.ServiceRoleSidecar {
			continue
		}
		if uses := c.Labels[runtime.UsesServiceLabel]; uses != "" {
			for _, name := range strings.Split(uses, ",") {
				consumedServices[strings.TrimSpace(name)] = true
			}
		}
	}

	var leaked []string
	for _, c := range containers {
		if c.Labels[runtime.ServiceRoleLabel] == runtime.ServiceRoleSidecar {
			if c.State == "running" && consumedServices[c.Labels[runtime.ServiceLabel]] {
				continue
			}
			leaked = append(leaked, strings.Join(c.Names, ","))
		} else {
			leaked = append(leaked, strings.Join(c.Names, ","))
		}
	}
	sort.Strings(leaked)
	if len(leaked) == 0 {
		return Check{Name: "leaked containers", Status: Pass, Message: "none"}
	}
	return Check{Name: "leaked containers", Status: Warn, Message: strings.Join(leaked, ", ") + " (remove with: docker rm -f ...)"}
}

func checkProfileValidity(userDir string) Check {
	cat, err := profile.LoadProfiles(userDir)
	if err != nil {
		return Check{Name: "profiles", Status: Fail, Message: err.Error()}
	}
	var errs []string
	var resources []string
	launchable := 0
	// Fragments are composition-only and not directly launchable; they are
	// validated at load (identity fields) and when resolved via a profile.
	for _, name := range cat.ProfileNames() {
		rc, ok := cat.Get(name)
		if !ok {
			continue
		}
		// Validate every profile for structural errors (version, image/build).
		// A profile with no command is a base — valid as an extends target,
		// just not directly launchable. ResolveProfile enforces command, so
		// tolerate that one error for base profiles and count them as valid.
		cfg, err := profile.ResolveProfile(cat, name)
		if err != nil {
			if len(rc.Command) == 0 && len(rc.ExtendsList.Raw) == 0 {
				// Base profile: missing command is expected, not an error.
				continue
			}
			errs = append(errs, name+": "+err.Error())
		}
		if cfg.Resources != nil {
			resources = append(resources, fmt.Sprintf("%s(%s,%s)", name, cfg.Resources.Memory, cfg.Resources.CPUs))
		}
		launchable++
	}
	if len(errs) > 0 {
		return Check{Name: "profiles", Status: Fail, Message: strings.Join(errs, "; ")}
	}
	msg := fmt.Sprintf("%d profiles, all valid", launchable)
	if len(resources) > 0 {
		msg += ", resources: " + strings.Join(resources, ", ")
	}
	return Check{Name: "profiles", Status: Pass, Message: msg}
}

func checkUserOverrides(userDir string) []Check {
	if userDir == "" {
		return []Check{{Name: "fragments", Status: Skip, Message: "no user profile directory"}}
	}

	catMerged, err := profile.LoadProfiles(userDir)
	if err != nil {
		return []Check{{Name: "fragments", Status: Warn, Message: err.Error()}}
	}

	var checks []Check
	userFileCount := 0
	for _, name := range catMerged.Names() {
		rc, ok := catMerged.Get(name)
		if !ok {
			continue
		}
		if rc.Namespace == "core" || catMerged.IsFragment(name) {
			continue
		}
		userFileCount++
		cfg, err := profile.ResolveProfile(catMerged, name)
		if err != nil {
			continue
		}
		if _, hasGit := cfg.Mounts["~/.gitconfig"]; !hasGit {
			checks = append(checks, Check{
				Name:    "gitconfig",
				Status:  Info,
				Message: fmt.Sprintf("%s: not mounted (run `tpd init %s --extends creds/gitconfig`)", name, name),
			})
		}
	}

	if userFileCount == 0 {
		return []Check{{Name: "fragments", Status: Info, Message: "no user profile overrides — run `tpd init <profile>` to add caches and gitconfig"}}
	}
	if len(checks) == 0 {
		return []Check{{Name: "fragments", Status: Pass, Message: "all user overrides mount gitconfig"}}
	}
	return checks
}

func checkProjectTools(ctx context.Context, workspace string) Check {
	toolsFile := filepath.Join(workspace, ".tool-versions")
	miseFile := filepath.Join(workspace, "mise.toml")

	var tools []string
	if data, err := os.ReadFile(toolsFile); err == nil {
		tools = parseToolVersions(string(data))
	}
	if data, err := os.ReadFile(miseFile); err == nil {
		tools = append(tools, parseMiseToml(string(data))...)
	}

	if len(tools) == 0 {
		return Check{Name: "project tools", Status: Info, Message: "none detected (no mise.toml or .tool-versions)"}
	}
	return Check{Name: "project tools", Status: Pass, Message: strings.Join(tools, ", ") + " (from project)"}
}

func checkWorkspaceWritable(ctx context.Context, workspace string) Check {
	suffix, err := randomSuffix(4)
	if err != nil {
		return Check{Name: "workspace", Status: Fail, Message: "cannot generate a safe workspace probe name: " + err.Error()}
	}
	probe := filepath.Join(workspace, ".tpd-write-test-"+suffix)
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW|os.O_WRONLY, 0o644)
	if err != nil {
		return Check{Name: "workspace", Status: Fail, Message: workspace + " is not writable: " + err.Error()}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(probe)
		return Check{Name: "workspace", Status: Fail, Message: workspace + " write probe close failed: " + err.Error()}
	}
	if err := os.Remove(probe); err != nil {
		return Check{Name: "workspace", Status: Warn, Message: workspace + " is writable but probe cleanup failed: " + err.Error()}
	}
	return Check{Name: "workspace", Status: Pass, Message: workspace + " is writable"}
}

func parseToolVersions(content string) []string {
	var tools []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			tools = append(tools, parts[0]+"@"+parts[1])
		}
	}
	return tools
}

func parseMiseToml(content string) []string {
	var data struct {
		Tools map[string]any `toml:"tools"`
	}
	if _, err := toml.Decode(content, &data); err != nil {
		return nil
	}
	var tools []string
	for name, val := range data.Tools {
		switch v := val.(type) {
		case string:
			tools = append(tools, name+"@"+v)
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok {
					tools = append(tools, name+"@"+s)
				}
			}
		case map[string]any:
			if ver, ok := v["version"].(string); ok {
				tools = append(tools, name+"@"+ver)
			}
		}
	}
	sort.Strings(tools)
	return tools
}
