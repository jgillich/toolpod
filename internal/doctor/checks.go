package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/jgillich/toolpod/internal/profile"
	"github.com/jgillich/toolpod/internal/runtime"
)

func runChecks(ctx context.Context, rt *dockerRT, opts Options) Result {
	var checks []Check

	checks = append(checks, checkRuntimeReachable(ctx, rt))
	checks = append(checks, checkRootless(ctx, rt))
	checks = append(checks, checkBuildKit(ctx, rt))
	checks = append(checks, checkMiseBaseImage(ctx, rt))
	checks = append(checks, checkVolumes(ctx, rt))
	checks = append(checks, checkPermissions(ctx, rt))

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

func checkBuildKit(ctx context.Context, rt *dockerRT) Check {
	ping, err := rt.cli.Ping(ctx)
	if err != nil {
		return Check{Name: "buildkit", Status: Warn, Message: "unreachable"}
	}
	if ping.BuilderVersion != "" {
		return Check{Name: "buildkit", Status: Pass, Message: "available (" + string(ping.BuilderVersion) + ")"}
	}
	return Check{Name: "buildkit", Status: Warn, Message: "not detected (build: profiles require it)"}
}

const miseBaseImage = "ghcr.io/jdx/mise:latest"

func checkMiseBaseImage(ctx context.Context, rt *dockerRT) Check {
	_, _, err := rt.cli.ImageInspectWithRaw(ctx, miseBaseImage)
	if err != nil {
		if client.IsErrNotFound(err) {
			return Check{Name: "mise base image", Status: Info, Message: "not present (will pull on first launch)"}
		}
		return Check{Name: "mise base image", Status: Warn, Message: err.Error()}
	}
	return Check{Name: "mise base image", Status: Pass, Message: "present"}
}

func checkVolumes(ctx context.Context, rt *dockerRT) Check {
	volumes, err := rt.cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return Check{Name: "volumes", Status: Warn, Message: err.Error()}
	}
	var found []string
	for _, v := range volumes.Volumes {
		if strings.HasPrefix(v.Name, "toolpod-") {
			found = append(found, v.Name)
		}
	}
	if len(found) == 0 {
		return Check{Name: "volumes", Status: Pass, Message: "none yet (will create on first launch)"}
	}
	return Check{Name: "volumes", Status: Pass, Message: strings.Join(found, ", ")}
}

func checkPermissions(ctx context.Context, rt *dockerRT) Check {
	_, err := rt.cli.VolumeCreate(ctx, volume.CreateOptions{Name: "toolpod-perm-test"})
	if err != nil {
		return Check{Name: "permissions", Status: Fail, Message: "cannot create volumes: " + err.Error()}
	}
	_ = rt.cli.VolumeRemove(ctx, "toolpod-perm-test", true)

	resp, err := rt.cli.ContainerCreate(ctx, &container.Config{
		Image: "alpine:latest",
		Cmd:   []string{"echo", "ok"},
	}, nil, nil, nil, "")
	if err != nil {
		return Check{Name: "permissions", Status: Fail, Message: "cannot create containers: " + err.Error()}
	}
	_ = rt.cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})

	return Check{Name: "permissions", Status: Pass, Message: "can create containers and volumes"}
}

func checkProfileValidity(userDir string) Check {
	cat, err := profile.LoadProfiles(userDir)
	if err != nil {
		return Check{Name: "profiles", Status: Fail, Message: err.Error()}
	}
	var errs []string
	launchable := 0
	for _, name := range cat.Names() {
		rc, ok := cat.Get(name)
		if !ok {
			continue
		}
		// Validate every profile for structural errors (version, image/build).
		// A profile with no command is a base — valid as an extends target,
		// just not directly launchable. ResolveProfile enforces command, so
		// tolerate that one error for base profiles and count them as valid.
		_, err := profile.ResolveProfile(cat, name)
		if err != nil {
			if len(rc.Command) == 0 && rc.Extends == "" {
				// Base profile: missing command is expected, not an error.
				continue
			}
			errs = append(errs, name+": "+err.Error())
		}
		launchable++
	}
	if len(errs) > 0 {
		return Check{Name: "profiles", Status: Fail, Message: strings.Join(errs, "; ")}
	}
	return Check{Name: "profiles", Status: Pass, Message: fmt.Sprintf("%d profiles, all valid", launchable)}
}

func checkUserOverrides(userDir string) []Check {
	if userDir == "" {
		return []Check{{Name: "presets", Status: Skip, Message: "no user profile directory"}}
	}

	catMerged, err := profile.LoadProfiles(userDir)
	if err != nil {
		return []Check{{Name: "presets", Status: Warn, Message: err.Error()}}
	}

	var checks []Check
	userFileCount := 0
	for _, name := range catMerged.Names() {
		rc, ok := catMerged.Get(name)
		if !ok {
			continue
		}
		// Skip built-in profiles; only check user-overridden files.
		// Built-in paths start with "built-in:".
		if strings.HasPrefix(rc.Path, "built-in:") {
			continue
		}
		userFileCount++
		cfg, err := profile.ResolveProfile(catMerged, name)
		if err != nil {
			continue
		}
		if len(cfg.Caches) == 0 {
			checks = append(checks, Check{
				Name:    "caches",
				Status:  Info,
				Message: fmt.Sprintf("%s: none configured (run `toolpod init %s` to enable)", name, name),
			})
		}
		if _, hasGit := cfg.Mounts["~/.gitconfig"]; !hasGit {
			checks = append(checks, Check{
				Name:    "gitconfig",
				Status:  Info,
				Message: fmt.Sprintf("%s: not mounted (run `toolpod init %s --presets gitconfig`)", name, name),
			})
		}
	}

	if userFileCount == 0 {
		return []Check{{Name: "presets", Status: Info, Message: "no user profile overrides; built-in profiles no longer auto-mount caches/gitconfig — run `toolpod init <profile>` to add them"}}
	}
	if len(checks) == 0 {
		return []Check{{Name: "presets", Status: Pass, Message: "all user overrides have caches and gitconfig"}}
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
	testFile := filepath.Join(workspace, ".toolpod-write-test")
	if err := os.WriteFile(testFile, []byte("x"), 0o644); err != nil {
		return Check{Name: "workspace", Status: Fail, Message: workspace + " is not writable"}
	}
	os.Remove(testFile)
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
