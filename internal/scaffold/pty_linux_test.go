//go:build linux

package scaffold

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// openPTY allocates a pseudo-terminal pair. The master is a terminal fd (as is
// the slave), so tests can exercise the both-streams-must-be-TTY gate with a
// real terminal on one side and a redirected buffer on the other. Tests skip
// where a pty is unavailable.
func openPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("cannot open /dev/ptmx: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	var unlock int
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), uintptr(syscall.TIOCSPTLCK), uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		t.Skipf("unlockpt: %v", errno)
	}
	var n uint
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), uintptr(syscall.TIOCGPTN), uintptr(unsafe.Pointer(&n))); errno != 0 {
		t.Skipf("ptsname: %v", errno)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR, 0)
	if err != nil {
		t.Skipf("open slave: %v", err)
	}
	t.Cleanup(func() { slave.Close() })
	return master, slave
}

func TestTTYGateRequiresBoth(t *testing.T) {
	master, slave := openPTY(t)
	redirected := &bytes.Buffer{}

	if ttyInteractive(master, redirected) {
		t.Error("stdin TTY + redirected stdout must not launch the huh UI")
	}
	if ttyInteractive(strings.NewReader(""), slave) {
		t.Error("redirected stdin + TTY stdout must not launch the huh UI")
	}
	if !ttyInteractive(master, slave) {
		t.Error("both TTYs should launch the huh UI")
	}
	if ttyInteractive(redirected, redirected) {
		t.Error("no TTY at all must not launch the huh UI")
	}
}
