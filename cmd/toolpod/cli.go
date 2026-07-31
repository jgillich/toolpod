package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/jgillich/toolpod/internal/doctor"
	"github.com/jgillich/toolpod/internal/prune"
	"github.com/jgillich/toolpod/internal/scaffold"
	"github.com/jgillich/toolpod/internal/ui"
	"github.com/jgillich/toolpod/pkg/toolpod"
)

type LaunchCmd struct {
	// Toolpod-owned flags. They appear before the profile name; kong
	// parses them as part of the default command.
	Command   string `short:"c" help:"Command to run in the profile (shell only)."`
	Workspace string `help:"Workspace directory to mount (default: $PWD)."`
	DryRun    bool   `help:"Print the spec without launching."`
	Verbose   bool   `help:"Print the spec before launching."`
	Rebuild   bool   `help:"Rebuild the image even if cached."`

	// Profile-and-args holds the profile name followed by everything
	// passed verbatim to the profile's command. passthrough:"" stops
	// flag parsing at the first positional, so agent flags like
	// "--model foo" survive unescaped.
	ProfileAndArgs []string `arg:"" name:"profile-and-args" passthrough:"partial" help:"Profile name followed by args passed verbatim to the profile."`
}

type InitCmd struct {
	Profile   string   `arg:"" optional:"" help:"Built-in profile to extend (${profiles})."`
	Fragments []string `sep:"," help:"Comma-separated fragment names (${fragments})." aliases:"presets"`
	Force     bool     `help:"Overwrite an existing user profile file."`
	DryRun    bool     `help:"Print the generated file without writing it."`
}

type DoctorCmd struct {
	Workspace string `help:"Workspace to check."`
}

type PruneCmd struct {
	Volumes bool `help:"Remove toolpod-managed volumes."`
	Images  bool `help:"Remove toolpod-tagged images."`
	Force   bool `help:"Skip confirmation prompt."`
	Yes     bool `short:"y" help:"Skip confirmation prompt (short)."`
}

type CLI struct {
	Launch LaunchCmd `cmd:"" default:"withargs" help:"Launch a profile (e.g. \"shell\")."`
	Init   InitCmd   `cmd:"" help:"Generate a user profile override with fragments."`
	Doctor DoctorCmd `cmd:"" help:"Run environment diagnostics."`
	Prune  PruneCmd  `cmd:"" help:"Remove toolpod-managed volumes and images."`
}

func main() {
	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name("toolpod"),
		kong.Description("ephemeral dev environments"),
		kong.Vars{
			"profiles": strings.Join(scaffold.BuiltInProfiles(), ", "),
			"fragments": strings.Join(scaffold.FragmentNames(), ", "),
		},
	)
	err := ctx.Run()
	ctx.FatalIfErrorf(err)
	if err != nil {
		// Non-parse errors from Run are handled per-command for exit codes;
		// anything that reaches here is already printed.
		os.Exit(1)
	}
}

func (l *LaunchCmd) Run() error {
	if len(l.ProfileAndArgs) == 0 {
		return fmt.Errorf("profile name required")
	}
	profileName := l.ProfileAndArgs[0]
	passthrough := l.ProfileAndArgs[1:]

	workspace := l.Workspace
	if workspace == "" {
		wd, _ := os.Getwd()
		workspace = wd
	}

	result := toolpod.Launch(context.Background(), toolpod.LaunchOpts{
		ProfileName: profileName,
		Workspace:   workspace,
		DryRun:      l.DryRun,
		Verbose:     l.Verbose,
		Rebuild:     l.Rebuild,
		Command:     l.Command,
		Args:        passthrough,
	})
	if result.Err != nil {
		fmt.Fprintln(os.Stderr, result.Err)
	}
	os.Exit(result.ExitCode)
	return nil
}

func (i *InitCmd) Run() error {
	opts := scaffold.Options{
		Profile:     i.Profile,
		Fragments:   i.Fragments,
		Force:       i.Force,
		DryRun:      i.DryRun,
		Interactive: scaffold.IsTTY(os.Stdin),
	}
	if err := scaffold.Run(context.Background(), opts, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	return nil
}

func (d *DoctorCmd) Run() error {
	workspace := d.Workspace
	if workspace == "" {
		wd, _ := os.Getwd()
		workspace = wd
	}
	result := doctor.Run(context.Background(), doctor.Options{Workspace: workspace})
	out := ui.NewOutput(ui.IsTTY(os.Stdout))
	for _, c := range result.Checks {
		color := "reset"
		switch c.Status {
		case doctor.Pass:
			color = "green"
		case doctor.Fail:
			color = "red"
		case doctor.Warn:
			color = "yellow"
		case doctor.Info, doctor.Skip:
			color = "blue"
		}
		fmt.Println(out.Color(color, c.Format()))
	}
	fmt.Println()
	fmt.Println(out.Color("reset", result.Summary()))
	if result.HasFailure() {
		os.Exit(1)
	}
	return nil
}

func (p *PruneCmd) Run() error {
	opts := prune.Options{
		Volumes: p.Volumes,
		Images:  p.Images,
		Force:   p.Force || p.Yes,
	}
	result, err := prune.Run(context.Background(), opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	if len(result.VolumesRemoved) > 0 {
		fmt.Printf("Removed %d volume(s):\n", len(result.VolumesRemoved))
		for _, v := range result.VolumesRemoved {
			fmt.Printf("  %s\n", v)
		}
	}
	if len(result.ImagesRemoved) > 0 {
		fmt.Printf("Removed %d image(s):\n", len(result.ImagesRemoved))
		for _, img := range result.ImagesRemoved {
			fmt.Printf("  %s\n", img)
		}
	}
	if len(result.VolumesRemoved) == 0 && len(result.ImagesRemoved) == 0 {
		fmt.Println("Nothing to prune.")
	}
	return nil
}