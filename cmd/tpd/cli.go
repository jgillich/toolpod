package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/tabwriter"

	"github.com/jgillich/tpd/internal/catalog"
	"github.com/jgillich/tpd/internal/doctor"
	"github.com/jgillich/tpd/internal/profile"
	"github.com/jgillich/tpd/internal/prune"
	"github.com/jgillich/tpd/internal/scaffold"
	"github.com/jgillich/tpd/internal/ui"
	"github.com/jgillich/tpd/pkg/tpd"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// launchFlags holds the tpd-owned launch flags shared by the default launch
// command and the explicit `tpd run` command.
type launchFlags struct {
	Command   string
	Workspace string
	DryRun    bool
	Verbose   bool
	Pull      bool
	AssumeYes bool
	AssumeNo  bool
}

// addLaunchFlags registers the launch flags on cmd. Interspersed flag parsing
// is disabled so flags parse only before the first positional: everything
// from the profile name onward reaches the profile's command verbatim, the
// same contract kong's passthrough:partial provided.
func addLaunchFlags(cmd *cobra.Command, o *launchFlags) {
	cmd.Flags().SetInterspersed(false)
	cmd.Flags().StringVarP(&o.Command, "command", "c", "", "Command to run in the profile (shell only); cannot be combined with positional arguments.")
	cmd.Flags().StringVar(&o.Workspace, "workspace", "", "Workspace directory to mount (default: $PWD).")
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "Print the spec without launching.")
	cmd.Flags().BoolVar(&o.Verbose, "verbose", false, "Print the spec before launching.")
	cmd.Flags().BoolVar(&o.Pull, "pull", false, "Pull the base image even if already present (refresh mutable tags).")
	cmd.Flags().BoolVar(&o.AssumeYes, "yes", false, "Auto-approve all unapproved sensitive fields and persist the choice.")
	cmd.Flags().BoolVar(&o.AssumeNo, "no", false, "Auto-deny all unapproved sensitive fields and persist the choice.")
	cmd.MarkFlagsMutuallyExclusive("yes", "no")
}

// runLaunch launches profileName with passthrough args. It returns an
// exitError carrying the container's exit code so main() can map it to the
// process exit status. Usage problems (--command combined with positional
// args, an unusable workspace) are rejected here, before any launch work.
func runLaunch(o *launchFlags, profileName string, passthrough []string) error {
	if o.Command != "" && len(passthrough) > 0 {
		return usageError{msg: "cannot combine --command with profile arguments"}
	}
	workspace, err := resolveWorkspace(o.Workspace)
	if err != nil {
		return usageError{msg: err.Error()}
	}
	result := tpd.Launch(context.Background(), tpd.LaunchOpts{
		ProfileName: profileName,
		Workspace:   workspace,
		DryRun:      o.DryRun,
		Verbose:     o.Verbose,
		Pull:        o.Pull,
		Command:     o.Command,
		Args:        passthrough,
		AssumeYes:   o.AssumeYes,
		AssumeNo:    o.AssumeNo,
	})
	if result.Err != nil {
		return &exitError{code: result.ExitCode, err: result.Err}
	}
	if result.ExitCode != 0 {
		return &exitError{code: result.ExitCode}
	}
	return nil
}

// resolveWorkspace returns the directory to mount as the workspace: the
// --workspace flag when given (it must exist and be a directory), else the
// current directory.
func resolveWorkspace(flag string) (string, error) {
	if flag != "" {
		fi, err := os.Stat(flag)
		if err != nil {
			return "", fmt.Errorf("workspace %q: %w", flag, err)
		}
		if !fi.IsDir() {
			return "", fmt.Errorf("workspace %q is not a directory", flag)
		}
		return flag, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determining working directory: %w", err)
	}
	return wd, nil
}

func newRunCommand(o *launchFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "run <profile> [args...]",
		Short:             "Run a profile (e.g. \"bash\").",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeProfileNames,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 {
				// Help still prints so the launch form stays discoverable, but
				// running without a profile is a usage error, not a no-op.
				_ = c.Help()
				return usageError{msg: "profile name is required"}
			}
			return runLaunch(o, args[0], args[1:])
		},
	}
	addLaunchFlags(cmd, o)
	return cmd
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

