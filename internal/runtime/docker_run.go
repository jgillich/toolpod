package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	"github.com/jgillich/tpd/internal/mise"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func (d *DockerRuntime) CreateContainer(ctx context.Context, spec Spec) (CreateResult, error) {
	runtimeHome := spec.RuntimeHome

	// Run an init process (tini) as PID 1 so SIGINT/SIGTERM forwarded to the
	// container reach the wrapped command; the kernel ignores signals sent to
	// a bare PID 1 that has no handler for them.
	initEnabled := true

	// Run as root so the wrapper can create/chown $HOME and the volumes, then
	// drop to the host user via setpriv (see containerIdentity).
	userns, rootUser, hostUID, hostGID := containerIdentity(d.podman)

	configDir := filepath.Join(runtimeHome, ".config", "mise")
	activateCmd := mise.ActivateCommand(configDir, spec.Tools)
	// The container's WorkingDir is the runtime home (see launch.go) so
	// Podman keep-id derives the passwd home from it; the command itself
	// cd's into the workspace so the actual cwd matches the user's.
	parts := []string{"cd " + shq(spec.Workspace.Target)}
	if activateCmd != "" {
		parts = append(parts, activateCmd)
	}
	if cmd := mise.BackendRuntimesCommand(configDir, spec.Tools); cmd != "" {
		parts = append(parts, cmd)
	}
	if mise.NeedsEmbeddedPlugin(spec.Tools) {
		parts = append(parts, mise.PluginInstallCommand())
	}
	parts = append(parts, "mise install")
	parts = append(parts, `eval "$(mise hook-env 2>/dev/null)" || true`)

	parts = append(parts, "exec "+shellQuote(spec.Command))
	userCmd := strings.Join(parts, " && ")

	writable := []string{runtimeHome}
	for _, c := range spec.Caches {
		writable = append(writable, c.Target)
	}
	writable = append(writable, spec.SocketPaths...)
	writable = append(writable, homeParents(runtimeHome, mountTargets(spec))...)
	writable = append(writable, homeParents(runtimeHome, fileTargets(spec))...)
	bootstrap := fmt.Sprintf("mkdir -p %s && chown %d:%d %s", shq(runtimeHome), hostUID, hostGID, quoteJoin(writable))
	cmd := wrapAsUser(bootstrap, hostUID, hostGID, []string{"sh", "-c", userCmd})

	mounts, err := buildMounts(spec, runtimeHome, d.subpathSupported(ctx))
	if err != nil {
		return CreateResult{}, fmt.Errorf("build mounts: %w", err)
	}
	envList := buildEnv(spec, runtimeHome)

	exposedPorts, portBindings := buildPortBindings(spec)
	devices := buildDevices(spec)
	cgroupRules := buildDeviceCgroupRules(spec)

	tty := spec.TTY == "true" || ((spec.TTY == "auto" || spec.TTY == "") && term.IsTerminal(int(os.Stdout.Fd())))

	create := func(name string) (container.CreateResponse, error) {
		return d.cli.ContainerCreate(ctx, &container.Config{
			Image:        spec.Image,
			Cmd:          cmd,
			Env:          envList,
			User:         rootUser,
			Tty:          tty,
			OpenStdin:    true,
			AttachStdin:  true,
			AttachStdout: true,
			AttachStderr: true,
			WorkingDir:   spec.RuntimeHome,
			Labels:       spec.Labels,
			Hostname:     strings.ReplaceAll(spec.ProfileName, "/", "-"),
			Entrypoint:   []string{},
			ExposedPorts: exposedPorts,
		}, &container.HostConfig{
			Mounts:       mounts,
			NetworkMode:  container.NetworkMode(spec.Network),
			UsernsMode:   userns,
			SecurityOpt:  d.securityOpts(),
			AutoRemove:   false,
			PortBindings: portBindings,
			Init:         &initEnabled,
			Resources: container.Resources{
				Devices:           devices,
				DeviceCgroupRules: cgroupRules,
				Memory:            spec.Resources.MemoryBytes,
				NanoCPUs:          spec.Resources.NanoCPUs,
			},
		}, &network.NetworkingConfig{}, nil, name)
	}

	// The random name suffix can collide with a leftover container from a
	// crashed launch; regenerate the name and retry instead of failing.
	var resp container.CreateResponse
	for attempt := 0; attempt < containerNameAttempts; attempt++ {
		resp, err = create(containerNameFor(spec.ProfileName) + randomID(8))
		if err == nil {
			break
		}
		if !errdefs.IsConflict(err) || attempt == containerNameAttempts-1 {
			return CreateResult{}, fmt.Errorf("create container: %w", err)
		}
	}

	if len(spec.Files) > 0 {
		if err := writeContainerFiles(ctx, d.cli, resp.ID, spec.Files, hostUID, hostGID); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = d.cli.ContainerRemove(cleanupCtx, resp.ID, container.RemoveOptions{Force: true})
			return CreateResult{}, fmt.Errorf("write profile files: %w", err)
		}
	}

	return CreateResult{ContainerID: resp.ID}, nil
}

