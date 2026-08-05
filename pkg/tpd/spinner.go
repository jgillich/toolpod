package tpd

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/jgillich/tpd/internal/runtime"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerProgress shows an indeterminate spinner in place of build log lines on
// a TTY; on a non-TTY it passes lines through to inner unchanged. Docker's
// buildkit backend emits no classic stream lines, so the build log would be
// silent there without it.
type spinnerProgress struct {
	mu       sync.Mutex
	out      io.Writer
	inner    runtime.ProgressWriter
	tty      bool
	interval time.Duration
	label    string
	stop     chan struct{}
	done     chan struct{}
	active   bool
}

func newSpinnerProgress(out io.Writer, inner runtime.ProgressWriter, tty bool) *spinnerProgress {
	return &spinnerProgress{out: out, inner: inner, tty: tty, interval: 100 * time.Millisecond}
}

func (s *spinnerProgress) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active || !s.tty {
		return
	}
	s.active = true
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	go s.run()
}

func (s *spinnerProgress) run() {
	defer close(s.done)
	i := 0
	for {
		select {
		case <-s.stop:
			return
		case <-time.After(s.interval):
		}
		s.mu.Lock()
		frame := spinnerFrames[i%len(spinnerFrames)]
		i++
		label := s.label
		s.mu.Unlock()
		fmt.Fprintf(s.out, "\r\x1b[2K%s %s", frame, label)
	}
}

func (s *spinnerProgress) Stop() {
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return
	}
	s.active = false
	close(s.stop)
	s.mu.Unlock()
	<-s.done
	s.mu.Lock()
	fmt.Fprint(s.out, "\r\x1b[2K")
	s.mu.Unlock()
}

func (s *spinnerProgress) WriteProgress(line string) {
	s.mu.Lock()
	if s.tty && s.active {
		s.label = truncate(line, 80)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.inner.WriteProgress(line)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