// usageError marks invalid CLI input (no profile name, an unusable
// --workspace, --command combined with profile arguments). exitCodeFor maps it
// to 2, the same exit code profile-layer errors use.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }
func (e usageError) ExitCode() int { return 2 }

func newShowCommand() *cobra.Command {
	var resolved, provenance bool
	cmd := &cobra.Command{
		Use:               "show <name>",
		Short:             "Print a profile (use --resolved to inline extends, --provenance to show per-file attribution).",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeNamesOnce,
		RunE: func(c *cobra.Command, args []string) error {
			return runShow(args[0], resolved, provenance)
		},
	}
	cmd.Flags().BoolVar(&resolved, "resolved", false, "Inline all extends and show the fully merged profile.")
	cmd.Flags().BoolVar(&provenance, "provenance", false, "Show which catalog entry contributed each field (implies --resolved).")
	return cmd
}

func runShow(name string, resolved, provenance bool) error {
	cat, err := profile.LoadProfiles(profile.DefaultProfileDir())
	if err != nil {
		return fmt.Errorf("loading profiles: %w", err)
	}
	key, ok := resolveCatalogName(cat, name)
	if !ok {
		return profile.ProfileError{Message: "profile not found: " + name}
	}
	if provenance {
		if cat.IsFragment(key) {
			resolvedProfile, err := profile.ResolveFragmentWithProv(cat, key)
			if err != nil {
				return err
			}
			out, err := resolvedProfile.ProvenanceYAML()
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		}
		resolvedProfile, err := profile.ResolveProfileWithProv(cat, key)
		if err != nil {
			return err
		}
		out, err := resolvedProfile.ProvenanceYAML()
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	}
	if resolved {
		if cat.IsFragment(key) {
			resolvedProfile, err := profile.ResolveFragment(cat, key)
			if err != nil {
				return err
			}
			out, err := yaml.Marshal(resolvedProfile)
			if err != nil {
				return err
			}
			fmt.Print(string(out))
			return nil
		}
		resolvedProfile, err := profile.ResolveProfile(cat, key)
		if err != nil {
			return err
		}
		out, err := yaml.Marshal(resolvedProfile)
		if err != nil {
			return err
		}
		fmt.Print(string(out))
		return nil
	}
	rc, _ := cat.Get(key)
	out, err := yaml.Marshal(rc.Profile)
	if err != nil {
		return err
	}
	fmt.Print(string(out))
	return nil
}

func newEditCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "edit <name>",
		Short:             "Open the user profile file in $EDITOR.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeNamesOnce,
		RunE: func(c *cobra.Command, args []string) error {
			return runEdit(args[0])
		},
	}
}

func runEdit(name string) error {
	userDir := profile.DefaultProfileDir()
	if userDir == "" {
		return fmt.Errorf("cannot determine profile directory (set XDG_CONFIG_HOME)")
	}
	// Load tolerantly so a malformed user file cannot block opening itself
	// for repair: the broken file is skipped (warned) while any built-in
	// shadow it replaces stays resolvable.
	cat, err := profile.LoadProfilesTolerant(userDir, func(msg string) {
		fmt.Fprintln(os.Stderr, "warning: "+msg)
	})
	if err != nil {
		return fmt.Errorf("loading profiles: %w", err)
	}
	key, ok := resolveCatalogName(cat, name)
	if !ok {
		// The tolerant load drops a malformed file with no built-in shadow;
		// fall back to the filesystem so it can still be edited.
		if targetPath, ok := userEditFile(userDir, name); ok {
			return openEditor(targetPath)
		}
		return profile.ProfileError{Message: "profile not found: " + name}
	}
	rc, _ := cat.Get(key)
	local := rc.Name
	targetPath := filepath.Join(userDir, local+".yaml")
	if cat.IsFragment(key) {
		targetPath = filepath.Join(profile.DefaultFragmentDir(), local+".yaml")
	}
	if _, err := os.Stat(targetPath); err == nil {
		return openEditor(targetPath)
	}
	// No user file yet: seed the target with a shadow that extends the
	// built-in, then remove the seed on return unless the editor wrote
	// changes. An editor that errors or quits without saving must not leave
	// an untouched shadow behind.
	fsys, root := catalog.Profiles, "profiles"
	kind := "profile"
	if cat.IsFragment(key) {
		fsys, root = catalog.Fragments, "fragments"
		kind = "fragment"
	}
	if _, err := fsys.ReadFile(root + "/" + local + ".yaml"); err != nil {
		return fmt.Errorf("reading built-in %s: %w", local, err)
	}
	data := builtinEditSeed(kind, key)
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
	// Registered right after the write so every path — including an editor
	// error — removes an unchanged seed; any change to the bytes preserves it.
	defer func() {
		after, err := os.Stat(targetPath)
		if err != nil {
			return
		}
		content, err := os.ReadFile(targetPath)
		if err != nil {
			return
		}
		if !savedEdit(before, after, data, content) {
			os.Remove(targetPath)
		}
	}()
	return openEditor(targetPath)
}

