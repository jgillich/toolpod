package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/jgillich/toolpod/internal/mise"
	"golang.org/x/term"
)

func (d *DockerRuntime) Run(ctx context.Context, spec Spec) (int, error) {
	runtimeHome := spec.RuntimeHome

	// Wrap the command with mise activate so installed tools are on PATH.
	// Spec §6.3: mise activate sets up PATH for config tools + project tools.
	activateCmd := mise.ActivateCommand(runtimeHome)
	shellCmd := activateCmd + " && exec " + shellQuote(spec.Command)
	cmd := []string{"sh", "-c", shellCmd}

	mounts := buildMounts(spec, runtimeHome)
	envList := buildEnv(spec, runtimeHome)
	containerName := "toolpod-" + spec.ProfileName + "-" + randomID(8)

	tty := spec.TTY == "true" || ((spec.TTY == "auto" || spec.TTY == "") && term.IsTerminal(int(os.Stdout.Fd())))

	resp, err := d.cli.ContainerCreate(ctx, &container.Config{
		Image:        spec.Image,
		Cmd:          cmd,
		Env:          envList,
		Tty:          tty,
		OpenStdin:    true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   spec.Workspace.Target,
		Labels:       spec.Labels,
		Entrypoint:   []string{},
	}, &container.HostConfig{
		Mounts:      mounts,
		NetworkMode: container.NetworkMode(spec.Network),
		AutoRemove:  false,
	}, &network.NetworkingConfig{}, nil, containerName)
	if err != nil {
		return 3, fmt.Errorf("create container: %w", err)
	}

	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = d.cli.ContainerRemove(cleanupCtx, resp.ID, container.RemoveOptions{Force: true})
	}()

	// Attach BEFORE start so we don't miss early output (spec §3.3).
	// attachAndPump would block until the stream closes, but the container
	// hasn't started yet — that's a deadlock. So we split: attach here,
	// start the container, then pump in a goroutine.
	hijacked, err := d.cli.ContainerAttach(ctx, resp.ID, container.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return 3, fmt.Errorf("attach: %w", err)
	}
	defer hijacked.Close()

	// Signal forwarding with cleanup
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer func() {
		signal.Stop(sigCh)
		close(sigCh)
	}()

	if tty {
		winCh := make(chan os.Signal, 1)
		signal.Notify(winCh, syscall.SIGWINCH)
		defer func() {
			signal.Stop(winCh)
			close(winCh)
		}()
		go d.handleResize(ctx, resp.ID, winCh)
	}

	go func() {
		for sig := range sigCh {
			if s, ok := sig.(syscall.Signal); ok {
				_ = d.cli.ContainerKill(ctx, resp.ID, strconv.Itoa(int(s)))
			}
		}
	}()

	if err := d.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return 3, fmt.Errorf("start container: %w", err)
	}

	// Pump streams AFTER start. This blocks until the container exits and
	// the output stream closes.
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		if tty {
			io.Copy(os.Stdout, hijacked.Reader)
		} else {
			// Non-TTY: Docker multiplexes stdout/stderr with 8-byte headers.
			// Use stdcopy to demultiplex; raw io.Copy would dump header bytes.
			stdcopy.StdCopy(os.Stdout, os.Stderr, hijacked.Reader)
		}
	}()
	go func() {
		io.Copy(hijacked.Conn, os.Stdin)
	}()

	statusCh, errCh := d.cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		<-pumpDone // drain remaining output before returning
		return 3, fmt.Errorf("container wait: %w", err)
	case status := <-statusCh:
		<-pumpDone // drain remaining output before returning
		return int(status.StatusCode), nil
	}
}

func buildMounts(spec Spec, runtimeHome string) []mount.Mount {
	m := []mount.Mount{
		{Type: mount.TypeBind, Source: spec.Workspace.HostPath, Target: spec.Workspace.Target},
	}
	for _, mt := range spec.Mounts {
		m = append(m, mount.Mount{
			Type:     mount.TypeBind,
			Source:   mt.Source,
			Target:   mt.Target,
			ReadOnly: mt.ReadOnly,
		})
	}
	m = append(m, mount.Mount{
		Type:   mount.TypeVolume,
		Source: "toolpod-mise",
		Target: runtimeHome + "/.local/share/mise",
	})
	for _, c := range spec.Caches {
		m = append(m, mount.Mount{
			Type:   mount.TypeVolume,
			Source: c.Name,
			Target: c.Target,
		})
	}
	return m
}

func buildEnv(spec Spec, runtimeHome string) []string {
	env := []string{"HOME=" + runtimeHome}
	for k, v := range spec.Env {
		if v == "" {
			if hostVal, ok := os.LookupEnv(k); ok {
				env = append(env, k+"="+hostVal)
			}
		} else {
			env = append(env, k+"="+v)
		}
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

func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.Itoa(os.Getpid())
	}
	return hex.EncodeToString(b)
}
