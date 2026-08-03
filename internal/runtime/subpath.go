package runtime

import (
	"context"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

// supportsVolumeSubpath reports whether the engine's Docker-compatible API
// honors VolumeOptions.Subpath. Podman gained compat subpath support in 6.0
// (podman-container-tools/podman#28085); Docker added volume-subpath in 27.1.
// Detection is deliberately conservative: wrongly falling back to separate
// volumes is safe, while enabling subpath on an engine that ignores it would
// silently mount the whole volume at each target.
func supportsVolumeSubpath(ctx context.Context, cli *client.Client) bool {
	ver, err := cli.ServerVersion(ctx)
	if err != nil {
		return false
	}
	return subpathSupportedByVersion(ver)
}

func subpathSupportedByVersion(ver types.Version) bool {
	for _, c := range ver.Components {
		if c.Name == "Podman Engine" {
			return atLeast(c.Version, 6, 0, 0)
		}
	}
	return atLeast(ver.Version, 27, 1, 0)
}

// atLeast compares a dotted version string against a threshold, ignoring
// non-numeric suffixes (pre-releases like 6.0.0-rc1 compare as 6.0.0).
func atLeast(v string, maj, min, pat int) bool {
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	var nums [3]int
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(strings.TrimFunc(parts[i], func(r rune) bool { return r < '0' || r > '9' }))
		if err != nil {
			return false
		}
		nums[i] = n
	}
	switch {
	case nums[0] != maj:
		return nums[0] > maj
	case nums[1] != min:
		return nums[1] > min
	default:
		return nums[2] >= pat
	}
}
