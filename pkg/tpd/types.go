package tpd

import (
	"context"
	"io"

	"github.com/jgillich/tpd/internal/approval"
	"github.com/jgillich/tpd/internal/runtime"
	"github.com/jgillich/tpd/internal/workspace"
)

// modeDetector is implemented by runtimes that can report the launch mode
// (rootless vs rootful) from their engine: a real launch honors the reported
// mode and propagates a detector failure instead of silently falling back to
// rootful. A dry-run never instantiates or queries a runtime, so a detector
// is never invoked there. Unexported because runtime injection is an
// in-module seam (LaunchOpts.Runtime is an internal interface), so no type
// outside the module can ever implement it.
type modeDetector interface {
	DetectMode(ctx context.Context) (workspace.Mode, error)
}

// LaunchOpts carries the inputs to Launch. Runtime, Progress,
// ApprovalStore, and ApprovalPrompt are injection points for in-module
// fakes: their interfaces live in internal/ packages, so only code inside
// this module can provide them. The remaining fields carry only public
// types and are what the CLI and any future consumer can set.
type LaunchOpts struct {
	ProfileName string
	Args        []string
	Command     string
	Workspace   string
	ProfileDir  string
	ExtraTools  []string
	DryRun      bool
	Verbose     bool
	// Pull re-pulls the base image even when already present locally,
	// refreshing mutable tags like latest.
	Pull    bool
	Runtime runtime.Runtime
	// Progress receives status lines during Prepare (image pull, mise
	// install). If nil, progress goes to Stderr. Set to a no-op writer
	// to silence progress entirely.
	Progress runtime.ProgressWriter
	// Stderr receives diagnostics during launch: spinner/progress output,
	// stop-services and dbus-proxy warnings. Container stdout is unaffected:
	// the runtime pumps it to os.Stdout directly; the writer passed to
	// LaunchWithWriter carries only rendered preview output and approval
	// prompts. If nil, diagnostics go to os.Stderr.
	Stderr io.Writer
	// PortAllocator reserves host ports for auto-allocated bindings. If
	// nil, an ephemeral socket bind is used. Injectable for deterministic
	// tests.
	PortAllocator PortAllocator

	In             io.Reader
	ApprovalStore  approval.Store
	ApprovalPrompt approval.Prompt
	IsTTY          func(io.Reader) bool
	AssumeYes      bool
	AssumeNo       bool
}

type Result struct {
	ExitCode int
	Err      error
}
