package profile

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/template"
)

// tmplData is the execution context for path templates. .Env exposes the
// host environment as a map, enabling {{ or .Env.FOO "fallback" }} in mount
// sources/targets. .UID exposes the host user ID.
type tmplData struct {
	Env map[string]string
	UID string
}

// expandEnvMap builds a map[string]string from os.Environ for template execution.
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
func renderTemplate(s string, env map[string]string) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	tmpl, err := template.New("path").Funcs(template.FuncMap{
		"trimPrefix": strings.TrimPrefix,
		"uid":        currentUID,
	}).Parse(s)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, tmplData{Env: env, UID: currentUID()}); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}

// ResolveTildes expands leading ~/ on mount sources (→ hostHome) and
// mount/cache targets (→ runtimeHome) per spec §5.6, then renders
// {{ }} text/template expressions against the host environment. Absolute
// paths are left as-is. The mode ("A" or "B") is informational only here;
// the caller has already determined runtimeHome based on the mode.
func ResolveTildes(cfg Profile, mode, hostHome, runtimeHome string) (Profile, error) {
	out := cfg
	env := expandEnvMap()

	if out.Mounts != nil {
		expanded := make(map[string]Mount, len(out.Mounts))
		for target, m := range out.Mounts {
			newTarget, err := expandTarget(target, runtimeHome, env)
			if err != nil {
				return out, err
			}
			m.Source, err = expandSource(m.Source, hostHome, env)
			if err != nil {
				return out, err
			}
			expanded[newTarget] = m
		}
		out.Mounts = expanded
	}

	if out.Caches != nil {
		expanded := make(map[string]string, len(out.Caches))
		for name, target := range out.Caches {
			var err error
			expanded[name], err = expandTarget(target, runtimeHome, env)
			if err != nil {
				return out, err
			}
		}
		out.Caches = expanded
	}

	return out, nil
}

func expandTarget(path, runtimeHome string, env map[string]string) (string, error) {
	path = expandTilde(path, runtimeHome)
	return renderTemplate(path, env)
}

func expandSource(path, hostHome string, env map[string]string) (string, error) {
	path = expandTilde(path, hostHome)
	return renderTemplate(path, env)
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
