package workspace

// Mode selects how a container is launched. ModeRootless is rootless Podman
// with keep-id (workspace mounted at its host absolute path, running as the
// host user); ModeRootful is the fallback (rootful Docker/Podman, workspace
// at /workspace, /root home).
type Mode int

const (
	ModeRootless Mode = iota
	ModeRootful
	ModeUnknown
)

func (m Mode) String() string {
	switch m {
	case ModeRootless:
		return "rootless"
	case ModeRootful:
		return "rootful"
	case ModeUnknown:
		return "unknown"
	}
	return "unknown"
}

func ComputeMountTarget(workspacePath string, mode Mode) string {
	switch mode {
	case ModeRootless:
		return workspacePath
	case ModeRootful:
		return "/workspace"
	default:
		return ""
	}
}
