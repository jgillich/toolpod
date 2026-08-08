package main

import (
	"bytes"
	"fmt"
	"os"
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

func TestBareTpdWithoutProfileIsUsageError(t *testing.T) {
	bin := buildTpd(t)
	cmd := exec.Command(bin)
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("bare tpd must exit nonzero, got err: %v\n%s", err, out)
	}
	if got := exitErr.ExitCode(); got != 2 {
		t.Errorf("bare tpd exit code = %d, want 2 (usage error)\n%s", got, out)
	}
	if !strings.Contains(string(out), "profile name is required") {
		t.Errorf("expected a usage error message, got:\n%s", out)
	}
	for _, c := range []string{"Usage:", "run", "show", "profile"} {
		if !strings.Contains(string(out), c) {
			t.Errorf("expected bare tpd help to mention %q, got:\n%s", c, out)
		}
	}
	if !strings.Contains(string(out), "tpd <profile>") {
		t.Errorf("expected bare tpd help to show the tpd <profile> launch form, got:\n%s", out)
	}
}

func TestBareHelpShowsAllCommands(t *testing.T) {
	bin := buildTpd(t)
	cmd := exec.Command(bin)
	out, err := cmd.CombinedOutput()
	if _, ok := err.(*exec.ExitError); !ok {
		t.Fatalf("bare tpd must exit nonzero, got err: %v\n%s", err, out)
	}
	for _, c := range []string{"run", "init", "profile", "doctor", "prune"} {
		if !strings.Contains(string(out), c) {
			t.Errorf("expected bare tpd help to mention %q, got:\n%s", c, out)
		}
	}
}

func TestLaunchFlagsBind(t *testing.T) {
	o := &launchFlags{}
	cmd := newRunCommand(o)
	if err := cmd.Flags().Parse([]string{"--pull", "--dry-run", "--verbose", "--command", "ls", "--workspace", "/tmp"}); err != nil {
		t.Fatalf("parse launch flags: %v", err)
	}
	if !o.Pull || !o.DryRun || !o.Verbose || o.Command != "ls" || o.Workspace != "/tmp" {
		t.Errorf("launch flags not bound: %+v", o)
	}
}

func TestPruneFlagsBind(t *testing.T) {
	cmd := newPruneCommand()
	if err := cmd.Flags().Parse([]string{"--all", "--volumes", "--images", "--networks", "--force"}); err != nil {
		t.Fatalf("parse prune flags: %v", err)
	}
	for _, name := range []string{"all", "volumes", "images", "networks", "force"} {
		v, err := cmd.Flags().GetBool(name)
		if err != nil {
			t.Fatalf("get prune flag %s: %v", name, err)
		}
		if !v {
			t.Errorf("prune flag --%s not bound", name)
		}
	}
	if !strings.Contains(cmd.Short, "network") {
		t.Errorf("prune Short should mention networks, got %q", cmd.Short)
	}
}

