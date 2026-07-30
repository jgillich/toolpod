package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckWorkspaceWritable(t *testing.T) {
	dir := t.TempDir()
	c := checkWorkspaceWritable(context.Background(), dir)
	if c.Status != Pass {
		t.Errorf("writable dir: status = %s, want pass", c.Status)
	}
}

func TestCheckWorkspaceNotWritable(t *testing.T) {
	dir := t.TempDir()
	err := os.Chmod(dir, 0o444)
	if err != nil {
		t.Skip("cannot chmod on this OS")
	}
	defer os.Chmod(dir, 0o755)
	c := checkWorkspaceWritable(context.Background(), dir)
	if c.Status == Pass {
		t.Error("read-only dir should not pass")
	}
}

func TestCheckProjectTools(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".tool-versions"), []byte("node 22\npython 3.13\n"), 0o644)
	c := checkProjectTools(context.Background(), dir)
	if c.Status != Pass {
		t.Errorf("status = %s, want pass", c.Status)
	}
	if !strings.Contains(c.Message, "node@22") {
		t.Errorf("message should list node@22; got %q", c.Message)
	}
}

func TestCheckProjectToolsNone(t *testing.T) {
	dir := t.TempDir()
	c := checkProjectTools(context.Background(), dir)
	if c.Status != Info {
		t.Errorf("no tool files: status = %s, want info", c.Status)
	}
}
