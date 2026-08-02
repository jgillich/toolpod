package runtime

import (
	"os"
	"strings"
)

const selinuxEnforceFile = "/sys/fs/selinux/enforce"

// selinuxEnforcing reports whether SELinux is in enforcing mode by reading
// the kernel's enforce flag. The file is absent when SELinux is disabled or
// not built into the kernel, which reads as not enforcing.
func selinuxEnforcing(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "1"
}

// SELinuxEnforcing reports whether SELinux is enforcing on this host.
// Exported for tpod doctor, which mirrors the launch-time detection.
func SELinuxEnforcing() bool {
	return selinuxEnforcing(selinuxEnforceFile)
}
