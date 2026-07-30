package mise

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gofrs/flock"
	"github.com/jgillich/toolpod/internal/runtime"
)

// EnsureTools acquires an exclusive flock on a sentinel file inside the mise
// volume (cross-process safety), then batches all tool installs into a single
// throwaway container. Spec §6.3 + concurrent-install lock.
func EnsureTools(ctx context.Context, runner runtime.ContainerRunner, spec runtime.Spec, runtimeHome string, w runtime.ProgressWriter) error {
	if len(spec.Tools) == 0 {
		return nil
	}

	miseVol := MiseVolume(runtimeHome)

	// Acquire flock on a sentinel file. The file lives inside the mise volume
	// (which is a Docker named volume), so we can't flock it directly from the
	// host. Instead, we flock a local file keyed by the volume name — this
	// serializes across toolpod processes on the same host. Include the UID
	// in the filename so multiple users on the same host don't contend.
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
	volumes := []runtime.VolumeMount{
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

// ActivateCommand returns the shell command string that activates mise for
// the given runtime home. Injected into the container's entrypoint so the
// profile command runs with mise-activated PATH.
func ActivateCommand(runtimeHome string) string {
	miseBin := filepath.Join(runtimeHome, ".local", "share", "mise", "mise")
	return fmt.Sprintf("eval \"$(%s activate sh)\"", miseBin)
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
