package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/alecthomas/kong"
	"github.com/jgillich/tpd/internal/catalog"
	"github.com/jgillich/tpd/internal/doctor"
	"github.com/jgillich/tpd/internal/profile"
	"github.com/jgillich/tpd/internal/prune"
	"github.com/jgillich/tpd/internal/scaffold"
	"github.com/jgillich/tpd/internal/ui"
	"github.com/jgillich/tpd/pkg/tpd"
	"gopkg.in/yaml.v3"
)

type LaunchCmd struct {
	// Tpd-owned flags. They appear before the profile name; kong
	// parses them as part of the default command.
	Command   string `short:"c" help:"Command to run in the profile (shell only)."`
	Workspace string `help:"Workspace directory to mount (default: $PWD)."`
	DryRun    bool   `help:"Print the spec without launching."`
	Verbose   bool   `help:"Print the spec before launching."`
	Pull      bool   `help:"Pull the base image even if already present (refresh mutable tags)."`

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
	All     bool `help:"Remove all tpd-managed resources, even ones the catalog still references."`
	Volumes bool `help:"Scope to tpd-managed volumes only (default: both volumes and images)."`
	Images  bool `help:"Scope to tpd/packages:* derived images only (default: both volumes and images)."`
	Force   bool `help:"Skip confirmation prompt."`
	Yes     bool `short:"y" help:"Skip confirmation prompt (short)."`
}

type CLI struct {
	Launch LaunchCmd      `cmd:"" default:"withargs" help:"Launch a profile (e.g. \"shell\")."`
	Init   InitCmd        `cmd:"" help:"Create a user profile (new or extending built-ins) with fragments."`
	Show   ProfileShowCmd `cmd:"" help:"Print a profile (use --resolved to inline extends)."`
	Edit   ProfileEditCmd `cmd:"" help:"Open the user profile file in $EDITOR."`
	List   ProfileListCmd `cmd:"" help:"List all profiles and fragments."`
	Doctor DoctorCmd      `cmd:"" help:"Run environment diagnostics."`
	Prune  PruneCmd       `cmd:"" help:"Remove tpd-managed volumes and images."`
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
		kong.Name("tpd"),
		kong.Description("ephemeral dev environments"),
	)
	args := os.Args[1:]
	if len(args) == 0 {
		// Bare tpd would select the default launch command and print only its
		// help; route it through --help to show the full command list.
		args = []string{"--help"}
	}
	ctx, err := parser.Parse(args)
	if err != nil {
		parser.FatalIfErrorf(err)
	}
	if err := ctx.Run(); err != nil {
		os.Exit(exitCodeFor(err))
	}
}

// exitCodeFor maps a Run error to the process exit code: exitError carries
// its own code; profile-layer errors exit 2; everything else exits 1.
func exitCodeFor(err error) int {
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.err != nil {
			fmt.Fprintln(os.Stderr, ee.err)
		}
		return ee.code
	}
	var pc profile.ExitCoder
	if errors.As(err, &pc) {
		fmt.Fprintln(os.Stderr, err)
		return pc.ExitCode()
	}
	fmt.Fprintln(os.Stderr, err)
	return 1
}

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *exitError) Unwrap() error { return e.err }

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

	result := tpd.Launch(context.Background(), tpd.LaunchOpts{
		ProfileName: profileName,
		Workspace:   workspace,
		DryRun:      l.DryRun,
		Verbose:     l.Verbose,
		Pull:        l.Pull,
		Command:     l.Command,
		Args:        passthrough,
	})
	if result.Err != nil {
		return &exitError{code: result.ExitCode, err: result.Err}
	}
	if result.ExitCode != 0 {
		return &exitError{code: result.ExitCode}
	}
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
		return &exitError{code: 2, err: err}
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
		return &exitError{code: 1}
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
		return &exitError{code: 3, err: err}
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
		return profile.ProfileError{Message: "profile not found: " + c.Name}
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
		return profile.ProfileError{Message: "profile not found: " + c.Name}
	}
	targetPath := filepath.Join(userDir, c.Name+".yaml")
	if cat.IsFragment(c.Name) {
		targetPath = filepath.Join(profile.DefaultFragmentDir(), c.Name+".yaml")
	}
	if _, err := os.Stat(targetPath); err == nil {
		return openEditor(targetPath)
	}
	// No user file yet: seed the target with a shadow that extends the
	// built-in and shows the resolved profile as a reference comment, then
	// remove the seed unless the user actually saved.
	fsys, root := catalog.Profiles, "profiles"
	kind := "profile"
	if cat.IsFragment(c.Name) {
		fsys, root = catalog.Fragments, "fragments"
		kind = "fragment"
	}
	if _, err := fsys.ReadFile(root + "/" + c.Name + ".yaml"); err != nil {
		return fmt.Errorf("reading built-in %s: %w", c.Name, err)
	}
	var resolved profile.Profile
	var resolveErr error
	if kind == "fragment" {
		resolved, resolveErr = profile.ResolveFragment(cat, c.Name)
	} else {
		resolved, resolveErr = profile.ResolveProfile(cat, c.Name)
	}
	if resolveErr != nil {
		return fmt.Errorf("resolving %s: %w", c.Name, resolveErr)
	}
	resolvedYAML, err := yaml.Marshal(resolved)
	if err != nil {
		return fmt.Errorf("marshaling resolved %s: %w", c.Name, err)
	}
	data := builtinEditSeed(kind, c.Name, resolvedYAML)
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
	content, err := os.ReadFile(targetPath)
	if err != nil {
		return err
	}
	if !savedEdit(before, after, data, content) {
		os.Remove(targetPath)
	}
	return nil
}

// builtinEditSeed renders the file seeded when editing a built-in that has no
// user file yet: a shadow extending the built-in, then the resolved (merged)
// profile as a reference comment.
func builtinEditSeed(kind, name string, resolved []byte) []byte {
	const rule = "# ──────────────────────────────────────────────────────────────────\n"
	var b bytes.Buffer
	fmt.Fprintf(&b, "# This file shadows the built-in %q %s. Settings here are merged on\n", name, kind)
	b.WriteString("# top of the built-in, so only change what you need.\n\n")
	fmt.Fprintf(&b, "version: 1\nextends: %s\n\n", name)
	b.WriteString(rule)
	fmt.Fprintf(&b, "# Resolved %s (reference) — snapshot from when this file was created;\n", kind)
	fmt.Fprintf(&b, "# the built-in may have changed since. Run `tpd show --resolved %s`\n", name)
	fmt.Fprintf(&b, "# for the current resolved %s.\n", kind)
	b.WriteString(rule)
	b.WriteString("\n")
	for _, line := range strings.Split(strings.TrimRight(string(resolved), "\n"), "\n") {
		b.WriteString("# ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.Bytes()
}

// savedEdit reports whether the editor actually wrote the target: the mtime
// advanced, or the content differs from the seed. The content fallback covers
// overlayfs, which under load can leave mtime unchanged despite a write.
func savedEdit(before, after os.FileInfo, seed, content []byte) bool {
	return !after.ModTime().Equal(before.ModTime()) || !bytes.Equal(content, seed)
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
