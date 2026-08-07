package tpd

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestExternalConsumerFixture runs go test inside the external-consumer
// fixture module (testdata/externalconsumer), making the boundary claim in
// doc.go a checked one: an external module compiles against pkg/tpd and its
// internal-surface probes fail exactly as required. Skipped under -short
// because it downloads the fixture module's dependencies.
func TestExternalConsumerFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("external-consumer fixture needs module downloads; skip in short mode")
	}
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = filepath.Join("testdata", "externalconsumer")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go test in external-consumer fixture failed: %v\n%s", err, out)
	}
}
