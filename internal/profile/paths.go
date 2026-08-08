package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/template"

	"github.com/jgillich/tpd/internal/workspace"
)

// tmplData is the execution context for path templates. .Env exposes the
// host environment as a map, enabling {{ or .Env.FOO "fallback" }} in mount
// sources/targets. .UID exposes the host user ID. .Ports exposes
// container-port → host-port mappings for rendering {{ index .Ports "8080" }}.
// .MemBytes and .NumCPU expose the host's total RAM (bytes) and logical CPU
// count so profiles can derive resource limits from the machine they run on.
type tmplData struct {
	Env      map[string]string
	UID      string
	Ports    map[string]string
	MemBytes int64
	NumCPU   int
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

// hostMemBytes returns the host's total RAM in bytes, or 0 when it cannot be
// determined. tpd's primary target is rootless Podman on the same host, so
// /proc/meminfo describes the machine the container runs on; for a remote
// Docker engine it describes the client instead.
func hostMemBytes() int64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}

// div is integer division for templates. A zero divisor (host data missing,
// misconfigured) yields 0 rather than panicking; 0 renders a memory value
// that spec.go treats as no limit.
func div(a, b int64) int64 {
	if b == 0 {
		return 0
	}
	return a / b
}

// renderTemplate compiles and executes s as a text/template against the host
// environment. Strings without {{ }} delimiters pass through unchanged.
// A small func map provides helpers useful for path expressions:
//
//	{{ or .Env.FOO "fallback" }}                   — first non-empty value
//	{{ trimPrefix .Env.DOCKER_HOST "unix://" }}    — strip a leading prefix
//	{{ uid }}                                      — host user ID (e.g. "1000")
//	{{ div .MemBytes 2 }}                          — integer division
func renderTemplate(s string, data tmplData) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	tmpl, err := template.New("path").Funcs(template.FuncMap{
		"trimPrefix": strings.TrimPrefix,
		"uid":        currentUID,
		"div":        div,
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
// as a template. Absolute paths are left as-is. ModeUnknown (dry-run without
// a daemon) keeps ~ targets literal rather than claiming a home; the caller
// otherwise determines runtimeHome based on the mode.
func ResolveTildes(cfg Profile, mode workspace.Mode, hostHome, runtimeHome string, ports map[string]string) (Profile, error) {
	out := cfg
	data := tmplData{Env: expandEnvMap(), UID: currentUID(), Ports: ports, MemBytes: hostMemBytes(), NumCPU: runtime.NumCPU()}

	// resolveTarget resolves an in-container mount/cache/file target. In
	// unknown mode (dry-run without a detected engine mode) a ~-prefixed target
	// stays literal instead of expanding against the host home, which would
	// claim a rootless container home no daemon confirmed. Sources are host
	// paths and keep expanding against hostHome regardless of mode.
	resolveTarget := func(raw string, d tmplData) (string, error) {
		return resolvePath(raw, runtimeHome, d)
	}
	if mode == workspace.ModeUnknown {
		resolveTarget = resolveUnknownPath
	}

	if out.Mounts != nil {
		expanded := make(map[string]Mount, len(out.Mounts))
		for target, m := range out.Mounts {
			newTarget, err := resolveTarget(target, data)
			if err != nil {
				if m.Optional && errors.Is(err, errEmptyPath) {
					continue
				}
				return out, fmt.Errorf("mount %q: %w", target, err)
			}
			// An empty rendered source is a config error (a bind mount with no
			// source fails confusingly later); optional mounts degrade to
			// absent. Service-socket mounts have no source by definition, so
			// only bind mounts are checked for one.
			if m.Service == "" {
				m.Source, err = resolvePath(m.Source, hostHome, data)
				if err != nil {
					if m.Optional && errors.Is(err, errEmptyPath) {
						continue
					}
					return out, fmt.Errorf("mount %q source: %w", target, err)
				}
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
				e, err := resolveTarget(p, data)
				if err != nil {
					return out, fmt.Errorf("cache %s: %w", name, err)
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
			newTarget, err := resolveTarget(target, data)
			if err != nil {
				return out, fmt.Errorf("file %q: %w", target, err)
			}
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

	if out.Resources != nil {
		res := *out.Resources
		if res.Memory != "" {
			rendered, err := renderTemplate(res.Memory, data)
			if err != nil {
				return out, fmt.Errorf("resources: memory: %w", err)
			}
			res.Memory = rendered
		}
		if res.CPUs != "" {
			rendered, err := renderTemplate(res.CPUs, data)
			if err != nil {
				return out, fmt.Errorf("resources: cpus: %w", err)
			}
			res.CPUs = rendered
		}
		out.Resources = &res
	}

	if out.Services != nil {
		const serviceHome = "/root"
		expanded := make(map[string]Service, len(out.Services))
		for name, svc := range out.Services {
			if svc.Mounts != nil {
				svcMounts := make(map[string]Mount, len(svc.Mounts))
				for target, m := range svc.Mounts {
					// Targets are in-container paths; expand against /root.
					newTarget, err := resolvePath(target, serviceHome, data)
					if err != nil {
						if m.Optional && errors.Is(err, errEmptyPath) {
							continue
						}
						return out, fmt.Errorf("service %s mount %q: %w", name, target, err)
					}
					// Sources are host paths; expand against hostHome.
					m.Source, err = resolvePath(m.Source, hostHome, data)
					if err != nil {
						if m.Optional && errors.Is(err, errEmptyPath) {
							continue
						}
						return out, fmt.Errorf("service %s mount %q source: %w", name, target, err)
					}
					svcMounts[newTarget] = m
				}
				svc.Mounts = svcMounts
			}
			if svc.Caches != nil {
				expandedCaches := make(map[string]CachePaths, len(svc.Caches))
				for cacheName, paths := range svc.Caches {
					var exps CachePaths
					for _, p := range paths {
						// Cache paths are in-container paths; expand against /root.
						e, err := resolvePath(p, serviceHome, data)
						if err != nil {
							return out, fmt.Errorf("service %s cache %s: %w", name, cacheName, err)
						}
						exps = append(exps, e)
					}
					expandedCaches[cacheName] = exps
				}
				svc.Caches = expandedCaches
			}
			if svc.Files != nil {
				expandedFiles := make(map[string]File, len(svc.Files))
				for target, f := range svc.Files {
					// File targets are in-container paths; expand against /root.
					newTarget, err := resolvePath(target, serviceHome, data)
					if err != nil {
						return out, fmt.Errorf("service %s file %q: %w", name, target, err)
					}
					f.Content, err = renderTemplate(f.Content, data)
					if err != nil {
						return out, fmt.Errorf("service %s file %s: %w", name, target, err)
					}
					expandedFiles[newTarget] = f
				}
				svc.Files = expandedFiles
			}
			if len(svc.Exposes) > 0 {
				exposes := make(map[string]string, len(svc.Exposes))
				for socketName, exposePath := range svc.Exposes {
					// Expose paths are in-container socket paths; expand
					// against /root and re-check the expose syntax.
					resolved, err := resolvePath(exposePath, serviceHome, data)
					if err != nil {
						return out, fmt.Errorf("service %s exposes %s: %w", name, socketName, err)
					}
					if err := checkExposePath(resolved); err != nil {
						return out, fmt.Errorf("service %s exposes %s: %w", name, socketName, err)
					}
					exposes[socketName] = resolved
				}
				svc.Exposes = exposes
			}
			if svc.Env != nil {
				for k, v := range svc.Env {
					rendered, err := renderTemplate(v, data)
					if err != nil {
						return out, fmt.Errorf("service %s environment %s: %w", name, k, err)
					}
					svc.Env[k] = rendered
				}
			}
			if svc.Command != nil {
				rendered, err := renderArgs(svc.Command, data)
				if err != nil {
					return out, fmt.Errorf("service %s command: %w", name, err)
				}
				svc.Command = rendered
			}
			expanded[name] = svc
		}
		out.Services = expanded
	}

	return out, nil
}

// renderArgs renders args containing a {{ }} template expression; args
// without one pass through unchanged, so shell snippets with single braces
// or no braces are untouched.
func renderArgs(args []string, data tmplData) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		rendered, err := renderTemplate(a, data)
		if err != nil {
			return nil, fmt.Errorf("arg %d: %w", i, err)
		}
		out[i] = rendered
	}
	return out, nil
}

// errEmptyPath is returned by resolvePath when a rendered path resolves empty.
// Optional mounts treat it as "drop the mount"; everything else propagates it.
var errEmptyPath = errors.New("resolved to an empty path after template expansion (is the host variable set?)")

// resolvePath renders raw as a {{ }} template, expands a resulting ~/ against
// home, and validates the resolved path: non-empty, absolute (after tilde
// expansion), and free of ".." segments. Rendering happens before tilde
// expansion so a ~/ produced by a template is honored. Validation exempts
// templates from the raw absolute/~-prefix rule, so a template that renders a
// relative path is rejected here. The result is cleaned; Clean cannot
// introduce "..".
func resolvePath(raw, home string, data tmplData) (string, error) {
	rendered, err := renderTemplate(raw, data)
	if err != nil {
		return "", err
	}
	if rendered == "" {
		return "", errEmptyPath
	}
	if !isAbsOrTilde(rendered) {
		return "", fmt.Errorf("resolved to path %q that is neither absolute nor ~-prefixed after template expansion", rendered)
	}
	expanded := expandTilde(rendered, home)
	if !filepath.IsAbs(expanded) {
		return "", fmt.Errorf("resolved to path %q that is not absolute after tilde expansion", expanded)
	}
	if hasDotDot(expanded) {
		return "", fmt.Errorf("resolved to path %q containing '..' after template expansion", expanded)
	}
	return filepath.Clean(expanded), nil
}

// resolveUnknownPath validates an in-container path without expanding a
// leading ~/: no engine mode has been detected, so no home can be claimed.
// Absolute paths pass through cleaned; ~-prefixed targets stay literal so the
// dry-run preview shows the unexpanded target instead of a guessed home.
func resolveUnknownPath(raw string, data tmplData) (string, error) {
	rendered, err := renderTemplate(raw, data)
	if err != nil {
		return "", err
	}
	if rendered == "" {
		return "", errEmptyPath
	}
	if !isAbsOrTilde(rendered) {
		return "", fmt.Errorf("resolved to path %q that is neither absolute nor ~-prefixed after template expansion", rendered)
	}
	if hasDotDot(rendered) {
		return "", fmt.Errorf("resolved to path %q containing '..' after template expansion", rendered)
	}
	return filepath.Clean(rendered), nil
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
