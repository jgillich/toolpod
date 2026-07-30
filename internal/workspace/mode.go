package workspace

func ModeFromRootless(rootless bool) string {
	if rootless {
		return "A"
	}
	return "B"
}

func ComputeMountTarget(workspacePath, mode string) string {
	if mode == "A" {
		return workspacePath
	}
	return "/workspace"
}
