package mise

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gofrs/flock"
)

// ContainerRunner runs a command in a throwaway container (auto-removed)
// with named volumes mounted. Implemented by DockerRuntime; accepted by
// EnsureTools to avoid an import cycle between runtime and mise.
type ContainerRunner interface {
	RunInContainer(ctx context.Context, image string, volumes []VolumeMount, env []string, cmd []string) (int, error)
}

// VolumeMount is a named volume to mount in a ContainerRunner execution.
type VolumeMount struct {
	Name   string
	Target string
}

// ToolsSpec is the subset of the container spec needed for tool installation.
type ToolsSpec struct {
	Image string
	Tools map[string]string
}

// EnsureTools acquires an exclusive flock on a sentinel file (cross-process
// safety), then batches all tool installs into a single throwaway container.
// Spec §6.3 + concurrent-install lock.
func EnsureTools(ctx context.Context, runner ContainerRunner, spec ToolsSpec, runtimeHome string, w ProgressWriter) error {
	if len(spec.Tools) == 0 {
		return nil
	}

	miseVol := MiseVolume(runtimeHome)

	lockDir := os.Getenv("XDG_RUNTIME_DIR")
	if lockDir == "" {
		lockDir = filepath.Join(os.TempDir(), fmt.Sprintf("toolpod-%d", os.Getuid()))
	}
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return fmt.Errorf("create mise lock dir: %w", err)
	}
	lockFile := filepath.Join(lockDir, fmt.Sprintf("mise-%d.lock", os.Getuid()))
	fl := flock.New(lockFile)
	locked, err := fl.TryLockContext(ctx, 0)
	if err != nil {
		return fmt.Errorf("acquire mise lock: %w", err)
	}
	if !locked {
		w.WriteProgress("mise: waiting for another install to finish...")
		if err := fl.Lock(); err != nil {
			return fmt.Errorf("acquire mise lock (waited): %w", err)
		}
	}
	defer fl.Unlock()

	w.WriteProgress(fmt.Sprintf("mise: installing %d tools", len(spec.Tools)))

	configDir := filepath.Join(runtimeHome, ".config", "mise")
	cmd := ""
	if needsEmbeddedPlugin(spec.Tools) {
		cmd = PluginInstallCommand() + " && "
	}
	cmd += ActivateCommand(configDir, spec.Tools) + " && mise install"
	volumes := []VolumeMount{
		{Name: miseVol.Name, Target: miseVol.Target},
	}
	env := []string{
		"HOME=" + runtimeHome,
		"MISE_CONFIG_DIR=" + configDir,
		"MISE_DATA_DIR=/mise",
		"PATH=/mise/shims:" + runtimeHome + "/.local/share/mise/shims:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}

	exitCode, err := runner.RunInContainer(ctx, spec.Image, volumes, env, []string{"sh", "-c", cmd})
	if err != nil {
		return fmt.Errorf("mise install: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("mise install failed (exit %d)", exitCode)
	}

	w.WriteProgress("mise: tools ready")
	return nil
}

// needsEmbeddedPlugin reports whether spec.Tools references a tool that needs
// an embedded mise plugin (currently the generic appimage backend).
func needsEmbeddedPlugin(tools map[string]string) bool {
	for name := range tools {
		if strings.HasPrefix(name, appimageBackendPrefix) {
			return true
		}
	}
	return false
}

// ProgressWriter reports progress lines during Prepare/Run.
type ProgressWriter interface {
	WriteProgress(line string)
}

// ActivateCommand returns the shell preamble that:
//  1. Writes the profile's tools into mise's global config at configDir
//     (ephemeral — lives in the container's own filesystem, NOT the shared
//     volume). configDir must match MISE_CONFIG_DIR set in the container env,
//     otherwise mise will not read the written config.
//  2. Activates mise so shims are on PATH.
//
// When the user cd's into the workspace, mise's directory walk picks up any
// project-local .tool-versions / mise.toml and overrides these defaults.
func ActivateCommand(configDir string, tools map[string]string) string {
	configFile := filepath.Join(configDir, "config.toml")

	if len(tools) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "mkdir -p %s && printf '%%s' '", shq(configDir))
	b.WriteString("[tools]\n")

	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&b, "%q = \"%s\"\n", name, tools[name])
	}
	b.WriteString("' > ")
	b.WriteString(shq(configFile))
	return b.String()
}

// shq single-quotes s for embedding in a shell command.
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
