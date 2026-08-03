package runtime

import "context"

// FakeRuntime is a test helper that records Prepare/Run calls. Exported so
// pkg/tpd tests can import it without redefining.
type FakeRuntime struct {
	PreparedSpec *Spec
	PreparePull  bool
	RanSpec      *Spec
	PrepareErr   error
	PrepareImage string
	RunErr       error
	ExitCode     int
}

func (f *FakeRuntime) Prepare(ctx context.Context, spec Spec, w ProgressWriter, pull bool) (string, error) {
	f.PreparedSpec = &spec
	f.PreparePull = pull
	return f.PrepareImage, f.PrepareErr
}

func (f *FakeRuntime) Run(ctx context.Context, spec Spec) (int, error) {
	f.RanSpec = &spec
	return f.ExitCode, f.RunErr
}
