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
