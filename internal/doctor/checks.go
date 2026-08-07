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
	"time"

	"github.com/BurntSushi/toml"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/jgillich/tpd/internal/profile"
	"github.com/jgillich/tpd/internal/runtime"
	"golang.org/x/sys/unix"
)

// runtimeDependentChecks require a reachable daemon; on a runtime failure they
// are reported as Skip instead of each failing individually.
var runtimeDependentChecks = []struct {
	name string
	run  func(context.Context, *dockerRT) Check
}{
	{"rootless", checkRootless},
	{"service network", checkServiceNetwork},
	{"mise base image", checkMiseBaseImage},
	{"derived images", checkDerivedImages},
	{"volumes", checkVolumes},
	{"legacy resources", checkUnlabeledLegacyResources},
	{"permissions", checkPermissions},
	{"leaked containers", checkLeakedContainers},
	{"stale dbus sockets", checkStaleBusSockets},
}

func runChecks(ctx context.Context, rt *dockerRT, opts Options) Result {
	var checks []Check

	runtimeCheck := checkRuntimeReachable(ctx, rt)
	checks = append(checks, runtimeCheck)
	checks = append(checks, checkSELinux(runtime.SELinuxEnforcing()))

	if runtimeCheck.Status != Fail {
		for _, rc := range runtimeDependentChecks {
			checks = append(checks, rc.run(ctx, rt))
		}
	} else {
		skipMsg := "skipped: daemon unreachable"
		if strings.HasPrefix(runtimeCheck.Message, "unsupported") {
			skipMsg = "skipped: unsupported daemon host scheme"
		}
		for _, rc := range runtimeDependentChecks {
			checks = append(checks, Check{Name: rc.name, Status: Skip, Message: skipMsg})
		}
	}

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
	// Reject daemon hosts the raw HTTP checks cannot dial (ssh://, npipe://)
	// before querying /info, so unsupported schemes are reported clearly.
	if _, _, err := runtime.NewDaemonHTTPClient(rt.cli.DaemonHost()); err != nil {
		return Check{Name: "runtime", Status: Fail, Message: err.Error()}
	}
	info, err := runtime.DaemonInfo(ctx, rt.cli)
	if err != nil {
		return Check{Name: "runtime", Status: Fail, Message: "unreachable: " + err.Error()}
	}
	engine := "docker"
	if info.OSType == "" || strings.Contains(info.Name, "podman") {
		engine = "podman"
	}
	rt.engine = engine
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

// checkServiceNetwork reports the shared service bridge. A canonical-name
// network missing an invariant is a hard failure (service discovery breaks
// silently); the check names the network and the failed invariant, mirroring
// runtime.validateServiceNetwork without depending on its unexported helper.
func checkServiceNetwork(ctx context.Context, rt *dockerRT) Check {
	inspected, err := rt.cli.NetworkInspect(ctx, runtime.ServiceNetworkName, network.InspectOptions{})
	if err != nil {
		if client.IsErrNotFound(err) {
			return Check{Name: "service network", Status: Info, Message: "not present (created on first launch with services)"}
		}
		return Check{Name: "service network", Status: Warn, Message: err.Error()}
	}
	switch {
	case inspected.Labels[runtime.OwnershipLabel] != "true":
		return Check{Name: "service network", Status: Fail, Message: fmt.Sprintf("%s is not tpd-managed (want %s=true, got %q)", inspected.Name, runtime.OwnershipLabel, inspected.Labels[runtime.OwnershipLabel])}
	case inspected.Labels[runtime.NetworkRoleLabel] != runtime.NetworkRoleServices:
		return Check{Name: "service network", Status: Fail, Message: fmt.Sprintf("%s has role %s=%q, want %q", inspected.Name, runtime.NetworkRoleLabel, inspected.Labels[runtime.NetworkRoleLabel], runtime.NetworkRoleServices)}
	case inspected.Driver != "bridge":
		return Check{Name: "service network", Status: Fail, Message: fmt.Sprintf("%s is not a bridge (driver %q)", inspected.Name, inspected.Driver)}
	}
	return Check{Name: "service network", Status: Pass, Message: fmt.Sprintf("%s (%s), %d connected (%s=%s, %s=%s)", inspected.Name, inspected.Driver, len(inspected.Containers), runtime.OwnershipLabel, inspected.Labels[runtime.OwnershipLabel], runtime.NetworkRoleLabel, inspected.Labels[runtime.NetworkRoleLabel])}
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

func checkPermissions(ctx context.Context, rt *dockerRT) (check Check) {
	suffix, err := randomSuffix(8)
	if err != nil {
		return Check{Name: "permissions", Status: Fail, Message: "cannot generate a safe probe name: " + err.Error()}
	}
	probe := "tpd-diag-" + suffix

	var cleanupIssues []string
	defer func() {
		if len(cleanupIssues) == 0 {
			return
		}
		suffix := "probe cleanup failed: " + strings.Join(cleanupIssues, "; ")
		if check.Status == Pass || check.Status == Info {
			check.Status = Warn
			check.Message = suffix
		} else {
			check.Message += " (" + suffix + ")"
		}
	}()

	if _, err := rt.cli.VolumeCreate(ctx, volume.CreateOptions{Name: probe}); err != nil {
		return Check{Name: "permissions", Status: Fail, Message: "cannot create volumes: " + err.Error()}
	}
	defer func() {
		cctx, cancel := cleanupCtx()
		defer cancel()
		if err := rt.cli.VolumeRemove(cctx, probe, true); err != nil {
			cleanupIssues = append(cleanupIssues, "volume "+probe+": "+err.Error())
		}
	}()

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
	defer func() {
		cctx, cancel := cleanupCtx()
		defer cancel()
		if err := rt.cli.ContainerRemove(cctx, resp.ID, container.RemoveOptions{Force: true}); err != nil {
			cleanupIssues = append(cleanupIssues, "container "+resp.ID+": "+err.Error())
		}
	}()

	return Check{Name: "permissions", Status: Pass, Message: "can create containers and volumes"}
}

// cleanupCtx returns a short-lived context so probe cleanup is still attempted
// after the doctor deadline expires instead of being abandoned with a stale
// context. cleanupTimeout is a var so tests can shrink it.
var cleanupTimeout = 5 * time.Second

func cleanupCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), cleanupTimeout)
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

