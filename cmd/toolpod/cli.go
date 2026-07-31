package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/jgillich/toolpod/internal/doctor"
	"github.com/jgillich/toolpod/internal/profile"
	"github.com/jgillich/toolpod/internal/prune"
	"github.com/jgillich/toolpod/internal/scaffold"
	"github.com/jgillich/toolpod/internal/ui"
	"github.com/jgillich/toolpod/pkg/toolpod"
	"gopkg.in/yaml.v3"
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
	Launch  LaunchCmd  `cmd:"" default:"withargs" help:"Launch a profile (e.g. \"shell\")."`
	Init    InitCmd    `cmd:"" help:"Generate a user profile override with fragments."`
	Profile ProfileCmd `cmd:"" help:"Inspect and edit profiles and fragments."`
	Doctor  DoctorCmd  `cmd:"" help:"Run environment diagnostics."`
	Prune   PruneCmd   `cmd:"" help:"Remove toolpod-managed volumes and images."`
}

type ProfileCmd struct {
	Show ProfileShowCmd `cmd:"" help:"Print a profile (use --resolved to inline extends)."`
	Edit ProfileEditCmd `cmd:"" help:"Open the user profile file in $EDITOR."`
	List ProfileListCmd `cmd:"" help:"List all profiles and fragments."`
}

type ProfileShowCmd struct {
	Name     string `arg:"" help:"Profile name to show."`
	Resolved bool   `help:"Inline all extends and show the fully merged profile."`
}

type ProfileEditCmd struct {
	Name string `arg:"" help:"Profile name to edit."`
}

type ProfileListCmd struct{}

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

func (c *ProfileShowCmd) Run() error {
	cat, err := profile.LoadProfiles(profile.DefaultProfileDir())
	if err != nil {
		return fmt.Errorf("loading profiles: %w", err)
	}
	if c.Resolved {
		if cat.IsFragment(c.Name) {
			return fmt.Errorf("%s is a fragment, not a profile (use 'profile show %s' without --resolved to view it)", c.Name, c.Name)
		}
		resolved, err := profile.ResolveProfile(cat, c.Name)
		if err != nil {
			return err
		}
		out, err := yaml.Marshal(resolved)
		if err != nil {
			return err
		}
		fmt.Print(string(out))
		return nil
	}
	rc, ok := cat.Get(c.Name)
	if !ok {
		return fmt.Errorf("profile not found: %s", c.Name)
	}
	out, err := yaml.Marshal(rc.Profile)
	if err != nil {
		return err
	}
	fmt.Print(string(out))
	return nil
}

func (c *ProfileEditCmd) Run() error {
	userDir := profile.DefaultProfileDir()
	if userDir == "" {
		return fmt.Errorf("cannot determine profile directory (set XDG_CONFIG_HOME)")
	}
	targetPath := filepath.Join(userDir, c.Name+".yaml")
	if _, err := os.Stat(targetPath); err == nil {
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		cmd := exec.Command(editor, targetPath)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	builtin, err := profile.LoadProfiles("")
	if err != nil {
		return fmt.Errorf("loading profiles: %w", err)
	}
	if _, ok := builtin.Get(c.Name); ok {
		return fmt.Errorf("this is a built-in profile. Run 'toolpod init %s' to create a user override.", c.Name)
	}
	if builtin.IsFragment(c.Name) {
		return fmt.Errorf("%s is a fragment, not an editable profile file", c.Name)
	}
	return fmt.Errorf("profile not found: %s", c.Name)
}

func (c *ProfileListCmd) Run() error {
	cat, err := profile.LoadProfiles(profile.DefaultProfileDir())
	if err != nil {
		return fmt.Errorf("loading profiles: %w", err)
	}
	for _, name := range cat.Names() {
		rc, _ := cat.Get(name)
		var label string
		switch {
		case cat.IsFragment(name) && strings.HasPrefix(rc.Path, "built-in"):
			label = "fragment"
		case cat.IsFragment(name):
			label = "user-fragment"
		case cat.IsUserShadow(name):
			label = "user-shadow"
		case strings.HasPrefix(rc.Path, "built-in"):
			label = "built-in"
		default:
			label = "user"
		}
		fmt.Printf("%-20s %s\n", name, label)
	}
	return nil
}