// RunContainer attaches, starts, waits on, and removes a container created by
// CreateContainer. The deferred removal is the primary cleanup: it covers the
// whole attach/start/wait lifecycle and normal exit.
func (d *DockerRuntime) RunContainer(ctx context.Context, spec Spec, created CreateResult) (int, error) {
	// cleanupMu serializes removal attempts and only records success, so a
	// failed signal-triggered removal can be retried during normal teardown.
	var cleanupMu sync.Mutex
	removed := false
	runDone := make(chan struct{})
	cleanupOnce := func() {
		cleanupMu.Lock()
		defer cleanupMu.Unlock()
		if removed {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := d.cli.ContainerRemove(cleanupCtx, created.ContainerID, container.RemoveOptions{Force: true}); err != nil {
			fmt.Fprintf(os.Stderr, "tpd: warning: remove container %s: %v\n", created.ContainerID, err)
			return
		}
		removed = true
	}
	defer cleanupOnce()

	// CreateResult carries only the container ID, so tty is re-derived here
	// for the raw-mode setup.
	tty := spec.TTY == "true" || ((spec.TTY == "auto" || spec.TTY == "") && term.IsTerminal(int(os.Stdout.Fd())))

	var oldState *term.State
	if tty && term.IsTerminal(int(os.Stdin.Fd())) {
		var err error
		oldState, err = term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			return 3, fmt.Errorf("set raw mode: %w", err)
		}
		defer term.Restore(int(os.Stdin.Fd()), oldState)
	}

	// Attach BEFORE start so we don't miss early output (spec §3.3).
	// attachAndPump would block until the stream closes, but the container
	// hasn't started yet — that's a deadlock. So we split: attach here,
	// start the container, then pump in a goroutine.
	hijacked, err := d.cli.ContainerAttach(ctx, created.ContainerID, container.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return 3, fmt.Errorf("attach: %w", err)
	}
	defer hijacked.Close()

	if tty {
		rows, cols := terminalSize()
		if rows > 0 && cols > 0 {
			_ = d.cli.ContainerResize(ctx, created.ContainerID, container.ResizeOptions{
				Height: rows,
				Width:  cols,
			})
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	var winCh chan os.Signal
	var stopCh chan os.Signal
	var signalWG sync.WaitGroup
	var fallbackWG sync.WaitGroup
	var shuttingDown atomic.Bool

	if tty {
		winCh = make(chan os.Signal, 1)
		signal.Notify(winCh, syscall.SIGWINCH)
		signalWG.Add(1)
		go func() {
			defer signalWG.Done()
			d.handleResize(ctx, created.ContainerID, winCh)
		}()
	}

	signalWG.Add(1)
	go func() {
		defer signalWG.Done()
		for sig := range sigCh {
			if shuttingDown.Load() {
				continue
			}
			s, ok := sig.(syscall.Signal)
			if !ok {
				continue
			}
			// Forward the signal so the app can exit gracefully; if it
			// doesn't within the grace period, force-remove so a killed tpd
			// never orphans a running container (e.g. closed terminal = SIGHUP).
			forwardSignalThenFallback(&fallbackWG, signalTeardownGrace, runDone,
				func() { _ = d.cli.ContainerKill(ctx, created.ContainerID, strconv.Itoa(int(s))) },
				cleanupOnce)
		}
	}()

	// Raw mode disables ISIG, so Ctrl+Z reaches the pump as a byte (see the
	// stdin pump); catch SIGTSTP too so suspend works however the signal
	// arrives. Without it, a TUI in the container that stops itself on
	// Ctrl+Z (opencode's suspend keybind) leaves this process holding a raw
	// terminal the shell can never regain.
	var inputGateMu sync.Mutex
	inputReady := make(chan struct{})
	close(inputReady)
	inputPaused := false
	pauseInput := func() {
		inputGateMu.Lock()
		defer inputGateMu.Unlock()
		if inputPaused {
			return
		}
		inputPaused = true
		inputReady = make(chan struct{})
		_ = unix.SetNonblock(int(os.Stdin.Fd()), true)
	}
	resumeInput := func() {
		inputGateMu.Lock()
		defer inputGateMu.Unlock()
		if !inputPaused {
			return
		}
		inputPaused = false
		close(inputReady)
	}
	var resumeOutputMu sync.Mutex
	var resumeOutput chan struct{}
	armResumeOutput := func() <-chan struct{} {
		resumeOutputMu.Lock()
		defer resumeOutputMu.Unlock()
		resumeOutput = make(chan struct{})
		return resumeOutput
	}
	noteResumeOutput := func() {
		resumeOutputMu.Lock()
		defer resumeOutputMu.Unlock()
		if resumeOutput != nil {
			close(resumeOutput)
			resumeOutput = nil
		}
	}
	suspendGate := make(chan struct{}, 1)
	suspendGate <- struct{}{}
	suspend := func(waitForApp bool) {
		if shuttingDown.Load() {
			return
		}
		<-suspendGate
		defer func() { suspendGate <- struct{}{} }()
		if shuttingDown.Load() {
			return
		}
		pauseInput()
		defer resumeInput()
		d.suspendSession(ctx, created.ContainerID, oldState, waitForApp, armResumeOutput)
	}
	if oldState != nil {
		stopCh = make(chan os.Signal, 1)
		signal.Notify(stopCh, syscall.SIGTSTP)
		signalWG.Add(1)
		go func() {
			defer signalWG.Done()
			for range stopCh {
				if shuttingDown.Load() {
					continue
				}
				suspend(false)
			}
		}()
	}

	defer func() {
		shuttingDown.Store(true)
		signal.Stop(sigCh)
		close(sigCh)
		if winCh != nil {
			signal.Stop(winCh)
			close(winCh)
		}
		if stopCh != nil {
			signal.Stop(stopCh)
			close(stopCh)
		}
		signalWG.Wait()
		close(runDone)
		fallbackWG.Wait()
		<-suspendGate
		suspendGate <- struct{}{}
	}()

	if err := d.cli.ContainerStart(ctx, created.ContainerID, container.StartOptions{}); err != nil {
		return 3, fmt.Errorf("start container: %w", err)
	}

	for _, p := range spec.PortSpecs {
		fmt.Fprintf(os.Stderr, "listening on %s://%s:%s\r\n", p.Protocol, p.HostIP, p.HostPort)
	}

	// Pump streams AFTER start. This blocks until the container exits and
	// the output stream closes.
	pumpDone := make(chan struct{})
	stdout := notifyingWriter{Writer: os.Stdout, notify: noteResumeOutput}
	go func() {
		defer close(pumpDone)
		defer noteResumeOutput()
		if tty {
			if _, err := io.Copy(stdout, hijacked.Reader); err != nil {
				fmt.Fprintf(os.Stderr, "stdout pump: %v\n", err)
			}
		} else {
			if _, err := stdcopy.StdCopy(stdout, os.Stderr, hijacked.Reader); err != nil {
				fmt.Fprintf(os.Stderr, "stdout pump: %v\n", err)
			}
		}
	}()

	// stdinPump copies os.Stdin to the hijacked connection. Unlike io.Copy,
	// it guards writes with a mutex so that shutdown can close the
	// connection without the goroutine writing to a closed socket (which
	// produces "broken pipe" / "use of closed network connection" errors).
	// Ctrl+Z is a byte in raw mode. Forward it so TUIs can run their own
	// suspend cleanup before tpd yields the terminal to the shell.
	var connMu sync.Mutex
	connClosed := false
	waitForInput := func() {
		inputGateMu.Lock()
		ready := inputReady
		inputGateMu.Unlock()
		<-ready
	}
	writeConn := func(b []byte) bool {
		connMu.Lock()
		defer connMu.Unlock()
		if connClosed {
			return false
		}
		for len(b) > 0 {
			n, err := hijacked.Conn.Write(b)
			if err != nil || n == 0 {
				return false
			}
			b = b[n:]
		}
		return true
	}
	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		buf := make([]byte, 32*1024)
		for {
			n, readErr := os.Stdin.Read(buf)
			if n > 0 {
				waitForInput()
				if oldState == nil {
					if !writeConn(buf[:n]) {
						return
					}
					continue
				}
				rest := buf[:n]
				for {
					i := suspendSequenceIndex(rest)
					if i < 0 {
						if !writeConn(rest) {
							return
						}
						break
					}
					if i > 0 && !writeConn(rest[:i]) {
						return
					}
					if rest[i] == ctrlZByte {
						if !writeConn(rest[i : i+1]) {
							return
						}
						rest = rest[i+1:]
					} else {
						if !writeConn(rest[i : i+len(kittyCtrlZ)]) {
							return
						}
						rest = rest[i+len(kittyCtrlZ):]
					}
					suspend(true)
				}
			}
			if readErr != nil {
				if errors.Is(readErr, syscall.EAGAIN) {
					waitForInput()
					continue
				}
				return
			}
		}
	}()

	// closeConn safely closes the hijacked connection, ensuring the stdin
	// pump goroutine stops writing before the connection is torn down.
	closeConn := func() {
		connMu.Lock()
		connClosed = true
		hijacked.Close()
		connMu.Unlock()
	}

	statusCh, errCh := d.cli.ContainerWait(ctx, created.ContainerID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		<-pumpDone
		closeConn()
		return 3, fmt.Errorf("container wait: %w", err)
	case status := <-statusCh:
		<-pumpDone
		closeConn()
		return int(status.StatusCode), nil
	}
}

// ctrlZByte is the raw-mode byte produced by Ctrl+Z; with ISIG off the kernel
// does not turn it into SIGTSTP.
const ctrlZByte = 0x1A

var kittyCtrlZ = []byte("\x1b[122;5u")

func suspendSequenceIndex(b []byte) int {
	byteIndex := bytes.IndexByte(b, ctrlZByte)
	kittyIndex := bytes.Index(b, kittyCtrlZ)
	if byteIndex < 0 {
		return kittyIndex
	}
	if kittyIndex < 0 || byteIndex < kittyIndex {
		return byteIndex
	}
	return kittyIndex
}

// signalTeardownGrace is how long a forwarded signal gets to stop the container
// before tpd force-removes it, so a terminating tpd (terminal close, SIGKILL
// fallback) never leaves a running container behind.
const signalTeardownGrace = 10 * time.Second

// resumeOutputTimeout bounds the post-SIGCONT output gate; after it elapses the
// terminal is reasserted instead of blocking resume on a quiet app.
const resumeOutputTimeout = 2 * time.Second

// containerNameAttempts bounds regenerating the container name when the random
// suffix collides with a leftover container.
const containerNameAttempts = 3

// forwardSignalThenFallback forwards a signal to the container and, if it
// hasn't stopped within grace (runDone signals normal exit), force-removes it.
func forwardSignalThenFallback(wg *sync.WaitGroup, grace time.Duration, runDone <-chan struct{}, forward func(), cleanupOnce func()) {
	forward()
	wg.Add(1)
	go func() {
		defer wg.Done()
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-timer.C:
			cleanupOnce()
		case <-runDone:
		}
	}()
}