// builtinEditSeed renders the file seeded when editing a built-in that has no
// user file yet: a shadow extending the built-in.
func builtinEditSeed(kind, canonicalKey string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# This file shadows the built-in %q %s. Settings here are merged on\n", canonicalKey, kind)
	b.WriteString("# top of the built-in, so only change what you need.\n\n")
	fmt.Fprintf(&b, "version: 1\nextends: %s\n", canonicalKey)
	return b.Bytes()
}

// savedEdit reports whether the editor actually wrote the target: the mtime
// advanced, or the content differs from the seed. The content fallback covers
// overlayfs, which under load can leave mtime unchanged despite a write.
func savedEdit(before, after os.FileInfo, seed, content []byte) bool {
	return !after.ModTime().Equal(before.ModTime()) || !bytes.Equal(content, seed)
}

// userEditFile returns the user profile/fragment file for name if it exists,
// preferring the profiles dir over fragments. The name is validated first so a
// qualified ref or path escape never reaches the filesystem. Fallback for when
// the tolerant catalog load dropped a malformed user-only file.
func userEditFile(userDir, name string) (string, bool) {
	if err := profile.ValidateName(name); err != nil {
		return "", false
	}
	if _, err := os.Stat(filepath.Join(userDir, name+".yaml")); err == nil {
		return filepath.Join(userDir, name+".yaml"), true
	}
	fragDir := filepath.Join(filepath.Dir(userDir), "fragments")
	if _, err := os.Stat(filepath.Join(fragDir, name+".yaml")); err == nil {
		return filepath.Join(fragDir, name+".yaml"), true
	}
	return "", false
}

func openEditor(path string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "nano"
	}
	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func newListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all profiles and fragments.",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			return runList()
		},
	}
}

