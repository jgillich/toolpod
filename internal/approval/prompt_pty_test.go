//go:build linux

package approval

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// waitForFrame reads the pty master until the prompt's first frame is
// rendered. By then bubbletea has enabled raw mode on the slave, so keys
// written afterwards are interpreted as key events instead of being mangled
// by the tty line discipline.
func waitForFrame(t *testing.T, master *os.File) {
	t.Helper()
	want := []byte("Review permissions for")
	deadline := time.After(5 * time.Second)
	var acc []byte
	for {
		buf := make([]byte, 4096)
		n, err := master.Read(buf)
		if err != nil {
			t.Fatalf("read pty master: %v", err)
		}
		acc = append(acc, buf[:n]...)
		if bytes.Contains(acc, want) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for the prompt frame; got %q", acc)
		default:
		}
	}
}

// TestDefaultPromptInteractiveToggleAndSubmit drives the real bubbletea
// prompt over a pseudo-terminal: ~/.ssh starts unchecked, space checks it,
// enter submits, and the returned choices reflect the toggles plus the
// prior-approved item.
func TestDefaultPromptInteractiveToggleAndSubmit(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("cannot open pty: %v", err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(master, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		t.Skipf("cannot set pty size: %v", err)
	}

	req := PromptRequest{ProfileName: "bash", Items: []GatedItem{
		{Field: "mounts", Key: "~/.ssh", Value: "~/.ssh (rw)", Source: testContrib()},
		{Field: "mounts", Key: "~/aws", Value: "~/aws (rw)", Source: testContrib(), PriorApproved: true},
	}}

	choices := make(chan map[string]map[string]bool, 1)
	errs := make(chan error, 1)
	go func() {
		c, err := DefaultPrompt(req, slave, slave)
		if err != nil {
			errs <- err
			return
		}
		choices <- c
	}()

	waitForFrame(t, master)
	if _, err := master.Write([]byte(" \r")); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case c := <-choices:
		if !c["mounts"]["~/.ssh"] {
			t.Error("space should check ~/.ssh, so it must be approved")
		}
		if !c["mounts"]["~/aws"] {
			t.Error("prior-approved ~/aws must remain approved")
		}
	case err := <-errs:
		t.Fatalf("DefaultPrompt: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for DefaultPrompt")
	}
}

// TestDefaultPromptInteractiveEscCancels verifies esc aborts with the
// "approval declined" error.
func TestDefaultPromptInteractiveEscCancels(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("cannot open pty: %v", err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(master, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		t.Skipf("cannot set pty size: %v", err)
	}

	req := PromptRequest{ProfileName: "bash", Items: []GatedItem{
		{Field: "env", Key: "A", Value: "A=1", Source: testContrib()},
	}}

	errs := make(chan error, 1)
	go func() {
		_, err := DefaultPrompt(req, slave, slave)
		errs <- err
	}()

	waitForFrame(t, master)
	if _, err := master.Write([]byte("\x1b")); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("esc should abort the prompt")
		}
		if !strings.Contains(err.Error(), "approval declined") {
			t.Errorf("cancel error = %v, want \"approval declined\"", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for DefaultPrompt")
	}
}