// suspendSession hands the terminal back to the shell and stops this process
// so the session can be backgrounded with Ctrl+Z and resumed with fg. When the
// TUI received Ctrl+Z itself, wait for its cleanup before stopping tpd.
func (d *DockerRuntime) suspendSession(ctx context.Context, containerID string, oldState *term.State, waitForApp bool, armResumeOutput func() <-chan struct{}) {
	if oldState == nil {
		return
	}
	if waitForApp {
		if !d.waitForContainerProcessStopped(ctx, containerID, 500*time.Millisecond) {
			if err := d.signalContainerForegroundGroup(ctx, containerID, syscall.SIGTSTP); err != nil {
				fmt.Fprintf(os.Stderr, "tpd: suspend container: %v\n", err)
			}
		}
	} else {
		if err := d.signalContainerForegroundGroup(ctx, containerID, syscall.SIGTSTP); err != nil {
			fmt.Fprintf(os.Stderr, "tpd: suspend container: %v\n", err)
		}
	}
	disableMouseModes(os.Stdout)
	if err := term.Restore(int(os.Stdin.Fd()), oldState); err != nil {
		fmt.Fprintf(os.Stderr, "tpd: restore terminal: %v\n", err)
	}
	// SIGSTOP cannot be caught, so fg resumes us right after this call.
	_ = unix.Kill(unix.Getpid(), unix.SIGSTOP)
	if err := waitForForegroundTerminal(int(os.Stdin.Fd())); err != nil {
		fmt.Fprintf(os.Stderr, "tpd: reclaim terminal: %v\n", err)
	}
	if _, err := term.MakeRaw(int(os.Stdin.Fd())); err != nil {
		fmt.Fprintf(os.Stderr, "tpd: restore raw terminal: %v\n", err)
	}
	_ = unix.SetNonblock(int(os.Stdin.Fd()), false)
	resumeOutputReady := armResumeOutput()
	if err := d.signalContainerForegroundGroup(ctx, containerID, syscall.SIGCONT); err != nil {
		fmt.Fprintf(os.Stderr, "tpd: resume container: %v\n", err)
	}
	// Wait for the first byte of resumed output so input can't race ahead of
	// SIGCONT, but bound the wait: an app that stays silent after CONT would
	// otherwise block resume forever.
	select {
	case <-resumeOutputReady:
	case <-time.After(resumeOutputTimeout):
		fmt.Fprintln(os.Stderr, "tpd: warning: no output after resume; reasserting terminal")
	}
	// fg may apply the shell's saved job settings after delivering SIGCONT.
	if _, err := term.MakeRaw(int(os.Stdin.Fd())); err != nil {
		fmt.Fprintf(os.Stderr, "tpd: reassert raw terminal: %v\n", err)
	}
}