func runList() error {
	cat, err := profile.LoadProfiles(profile.DefaultProfileDir())
	if err != nil {
		return fmt.Errorf("loading profiles: %w", err)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKIND\tSOURCE\tDESCRIPTION")
	for _, dn := range cat.DisplayNames() {
		kind := "profile"
		if ref, err := cat.ParseRefForCatalog(dn); err == nil {
			if key, ok := cat.ResolveRef(ref); ok && cat.IsFragment(key) {
				kind = "fragment"
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", dn, kind, cat.Source(dn), truncateRunes(cat.Description(dn), 60))
	}
	w.Flush()
	return nil
}

// truncateRunes shortens s to at most max runes (a multi-byte-safe "~60
// chars"), appending an ellipsis when it cuts. tabwriter cannot wrap, so list
// keeps descriptions on one line.
func truncateRunes(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "…"
}

func newInitCommand() *cobra.Command {
	var (
		name    string
		extends []string
		force   bool
		merge   bool
		dryRun  bool
	)
	cmd := &cobra.Command{
		Use:   "init [name]",
		Short: "Create a user profile (new or extending built-ins) with fragments.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				name = args[0]
			}
			opts := scaffold.Options{
				Name:        name,
				Extends:     extends,
				Force:       force,
				Merge:       merge,
				DryRun:      dryRun,
				Interactive: ui.IsTTYReader(os.Stdin) && ui.IsTTY(os.Stdout),
			}
			if err := scaffold.Run(context.Background(), opts, os.Stdin, os.Stdout, os.Stderr); err != nil {
				return &exitError{code: 2, err: err}
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&extends, "extends", nil, "Comma-separated bases to extend: profiles, fragments, or mise.")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite an existing user profile file.")
	cmd.Flags().BoolVar(&merge, "merge", false, "Merge the selection into an existing user profile file.")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the generated file without writing it.")
	cmd.RegisterFlagCompletionFunc("extends", completeNames)
	return cmd
}

func newDoctorCommand() *cobra.Command {
	var workspace string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run environment diagnostics.",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if workspace == "" {
				wd, _ := os.Getwd()
				workspace = wd
			}
			result := doctor.Run(context.Background(), doctor.Options{Workspace: workspace})
			out := ui.NewOutput(ui.IsTTY(os.Stdout))
			for _, chk := range result.Checks {
				color := "reset"
				switch chk.Status {
				case doctor.Pass:
					color = "green"
				case doctor.Fail:
					color = "red"
				case doctor.Warn:
					color = "yellow"
				case doctor.Info, doctor.Skip:
					color = "blue"
				}
				fmt.Println(out.Color(color, chk.Format()))
			}
			fmt.Println()
			fmt.Println(out.Color("reset", result.Summary()))
			if result.HasFailure() {
				return &exitError{code: 1}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "Workspace to check.")
	return cmd
}

func newPruneCommand() *cobra.Command {
	var (
		all      bool
		volumes  bool
		images   bool
		networks bool
		force    bool
		yes      bool
	)
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove tpd-managed volumes, images, and the service network.",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			opts := prune.Options{
				All:      all,
				Volumes:  volumes,
				Images:   images,
				Networks: networks,
				Force:    force || yes,
			}
			result, err := prune.Run(context.Background(), opts)
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
			if err != nil {
				return &exitError{code: 3, err: err}
			}
			if len(result.NetworksRemoved) > 0 {
				fmt.Printf("Removed %d network(s):\n", len(result.NetworksRemoved))
				for _, n := range result.NetworksRemoved {
					fmt.Printf("  %s\n", n)
				}
			}
			if len(result.VolumesRemoved) == 0 && len(result.ImagesRemoved) == 0 && len(result.NetworksRemoved) == 0 {
				fmt.Println("Nothing to prune.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Remove all tpd-managed resources, even ones the catalog still references.")
	cmd.Flags().BoolVar(&volumes, "volumes", false, "Scope to tpd-managed volumes only (default: both volumes and images).")
	cmd.Flags().BoolVar(&images, "images", false, "Scope to tpd/packages:* derived images only (default: both volumes and images).")
	cmd.Flags().BoolVar(&networks, "networks", false, "Scope to the tpd-managed service network only.")
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt.")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt (short).")
	return cmd
}

// newRootCommand wires the dispatch surface. Profiles are positional args of
// the root command, not registered subcommands, so a profile whose name equals
// a subcommand (run, show, edit, list, ...) is shadowed by the subcommand in
// the bare form; the profile stays reachable as `tpd run <name>`. Names are
// not reserved for these, so existing run/show/edit/list profiles keep loading.
func newRootCommand() *cobra.Command {
	o := &launchFlags{}
	root := &cobra.Command{
		Use:               "tpd <profile> [args...]",
		Short:             "ephemeral dev environments",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeProfileNames,
		SilenceErrors:     true,
		SilenceUsage:      true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 {
				// Bare tpd shows the full command list; otherwise the only
				// way to see it is the help flag. Running with no profile is
				// a usage error, not a no-op.
				_ = c.Help()
				return usageError{msg: "profile name is required"}
			}
			return runLaunch(o, args[0], args[1:])
		},
	}
	addLaunchFlags(root, o)
	root.AddCommand(newRunCommand(o))
	root.AddCommand(newShowCommand())
	root.AddCommand(newEditCommand())
	root.AddCommand(newListCommand())
	root.AddCommand(newInitCommand())
	root.AddCommand(newDoctorCommand())
	root.AddCommand(newPruneCommand())
	return root
}

func main() {
	if err := newRootCommand().Execute(); err != nil {
		os.Exit(exitCodeFor(err))
	}
}

// resolveCatalogName maps a user-supplied profile/fragment name to its
// canonical catalog key (user entry first, then core fallback).
func resolveCatalogName(cat profile.Catalog, name string) (string, bool) {
	ref, err := cat.ParseRefForCatalog(name)
	if err != nil {
		return "", false
	}
	return cat.ResolveRef(ref)
}
