package catalog

// Advisory returns a one-line warning about the host access a sensitive
// built-in fragment grants, or "" for fragments that need none. Shown by
// `tpd show`, `tpd edit`, and `tpd init` so the capability is visible
// whenever a sensitive fragment is named, not just after launch. Kept as a
// curated table: per-mount "high risk" labels can go stale and the review
// specifically declined them.
func Advisory(name string) string {
	switch name {
	case "docker":
		return "mounts the Docker socket read-write — container processes can administer the daemon (host-root access on a rootful daemon)"
	case "podman":
		return "mounts the Podman socket read-write — container processes can control the container engine"
	case "gui":
		return "exposes the host display, /dev/dri, and X11/Wayland sockets to container processes"
	case "gui-runtime":
		return "mounts the entire $XDG_RUNTIME_DIR — exposes audio, compositor, notification, and agent sockets to container processes"
	case "ssh", "netrc", "aws", "azure", "gcloud", "github", "gitlab", "vault":
		return "mounts host credentials read-only — any process in the profile can read them"
	default:
		return ""
	}
}