func waitForForegroundTerminal(fd int) error {
	processGroup := unix.Getpgrp()
	for {
		foregroundGroup, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
		if err != nil {
			return err
		}
		if foregroundGroup == processGroup {
			return nil
		}
		if err := unix.Kill(unix.Getpid(), unix.SIGTTIN); err != nil {
			return err
		}
	}
}

// containerTopColumn returns the index of the named ContainerTop title
// (case-insensitive), or -1.
func containerTopColumn(titles []string, name string) int {
	for i, title := range titles {
		if strings.EqualFold(title, name) {
			return i
		}
	}
	return -1
}

func (d *DockerRuntime) waitForContainerProcessStopped(ctx context.Context, containerID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		top, err := d.cli.ContainerTop(ctx, containerID, nil)
		if err == nil {
			statIndex := containerTopColumn(top.Titles, "STAT")
			pidIndex := containerTopColumn(top.Titles, "PID")
			if statIndex >= 0 {
				for _, process := range top.Processes {
					if statIndex >= len(process) {
						continue
					}
					// PID 1 is the init (tini) and never stops itself; skip it
					// only when the PID column identifies it.
					if pidIndex >= 0 && pidIndex < len(process) && process[pidIndex] == "1" {
						continue
					}
					// Stopped state is the leading STAT rune; a substring match
					// would treat e.g. "DT" (uninterruptible + threaded) as stopped.
					r, _ := utf8.DecodeRuneInString(process[statIndex])
					if r == 'T' || r == 't' {
						return true
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// signalContainerForegroundGroup sends sig to the app's process group in the
// container. The group is asked of the container (ps) because it depends on
// the init layout; when it can't be determined, the legacy PID 2 group is
// signalled so suspend keeps working.
func (d *DockerRuntime) signalContainerForegroundGroup(ctx context.Context, containerID string, sig syscall.Signal) error {
	group := 2
	pgid, err := d.foregroundAppPGID(ctx, containerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tpd: warning: container group signal: %v; falling back to -2\n", err)
	} else {
		group = pgid
	}
	_, err = d.runContainerExec(ctx, containerID, []string{"kill", "-" + strconv.Itoa(int(sig)), "--", "-" + strconv.Itoa(group)})
	return err
}

// foregroundAppPGID asks the container which process group the app runs in, so
// group signalling targets the real group instead of an assumed one.
func (d *DockerRuntime) foregroundAppPGID(ctx context.Context, containerID string) (int, error) {
	appPID, err := d.foregroundAppPID(ctx, containerID)
	if err != nil {
		return 0, err
	}
	out, err := d.runContainerExec(ctx, containerID, []string{"ps", "-o", "pgid=", "-p", strconv.Itoa(appPID)})
	if err != nil {
		return 0, fmt.Errorf("query pgid of pid %d: %w", appPID, err)
	}
	pgid, err := strconv.Atoi(strings.TrimSpace(out))
	// PGID 1 is the init's group; kill -- -1 broadcasts to every process in
	// the namespace instead of targeting the group, so it is never a valid
	// signal target.
	if err != nil || pgid <= 1 {
		return 0, fmt.Errorf("invalid pgid %q for pid %d", strings.TrimSpace(out), appPID)
	}
	return pgid, nil
}

// foregroundAppPID is the container's main process: tini holds PID 1 and the
// app replaces the launch wrapper through the exec chain, so it is the lowest
// non-init PID.
func (d *DockerRuntime) foregroundAppPID(ctx context.Context, containerID string) (int, error) {
	top, err := d.cli.ContainerTop(ctx, containerID, nil)
	if err != nil {
		return 0, err
	}
	pidIndex := containerTopColumn(top.Titles, "PID")
	if pidIndex < 0 {
		return 0, errors.New("process table has no PID column")
	}
	appPID := 0
	for _, process := range top.Processes {
		if pidIndex >= len(process) {
			continue
		}
		pid, err := strconv.Atoi(process[pidIndex])
		if err != nil || pid <= 1 {
			continue
		}
		if appPID == 0 || pid < appPID {
			appPID = pid
		}
	}
	if appPID == 0 {
		return 0, errors.New("no app process in container")
	}
	return appPID, nil
}

// runContainerExec runs cmd in the container as root and returns its stdout.
// It blocks until the exec finishes and reports a non-zero exit code so
// signal-path failures surface instead of being dropped.
func (d *DockerRuntime) runContainerExec(ctx context.Context, containerID string, cmd []string) (string, error) {
	execResp, err := d.cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          cmd,
		AttachStdin:  false,
		AttachStdout: true,
		AttachStderr: true,
		User:         "0",
	})
	if err != nil {
		return "", err
	}
	// Attaching blocks until the exec finishes, giving the caller the same
	// completion barrier local job control gets from kill(2) so input cannot
	// race ahead of signal delivery.
	attached, err := d.cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", err
	}
	defer attached.Close()
	var stdout bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, io.Discard, attached.Reader); err != nil {
		return stdout.String(), err
	}
	inspect, err := d.cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return stdout.String(), err
	}
	if inspect.ExitCode != 0 {
		return stdout.String(), fmt.Errorf("exec %v: exit code %d", cmd, inspect.ExitCode)
	}
	return stdout.String(), nil
}

