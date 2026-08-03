package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/jgillich/tpod/internal/workspace"
)

// tmplData is the execution context for path templates. .Env exposes the
// host environment as a map, enabling {{ or .Env.FOO "fallback" }} in mount
// sources/targets. .UID exposes the host user ID. .Ports exposes
// container-port → host-port mappings for rendering {{ index .Ports "8080" }}.
type tmplData struct {
	Env   map[string]string
	UID   string
	Ports map[string]string
}

func expandEnvMap() map[string]string {
	out := make(map[string]string)
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}

func currentUID() string {
	return strconv.Itoa(os.Getuid())
}

// renderTemplate compiles and executes s as a text/template against the host
// environment. Strings without {{ }} delimiters pass through unchanged.
// A small func map provides helpers useful for path expressions:
//
//	{{ or .Env.FOO "fallback" }}                   — first non-empty value
//	{{ trimPrefix .Env.DOCKER_HOST "unix://" }}    — strip a leading prefix
//	{{ uid }}                                      — host user ID (e.g. "1000")
func renderTemplate(s string, data tmplData) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	tmpl, err := template.New("path").Funcs(template.FuncMap{
		"trimPrefix": strings.TrimPrefix,
		"uid":        currentUID,
	}).Option("missingkey=zero").Parse(s)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}

// ResolveTildes expands leading ~/ on mount sources (→ hostHome) and
// mount/cache targets (→ runtimeHome) per spec §5.6, then renders
// {{ }} text/template expressions against the host environment. Files
// targets expand ~ (→ runtimeHome) too, and each File.Content is rendered
// as a template. Absolute paths are left as-is. The mode is informational
// only here; the caller has already determined runtimeHome based on the mode.
func ResolveTildes(cfg Profile, mode workspace.Mode, hostHome, runtimeHome string, ports map[string]string) (Profile, error) {
	out := cfg
	data := tmplData{Env: expandEnvMap(), UID: currentUID(), Ports: ports}

	if out.Mounts != nil {
		expanded := make(map[string]Mount, len(out.Mounts))
		for target, m := range out.Mounts {
			newTarget, err := expandTarget(target, runtimeHome, data)
			if err != nil {
				return out, err
			}
			m.Source, err = expandSource(m.Source, hostHome, data)
			if err != nil {
				return out, err
			}
			// An empty rendered path is a config error (a bind mount with no
			// source or target fails confusingly later); optional mounts
			// degrade to absent.
			if newTarget == "" || m.Source == "" {
				if m.Optional {
					continue
				}
				return out, fmt.Errorf("mount %q resolved to an empty path after template expansion (is the host variable set?)", target)
			}
			expanded[newTarget] = m
		}
		out.Mounts = expanded
	}

	if out.Caches != nil {
		expanded := make(map[string]CachePaths, len(out.Caches))
		for name, paths := range out.Caches {
			var exps CachePaths
			for _, p := range paths {
				e, err := expandTarget(p, runtimeHome, data)
				if err != nil {
					return out, err
				}
				if e == "" {
					return out, fmt.Errorf("cache %s resolved to an empty target after template expansion (is the host variable set?)", name)
				}
				exps = append(exps, e)
			}
			expanded[name] = exps
		}
		out.Caches = expanded
	}

	if out.Files != nil {
		expanded := make(map[string]File, len(out.Files))
		for target, f := range out.Files {
			newTarget, err := expandTarget(target, runtimeHome, data)
			if err != nil {
				return out, err
			}
			if newTarget == "" {
				return out, fmt.Errorf("file %q resolved to an empty target after template expansion (is the host variable set?)", target)
			}
			// Template expansion can inject ".." segments that validateFiles
			// never saw (e.g. {{ .Env.X }} with X=/..); reject them here so the
			// resolved target is a clean path. Clean then normalizes //, /./,
			// and trailing-slash noise; it cannot introduce "..".
			for _, seg := range strings.Split(newTarget, "/") {
				if seg == ".." {
					return out, fmt.Errorf("file %q resolved to path %q containing '..' after template expansion", target, newTarget)
				}
			}
			newTarget = filepath.Clean(newTarget)
			f.Content, err = renderTemplate(f.Content, data)
			if err != nil {
				return out, fmt.Errorf("file %s: %w", target, err)
			}
			expanded[newTarget] = f
		}
		out.Files = expanded
	}

	if out.Env != nil {
		for k, v := range out.Env {
			if !strings.HasPrefix(v, "{{") {
				continue
			}
			rendered, err := renderTemplate(v, data)
			if err != nil {
				return out, fmt.Errorf("environment %s: %w", k, err)
			}
			out.Env[k] = rendered
		}
	}

	if out.Command != nil {
		rendered, err := renderArgs(out.Command, data)
		if err != nil {
			return out, fmt.Errorf("command: %w", err)
		}
		out.Command = rendered
	}

	return out, nil
}

// renderArgs renders args that start with "{{" as templates; all other
// args pass through literally (so shell snippets with literal braces work).
func renderArgs(args []string, data tmplData) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.HasPrefix(a, "{{") {
			rendered, err := renderTemplate(a, data)
			if err != nil {
				return nil, fmt.Errorf("arg %d: %w", i, err)
			}
			out[i] = rendered
			continue
		}
		out[i] = a
	}
	return out, nil
}

func expandTarget(path, runtimeHome string, data tmplData) (string, error) {
	path = expandTilde(path, runtimeHome)
	return renderTemplate(path, data)
}

func expandSource(path, hostHome string, data tmplData) (string, error) {
	path = expandTilde(path, hostHome)
	return renderTemplate(path, data)
}

func expandTilde(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return home + path[1:]
	}
	return path
}
