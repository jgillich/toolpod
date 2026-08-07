package main

import (
	"strings"

	"github.com/jgillich/tpd/internal/profile"
	"github.com/spf13/cobra"
)

// loadCatalog loads the merged catalog tolerantly: a malformed user file must
// not break completion, only hide that profile's completions. Commands that
// must surface catalog errors (show, list) load strictly themselves; edit
// loads tolerantly too so a broken file can still be opened for repair.
func loadCatalog() (profile.Catalog, error) {
	return profile.LoadProfilesTolerant(profile.DefaultProfileDir(), func(string) {})
}

// completeProfileNames completes profile names for the launch commands (bare
// form and `tpd run`). Once the profile name is given, everything after it is
// passthrough to the profile's command: tpd offers no more names but keeps
// file completion (ShellCompDirectiveDefault) so the shell completes the
// contained command's own file arguments.
func completeProfileNames(c *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveDefault
	}
	cat, err := loadCatalog()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return filterCompletion(cat.ProfileDisplayNames(), toComplete)
}

// completeNames completes profile and fragment names for commands that accept
// either (show, edit, init --extends).
func completeNames(c *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cat, err := loadCatalog()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return filterCompletion(cat.DisplayNames(), toComplete)
}

// completeNamesOnce completes profile and fragment names for single-positional
// commands (show, edit): nothing once the name is given — the command takes no
// more positionals, so file completion would only offer invalid args.
func completeNamesOnce(c *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeNames(c, args, toComplete)
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