func disableMouseModes(dst io.Writer) {
	_, _ = io.WriteString(dst, "\x1b[?1000;1002;1003;1004;1006l")
}

type notifyingWriter struct {
	io.Writer
	notify func()
}

func (w notifyingWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if n > 0 {
		w.notify()
	}
	return n, err
}

// tarFiles renders the container-file tar stream: one regular file entry per
// target with a relative path (CopyToContainer untars at "/"), the file's
// mode, and the execution user's uid/gid. The header requests FormatPAX
// (Go's writer falls back to USTAR for short headers and uses PAX only when
// needed, e.g. long paths); explicit TypeReg/mode/uid/gid are the real
// guarantees.
func tarFiles(files []FileSpec, uid, gid int) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		rel := strings.TrimPrefix(f.Target, "/")
		if err := tw.WriteHeader(&tar.Header{
			Name:     rel,
			Typeflag: tar.TypeReg,
			Mode:     int64(f.Mode),
			Uid:      uid,
			Gid:      gid,
			Size:     int64(len(f.Content)),
			Format:   tar.FormatPAX,
		}); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(f.Content)); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeContainerFiles untars the profile's files into the created-but-not-
// yet-started container, so they exist before the command runs and are owned
// by the execution user.
func writeContainerFiles(ctx context.Context, cli *client.Client, containerID string, files []FileSpec, uid, gid int) error {
	data, err := tarFiles(files, uid, gid)
	if err != nil {
		return err
	}
	return cli.CopyToContainer(ctx, containerID, "/", bytes.NewReader(data), container.CopyToContainerOptions{})
}

