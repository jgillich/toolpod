package prune

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"golang.org/x/term"
)

type Options struct {
	Volumes bool
	Force   bool
}

type Result struct {
	VolumesRemoved []string
}

func Run(ctx context.Context, opts Options) (Result, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return Result{}, fmt.Errorf("docker client: %w", err)
	}

	var result Result

	vols, err := listTpodVolumes(ctx, cli)
	if err != nil {
		return result, fmt.Errorf("list volumes: %w", err)
	}
	if len(vols) > 0 {
		if !opts.Force {
			if !confirm("volumes", volNames(vols), os.Stdin) {
				return result, nil
			}
		}
		for _, v := range vols {
			if err := cli.VolumeRemove(ctx, v.Name, true); err != nil {
				fmt.Fprintf(os.Stderr, "  failed to remove volume %s: %v\n", v.Name, err)
			} else {
				result.VolumesRemoved = append(result.VolumesRemoved, v.Name)
			}
		}
	}

	return result, nil
}

func listTpodVolumes(ctx context.Context, cli *client.Client) ([]*volume.Volume, error) {
	resp, err := cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return nil, err
	}
	var found []*volume.Volume
	for _, v := range resp.Volumes {
		if isTpodVolume(v.Name) {
			found = append(found, v)
		}
	}
	return found, nil
}

func isTpodVolume(name string) bool {
	return strings.HasPrefix(name, "tpod-")
}

func confirm(kind string, items []string, r io.Reader) bool {
	if f, ok := r.(*os.File); ok && !term.IsTerminal(int(f.Fd())) {
		fmt.Fprintln(os.Stderr, "Error: cannot prompt for confirmation in non-interactive shell. Use --force.")
		return false
	}
	fmt.Printf("The following %s will be removed:\n", kind)
	for _, item := range items {
		fmt.Printf("  %s\n", item)
	}
	fmt.Print("Proceed? [y/N] ")
	scanner := bufio.NewScanner(r)
	scanner.Scan()
	return strings.ToLower(strings.TrimSpace(scanner.Text())) == "y"
}

func volNames(vols []*volume.Volume) []string {
	out := make([]string, len(vols))
	for i, v := range vols {
		out[i] = v.Name
	}
	return out
}
