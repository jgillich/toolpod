package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jgillich/toolpod/internal/doctor"
	"github.com/jgillich/toolpod/internal/prune"
	"github.com/jgillich/toolpod/internal/ui"
	"github.com/jgillich/toolpod/pkg/toolpod"
	"github.com/spf13/pflag"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "doctor":
		os.Exit(runDoctor(args))
	case "prune":
		os.Exit(runPrune(args))
	case "shell":
		os.Exit(runShell(args))
	case "help", "-h", "--help":
		usage()
	default:
		os.Exit(runProfile(cmd, args))
	}
}

func runShell(args []string) int {
	return runProfile("shell", args)
}

func runProfile(profileName string, args []string) int {
	opts := toolpod.LaunchOpts{ProfileName: profileName}
	var cmd string
	fs := pflag.NewFlagSet(profileName, pflag.ContinueOnError)
	fs.StringVarP(&cmd, "command", "c", "", "command to run in the profile")
	fs.StringSliceVar(&opts.Args, "args", nil, "arguments to pass to the profile command")
	fs.StringVar(&opts.Workspace, "workspace", "", "workspace directory to mount")
	fs.StringVar(&opts.ProfileDir, "profile-dir", "", "override user profile directory")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "print the spec without launching")
	fs.BoolVar(&opts.Verbose, "verbose", false, "print the spec before launching")
	fs.BoolVar(&opts.Rebuild, "rebuild", false, "rebuild the image even if cached")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if opts.Workspace == "" {
		wd, _ := os.Getwd()
		opts.Workspace = wd
	}
	if cmd != "" {
		opts.Args = append(opts.Args, "-c", cmd)
	}

	result := toolpod.Launch(context.Background(), opts)
	if result.Err != nil {
		fmt.Fprintln(os.Stderr, result.Err)
	}
	return result.ExitCode
}

func runDoctor(args []string) int {
	opts := doctor.Options{}
	fs := pflag.NewFlagSet("doctor", pflag.ContinueOnError)
	fs.StringVar(&opts.Workspace, "workspace", "", "workspace to check")
	fs.StringVar(&opts.ProfileDir, "profile-dir", "", "override user config dir")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if opts.Workspace == "" {
		wd, _ := os.Getwd()
		opts.Workspace = wd
	}

	result := doctor.Run(context.Background(), opts)

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
		return 1
	}
	return 0
}

func runPrune(args []string) int {
	var opts prune.Options
	fs := pflag.NewFlagSet("prune", pflag.ContinueOnError)
	fs.BoolVar(&opts.Volumes, "volumes", false, "remove toolpod-managed volumes")
	fs.BoolVar(&opts.Images, "images", false, "remove toolpod-tagged images")
	fs.BoolVar(&opts.Force, "force", false, "skip confirmation prompt")
	fs.BoolVarP(&opts.Force, "yes", "y", false, "skip confirmation prompt (short)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	result, err := prune.Run(context.Background(), opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
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
	return 0
}

func usage() {
	fmt.Println(`toolpod — ephemeral dev environments

Usage:
  toolpod <profile> [flags]      Launch a profile (e.g. "shell")
  toolpod doctor [flags]         Run environment diagnostics
  toolpod prune [flags]          Remove toolpod-managed volumes and images
  toolpod help                   Show this help

Commands:
  shell                          Launch the built-in "shell" profile
  doctor                         Check runtime, profiles, workspace, and project tools
  prune                          Remove toolpod-prefixed volumes and images

Flags:
  --workspace string             Workspace directory to mount
  --profile-dir string           Override user profile directory
  --dry-run                      Print the spec without launching
  --verbose                      Print the spec before launching
  --rebuild                      Rebuild the image even if cached
  --volumes, --images, --force   Prune-specific flags

Examples:
  toolpod shell
  toolpod shell -c "echo hello"
  toolpod doctor
  toolpod prune --force --volumes`)
}
