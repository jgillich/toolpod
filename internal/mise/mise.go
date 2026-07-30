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

	cmd := batchInstallCommand(spec.Tools)
	volumes := []VolumeMount{
		{Name: miseVol.Name, Target: miseVol.Target},
	}
	env := []string{"HOME=" + runtimeHome, "MISE_DATA_DIR=/mise", "PATH=/mise/shims:" + runtimeHome + "/.local/share/mise/shims:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}

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

// ProgressWriter reports progress lines during Prepare/Run.
type ProgressWriter interface {
	WriteProgress(line string)
}

// ActivateCommand returns the shell preamble that:
//  1. Writes the profile's tools into mise's global config (ephemeral — lives
//     in the container's own filesystem, NOT the shared volume).
//  2. Activates mise so shims are on PATH.
//
// When the user cd's into the workspace, mise's directory walk picks up any
// project-local .tool-versions / mise.toml and overrides these defaults.
func ActivateCommand(runtimeHome string, tools map[string]string) string {
	configDir := filepath.Join(runtimeHome, ".config", "mise")
	configFile := filepath.Join(configDir, "config.toml")

	var b strings.Builder

	if len(tools) > 0 {
		fmt.Fprintf(&b, "mkdir -p %s && cat > %s << 'TOOLPOD_EOF'\n", configDir, configFile)
		b.WriteString("[tools]\n")

		names := make([]string, 0, len(tools))
		for name := range tools {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(&b, "%s = \"%s\"\n", name, tools[name])
		}
		b.WriteString("TOOLPOD_EOF\n")
	}

	fmt.Fprintf(&b, "eval \"$(mise hook-env)\"")

	return b.String()
}

// batchInstallCommand builds a single shell command that installs all tools
// in one mise invocation chain. Tools are sorted for deterministic ordering.
func batchInstallCommand(tools map[string]string) string {
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	var cmds []string
	for _, name := range names {
		cmds = append(cmds, fmt.Sprintf("mise install %s@%s", name, tools[name]))
	}
	return strings.Join(cmds, " && ")
}
