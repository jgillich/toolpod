# Cobra CLI Rewrite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the kong-based CLI in `cmd/tpd/` with cobra, preserving every current flag, passthrough and exit-code behavior, and gain native shell completion for profile/fragment names.

**Architecture:** `cmd/tpd/cli.go` is rewritten from kong struct-tags to a cobra command tree. The launch command keeps kong's `passthrough:"partial"` contract via pflag's `SetInterspersed(false)` (flags parse only before the first positional; everything from the profile name onward reaches the profile verbatim). Profiles are NOT registered as cobra subcommands — they are the root command's positional args, completed by a `ValidArgsFunction` backed by the profile catalog. Cobra's built-in `completion` command and hidden `__complete` request command provide shell completion for bash/zsh/fish/powershell with no extra library.

**Tech Stack:** Go 1.25, [spf13/cobra v1.10.2](https://github.com/spf13/cobra) + spf13/pflag (pulled in by cobra). kong is removed from `go.mod` (it is only used by `cmd/tpd/cli.go` and `cmd/tpd/cli_test.go`).

## Global Constraints

- **Exit codes preserved exactly** (existing `exitCodeFor`/`exitError` in `cmd/tpd/cli.go` are kept verbatim): container exit codes pass through; `profile.ProfileError` → 2; prune errors → 3; doctor failure → 1; scaffold errors → 2; anything else → 1.
- **Passthrough contract preserved**: `tpd <profile> [args...]` passes `[args...]` verbatim to the profile's command, even tokens that look like flags (`tpd shell --model foo`, `tpd node -e x`). Flags before the profile name are tpd's own (`tpd -c "echo hi" shell`). Implemented with `SetInterspersed(false)` on the launch flag sets — no argv pre-parsing.
- **Flag names/shorthands unchanged**: launch/root `-c|--command`, `--workspace`, `--dry-run`, `--verbose`, `--pull`; `show --resolved`; `init --extends`, `--force`, `--dry-run`; `doctor --workspace`; `prune --all`, `--volumes`, `--images`, `--force`, `-y|--yes`.
- **Root command sets `SilenceErrors: true` and `SilenceUsage: true`** so cobra prints nothing on RunE errors; `main()` maps the returned error to the exit code via `exitCodeFor`.
- **Keep cobra's default `completion` command** (`tpd completion bash|zsh|fish|powershell`) and hidden `__complete`; do not set `CompletionOptions.DisableDefaultCmd`.
- **No changes outside `cmd/tpd/`** and `go.mod` (except AGENTS.md docs note in the last task). `pkg/tpd`, `internal/*` are untouched.
- Only `cmd/tpd` files: `cli.go` (rewrite), `cli_test.go` (update), new `completion.go` and `completion_test.go`. `main.go` (empty `package main`) stays as-is.
- **No comments** except where the code's contract is not apparent (e.g. the passthrough rationale).
- Work on a fresh branch `feat/cobra-cli` created from `main`. There is an existing worktree `.worktrees/namespaces` (branch `feat/profile-namespaces`) that also edits `cmd/tpd/cli.go` — do not mix them. Follow `superpowers:using-git-worktrees` to create the isolated worktree before starting Task 1.
- Conventional commit messages.

---

### Task 1: Swap kong for cobra and port the full command tree

**Files:**
- Modify: `go.mod` (swap kong → cobra)
- Rewrite: `cmd/tpd/cli.go`
- Rewrite: `cmd/tpd/cli_test.go`
- Test: `cmd/tpd/cli_test.go`, `cmd/tpd/profile_test.go`, `cmd/tpd/e2e_runtime_test.go` (unchanged, must stay green)

**Interfaces:**
- Consumes: `pkg/tpd.Launch`/`LaunchOpts` (`DryRun`, `Verbose`, `Pull`, `Command`, `Args`, `ProfileName`, `Workspace`), `doctor.Run`/`Options`, `prune.Run`/`Options` + `Result.VolumesRemoved`/`ImagesRemoved`, `scaffold.Run`/`Options`/`IsTTY`, `profile.LoadProfiles`/`DefaultProfileDir`/`DefaultFragmentDir`/`ProfileError`/`ExitCoder`, `catalog.Advisory`/`Profiles`/`Fragments`, `ui.NewOutput`/`IsTTY` — all unchanged.
- Produces: `launchFlags` struct, `addLaunchFlags`, `runLaunch`, `newLaunchCommand`, `newRootCommand`, `runShow`, `runEdit`, `runList`, `main()`. These exact names/types are used by Task 2.

- [ ] **Step 1: Swap the dependency in `go.mod`**

Run in the repo root:

```bash
go get github.com/spf13/cobra@v1.10.2
go mod tidy
```

Expected: `go.mod` now requires `github.com/spf13/cobra v1.10.2` (and `github.com/spf13/pflag v1.0.8` as indirect), and `github.com/alecthomas/kong` is gone. The tests below that still import kong are red for now — that is expected until Step 5.

- [ ] **Step 2: Rewrite `cmd/tpd/cli.go` — imports, error plumbing, launch command**

Replace the entire file with:

```go
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
// command and the explicit `tpd launch` command.
type launchFlags struct {
	Command   string
	Workspace string
	DryRun    bool
	Verbose   bool
	Pull      bool
}

// addLaunchFlags registers the launch flags on cmd. Interspersed flag parsing
// is disabled so flags parse only before the first positional: everything
// from the profile name onward reaches the profile's command verbatim, the
// same contract kong's passthrough:partial provided.
func addLaunchFlags(cmd *cobra.Command, o *launchFlags) {
	cmd.Flags().SetInterspersed(false)
	cmd.Flags().StringVarP(&o.Command, "command", "c", "", "Command to run in the profile (shell only).")
	cmd.Flags().StringVar(&o.Workspace, "workspace", "", "Workspace directory to mount (default: $PWD).")
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "Print the spec without launching.")
	cmd.Flags().BoolVar(&o.Verbose, "verbose", false, "Print the spec before launching.")
	cmd.Flags().BoolVar(&o.Pull, "pull", false, "Pull the base image even if already present (refresh mutable tags).")
}

// runLaunch launches profileName with passthrough args. It returns an
// exitError carrying the container's exit code so main() can map it to the
// process exit status.
func runLaunch(o *launchFlags, profileName string, passthrough []string) error {
	workspace := o.Workspace
	if workspace == "" {
		wd, _ := os.Getwd()
		workspace = wd
	}
	result := tpd.Launch(context.Background(), tpd.LaunchOpts{
		ProfileName: profileName,
		Workspace:   workspace,
		DryRun:      o.DryRun,
		Verbose:     o.Verbose,
		Pull:        o.Pull,
		Command:     o.Command,
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

func newLaunchCommand(o *launchFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "launch <profile> [args...]",
		Short: "Launch a profile (e.g. \"shell\").",
		Args:  cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 {
				return c.Help()
			}
			return runLaunch(o, args[0], args[1:])
		},
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
```

- [ ] **Step 3: `cmd/tpd/cli.go` — port show/edit/list**

Append to `cli.go`:

```go
func newShowCommand() *cobra.Command {
	var resolved bool
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Print a profile (use --resolved to inline extends).",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runShow(args[0], resolved)
		},
	}
	cmd.Flags().BoolVar(&resolved, "resolved", false, "Inline all extends and show the fully merged profile.")
	return cmd
}

func runShow(name string, resolved bool) error {
	cat, err := profile.LoadProfiles(profile.DefaultProfileDir())
	if err != nil {
		return fmt.Errorf("loading profiles: %w", err)
	}
	if resolved {
		if cat.IsFragment(name) {
			return fmt.Errorf("%s is a fragment, not a profile (use 'show %s' without --resolved to view it)", name, name)
		}
		resolvedProfile, err := profile.ResolveProfile(cat, name)
		if err != nil {
			return err
		}
		out, err := yaml.Marshal(resolvedProfile)
		if err != nil {
			return err
		}
		fmt.Print(string(out))
		if msg := catalog.Advisory(name); msg != "" {
			fmt.Fprintln(os.Stderr, "warning: "+msg)
		}
		return nil
	}
	rc, ok := cat.Get(name)
	if !ok {
		return profile.ProfileError{Message: "profile not found: " + name}
	}
	out, err := yaml.Marshal(rc.Profile)
	if err != nil {
		return err
	}
	fmt.Print(string(out))
	if msg := catalog.Advisory(name); msg != "" {
		fmt.Fprintln(os.Stderr, "warning: "+msg)
	}
	return nil
}

func newEditCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "edit <name>",
		Short: "Open the user profile file in $EDITOR.",
		Args:  cobra.ExactArgs(1),
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
	cat, err := profile.LoadProfiles(userDir)
	if err != nil {
		return fmt.Errorf("loading profiles: %w", err)
	}
	if _, ok := cat.Get(name); !ok {
		return profile.ProfileError{Message: "profile not found: " + name}
	}
	if msg := catalog.Advisory(name); msg != "" {
		fmt.Fprintln(os.Stderr, "warning: "+msg)
	}
	targetPath := filepath.Join(userDir, name+".yaml")
	if cat.IsFragment(name) {
		targetPath = filepath.Join(profile.DefaultFragmentDir(), name+".yaml")
	}
	if _, err := os.Stat(targetPath); err == nil {
		return openEditor(targetPath)
	}
	fsys, root := catalog.Profiles, "profiles"
	kind := "profile"
	if cat.IsFragment(name) {
		fsys, root = catalog.Fragments, "fragments"
		kind = "fragment"
	}
	if _, err := fsys.ReadFile(root + "/" + name + ".yaml"); err != nil {
		return fmt.Errorf("reading built-in %s: %w", name, err)
	}
	var resolved profile.Profile
	var resolveErr error
	if kind == "fragment" {
		resolved, resolveErr = profile.ResolveFragment(cat, name)
	} else {
		resolved, resolveErr = profile.ResolveProfile(cat, name)
	}
	if resolveErr != nil {
		return fmt.Errorf("resolving %s: %w", name, resolveErr)
	}
	resolvedYAML, err := yaml.Marshal(resolved)
	if err != nil {
		return fmt.Errorf("marshaling resolved %s: %w", name, err)
	}
	data := builtinEditSeed(kind, name, resolvedYAML)
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
```

- [ ] **Step 4: `cmd/tpd/cli.go` — port init/doctor/prune, wire root and main**

Append to `cli.go`:

```go
func newInitCommand() *cobra.Command {
	var (
		name    string
		extends []string
		force   bool
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
				DryRun:      dryRun,
				Interactive: scaffold.IsTTY(os.Stdin),
			}
			if err := scaffold.Run(context.Background(), opts, os.Stdin, os.Stdout, os.Stderr); err != nil {
				return &exitError{code: 2, err: err}
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&extends, "extends", nil, "Comma-separated bases to extend: profiles, fragments, or mise.")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite an existing user profile file.")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the generated file without writing it.")
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
		all     bool
		volumes bool
		images  bool
		force   bool
		yes     bool
	)
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove tpd-managed volumes and images.",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			opts := prune.Options{
				All:     all,
				Volumes: volumes,
				Images:  images,
				Force:   force || yes,
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
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Remove all tpd-managed resources, even ones the catalog still references.")
	cmd.Flags().BoolVar(&volumes, "volumes", false, "Scope to tpd-managed volumes only (default: both volumes and images).")
	cmd.Flags().BoolVar(&images, "images", false, "Scope to tpd/packages:* derived images only (default: both volumes and images).")
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt.")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt (short).")
	return cmd
}

func newRootCommand() *cobra.Command {
	o := &launchFlags{}
	root := &cobra.Command{
		Use:           "tpd",
		Short:         "ephemeral dev environments",
		Args:          cobra.ArbitraryArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 {
				// Bare tpd shows the full command list; otherwise the only
				// way to see it is the help flag.
				return c.Help()
			}
			return runLaunch(o, args[0], args[1:])
		},
	}
	addLaunchFlags(root, o)
	root.AddCommand(newLaunchCommand(o))
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
```

- [ ] **Step 5: Rewrite `cmd/tpd/cli_test.go`**

Replace the file with:

```go
package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgillich/tpd/internal/profile"
)

func buildTpd(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "tpd")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func TestLaunchBareShowsHelp(t *testing.T) {
	bin := buildTpd(t)
	cmd := exec.Command(bin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bare tpd should show help and exit 0, got err: %v\n%s", err, out)
	}
	for _, c := range []string{"Usage:", "launch", "show", "profile"} {
		if !strings.Contains(string(out), c) {
			t.Errorf("expected bare tpd help to mention %q, got:\n%s", c, out)
		}
	}
}

func TestBareHelpShowsAllCommands(t *testing.T) {
	bin := buildTpd(t)
	cmd := exec.Command(bin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bare tpd should show help and exit 0, got err: %v\n%s", err, out)
	}
	for _, c := range []string{"launch", "init", "profile", "doctor", "prune"} {
		if !strings.Contains(string(out), c) {
			t.Errorf("expected bare tpd help to mention %q, got:\n%s", c, out)
		}
	}
}

func TestLaunchFlagsBind(t *testing.T) {
	o := &launchFlags{}
	cmd := newLaunchCommand(o)
	if err := cmd.Flags().Parse([]string{"--pull", "--dry-run", "--verbose", "--command", "ls", "--workspace", "/tmp"}); err != nil {
		t.Fatalf("parse launch flags: %v", err)
	}
	if !o.Pull || !o.DryRun || !o.Verbose || o.Command != "ls" || o.Workspace != "/tmp" {
		t.Errorf("launch flags not bound: %+v", o)
	}
}

func TestLaunchPassthroughAfterProfile(t *testing.T) {
	// kong's passthrough:partial contract: everything from the profile name
	// onward reaches the profile verbatim, even tokens that look like flags.
	o := &launchFlags{}
	cmd := newLaunchCommand(o)
	if err := cmd.Flags().Parse([]string{"shell", "--model", "foo", "-c", "ls"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := cmd.Flags().Args(); fmt.Sprint(got) != "[shell --model foo -c ls]" {
		t.Errorf("passthrough args = %v, want [shell --model foo -c ls]", got)
	}
	if o.Command != "" {
		t.Errorf("-c after profile must not bind to the launch flag, got %q", o.Command)
	}
}

func TestInitHelpMentionsExtends(t *testing.T) {
	bin := buildTpd(t)
	cmd := exec.Command(bin, "init", "--help")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("init --help: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--extends") {
		t.Errorf("expected --extends in init help, got:\n%s", out)
	}
}

func TestExitCodeFor(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"exitError code 0", &exitError{code: 0}, 0},
		{"exitError code 1", &exitError{code: 1}, 1},
		{"exitError code 2", &exitError{code: 2}, 2},
		{"exitError code 3", &exitError{code: 3}, 3},
		{"exitError code 5 (container exit)", &exitError{code: 5}, 5},
		{"exitError carries message", &exitError{code: 2, err: fmt.Errorf("boom")}, 2},
		{"plain error", fmt.Errorf("boom"), 1},
		{"profile error", profile.ProfileError{Message: "bad"}, 2},
		{"wrapped profile error", fmt.Errorf("loading profiles: %w", profile.ProfileError{Message: "bad"}), 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCodeFor(tt.err); got != tt.want {
				t.Errorf("exitCodeFor(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestShowDockerPrintsSensitiveAdvisory(t *testing.T) {
	out, err := runTpd(t, "show", "docker")
	if err != nil {
		t.Fatalf("show docker: %v\n%s", err, out)
	}
	if !strings.Contains(out, "warning:") {
		t.Errorf("expected advisory warning on stderr, got:\n%s", out)
	}
	if !strings.Contains(out, "Docker socket") {
		t.Errorf("expected Docker socket advisory, got:\n%s", out)
	}
}

func TestEditDockerPrintsSensitiveAdvisory(t *testing.T) {
	cfg := t.TempDir()
	env := []string{
		"XDG_CONFIG_HOME=" + cfg,
		"EDITOR=" + writeEditor(t, cfg, "editor", "#!/bin/sh\nexit 0\n"),
	}
	out, err := runTpdEnv(t, env, "edit", "docker")
	if err != nil {
		t.Fatalf("edit docker: %v\n%s", err, out)
	}
	if !strings.Contains(out, "warning:") {
		t.Errorf("expected advisory warning on stderr, got:\n%s", out)
	}
	if !strings.Contains(out, "Docker socket") {
		t.Errorf("expected Docker socket advisory, got:\n%s", out)
	}
}

func TestProfileShowNonexistentExitCode(t *testing.T) {
	out, err := runTpd(t, "show", "nope")
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("show nope should fail; got err: %v\n%s", err, out)
	}
	if got := exitErr.ExitCode(); got != 2 {
		t.Errorf("show nope exit code = %d, want 2 (profile not found)\n%s", got, out)
	}
	if !strings.Contains(out, "not found") {
		t.Errorf("expected 'not found' error, got:\n%s", out)
	}
}
```

- [ ] **Step 6: Build, test, vet**

```bash
go build ./...
go vet ./...
go test ./...
```

Expected: all green. `cmd/tpd/profile_test.go` and `cmd/tpd/e2e_runtime_test.go` pass unchanged (e2e tests skip when `-short` or no docker). The kong import is gone from the whole repo.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum cmd/tpd/cli.go cmd/tpd/cli_test.go
git commit -m "feat(cli): migrate CLI from kong to cobra"
```

---

### Task 2: Native shell completion for profile and fragment names

**Files:**
- Create: `cmd/tpd/completion.go`
- Modify: `cmd/tpd/cli.go` (add `ValidArgsFunction` fields, `RegisterFlagCompletionFunc`)
- Test: `cmd/tpd/completion_test.go`

**Interfaces:**
- Consumes: `launchFlags`, `newLaunchCommand`, `newRootCommand`, `newShowCommand`, `newEditCommand`, `newInitCommand` from Task 1.
- Produces: `completeProfileNames`, `completeNames`, `filterCompletion` in package `main`. `completeProfileNames` is referenced by the launch command and root; `completeNames` by show/edit and init's `--extends` flag.

- [ ] **Step 1: Write the failing completion tests**

Create `cmd/tpd/completion_test.go`:

```go
package main

import (
	"bytes"
	"strings"
	"testing"
)

// runCompletion invokes cobra's hidden __complete command and returns the
// candidate names (directive lines stripped). XDG_CONFIG_HOME is isolated so
// only embedded built-ins contribute completions.
func runCompletion(t *testing.T, args ...string) []string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := newRootCommand()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"__complete"}, args...))
	if err := root.Execute(); err != nil {
		t.Fatalf("__complete %v: %v\n%s", args, err, errOut.String())
	}
	var names []string
	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		names = append(names, strings.SplitN(line, "\t", 2)[0])
	}
	return names
}

