package toolpod

import (
	"fmt"
	"io"
)

// RenderSpec writes the resolved container spec as YAML to w, for --dry-run.
func RenderSpec(w io.Writer, spec Spec) error {
	_, err := fmt.Fprintf(w, "profile: %s\n", spec.ProfileName)
	if err != nil {
		return err
	}
	if spec.Image != "" {
		_, err = fmt.Fprintf(w, "image: %s\n", spec.Image)
	} else if spec.Build != nil {
		_, err = fmt.Fprintf(w, "build:\n  dockerfile: %s\n", spec.Build.Dockerfile)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "command: %v\n", spec.Command)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "workspace:\n  host: %s\n  target: %s\n  mode: %s\n", spec.Workspace.HostPath, spec.Workspace.Target, spec.Workspace.Mode)
	if err != nil {
		return err
	}
	if len(spec.Mounts) > 0 {
		_, err = fmt.Fprintln(w, "mounts:")
		for _, m := range spec.Mounts {
			ro := "ro"
			if !m.ReadOnly {
				ro = "rw"
			}
			_, err = fmt.Fprintf(w, "  %s <- %s (%s)\n", m.Target, m.Source, ro)
		}
	}
	if len(spec.Tools) > 0 {
		_, err = fmt.Fprintln(w, "tools:")
		for name, ver := range spec.Tools {
			_, err = fmt.Fprintf(w, "  %s: %s\n", name, ver)
		}
	}
	if len(spec.Caches) > 0 {
		_, err = fmt.Fprintln(w, "caches:")
		for _, c := range spec.Caches {
			_, err = fmt.Fprintf(w, "  %s -> %s\n", c.Name, c.Target)
		}
	}
	if len(spec.Env) > 0 {
		_, err = fmt.Fprintln(w, "environment:")
		for k, v := range spec.Env {
			_, err = fmt.Fprintf(w, "  %s: %q\n", k, v)
		}
	}
	if spec.Network != "" {
		_, err = fmt.Fprintf(w, "network: %s\n", spec.Network)
	}
	return err
}
