package scaffold

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/jgillich/tpd/internal/profile"
	"github.com/jgillich/tpd/internal/ui"
	"gopkg.in/yaml.v3"
)

type Options struct {
	Name        string
	Extends     []string
	Force       bool
	Merge       bool
	DryRun      bool
	Interactive bool
	ProfileDir  string
}

// newProfileOption is the wizard picker entry for creating a brand-new
// profile instead of shadowing a built-in.
const newProfileOption = "New"

// loadCatalog loads the profile catalog Run operates on. Production uses the
// embedded catalog; tests override it to run against a stable fixture so the
// wizard never depends on the live catalog contents.
var loadCatalog = profile.LoadProfiles

// ttyInteractive reports whether the huh/bubbletea UI is safe to launch: both
// stdin and stdout must be terminals, since huh renders to stdout as well as
// reading stdin.
func ttyInteractive(stdin io.Reader, stdout io.Writer) bool {
	return ui.IsTTYReader(stdin) && ui.IsTTY(stdout)
}

func Run(ctx context.Context, opts Options, stdin io.Reader, stdout, stderr io.Writer) error {
	if opts.Force && opts.Merge {
		return fmt.Errorf("--force and --merge are mutually exclusive")
	}
	userDir := opts.ProfileDir
	if userDir == "" {
		userDir = profile.DefaultProfileDir()
	}
	if userDir == "" {
		return fmt.Errorf("cannot determine profile directory (set XDG_CONFIG_HOME)")
	}

	// Determine if interactive. The CLI sets opts.Interactive via IsTTY;
	// tests set it directly so the wizard is exercisable with strings.NewReader.
	interactive := opts.Interactive

	// tty reports whether the huh/bubbletea UI is safe to launch. When true, we
	// use charmbracelet/huh for interactive TUI prompts; otherwise we fall back
	// to simple text prompts so tests with strings.NewReader still work. Both
	// stdin and stdout must be terminals: huh renders to stdout as well as
	// reading stdin, so a redirected stdout misbehaves.
	tty := ttyInteractive(stdin, stdout)

	// wizardUsed tracks whether interactive prompts for profile/fragments were
	// actually shown. When the user provides all args explicitly (even in a
	// TTY), we skip the overwrite prompt to avoid surprising prompts.
	wizardUsed := false

	// Shared buffered reader for interactive prompts. Creating separate
	// bufio.NewReader values per prompt would lose buffered data from the
	// same underlying stdin.
	reader := bufio.NewReader(stdin)

	// Full catalog (built-ins + user profiles/fragments) for extends
	// validation and as wizard base options.
	cat, err := loadCatalog(userDir)
	if err != nil {
		return fmt.Errorf("loading profiles: %w", err)
	}

	// Built-in-only catalog for the profile picker (extending a built-in).
	builtinCat, err := loadCatalog("")
	if err != nil {
		return fmt.Errorf("loading built-in profiles: %w", err)
	}
	builtinProfiles := builtinCat.ProfileDisplayNames()
	if len(builtinProfiles) == 0 {
		return fmt.Errorf("no built-in profiles available")
	}

	// All extendable display names for the wizard base picker: profiles
	// (built-in + user) and fragments. "mise" is listed first so it reads as
	// the default.
	baseNames := dedup(append([]string{"mise"}, cat.ProfileDisplayNames()...))

	bases := opts.Extends
	profileName := opts.Name

	if profileName == "" {
		if !interactive {
			return fmt.Errorf("profile name is required")
		}
		if tty {
			selection, err := promptProfileHuh(builtinProfiles, catalogDescriptions(builtinCat, builtinProfiles), stdin, stdout)
			if err != nil {
				return err
			}
			if selection == newProfileOption {
				profileName, bases, err = promptNewProfileHuh(baseNames, catalogDescriptions(cat, baseNames), stdin, stdout)
				if err != nil {
					return err
				}
			} else {
				profileName = selection
			}
		} else {
			selection := promptProfile(builtinProfiles, reader, stderr)
			if selection == newProfileOption {
				profileName, bases, err = promptNewProfile(baseNames, reader, stderr)
				if err != nil {
					return err
				}
			} else {
				profileName = selection
			}
		}
		wizardUsed = true
		if profileName == "" {
			return fmt.Errorf("profile name is required")
		}
		// Bases from the wizard are display names; resolve to canonical.
		canonicalBases := make([]string, 0, len(bases))
		for _, dn := range bases {
			ref, err := profile.ParseRef(dn, cat.Namespaces())
			if err != nil {
				return err
			}
			key, ok := cat.ResolveRef(ref)
			if !ok {
				return fmt.Errorf("unknown base: %s", dn)
			}
			canonicalBases = append(canonicalBases, key)
		}
		bases = canonicalBases
	}

	if err := profile.ValidateName(profileName); err != nil {
		return err
	}
	if key, ok := resolveCatalogName(cat, profileName); ok && cat.IsFragment(key) {
		return fmt.Errorf("profile name %q collides with an existing fragment", profileName)
	}

	// Resolve bases: explicit --extends, wizard choice, or default. When
	// --extends names only fragments (or nothing), fall back to the default
	// base — the built-in of the same name (shadow) or the shared mise base —
	// so fragments stay additions to a base, not replacements.
	hasProfile := false
	for _, b := range bases {
		ref, err := profile.ParseRef(b, cat.Namespaces())
		if err != nil {
			// Defer the error to the validation loop below.
			continue
		}
		key, ok := cat.ResolveRef(ref)
		if ok && !cat.IsFragment(key) {
			hasProfile = true
			break
		}
	}
	if !hasProfile {
		if _, ok := cat.Get("core/" + profileName); ok {
			bases = append([]string{"core/" + profileName}, bases...)
		} else {
			bases = append([]string{"core/mise"}, bases...)
		}
	}

	// Interactively pick extra fragments when the user gave no --extends.
	// Picked fragments are appended to the same extends list as bases, mapped
	// from their display names to canonical FullNames.
	if interactive && len(opts.Extends) == 0 {
		fragNames := cat.FragmentDisplayNames()
		var picked []string
		if tty {
			p, err := promptFragmentsBrowserHuh(fragNames, catalogDescriptions(cat, fragNames), stdin, stdout)
			if err != nil {
				return err
			}
			picked = p
		} else {
			picked = promptFragments(fragNames, reader, stderr)
		}
		for _, dn := range picked {
			full, ok := cat.FragmentByDisplayName(dn)
			if !ok {
				return fmt.Errorf("unknown fragment: %s", dn)
			}
			bases = append(bases, full)
		}
		wizardUsed = true
	}

	for i, b := range bases {
		ref, err := profile.ParseRef(b, cat.Namespaces())
		if err != nil {
			return fmt.Errorf("invalid extends target %q: %w", b, err)
		}
		key, ok := cat.ResolveRef(ref)
		if !ok {
			return fmt.Errorf("unknown extends target: %s", b)
		}
		bases[i] = key
	}
	bases = dedup(bases)

	content, err := generate(profileName, bases, cat)
	if err != nil {
		return fmt.Errorf("generating profile: %w", err)
	}

	targetPath := filepath.Join(userDir, profileName+".yaml")

	// Dry-run without merge mirrors the pre-merge behavior: print the generated
	// content without touching the filesystem or checking for an existing file.
	if opts.DryRun && !opts.Merge {
		if err := validateGenerated(content, profileName, targetPath, cat, stderr); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "# dry-run: would write %s\n", targetPath)
		fmt.Fprint(stdout, content)
		return nil
	}

	action, err := overwriteAction(tty, opts, wizardUsed, targetPath, stdin, stdout, reader)
	if err != nil {
		return err
	}
	if action == writeAbort {
		fmt.Fprintf(stdout, "skipped %s\n", targetPath)
		return nil
	}
	if action == writeMerge {
		existing, err := os.ReadFile(targetPath)
		if err != nil {
			return fmt.Errorf("reading %s: %w", targetPath, err)
		}
		content, err = mergeYAML(existing, []byte(content))
		if err != nil {
			return fmt.Errorf("merging %s: %w", targetPath, err)
		}
	}
	if err := validateGenerated(content, profileName, targetPath, cat, stderr); err != nil {
		return err
	}

	if opts.DryRun {
		fmt.Fprintf(stdout, "# dry-run: would write %s\n", targetPath)
		fmt.Fprint(stdout, content)
		return nil
	}

	if err := writeFileAtomic(targetPath, []byte(content), 0o644); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "created %s\n", targetPath)
	return nil
}

