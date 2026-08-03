package mise

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

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
func ActivateCommand(configDir string, tools map[string]Tool) string {
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
		fmt.Fprintf(&b, "%q = \"%s\"\n", name, tools[name].Version)
	}
	b.WriteString("' > ")
	b.WriteString(shq(configFile))
	return b.String()
}

// BackendRuntimesCommand returns a shell snippet that ensures the runtime for
// mise backends is present before `mise install` runs. The npm backend needs
// `node` and the pipx backend needs `uv` (or `pipx`) on PATH during install
// lifecycle scripts; without them aube fails with exit 127 (command not found).
//
// Tools whose backend is encoded in their name (npm:*, pipx:*) set the flag
// directly. Unprefixed tools resolve their backend through mise's registry
// (`mise registry <tool>`); only the primary (first) backend counts, so
// aqua-primary tools like codex or biome never trigger a node install. The
// snippet appends the runtime to the already-written [tools] config when it is
// missing.
//
// Returns "" when no runtime is needed or already present.
func BackendRuntimesCommand(configDir string, tools map[string]Tool) string {
	needNode := tools["node"].Version == ""
	needUV := tools["uv"].Version == "" && tools["pipx"].Version == ""
	if !needNode && !needUV {
		return ""
	}

	var nodePrefixed, uvPrefixed, unprefixed []string
	for name := range tools {
		switch {
		case strings.HasPrefix(name, "npm:"):
			if needNode {
				nodePrefixed = append(nodePrefixed, name)
			}
		case strings.HasPrefix(name, "pipx:"):
			if needUV {
				uvPrefixed = append(uvPrefixed, name)
			}
		case name == "node" || name == "uv" || name == "pipx":
			// Runtime tools are never npm/pipx-backed themselves.
		default:
			unprefixed = append(unprefixed, name)
		}
	}
	sort.Strings(nodePrefixed)
	sort.Strings(uvPrefixed)
	sort.Strings(unprefixed)

	nodeCandidates := len(nodePrefixed) > 0 || len(unprefixed) > 0
	uvCandidates := len(uvPrefixed) > 0 || len(unprefixed) > 0
	if !nodeCandidates && !uvCandidates {
		return ""
	}

	var b strings.Builder
	configFile := filepath.Join(configDir, "config.toml")
	// Lines are joined with "\n" and no trailing newline so callers can chain
	// the snippet with " && " (a trailing newline would orphan the next part).
	var lines []string
	lines = append(lines, "__cfg="+shq(configFile))
	if needNode {
		lines = append(lines, "__node=0")
	}
	if needUV {
		lines = append(lines, "__uv=0")
	}
	for _, name := range unprefixed {
		var line strings.Builder
		fmt.Fprintf(&line, "case \"$(mise registry %s 2>/dev/null)\" in", shq(name))
		if needNode {
			line.WriteString(" npm:*) __node=1;;")
		}
		if needUV {
			line.WriteString(" pipx:*) __uv=1;;")
		}
		line.WriteString(" esac")
		lines = append(lines, line.String())
	}
	for range nodePrefixed {
		lines = append(lines, "__node=1")
	}
	for range uvPrefixed {
		lines = append(lines, "__uv=1")
	}
	if needNode && nodeCandidates {
		lines = append(lines, `if [ "$__node" = 1 ] && ! grep -q '"node" =' "$__cfg"; then printf '"node" = "latest"\n' >> "$__cfg"; fi`)
	}
	if needUV && uvCandidates {
		lines = append(lines, `if [ "$__uv" = 1 ] && ! grep -q '"uv" =' "$__cfg"; then printf '"uv" = "latest"\n' >> "$__cfg"; fi`)
	}
	b.WriteString(strings.Join(lines, "\n"))
	return b.String()
}

// NeedsEmbeddedPlugin reports whether tools reference a tool backed by an
// embedded mise plugin (currently the generic appimage backend). When true,
// PluginInstallCommand must run before `mise install` so the prefixed tools
// can resolve.
func NeedsEmbeddedPlugin(tools map[string]Tool) bool {
	for name := range tools {
		if strings.HasPrefix(name, appimageBackendPrefix) {
			return true
		}
	}
	return false
}

func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
