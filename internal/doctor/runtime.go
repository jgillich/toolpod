package doctor

import (
	"context"
	"fmt"

	"github.com/jgillich/toolpod/internal/runtime"
)

func newRuntime() (*runtime.DockerRuntime, error) {
	return runtime.NewDockerRuntime()
}

func runChecks(ctx context.Context, rt *runtime.DockerRuntime, opts Options) Result {
	fmt.Println("placeholder")
	return Result{}
}