func fileTargets(spec Spec) []string {
	targets := make([]string, 0, len(spec.Files))
	for _, f := range spec.Files {
		targets = append(targets, f.Target)
	}
	return targets
}

// homeParents returns the engine-created (root-owned) parent dirs under home
// of the mount targets; chowning them lets the user create non-mounted paths
// like $HOME/.config/mise. Mount targets themselves are excluded so a chown
// never propagates into a bind-mounted host directory.
func homeParents(home string, targets []string) []string {
	leaf := make(map[string]bool, len(targets))
	for _, t := range targets {
		leaf[t] = true
	}
	var out []string
	seen := map[string]bool{home: true}
	for _, t := range targets {
		if !strings.HasPrefix(t, home+"/") {
			continue
		}
		for dir := filepath.Dir(t); dir != home && dir != "/" && dir != "."; dir = filepath.Dir(dir) {
			if seen[dir] || leaf[dir] {
				continue
			}
			seen[dir] = true
			out = append(out, dir)
		}
	}
	return out
}

func mountTargets(spec Spec) []string {
	targets := []string{spec.Workspace.Target}
	for _, mt := range spec.Mounts {
		targets = append(targets, mt.Target)
	}
	for _, c := range spec.Caches {
		targets = append(targets, c.Target)
	}
	return targets
}