// writeFileAtomic writes data to path via a same-directory temp file, fsync,
// and rename so a crash or concurrent reader never observes a partial profile.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating profile directory: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), "tpd-*.tmp")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	tmp := f.Name()
	renamed := false
	defer func() {
		if !renamed {
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	renamed = true
	return nil
}

// writeAction is what init does with an existing target file.
type writeAction int

const (
	writeReplace writeAction = iota
	writeMerge
	writeAbort
)

// overwriteAction decides how to treat an existing target: --force replaces,
// --merge merges, an interactive wizard asks, and anything else fails. A
// missing target always means replace (plain generation).
func overwriteAction(tty bool, opts Options, wizardUsed bool, targetPath string, stdin io.Reader, stdout io.Writer, reader *bufio.Reader) (writeAction, error) {
	if _, err := os.Stat(targetPath); err != nil {
		if os.IsNotExist(err) {
			return writeReplace, nil
		}
		return writeAbort, err
	}
	switch {
	case opts.Merge:
		return writeMerge, nil
	case opts.Force:
		return writeReplace, nil
	case wizardUsed:
		return promptOverwrite(tty, targetPath, stdin, stdout, reader)
	default:
		return writeAbort, fmt.Errorf("%s already exists (use --force to overwrite or --merge to merge)", targetPath)
	}
}

// validateGenerated resolves the content init is about to write. A brand-new
// profile without a command/image is written anyway with a warning — the user
// is expected to edit it before launching.
func validateGenerated(content, profileName, targetPath string, cat profile.Catalog, stderr io.Writer) error {
	_, resolveErr := resolveGeneratedProfile(content, profileName, cat)
	if resolveErr != nil {
		if !isIncompleteProfileErr(resolveErr) {
			return fmt.Errorf("generated config failed validation: %s: %w", targetPath, resolveErr)
		}
		fmt.Fprintf(stderr, "note: %s is not runnable yet (no command or image); edit the file before launching\n", targetPath)
	}
	return nil
}

func generate(name string, extends []string, cat profile.Catalog) (string, error) {
	el := profile.ExtendsList{Raw: extends}
	if err := el.Resolve(cat.Namespaces()); err != nil {
		return "", err
	}
	p := profile.Profile{
		Version:     1,
		ExtendsList: el,
	}
	if !basesProvideCommand(cat, extends) {
		p.Command = []string{"bash"}
	}
	data, err := yaml.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// basesProvideCommand reports whether any base in the extends chain provides a
// command, so init does not generate a not-yet-runnable profile for a base
// like mise. When none does, generate defaults the command to bash.
func basesProvideCommand(cat profile.Catalog, bases []string) bool {
	for _, b := range bases {
		cfg, err := profile.ResolveProfile(cat, b)
		if err == nil && len(cfg.Command) > 0 {
			return true
		}
	}
	return false
}

// isIncompleteProfileErr reports whether err is a validation failure caused by
// a profile that is not yet runnable — missing command or image — rather than
// an actual config error. init writes these anyway with a warning so the user
// can edit the file.
func isIncompleteProfileErr(err error) bool {
	var pe profile.ProfileError
	if !errors.As(err, &pe) {
		return false
	}
	return strings.Contains(pe.Message, "missing required field: image") ||
		strings.Contains(pe.Message, "missing required field: command")
}

func dedup(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

// resolveCatalogName maps a user-supplied extends target to its canonical
// catalog key (user entry first, then core fallback).
func resolveCatalogName(cat profile.Catalog, name string) (string, bool) {
	ref, err := cat.ParseRefForCatalog(name)
	if err != nil {
		return "", false
	}
	return cat.ResolveRef(ref)
}

func promptProfile(names []string, reader *bufio.Reader, stderr io.Writer) string {
	fmt.Fprintf(stderr, "Available built-in profiles (or '%s'): %s\n", newProfileOption, strings.Join(names, ", "))
	fmt.Fprintf(stderr, "Profile: ")
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

func promptNewProfile(baseNames []string, reader *bufio.Reader, stderr io.Writer) (string, []string, error) {
	fmt.Fprintf(stderr, "New profile name: ")
	line, _ := reader.ReadString('\n')
	name := strings.TrimSpace(line)
	if name == "" {
		return "", nil, fmt.Errorf("profile name is required")
	}
	fmt.Fprintf(stderr, "Extend (%s) [mise]: ", strings.Join(baseNames, ", "))
	line, _ = reader.ReadString('\n')
	line = strings.TrimSpace(line)
	var bases []string
	if line != "" {
		bases = dedup(strings.Split(line, ","))
	}
	if len(bases) == 0 {
		bases = []string{"mise"}
	}
	return name, bases, nil
}

func promptFragments(names []string, reader *bufio.Reader, stderr io.Writer) []string {
	fmt.Fprintf(stderr, "Fragments (%s) [none]: ", strings.Join(names, ", "))
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	return strings.Split(line, ",")
}

// catalogDescriptions builds a display-name → description map for wizard
// options. Entries without a description are absent, so labels stay bare.
func catalogDescriptions(cat profile.Catalog, names []string) map[string]string {
	descs := map[string]string{}
	for _, dn := range names {
		if d := cat.Description(dn); d != "" {
			descs[dn] = d
		}
	}
	return descs
}

// metaLabel renders a wizard option label. huh (v1.0.0 and upstream) has no
// per-option descriptions, so the description rides in the label; the / filter
// then searches it too.
func metaLabel(name string, descs map[string]string) string {
	if d, ok := descs[name]; ok && d != "" {
		return name + " — " + d
	}
	return name
}

func promptProfileHuh(names []string, descs map[string]string, stdin io.Reader, stdout io.Writer) (string, error) {
	var selected string
	opts := []huh.Option[string]{huh.NewOption(newProfileOption, newProfileOption)}
	for _, n := range names {
		opts = append(opts, huh.NewOption(metaLabel(n, descs), n))
	}
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select a built-in profile").
				Options(opts...).
				Value(&selected),
		),
	).WithInput(stdin).WithOutput(stdout)
	if err := form.Run(); err != nil {
		return "", err
	}
	return selected, nil
}

func promptNewProfileHuh(baseNames []string, descs map[string]string, stdin io.Reader, stdout io.Writer) (string, []string, error) {
	var name string
	var base string
	opts := make([]huh.Option[string], len(baseNames))
	for i, n := range baseNames {
		opts[i] = huh.NewOption(metaLabel(n, descs), n)
	}
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("New profile name").
				Value(&name),
		),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Extend a base profile").
				Options(opts...).
				Value(&base),
		),
	).WithInput(stdin).WithOutput(stdout)
	if err := form.Run(); err != nil {
		return "", nil, err
	}
	if base == "" {
		base = "mise"
	}
	return name, []string{base}, nil
}