func containsAll(t *testing.T, got []string, want ...string) {
	t.Helper()
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("completion candidates %v missing %q", got, w)
		}
	}
}

func TestCompletionTopLevel(t *testing.T) {
	names := runCompletion(t, "")
	containsAll(t, names, "shell", "launch", "show") // built-in profile + commands
}

func TestCompletionProfilePrefix(t *testing.T) {
	names := runCompletion(t, "shel")
	if len(names) != 1 || names[0] != "shell" {
		t.Errorf("expected only 'shell', got %v", names)
	}
}

func TestCompletionLaunch(t *testing.T) {
	containsAll(t, runCompletion(t, "launch", ""), "shell")
}

func TestCompletionShow(t *testing.T) {
	// show accepts profiles and fragments.
	containsAll(t, runCompletion(t, "show", ""), "shell", "docker")
}

func TestCompletionShowPrefix(t *testing.T) {
	names := runCompletion(t, "show", "doc")
	containsAll(t, names, "docker")
}

func TestCompletionPassthroughAfterProfile(t *testing.T) {
	// Everything after the profile name is passthrough: nothing to complete.
	if names := runCompletion(t, "shell", ""); len(names) != 0 {
		t.Errorf("expected no completions after profile name, got %v", names)
	}
}

func TestCompletionInitExtends(t *testing.T) {
	containsAll(t, runCompletion(t, "init", "--extends", ""), "shell", "docker")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./cmd/tpd/ -run TestCompletion -v
```

Expected: FAIL. Without a `ValidArgsFunction`, `tpd __complete` returns only subcommand names, so `"shell"` is missing from the candidates and the prefix tests error.

- [ ] **Step 3: Create `cmd/tpd/completion.go`**

```go
package main

import (
	"strings"

	"github.com/jgillich/tpd/internal/profile"
	"github.com/spf13/cobra"
)

// loadCatalog loads the merged catalog tolerantly: a malformed user file must
// not break completion, only hide that profile's completions. Commands that
// must surface catalog errors (show, edit, list) load strictly themselves.
func loadCatalog() (profile.Catalog, error) {
	return profile.LoadProfilesTolerant(profile.DefaultProfileDir(), func(string) {})
}

// completeProfileNames completes profile names for the launch commands. Once
// the profile name is given, everything after it is passthrough to the
// profile's command, so there is nothing left for tpd to complete.
func completeProfileNames(c *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveDefault
	}
	cat, err := loadCatalog()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return filterCompletion(cat.ProfileNames(), toComplete)
}

