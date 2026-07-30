package runtime

import (
	"context"
	"testing"
)

// FakeRuntime is a test helper that records Prepare/Run calls. Exported via
// the runtime package so pkg/toolpod tests can import it without redefining.
type FakeRuntime struct {
	PreparedSpec *Spec
	RanSpec      *Spec
	PrepareErr   error
	PrepareImage string
	RunErr       error
	ExitCode     int
}

func (f *FakeRuntime) Prepare(ctx context.Context, spec Spec, w ProgressWriter) (string, error) {
	f.PreparedSpec = &spec
	return f.PrepareImage, f.PrepareErr
}

func (f *FakeRuntime) Run(ctx context.Context, spec Spec) (int, error) {
	f.RanSpec = &spec
	return f.ExitCode, f.RunErr
}

func TestFakeRuntimeImplementsInterface(t *testing.T) {
	var _ Runtime = (*FakeRuntime)(nil)
	rt := &FakeRuntime{ExitCode: 0}
	if _, err := rt.Prepare(context.Background(), Spec{}, NoopProgressWriter{}); err != nil {
		t.Fatal(err)
	}
	if rt.PreparedSpec == nil {
		t.Error("Prepare did not record spec")
	}
	code, err := rt.Run(context.Background(), Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if rt.RanSpec == nil {
		t.Error("Run did not record spec")
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}