func promptOverwrite(tty bool, targetPath string, stdin io.Reader, stdout io.Writer, reader *bufio.Reader) (writeAction, error) {
	if tty {
		// Abort is the safe default: a stray Enter never clobbers or mutates an
		// existing file.
		choice := "abort"
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title(targetPath+" already exists.").
					Options(
						huh.NewOption("Overwrite", "overwrite"),
						huh.NewOption("Merge", "merge"),
						huh.NewOption("Abort", "abort"),
					).
					Value(&choice),
			),
		).WithInput(stdin).WithOutput(stdout)
		if err := form.Run(); err != nil {
			return writeAbort, err
		}
		switch choice {
		case "overwrite":
			return writeReplace, nil
		case "merge":
			return writeMerge, nil
		default:
			return writeAbort, nil
		}
	}
	fmt.Fprintf(stdout, "%s already exists. Overwrite, Merge, or Abort? [a]: ", targetPath)
	line, _ := reader.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "o", "overwrite":
		return writeReplace, nil
	case "m", "merge":
		return writeMerge, nil
	default:
		return writeAbort, nil
	}
}

func resolveGeneratedProfile(content, profileName string, cat profile.Catalog) (profile.Profile, error) {
	rc, err := profile.ParseRaw([]byte(content), "generated:"+profileName)
	if err != nil {
		return profile.Profile{}, err
	}
	// Resolve against a copy so validating generated content never mutates the
	// caller's catalog.
	tmp := cat.Clone()
	tmp.AddRaw("", profileName, rc)
	return profile.ResolveProfile(tmp, profileName)
}
