package runtime

import (
	"context"
	"os"

	"github.com/docker/docker/api/types/container"
	"golang.org/x/sys/unix"
)

func (d *DockerRuntime) handleResize(ctx context.Context, containerID string, winCh chan os.Signal) {
	for range winCh {
		rows, cols := terminalSize()
		if rows > 0 && cols > 0 {
			_ = d.cli.ContainerResize(ctx, containerID, container.ResizeOptions{
				Height: rows,
				Width:  cols,
			})
		}
	}
}

func terminalSize() (uint, uint) {
	ws, err := unix.IoctlGetWinsize(int(os.Stdin.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0
	}
	return uint(ws.Row), uint(ws.Col)
}