func checkStaleBusSockets(ctx context.Context, rt *dockerRT) Check {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return Check{Name: "stale dbus sockets", Status: Pass, Message: "no XDG_RUNTIME_DIR"}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "tpd-bus-*.sock"))
	if len(matches) == 0 {
		return Check{Name: "stale dbus sockets", Status: Pass, Message: "none"}
	}
	sort.Strings(matches)
	mounted, listErr := mountedBusSockets(ctx, rt)
	var active, stale []string
	for _, m := range matches {
		if mounted[m] || socketHasLiveOwner(m) {
			active = append(active, m)
		} else {
			stale = append(stale, m)
		}
	}
	// A container-list failure invalidates the container-mount signal, so
	// sockets may be misreported stale; surface it instead of silently
	// degrading to host-signal-only.
	if listErr != nil {
		return Check{Name: "stale dbus sockets", Status: Warn, Message: "cannot list containers: " + listErr.Error() + " (stale-socket assessment incomplete)"}
	}
	if len(stale) == 0 {
		return Check{Name: "stale dbus sockets", Status: Pass, Message: "none stale"}
	}
	msg := "stale: " + strings.Join(stale, ", ")
	if len(active) > 0 {
		msg += " (active: " + strings.Join(active, ", ") + ")"
	}
	return Check{Name: "stale dbus sockets", Status: Warn, Message: msg}
}

// mountedBusSockets returns the host paths mounted into non-exited tpd-owned
// containers; a socket held this way is live even though its socket object
// lives in the container's netns and may not appear in the host's socket table
// (it does when the container shares the host netns).
func mountedBusSockets(ctx context.Context, rt *dockerRT) (map[string]bool, error) {
	mounted := map[string]bool{}
	if rt == nil || rt.cli == nil {
		return mounted, nil
	}
	f := filters.NewArgs()
	f.Add("label", runtime.OwnershipLabel+"=true")
	containers, err := rt.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}
	for _, c := range containers {
		if c.State == "exited" || c.State == "dead" {
			continue
		}
		for _, m := range c.Mounts {
			if m.Source != "" {
				mounted[m.Source] = true
			}
		}
	}
	return mounted, nil
}

// socketHasLiveOwner reports whether the socket object at path is still
// bound. The kernel's unix socket table lists bound pathname sockets; once the
// last fd closes the socket is garbage-collected and its entry disappears even
// though the pathname file may linger on disk. Container-held sockets are
// detected by mountedBusSockets because they usually live in another netns.
func socketHasLiveOwner(path string) bool {
	data, err := os.ReadFile("/proc/net/unix")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 8 && fields[len(fields)-1] == path {
			return true
		}
	}
	return false
}

func checkLeakedContainers(ctx context.Context, rt *dockerRT) Check {
	f := filters.NewArgs()
	f.Add("label", runtime.OwnershipLabel+"=true")
	containers, err := rt.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return Check{Name: "leaked containers", Status: Warn, Message: err.Error()}
	}

	// running/created/paused/restarting containers are active resources of a
	// live session; only exited/dead owned containers are cleanup leaks.
	var leaked, active []string
	for _, c := range containers {
		names := strings.Join(c.Names, ",")
		switch c.State {
		case "exited", "dead":
			leaked = append(leaked, names)
		default:
			active = append(active, names)
		}
	}
	sort.Strings(leaked)
	sort.Strings(active)

	if len(leaked) == 0 && len(active) == 0 {
		return Check{Name: "leaked containers", Status: Pass, Message: "none"}
	}
	var parts []string
	if len(leaked) > 0 {
		engine := "docker"
		if rt.engine != "" {
			engine = rt.engine
		}
		parts = append(parts, "leaked (exited/dead): "+strings.Join(leaked, ", ")+" (remove with: "+engine+" rm -f "+strings.Join(leaked, " ")+")")
	}
	if len(active) > 0 {
		parts = append(parts, "active: "+strings.Join(active, ", "))
	}
	status := Pass
	if len(leaked) > 0 {
		status = Warn
	}
	return Check{Name: "leaked containers", Status: status, Message: strings.Join(parts, "; ")}
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
	var parseErrs []string
	if data, err := os.ReadFile(miseFile); err == nil {
		parsed, err := parseMiseToml(string(data))
		if err != nil {
			parseErrs = append(parseErrs, err.Error())
		} else {
			tools = append(tools, parsed...)
		}
	}

	if len(parseErrs) > 0 {
		msg := "mise.toml parse error: " + strings.Join(parseErrs, "; ")
		if len(tools) > 0 {
			msg += " (detected: " + strings.Join(tools, ", ") + ")"
		}
		return Check{Name: "project tools", Status: Warn, Message: msg}
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

func parseMiseToml(content string) ([]string, error) {
	var data struct {
		Tools map[string]any `toml:"tools"`
	}
	if _, err := toml.Decode(content, &data); err != nil {
		return nil, err
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
	return tools, nil
}