func TestLaunchPassthroughAfterProfile(t *testing.T) {
	// kong's passthrough:partial contract: everything from the profile name
	// onward reaches the profile verbatim, even tokens that look like flags.
	o := &launchFlags{}
	cmd := newRunCommand(o)
	if err := cmd.Flags().Parse([]string{"bash", "--model", "foo", "-c", "ls"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := cmd.Flags().Args(); fmt.Sprint(got) != "[bash --model foo -c ls]" {
		t.Errorf("passthrough args = %v, want [bash --model foo -c ls]", got)
	}
	if o.Command != "" {
		t.Errorf("-c after profile must not bind to the launch flag, got %q", o.Command)
	}
}

func TestRootLaunchPassthrough(t *testing.T) {
	root := newRootCommand()
	target, args, err := root.Find([]string{"--dry-run", "bash", "--model", "foo"})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if target != root {
		t.Fatalf("expected root as dispatch target, got %q", target.Name())
	}
	if err := target.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if got := target.Flags().Args(); fmt.Sprint(got) != "[bash --model foo]" {
		t.Errorf("passthrough args = %v, want [bash --model foo]", got)
	}
	if !target.Flags().Changed("dry-run") {
		t.Error("--dry-run before the profile name must bind to the root launch flags")
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
	if !strings.Contains(string(out), "--merge") {
		t.Errorf("expected --merge in init help, got:\n%s", out)
	}
}

func TestInitMergeFlag(t *testing.T) {
	cfg := t.TempDir()
	profilesDir := filepath.Join(cfg, "tpd", "profiles")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(profilesDir, "opencode.yaml")
	if err := os.WriteFile(target, []byte("version: 1\n# my shell\ncommand: [zsh]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"XDG_CONFIG_HOME=" + cfg}
	out, err := runTpdEnv(t, env, "init", "--merge", "--extends", "toolchain/javascript", "opencode")
	if err != nil {
		t.Fatalf("init --merge: %v\n%s", err, out)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "# my shell") || !strings.Contains(content, "zsh") {
		t.Errorf("merge wiped existing content:\n%s", content)
	}
	if !strings.Contains(content, "- core/toolchain/javascript") {
		t.Errorf("merge did not add the fragment:\n%s", content)
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
		{"usage error", usageError{msg: "profile name is required"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCodeFor(tt.err); got != tt.want {
				t.Errorf("exitCodeFor(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
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

func TestYesNoMutuallyExclusive(t *testing.T) {
	cmd := newRootCommand()
	cmd.SetArgs([]string{"--yes", "--no", "bash"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for --yes and --no together")
	}
}

func TestBareRunWithoutProfileIsUsageError(t *testing.T) {
	out, err := runTpd(t, "run")
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("tpd run must exit nonzero, got err: %v\n%s", err, out)
	}
	if got := exitErr.ExitCode(); got != 2 {
		t.Errorf("tpd run exit code = %d, want 2 (usage error)\n%s", got, out)
	}
	if !strings.Contains(out, "profile name is required") {
		t.Errorf("expected a usage error message, got:\n%s", out)
	}
}

func TestRunWorkspaceMustExist(t *testing.T) {
	cfg := t.TempDir()
	seedUserConfig(t, cfg)
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	out, err := runTpdCfg(t, cfg, "run", "--dry-run", "--workspace", missing, "myapp")
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected failure for missing workspace, got err: %v\n%s", err, out)
	}
	if got := exitErr.ExitCode(); got != 2 {
		t.Errorf("missing workspace exit code = %d, want 2\n%s", got, out)
	}
	if !strings.Contains(out, missing) {
		t.Errorf("error must name the workspace path %q, got:\n%s", missing, out)
	}
}

func TestRunWorkspaceMustBeDirectory(t *testing.T) {
	cfg := t.TempDir()
	seedUserConfig(t, cfg)
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runTpdCfg(t, cfg, "run", "--dry-run", "--workspace", file, "myapp")
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected failure for file workspace, got err: %v\n%s", err, out)
	}
	if got := exitErr.ExitCode(); got != 2 {
		t.Errorf("file workspace exit code = %d, want 2\n%s", got, out)
	}
	if !strings.Contains(out, "not a directory") || !strings.Contains(out, file) {
		t.Errorf("expected a not-a-directory error naming %q, got:\n%s", file, out)
	}
}

func TestRunWorkspaceDirectoryLaunches(t *testing.T) {
	cfg := t.TempDir()
	seedUserConfig(t, cfg)
	dir := t.TempDir()
	out, err := runTpdCfg(t, cfg, "run", "--dry-run", "--workspace", dir, "myapp")
	if err != nil {
		t.Fatalf("dry-run with a real workspace should succeed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "host: "+dir) {
		t.Errorf("expected the dry-run spec to mount the workspace host path %q, got:\n%s", dir, out)
	}
}

func TestRunCommandWithProfileArgsRejected(t *testing.T) {
	cfg := t.TempDir()
	seedUserConfig(t, cfg)
	out, err := runTpdCfg(t, cfg, "run", "--dry-run", "-c", "echo hi", "myapp", "extra")
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected rejection of --command with profile args, got err: %v\n%s", err, out)
	}
	if got := exitErr.ExitCode(); got != 2 {
		t.Errorf("--command + args exit code = %d, want 2 (usage error)\n%s", got, out)
	}
	if !strings.Contains(out, "--command") {
		t.Errorf("expected the error to mention --command, got:\n%s", out)
	}
}

func TestProfileNamedRunReachableViaRunCommand(t *testing.T) {
	cfg := t.TempDir()
	seedUserConfig(t, cfg)
	if err := os.WriteFile(filepath.Join(cfg, "tpd", "profiles", "run.yaml"), []byte("version: 1\nimage: x\ncommand: [\"run\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runTpdCfg(t, cfg, "run", "--dry-run", "run")
	if err != nil {
		t.Fatalf("tpd run --dry-run run should launch the profile named run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "profile: run") {
		t.Errorf("expected the dry-run spec for the profile named run, got:\n%s", out)
	}
}

func TestBareRunRoutesToSubcommandNotProfile(t *testing.T) {
	cfg := t.TempDir()
	seedUserConfig(t, cfg)
	if err := os.WriteFile(filepath.Join(cfg, "tpd", "profiles", "run.yaml"), []byte("version: 1\nimage: x\ncommand: [\"run\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The explicit `run` subcommand wins over the profile of the same name,
	// so a bare `tpd run` is the subcommand's usage error, not the profile.
	out, err := runTpdCfg(t, cfg, "run")
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("bare tpd run must exit nonzero, got err: %v\n%s", err, out)
	}
	if got := exitErr.ExitCode(); got != 2 {
		t.Errorf("bare tpd run exit code = %d, want 2 (usage error)\n%s", got, out)
	}
	if !strings.Contains(out, "profile name is required") {
		t.Errorf("expected a usage error message, got:\n%s", out)
	}
}

func TestProfileNamedListShadowedByListCommand(t *testing.T) {
	cfg := t.TempDir()
	seedUserConfig(t, cfg)
	if err := os.WriteFile(filepath.Join(cfg, "tpd", "profiles", "list.yaml"), []byte("version: 1\nimage: x\ncommand: [\"list\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runTpdCfg(t, cfg, "list")
	if err != nil {
		t.Fatalf("tpd list should run the list subcommand: %v\n%s", err, out)
	}
	if strings.Contains(out, "profile: list") {
		t.Errorf("bare tpd list must be the list subcommand, not the profile:\n%s", out)
	}
	if !strings.Contains(out, "NAME") {
		t.Errorf("expected the list table, got:\n%s", out)
	}
	out2, err2 := runTpdCfg(t, cfg, "run", "--dry-run", "list")
	if err2 != nil {
		t.Fatalf("tpd run --dry-run list should launch the profile named list: %v\n%s", err2, out2)
	}
	if !strings.Contains(out2, "profile: list") {
		t.Errorf("expected the dry-run spec for the profile named list, got:\n%s", out2)
	}
}

func TestResolveWorkspaceValidatesFlag(t *testing.T) {
	dir := t.TempDir()
	if got, err := resolveWorkspace(dir); err != nil || got != dir {
		t.Errorf("resolveWorkspace(%q) = %q, %v; want the dir unchanged", dir, got, err)
	}
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveWorkspace(file); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("resolveWorkspace(file) = %v, want a not-a-directory error", err)
	}
	missing := filepath.Join(t.TempDir(), "nope")
	if _, err := resolveWorkspace(missing); err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("resolveWorkspace(missing) = %v, want an error naming %q", err, missing)
	}
}

func TestResolveWorkspaceGetwdFailure(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveWorkspace(""); err == nil || !strings.Contains(err.Error(), "working directory") {
		t.Fatalf("resolveWorkspace with a deleted cwd = %v, want a working-directory error", err)
	}
}
