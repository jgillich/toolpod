package toolpod

import (
	"fmt"
	"io"
	"sort"
)

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
		if err != nil {
			return err
		}
		for _, m := range spec.Mounts {
			ro := "ro"
			if !m.ReadOnly {
				ro = "rw"
			}
			suffix := ""
			if m.Optional {
				suffix = " optional"
			}
			_, err = fmt.Fprintf(w, "  %s <- %s (%s%s)\n", m.Target, m.Source, ro, suffix)
			if err != nil {
				return err
			}
		}
	}
	if len(spec.Tools) > 0 {
		_, err = fmt.Fprintln(w, "tools:")
		if err != nil {
			return err
		}
		toolNames := make([]string, 0, len(spec.Tools))
		for name := range spec.Tools {
			toolNames = append(toolNames, name)
		}
		sort.Strings(toolNames)
		for _, name := range toolNames {
			_, err = fmt.Fprintf(w, "  %s: %s\n", name, spec.Tools[name])
			if err != nil {
				return err
			}
		}
	}
	if len(spec.Caches) > 0 {
		_, err = fmt.Fprintln(w, "caches:")
		if err != nil {
			return err
		}
		for _, c := range spec.Caches {
			_, err = fmt.Fprintf(w, "  %s -> %s\n", c.Name, c.Target)
			if err != nil {
				return err
			}
		}
	}
	if len(spec.Env) > 0 {
		_, err = fmt.Fprintln(w, "environment:")
		if err != nil {
			return err
		}
		envKeys := make([]string, 0, len(spec.Env))
		for k := range spec.Env {
			envKeys = append(envKeys, k)
		}
		sort.Strings(envKeys)
		for _, k := range envKeys {
			_, err = fmt.Fprintf(w, "  %s: %q\n", k, spec.Env[k])
			if err != nil {
				return err
			}
		}
	}
	if spec.Network != "" {
		_, err = fmt.Fprintf(w, "network: %s\n", spec.Network)
		if err != nil {
			return err
		}
	}
	return nil
}