// containerUser runs the container as root so the bootstrap can create/chown
// $HOME before setpriv drops to the host user.
const containerUser = "0:0"

// securityOpts disables SELinux label separation when SELinux is enforcing so
// bind-mounted host paths (workspace, home, dbus socket) keep their host
// labels and stay readable to the container. Relabeling with :Z would relabel
// the user's own files, breaking host access to the shared workspace.
func (d *DockerRuntime) securityOpts() []string {
	if d.selinux {
		return []string{"label=disable"}
	}
	return nil
}

// containerIdentity returns the userns mode, the container user, and the host
// uid/gid to drop to via setpriv — getuid() must match SO_PEERCRED for
// dbus-broker EXTERNAL auth. Rootless Podman needs keep-id so the dropped uid
// equals the host uid.
func containerIdentity(podman bool) (container.UsernsMode, string, int, int) {
	if podman {
		return "keep-id", containerUser, os.Getuid(), os.Getgid()
	}
	return "", containerUser, os.Getuid(), os.Getgid()
}

func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func quoteJoin(paths []string) string {
	quoted := make([]string, len(paths))
	for i, p := range paths {
		quoted[i] = shq(p)
	}
	return strings.Join(quoted, " ")
}

// wrapAsUser runs a root bootstrap that creates/chowns the user-owned
// locations, then drops to the host user via setpriv (all caps dropped).
// Images without setpriv fall back to running un-dropped.
func wrapAsUser(bootstrap string, uid, gid int, shellCmd []string) []string {
	inner := strings.Join(shellCmd, " ")
	if len(shellCmd) == 3 && shellCmd[0] == "sh" && shellCmd[1] == "-c" {
		inner = shellCmd[2]
	}
	drop := fmt.Sprintf("exec setpriv --reuid=%d --regid=%d --clear-groups --inh-caps=-all --bounding-set=-all sh -c %s", uid, gid, shq(inner))
	fallback := fmt.Sprintf(`echo "tpd: setpriv not found, running as root" >&2; exec sh -c %s`, shq(inner))
	run := fmt.Sprintf("if command -v setpriv >/dev/null 2>&1; then %s; else %s; fi", drop, fallback)
	return []string{"sh", "-c", bootstrap + " && " + run}
}

func buildMounts(spec Spec, runtimeHome string, subpath bool) ([]mount.Mount, error) {
	m := []mount.Mount{
		{Type: mount.TypeBind, Source: spec.Workspace.HostPath, Target: spec.Workspace.Target},
	}
	for _, mt := range spec.Mounts {
		if mt.Create {
			if _, err := os.Stat(mt.Source); os.IsNotExist(err) {
				if err := os.MkdirAll(mt.Source, 0o755); err != nil {
					if mt.Optional {
						fmt.Fprintf(os.Stderr, "warning: creating mount source %s: %v\n", mt.Source, err)
						continue
					}
					return nil, fmt.Errorf("create mount source %s: %w", mt.Source, err)
				}
				fmt.Fprintf(os.Stderr, "creating mount source %s\n", mt.Source)
			}
		}
		if mt.Optional {
			if _, err := os.Stat(mt.Source); err != nil {
				continue
			}
		}
		m = append(m, mount.Mount{
			Type:     mount.TypeBind,
			Source:   mt.Source,
			Target:   mt.Target,
			ReadOnly: mt.ReadOnly,
		})
	}
	if rtDir := spec.Env["XDG_RUNTIME_DIR"]; rtDir != "" {
		busPath := filepath.Join(rtDir, "bus")
		overlaid := false
		for _, existing := range m {
			if existing.Target == busPath {
				overlaid = true
				break
			}
		}
		if !overlaid {
			m = append(m, mount.Mount{
				Type:   mount.TypeBind,
				Source: "/dev/null",
				Target: busPath,
			})
		}
	}
	for _, c := range spec.Caches {
		mnt := mount.Mount{Type: mount.TypeVolume, Source: c.Name, Target: c.Target}
		if subpath {
			mnt.VolumeOptions = &mount.VolumeOptions{Subpath: c.Subpath}
		} else {
			// Engines that ignore VolumeOptions.Subpath get a dedicated
			// volume per target so each path stays separate.
			mnt.Source = c.Name + "-" + c.Subpath
		}
		m = append(m, mnt)
	}
	return m, nil
}