// completeNames completes profile and fragment names for commands that accept
// either (show, edit, init --extends).
func completeNames(c *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cat, err := loadCatalog()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return filterCompletion(cat.Names(), toComplete)
}

func filterCompletion(names []string, prefix string) ([]string, cobra.ShellCompDirective) {
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
```

- [ ] **Step 4: Wire the completion functions in `cmd/tpd/cli.go`**

In `newLaunchCommand` (Task 1), add `ValidArgsFunction: completeProfileNames,` to the `cobra.Command{...}` literal, right after `Args:`:

```go
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeProfileNames,
```

In `newRootCommand` (Task 1), add the same line to the root `cobra.Command{...}` literal:

```go
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeProfileNames,
```

In `newShowCommand` and `newEditCommand`, add `ValidArgsFunction: completeNames,` after the `Args:` line in each literal.

In `newInitCommand`, after the three `cmd.Flags()...` lines, add:

```go
	cmd.RegisterFlagCompletionFunc("extends", completeNames)
```

- [ ] **Step 5: Run the completion tests**

```bash
go test ./cmd/tpd/ -run TestCompletion -v
```

Expected: all PASS.

- [ ] **Step 6: Full suite + commit**

```bash
go build ./...
go vet ./...
go test ./...
```

```bash
git add cmd/tpd/completion.go cmd/tpd/completion_test.go cmd/tpd/cli.go
git commit -m "feat(cli): add native shell completion for profiles and fragments"
```

---

### Task 3: Verification and docs

**Files:**
- Modify: `AGENTS.md` (CLI wiring note)
- Test: whole repo

- [ ] **Step 1: Manually verify the new behaviors**

```bash
go build -o /tmp/tpd ./cmd/tpd
/tmp/tpd                                   # full help, exit 0
/tmp/tpd --help                           # same
/tmp/tpd shell --dry-run                  # prints resolved spec, exit 0
/tmp/tpd --dry-run shell                  # flags before profile also work
/tmp/tpd --dry-run nope                   # "profile not found", exit 2
/tmp/tpd completion bash | head -3        # cobra completion script
/tmp/tpd completion zsh | head -3
/tmp/tpd completion fish | head -3
/tmp/tpd __complete ""                    # commands + profile names
/tmp/tpd __complete show ""               # profiles + fragments
/tmp/tpd __complete shell ""              # nothing (passthrough)
```

Expected: help lists all commands and flags; `--dry-run` prints a spec without launching; unknown profile exits 2; the completion script and `__complete` output match the probe behavior described in the plan preamble.

- [ ] **Step 2: Run the full test suite**

```bash
go test ./...
go vet ./...
```

Expected: all green (e2e tests skip without docker; if docker is available they run and pass, including `TestE2EShellLaunch` which exercises `tpd -c "echo hello-from-tpd" shell`).

- [ ] **Step 3: Update the AGENTS.md CLI note**

In `AGENTS.md`, replace line 10:

> CLI is wired with [kong](https://github.com/alecthomas/kong); commands live in `cmd/tpd/cli.go`. `LaunchCmd.ProfileAndArgs` uses `passthrough:"partial"` so flags after the profile name reach the profile's command verbatim.

with:

> CLI is wired with [cobra](https://github.com/spf13/cobra); commands live in `cmd/tpd/cli.go`. The launch command (root and `tpd launch`) disables interspersed flag parsing (`SetInterspersed(false)`), so flags parse only before the profile name and everything after it reaches the profile's command verbatim. `cmd/tpd/completion.go` provides native shell completion for profile/fragment names via `ValidArgsFunction`; `tpd completion bash|zsh|fish|powershell` prints the activation script.

- [ ] **Step 4: Commit**

```bash
git add AGENTS.md
git commit -m "docs: update CLI notes for the cobra migration"
```

---

## Self-Review

**Spec coverage:** The spec is: switch to cobra natively; preserve all CLI behavior; completion for profiles/fragments. Task 1 covers the migration and behavior parity (every command, flag, exit code; verified by the untouched `profile_test.go`/`e2e_runtime_test.go` plus updated launch/help tests). Task 2 covers completion for profiles (launch/root), profiles+fragments (show/edit, `init --extends`), and the passthrough-empty case. Task 3 covers activation scripts and docs.

**Placeholder scan:** Every step has concrete code or an exact run command. No TBDs, no "add error handling" stubs.

**Type consistency:** `launchFlags` fields (`Command`, `Workspace`, `DryRun`, `Verbose`, `Pull`) match the `tpd.LaunchOpts` fields they feed; `completeProfileNames`/`completeNames`/`filterCompletion` signatures are identical across Task 2 steps; `runShow`/`runEdit`/`runList` names match their call sites in the command builders. The `ValidArgsFunction` signature matches cobra's `func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective)`.

**Known, accepted behavior deltas** (all verified against probes during planning; none change realistic invocations):
- `tpd --dry-run show foo` (a tpd flag *before* a real command) now dispatches the `show` command instead of launching a profile named `show`; the realistic forms `tpd --dry-run shell` and `tpd show --resolved foo` are unchanged.
- Help text format changes from kong to cobra; `TestLaunchBareShowsHelp`/`TestBareHelpShowsAllCommands` assertions were updated accordingly (the obsolete `profile-and-args` token is gone).
- `tpd <profile>` completions come from the catalog's `ProfileNames()` rather than command registration; a profile named after a real command (e.g. `show`) is dispatched to the command, matching current kong behavior.
