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

	lockFile := filepath.Join(os.TempDir(), fmt.Sprintf("toolpod-mise-%d.lock", os.Getuid()))
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
	env := []string{"HOME=" + runtimeHome, "PATH=" + runtimeHome + "/.local/share/mise/shims:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}

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

// ActivateCommand returns the shell command string that activates mise for
// the given runtime home. Injected into the container's entrypoint so the
// profile command runs with mise-activated PATH.
func ActivateCommand(runtimeHome string) string {
	return `eval "$(mise hook-env)"`
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
