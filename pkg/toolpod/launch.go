package toolpod

import "context"

// Launch orchestrates: resolve config → (Plan 2: Prepare + Run) → result.
// In Plan 1, this resolves the config and returns a Spec for --dry-run;
// the Runtime integration is added in Plan 2.
func Launch(ctx context.Context, opts LaunchOpts) Result {
	// Stub: Plan 1 implements config resolution + dry-run spec rendering.
	_ = opts
	return Result{ExitCode: 0}
}
