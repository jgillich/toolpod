package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/alecthomas/kong"
	"github.com/jgillich/tpod/internal/catalog"
	"github.com/jgillich/tpod/internal/doctor"
	"github.com/jgillich/tpod/internal/profile"
	"github.com/jgillich/tpod/internal/prune"
	"github.com/jgillich/tpod/internal/scaffold"
	"github.com/jgillich/tpod/internal/ui"
	"github.com/jgillich/tpod/pkg/tpod"
	"gopkg.in/yaml.v3"
)

type LaunchCmd struct {
	// Tpod-owned flags. They appear before the profile name; kong
	// parses them as part of the default command.
	Command   string `short:"c" help:"Command to run in the profile (shell only)."`
	Workspace string `help:"Workspace directory to mount (default: $PWD)."`
	DryRun    bool   `help:"Print the spec without launching."`
	Verbose   bool   `help:"Print the spec before launching."`

	// Profile-and-args holds the profile name followed by everything
	// passed verbatim to the profile's command. passthrough:"" stops
	// flag parsing at the first positional, so agent flags like
	// "--model foo" survive unescaped.
	ProfileAndArgs []string `arg:"" optional:"" name:"profile-and-args" passthrough:"partial" help:"Profile name followed by args passed verbatim to the profile."`
}

type InitCmd struct {
	Name    string   `arg:"" optional:"" help:"Profile name to create."`
	Extends []string `sep:"," help:"Comma-separated bases to extend: profiles, fragments, or mise."`
	Force   bool     `help:"Overwrite an existing user profile file."`
	DryRun  bool     `help:"Print the generated file without writing it."`
}

type DoctorCmd struct {
	Workspace string `help:"Workspace to check."`
}

type PruneCmd struct {
	All     bool `help:"Remove all tpod-managed resources, even ones the catalog still references."`
	Volumes bool `help:"Scope to tpod-managed volumes only (default: both volumes and images)."`
	Images  bool `help:"Scope to tpod/packages:* derived images only (default: both volumes and images)."`
	Force   bool `help:"Skip confirmation prompt."`
	Yes     bool `short:"y" help:"Skip confirmation prompt (short)."`
}

type CLI struct {
	Launch LaunchCmd       `cmd:"" default:"withargs" help:"Launch a profile (e.g. \"shell\")."`
	Init   InitCmd         `cmd:"" help:"Create a user profile (new or extending built-ins) with fragments."`
	Show   ProfileShowCmd  `cmd:"" help:"Print a profile (use --resolved to inline extends)."`
	Edit   ProfileEditCmd  `cmd:"" help:"Open the user profile file in $EDITOR."`
	List   ProfileListCmd  `cmd:"" help:"List all profiles and fragments."`
	Doctor DoctorCmd       `cmd:"" help:"Run environment diagnostics."`
	Prune  PruneCmd        `cmd:"" help:"Remove tpod-managed volumes and images."`
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
	parser := kong.Must(&cli,
		kong.Name("tpod"),
		kong.Description("ephemeral dev environments"),
	)
	args := os.Args[1:]
	if len(args) == 0 {
		// Bare tpod would select the default launch command and print only its
		// help; route it through --help to show the full command list.
		args = []string{"--help"}
	}
	ctx, err := parser.Parse(args)
	parser.FatalIfErrorf(err)
	err = ctx.Run()
	parser.FatalIfErrorf(err)
	if err != nil {
		// Non-parse errors from Run are handled per-command for exit codes;
		// anything that reaches here is already printed.
		os.Exit(1)
	}
}

func (l *LaunchCmd) Run(ctx *kong.Context) error {
	if len(l.ProfileAndArgs) == 0 {
		return ctx.PrintUsage(false)
	}
	profileName := l.ProfileAndArgs[0]
	passthrough := l.ProfileAndArgs[1:]

	workspace := l.Workspace
	if workspace == "" {
		wd, _ := os.Getwd()
		workspace = wd
	}

	result := tpod.Launch(context.Background(), tpod.LaunchOpts{
		ProfileName: profileName,
		Workspace:   workspace,
		DryRun:      l.DryRun,
		Verbose:     l.Verbose,
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
		Name:        i.Name,
		Extends:     i.Extends,
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
		All:     p.All,
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
		for _, r := range result.ImagesRemoved {
			fmt.Printf("  %s\n", r)
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
			return fmt.Errorf("%s is a fragment, not a profile (use 'show %s' without --resolved to view it)", c.Name, c.Name)
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
	cat, err := profile.LoadProfiles(userDir)
	if err != nil {
		return fmt.Errorf("loading profiles: %w", err)
	}
	if _, ok := cat.Get(c.Name); !ok {
		return fmt.Errorf("profile not found: %s", c.Name)
	}
	targetPath := filepath.Join(userDir, c.Name+".yaml")
	if cat.IsFragment(c.Name) {
		targetPath = filepath.Join(profile.DefaultFragmentDir(), c.Name+".yaml")
	}
	if _, err := os.Stat(targetPath); err == nil {
		return openEditor(targetPath)
	}
	// No user file yet: seed the target with the built-in's content so the
	// editor loads it, then remove the seed unless the user actually saved.
	fsys, root := catalog.Profiles, "profiles"
	if cat.IsFragment(c.Name) {
		fsys, root = catalog.Fragments, "fragments"
	}
	data, err := fsys.ReadFile(root + "/" + c.Name + ".yaml")
	if err != nil {
		return fmt.Errorf("reading built-in %s: %w", c.Name, err)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
		return fmt.Errorf("creating profile directory: %w", err)
	}
	if err := os.WriteFile(targetPath, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", targetPath, err)
	}
	before, err := os.Stat(targetPath)
	if err != nil {
		return err
	}
	if err := openEditor(targetPath); err != nil {
		return err
	}
	after, err := os.Stat(targetPath)
	if err != nil {
		return err
	}
	if after.ModTime().Equal(before.ModTime()) {
		os.Remove(targetPath)
	}
	return nil
}

func openEditor(path string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (c *ProfileListCmd) Run() error {
	cat, err := profile.LoadProfiles(profile.DefaultProfileDir())
	if err != nil {
		return fmt.Errorf("loading profiles: %w", err)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKIND\tSOURCE")
	for _, name := range cat.Names() {
		rc, _ := cat.Get(name)
		kind, source := "profile", "built-in"
		if cat.IsFragment(name) {
			kind = "fragment"
		}
		if !strings.HasPrefix(rc.Path, "built-in") {
			source = "user"
			if cat.IsUserShadow(name) {
				source = "user shadow"
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", name, kind, source)
	}
	w.Flush()
	return nil
}
