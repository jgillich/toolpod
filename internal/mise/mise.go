package mise

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// ProgressWriter reports progress during Prepare/Run.
type ProgressWriter interface {
	WriteProgress(line string)
}

// ActivateCommand returns the shell preamble that:
//  1. Writes the profile's tools into mise's global config at configDir
//     (ephemeral — lives in the container's own filesystem, NOT the shared
//     volume). configDir must match MISE_CONFIG_DIR set in the container env,
//     otherwise mise will not read the written config.
//  2. Activates mise so shims are on PATH.
//
// When the user cd's into the workspace, mise's directory walk picks up any
// project-local .tool-versions / mise.toml and overrides these defaults.
func ActivateCommand(configDir string, tools map[string]string) string {
	configFile := filepath.Join(configDir, "config.toml")

	if len(tools) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "mkdir -p %s && printf '%%s' '", shq(configDir))
	b.WriteString("[tools]\n")

	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&b, "%q = \"%s\"\n", name, tools[name])
	}
	b.WriteString("' > ")
	b.WriteString(shq(configFile))
	return b.String()
}

// NeedsEmbeddedPlugin reports whether tools reference a tool backed by an
// embedded mise plugin (currently the generic appimage backend). When true,
// PluginInstallCommand must run before `mise install` so the prefixed tools
// can resolve.
func NeedsEmbeddedPlugin(tools map[string]string) bool {
	for name := range tools {
		if strings.HasPrefix(name, appimageBackendPrefix) {
			return true
		}
	}
	return false
}

// shq single-quotes s for embedding in a shell command.
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
