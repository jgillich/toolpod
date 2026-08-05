package tpd

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordProgress struct {
	mu    sync.Mutex
	lines []string
}

func (r *recordProgress) WriteProgress(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
}

func TestSpinnerNonTTYPassesThrough(t *testing.T) {
	var out bytes.Buffer
	var inner recordProgress
	s := newSpinnerProgress(&out, &inner, false)
	s.Start()
	s.WriteProgress("pull: debian:13-slim")
	s.WriteProgress("build: tpd/packages:abc")
	s.Stop()
	inner.mu.Lock()
	defer inner.mu.Unlock()
	if len(inner.lines) != 2 {
		t.Fatalf("inner lines = %v, want 2 pass-through lines", inner.lines)
	}
	if out.Len() != 0 {
		t.Errorf("non-TTY must not write spinner output: %q", out.String())
	}
}

func TestSpinnerTTYSwallowsLinesAndClears(t *testing.T) {
	var out bytes.Buffer
	var inner recordProgress
	s := newSpinnerProgress(&out, &inner, true)
	s.interval = time.Millisecond
	s.Start()
	s.WriteProgress("build: tpd/packages:abc")
	time.Sleep(20 * time.Millisecond)
	s.Stop()
	inner.mu.Lock()
	defer inner.mu.Unlock()
	if len(inner.lines) != 0 {
		t.Errorf("TTY must not pass lines through: %v", inner.lines)
	}
	if !strings.Contains(out.String(), "\r\x1b[2K") {
		t.Errorf("TTY must render frames and clear the line on Stop: %q", out.String())
	}
}

func TestSpinnerConcurrentWriteAndStop(t *testing.T) {
	var out bytes.Buffer
	var inner recordProgress
	s := newSpinnerProgress(&out, &inner, true)
	s.interval = time.Millisecond
	s.Start()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s.WriteProgress(strings.Repeat("x", 100))
			}
		}()
	}
	time.Sleep(5 * time.Millisecond)
	s.Stop()
	wg.Wait()
}

func TestSpinnerStopIdempotent(t *testing.T) {
	var out bytes.Buffer
	var inner recordProgress
	s := newSpinnerProgress(&out, &inner, true)
	s.Start()
	s.Stop()
	s.Stop()
}
