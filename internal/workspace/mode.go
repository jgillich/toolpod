package workspace

// Mode selects how a container is launched. ModeRootless is rootless Podman
// with keep-id (workspace mounted at its host absolute path, running as the
// host user); ModeRootful is the fallback (rootful Docker/Podman, workspace
// at /workspace, /root home).
type Mode int

const (
	ModeRootless Mode = iota
	ModeRootful
)

func (m Mode) String() string {
	switch m {
	case ModeRootless:
		return "rootless"
	case ModeRootful:
		return "rootful"
	}
	return "unknown"
}

func ComputeMountTarget(workspacePath string, mode Mode) string {
	if mode == ModeRootless {
		return workspacePath
	}
	return "/workspace"
}
