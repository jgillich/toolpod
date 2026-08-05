package tpd

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
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "command: %v\n", spec.Command)
	if err != nil {
		return err
	}
	if len(spec.Packages) > 0 {
		_, err = fmt.Fprintf(w, "packages: %v\n", spec.Packages)
		if err != nil {
			return err
		}
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
			if m.Service != "" {
				_, err = fmt.Fprintf(w, "  %s <- service:%s socket:%s\n", m.Target, m.Service, m.Socket)
				if err != nil {
					return err
				}
				continue
			}
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
			_, err = fmt.Fprintf(w, "  %s: %s\n", name, spec.Tools[name].Version)
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
	if len(spec.PortSpecs) > 0 {
		_, err = fmt.Fprintln(w, "ports:")
		if err != nil {
			return err
		}
		for _, p := range spec.PortSpecs {
			hostIP := p.HostIP
			if hostIP == "" {
				hostIP = "0.0.0.0"
			}
			_, err = fmt.Fprintf(w, "  %s/%s -> %s:%s\n", p.Container, p.Protocol, hostIP, p.HostPort)
			if err != nil {
				return err
			}
		}
	}
	if len(spec.DeviceSpecs) > 0 {
		_, err = fmt.Fprintln(w, "devices:")
		if err != nil {
			return err
		}
		for _, d := range spec.DeviceSpecs {
			suffix := ""
			if d.Cgroup {
				suffix = " cgroup"
			}
			_, err = fmt.Fprintf(w, "  %s <- %s (%s%s)\n", d.Container, d.Host, d.Perms, suffix)
			if err != nil {
				return err
			}
		}
	}
	if len(spec.Services) > 0 {
		_, err = fmt.Fprintln(w, "services:")
		if err != nil {
			return err
		}
		for _, svc := range spec.Services {
			_, err = fmt.Fprintf(w, "  %s:\n    image: %s\n    command: %v\n", svc.Name, svc.Image, svc.Command)
			if err != nil {
				return err
			}
			if len(svc.Exposes) > 0 {
				_, err = fmt.Fprintln(w, "    exposes:")
				if err != nil {
					return err
				}
				exposeNames := make([]string, 0, len(svc.Exposes))
				for n := range svc.Exposes {
					exposeNames = append(exposeNames, n)
				}
				sort.Strings(exposeNames)
				for _, n := range exposeNames {
					_, err = fmt.Fprintf(w, "      %s: %s\n", n, svc.Exposes[n])
					if err != nil {
						return err
					}
				}
			}
			if len(svc.Caches) > 0 {
				_, err = fmt.Fprintln(w, "    caches:")
				if err != nil {
					return err
				}
				for _, c := range svc.Caches {
					_, err = fmt.Fprintf(w, "      %s -> %s\n", c.Name, c.Target)
					if err != nil {
						return err
					}
				}
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
	if spec.Resources.MemoryBytes > 0 || spec.Resources.NanoCPUs > 0 {
		_, err = fmt.Fprintln(w, "resources:")
		if err != nil {
			return err
		}
		if spec.Resources.MemoryBytes > 0 {
			_, err = fmt.Fprintf(w, "  memory: %d\n", spec.Resources.MemoryBytes)
			if err != nil {
				return err
			}
		}
		if spec.Resources.NanoCPUs > 0 {
			_, err = fmt.Fprintf(w, "  cpus: %d (nano)\n", spec.Resources.NanoCPUs)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
