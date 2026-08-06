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
	DryRun      bool
	Interactive bool
	ProfileDir  string
}

// newProfileOption is the wizard picker entry for creating a brand-new
// profile instead of shadowing a built-in.
const newProfileOption = "New"

func Run(ctx context.Context, opts Options, stdin io.Reader, stdout, stderr io.Writer) error {
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

	// tty reports whether stdin is an actual terminal. When true, we use
	// charmbracelet/huh for interactive TUI prompts; otherwise we fall back
	// to simple text prompts so tests with strings.NewReader still work.
	tty := ui.IsTTYReader(stdin)

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
	cat, err := profile.LoadProfiles(userDir)
	if err != nil {
		return fmt.Errorf("loading profiles: %w", err)
	}

	// Built-in-only catalog for the profile picker (extending a built-in).
	builtinCat, err := profile.LoadProfiles("")
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
			selection, err := promptProfileHuh(builtinProfiles, stdin, stdout)
			if err != nil {
				return err
			}
			if selection == newProfileOption {
				profileName, bases, err = promptNewProfileHuh(baseNames, stdin, stdout)
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
		var picked []string
		if tty {
			p, err := promptFragmentsHuh(FragmentNames(), stdin, stdout)
			if err != nil {
				return err
			}
			picked = p
		} else {
			picked = promptFragments(FragmentNames(), reader, stderr)
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

	// Resolve the generated profile to generate a summary and validate it.
	// A brand-new profile without a command/image is written anyway with a
	// warning — the user is expected to edit it before launching.
	resolved, resolveErr := resolveGeneratedProfile(content, profileName, cat)
	if resolveErr != nil {
		if !isIncompleteProfileErr(resolveErr) {
			return fmt.Errorf("generated config failed validation: %s: %w", targetPath, resolveErr)
		}
		fmt.Fprintf(stderr, "note: %s is not runnable yet (no command or image); edit the file before launching\n", targetPath)
	} else {
		// Embed the resolved profile as a comment so the file shows the full
		// container view, mirroring the edit command's seed.
		content, err = appendResolvedReference(content, profileName, resolved)
		if err != nil {
			return fmt.Errorf("appending resolved reference: %w", err)
		}
	}

	if opts.DryRun {
		fmt.Fprintf(stdout, "# dry-run: would write %s\n", targetPath)
		fmt.Fprint(stdout, content)
		return nil
	}

	if _, err := os.Stat(targetPath); err == nil {
		needPrompt := false
		if !opts.Force {
			if !wizardUsed {
				return fmt.Errorf("%s already exists (use --force to overwrite)", targetPath)
			}
			needPrompt = true
		}

		if needPrompt {
			confirmed, err := promptOverwrite(tty, targetPath, stdin, stdout, reader)
			if err != nil {
				return err
			}
			if !confirmed {
				fmt.Fprintf(stdout, "skipped %s\n", targetPath)
				return nil
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
		return fmt.Errorf("creating profile directory: %w", err)
	}

	if err := os.WriteFile(targetPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", targetPath, err)
	}

	fmt.Fprintf(stdout, "created %s\n", targetPath)
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

// appendResolvedReference appends the fully merged profile to the generated
// content as a commented block, mirroring the edit command's seed, so the file
// shows the effective container view next to the live extends override.
func appendResolvedReference(content, profileName string, resolved profile.Profile) (string, error) {
	data, err := yaml.Marshal(resolved)
	if err != nil {
		return "", err
	}
	const rule = "# ──────────────────────────────────────────────────────────────────\n"
	var b strings.Builder
	b.WriteString(content)
	b.WriteString("\n")
	b.WriteString(rule)
	b.WriteString("# Resolved profile (reference) — snapshot from when this file was\n")
	b.WriteString("# generated; the built-ins may have changed since. Run\n")
	fmt.Fprintf(&b, "# `tpd show --resolved %s` for the current resolved profile.\n", profileName)
	b.WriteString(rule)
	b.WriteString("\n")
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		b.WriteString("# ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String(), nil
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
	return strings.Contains(pe.Message, "missing required field: command") ||
		strings.Contains(pe.Message, "neither set")
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

func promptProfileHuh(names []string, stdin io.Reader, stdout io.Writer) (string, error) {
	var selected string
	opts := []huh.Option[string]{huh.NewOption(newProfileOption, newProfileOption)}
	for _, n := range names {
		opts = append(opts, huh.NewOption(n, n))
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

func promptNewProfileHuh(baseNames []string, stdin io.Reader, stdout io.Writer) (string, []string, error) {
	var name string
	var base string
	opts := make([]huh.Option[string], len(baseNames))
	for i, n := range baseNames {
		opts[i] = huh.NewOption(n, n)
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

func promptFragmentsHuh(names []string, stdin io.Reader, stdout io.Writer) ([]string, error) {
	var selected []string
	opts := make([]huh.Option[string], len(names))
	for i, n := range names {
		opts[i] = huh.NewOption(n, n)
	}
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Select fragments (space to toggle, enter to confirm)").
				Options(opts...).
				Value(&selected),
		),
	).WithInput(stdin).WithOutput(stdout)
	if err := form.Run(); err != nil {
		return nil, err
	}
	return selected, nil
}

func promptConfirm(tty bool, title string, stdin io.Reader, stdout io.Writer, reader *bufio.Reader) (bool, error) {
	if tty {
		var confirmed bool
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title(title).
					Affirmative("Yes").
					Negative("No").
					Value(&confirmed),
			),
		).WithInput(stdin).WithOutput(stdout)
		if err := form.Run(); err != nil {
			return false, err
		}
		return confirmed, nil
	}
	fmt.Fprintf(stdout, "%s [y/N]: ", title)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes", nil
}

func promptOverwrite(tty bool, targetPath string, stdin io.Reader, stdout io.Writer, reader *bufio.Reader) (bool, error) {
	return promptConfirm(tty, fmt.Sprintf("%s already exists. Overwrite?", targetPath), stdin, stdout, reader)
}

func resolveGeneratedProfile(content, profileName string, cat profile.Catalog) (profile.Profile, error) {
	rc, err := profile.ParseRaw([]byte(content), "generated:"+profileName)
	if err != nil {
		return profile.Profile{}, err
	}
	cat.AddRaw("", profileName, rc)
	return profile.ResolveProfile(cat, profileName)
}