func buildPortBindings(spec Spec) (nat.PortSet, nat.PortMap) {
	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, p := range spec.PortSpecs {
		port := nat.Port(p.Container + "/" + p.Protocol)
		exposed[port] = struct{}{}
		bindings[port] = []nat.PortBinding{{HostIP: p.HostIP, HostPort: p.HostPort}}
	}
	return exposed, bindings
}

func buildDevices(spec Spec) []container.DeviceMapping {
	var out []container.DeviceMapping
	for _, d := range spec.DeviceSpecs {
		if _, err := os.Stat(d.Host); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping device %s: %v\n", d.Container, err)
			continue
		}
		out = append(out, container.DeviceMapping{
			PathOnHost:        d.Host,
			PathInContainer:   d.Container,
			CgroupPermissions: d.Perms,
		})
	}
	return out
}

func buildDeviceCgroupRules(spec Spec) []string {
	var out []string
	for _, d := range spec.DeviceSpecs {
		if !d.Cgroup {
			continue
		}
		major, minor, prefix, ok := deviceMajorMinor(d.Host)
		if !ok {
			fmt.Fprintf(os.Stderr, "warning: device %s: cannot stat %s, no cgroup rule emitted\n", d.Container, d.Host)
			continue
		}
		// Only character/block nodes map to a device-cgroup rule; anything
		// else is not a device node and gets no rule (and never a broad one).
		if prefix == "" {
			fmt.Fprintf(os.Stderr, "warning: device %s: %s is not a character or block device, no cgroup rule emitted\n", d.Container, d.Host)
			continue
		}
		perms := d.Perms
		if perms == "" {
			perms = "rwm"
		}
		out = append(out, fmt.Sprintf("%s %d:%d %s", prefix, major, minor, perms))
	}
	return out
}

// deviceTypePrefix maps a device node's stat mode to a cgroup device rule
// prefix. The type comes from st_mode rather than /sys/dev lookups because
// major numbers are shared across device classes (major 7 is both the loop
// block family and the vcs char family).
func deviceTypePrefix(mode uint32) string {
	switch mode & unix.S_IFMT {
	case unix.S_IFCHR:
		return "c"
	case unix.S_IFBLK:
		return "b"
	}
	return ""
}

func deviceMajorMinor(path string) (int, int, string, bool) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, 0, "", false
	}
	return int(unix.Major(uint64(st.Rdev))), int(unix.Minor(uint64(st.Rdev))), deviceTypePrefix(st.Mode), true
}

func buildEnv(spec Spec, runtimeHome string) []string {
	env := []string{
		"HOME=" + runtimeHome,
		"MISE_CONFIG_DIR=" + filepath.Join(runtimeHome, ".config", "mise"),
		// aube (mise's npm backend) defaults its cache and store to $HOME, which
		// is ephemeral inside the container; the mise profile declares an `aube`
		// cache volume at ~/.aube, so point aube there to survive container exit.
		"AUBE_CACHE_DIR=" + filepath.Join(runtimeHome, ".aube", "cache"),
		"AUBE_STORE_DIR=" + filepath.Join(runtimeHome, ".aube", "store"),
	}
	for k, v := range spec.Env {
		if v == "" {
			// Empty values are not set; forward a host variable via {{ .Env.FOO }}.
			continue
		}
		env = append(env, k+"="+v)
	}
	return env
}

func shellQuote(cmd []string) string {
	var parts []string
	for _, s := range cmd {
		escaped := strings.ReplaceAll(s, "'", `'\''`)
		parts = append(parts, "'"+escaped+"'")
	}
	return strings.Join(parts, " ")
}

// randomIDFallbackCounter keeps the crypto/rand failure path unique even for
// back-to-back calls in the same process.
var randomIDFallbackCounter atomic.Uint64

func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand can fail on seccomp-restricted or early-boot hosts; a
		// bare PID is a collision source across restarts, so mix a timestamp
		// and a monotonic counter into the tag.
		return fmt.Sprintf("%d-%d-%d", time.Now().UnixNano(), randomIDFallbackCounter.Add(1), os.Getpid())
	}
	return hex.EncodeToString(b)
}

// containerNameFor builds the Docker container-name prefix from a profile
// name. Profile names may be hierarchical (toolchain/go); '/' is not valid in a
// container name, so it becomes '-'.
func containerNameFor(profileName string) string {
	return "tpd-" + strings.ReplaceAll(profileName, "/", "-") + "-"
}
