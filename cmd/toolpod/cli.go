package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jgillich/toolpod/internal/doctor"
	"github.com/jgillich/toolpod/internal/prune"
	"github.com/jgillich/toolpod/internal/scaffold"
	"github.com/jgillich/toolpod/internal/ui"
	"github.com/jgillich/toolpod/pkg/toolpod"
	"github.com/spf13/pflag"
)

// globalFlags holds toolpod-owned flags parsed BEFORE the profile name.
// Everything after the profile name is passed through verbatim to the
// profile's command — no flag parsing, no collisions with agent flags.
type globalFlags struct {
	workspace string
	command   string
	dryRun    bool
	verbose   bool
	rebuild   bool
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]

	// Subcommands with their own flag sets.
	switch cmd {
	case "doctor":
		os.Exit(runDoctor(os.Args[2:]))
	case "init":
		os.Exit(runInit(os.Args[2:]))
	case "prune":
		os.Exit(runPrune(os.Args[2:]))
	case "help", "-h", "--help":
		usage()
		return
	}

	// Default: launch a profile. toolpod-owned flags come before the
	// profile name; everything after is passthrough to the profile command.
	//   toolpod [--workspace X] [--verbose] [--dry-run] [--rebuild] [-c CMD] <profile> [args...]
	gf, profileName, passthrough, ok := parseGlobalFlags(os.Args[1:])
	if !ok {
		os.Exit(2)
	}
	os.Exit(runProfile(gf, profileName, passthrough))
}

// parseGlobalFlags scans os.Args[1:] for toolpod-owned flags, stopping at the
// first non-flag argument (the profile name). Everything after the profile
// name is returned verbatim as passthrough. Returns ok=false on parse error
// (error already printed).
func parseGlobalFlags(args []string) (gf globalFlags, profileName string, passthrough []string, ok bool) {
	fs := pflag.NewFlagSet("toolpod", pflag.ContinueOnError)
	fs.StringVarP(&gf.command, "command", "c", "", "command to run in the profile (shell only)")
	fs.StringVar(&gf.workspace, "workspace", "", "workspace directory to mount")
	fs.BoolVar(&gf.dryRun, "dry-run", false, "print the spec without launching")
	fs.BoolVar(&gf.verbose, "verbose", false, "print the spec before launching")
	fs.BoolVar(&gf.rebuild, "rebuild", false, "rebuild the image even if cached")
	// Stop at the first non-flag arg; treat everything from there on as the
	// profile name + passthrough. pflag.InterspersedFlags=false makes Parse
	// return at the first positional, leaving the rest in fs.Args().
	fs.SetInterspersed(false)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return gf, "", nil, false
	}
	remaining := fs.Args()
	if len(remaining) == 0 {
		fmt.Fprintln(os.Stderr, "profile name required")
		fmt.Fprintln(os.Stderr, "Usage: toolpod [flags] <profile> [args...]")
		fmt.Fprintln(os.Stderr, "Run 'toolpod help' for details.")
		return gf, "", nil, false
	}
	return gf, remaining[0], remaining[1:], true
}

func runProfile(gf globalFlags, profileName string, passthrough []string) int {
	opts := toolpod.LaunchOpts{
		ProfileName: profileName,
		Workspace:   gf.workspace,
		DryRun:      gf.dryRun,
		Verbose:     gf.verbose,
		Rebuild:     gf.rebuild,
		Command:     gf.command,
		Args:        passthrough,
	}
	if opts.Workspace == "" {
		wd, _ := os.Getwd()
		opts.Workspace = wd
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

func runInit(args []string) int {
	var opts scaffold.Options
	fs := pflag.NewFlagSet("init", pflag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: toolpod init [profile] [flags]")
		fmt.Fprintln(os.Stderr, "  profile  Built-in profile to extend (e.g. opencode, shell, codex)")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	fs.StringSliceVar(&opts.Presets, "presets", nil, "comma-separated preset names")
	fs.BoolVar(&opts.Force, "force", false, "overwrite an existing user profile file")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "print the generated file without writing it")
	if err := fs.Parse(args); err != nil {
		if err == pflag.ErrHelp {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	opts.Profile = fs.Arg(0)
	opts.Interactive = scaffold.IsTTY(os.Stdin)

	err := scaffold.Run(context.Background(), opts, os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}

func usage() {
	presetList := strings.Join(scaffold.PresetNames(), ", ")
	profileList := strings.Join(scaffold.BuiltInProfiles(), ", ")
	fmt.Printf(`toolpod — ephemeral dev environments

Usage:
  toolpod [flags] <profile> [args...]  Launch a profile (e.g. "shell")
  toolpod init [<profile>] [flags]      Generate a user profile override with presets
  toolpod doctor [flags]                Run environment diagnostics
  toolpod prune [flags]                 Remove toolpod-managed volumes and images
  toolpod help                          Show this help

Flags (must come before the profile name):
  -c, --command <cmd>            Run a command in the profile (shell only)
  --workspace <path>             Workspace directory to mount (default: $PWD)
  --dry-run                      Print the spec without launching
  --verbose                       Print the spec before launching
  --rebuild                      Rebuild the image even if cached

Everything after the profile name is passed verbatim to the profile's command,
so agent flags like "--model foo" work without any escaping:
  toolpod opencode --model foo

Commands:
  shell                          Launch the built-in "shell" profile
  init [profile]                 Generate a user profile override with presets
                                 (profile: %s)
  doctor                         Check runtime, profiles, workspace, and project tools
  prune                          Remove toolpod-prefixed volumes and images

Init/prune flags:
  --presets <names>              Init: comma-separated preset names (%s)
  --force                        Init: overwrite existing profile file
  --volumes, --images            Prune: what to remove (default: both)

Examples:
  toolpod shell
  toolpod -c "echo hello" shell
  toolpod --workspace ~/p2 shell
  toolpod opencode --model foo
  toolpod opencode config view
  toolpod init opencode --presets npm,go,gitconfig,ssh
  toolpod doctor
  toolpod prune --force --volumes
  `, profileList, presetList)
}
