package runtime

import (
	"context"
	"testing"
)

func TestFakeRuntimeImplementsInterface(t *testing.T) {
	var _ Runtime = (*FakeRuntime)(nil)
	rt := &FakeRuntime{ExitCode: 0}
	if _, err := rt.Prepare(context.Background(), Spec{}, NoopProgressWriter{}, false); err != nil {
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
