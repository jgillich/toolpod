package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jgillich/toolpod/pkg/toolpod"
)

func main() {
	result := toolpod.Launch(context.Background(), toolpod.LaunchOpts{
		ProfileName: os.Args[1],
	})
	if result.Err != nil {
		fmt.Fprintln(os.Stderr, result.Err)
	}
	os.Exit(result.ExitCode)
}
