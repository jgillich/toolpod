package runtime

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestForwardSignalThenFallbackRemovesAfterGrace(t *testing.T) {
	var forwards, cleanups atomic.Int32
	var fallbackWG sync.WaitGroup
	runDone := make(chan struct{})
	forwardSignalThenFallback(&fallbackWG, 50*time.Millisecond, runDone,
		func() { forwards.Add(1) },
		func() { cleanups.Add(1) },
	)
	deadline := time.Now().Add(2 * time.Second)
	for cleanups.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("cleanupOnce not called within the grace period")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if forwards.Load() != 1 {
		t.Errorf("forward called %d times, want 1", forwards.Load())
	}
	if cleanups.Load() != 1 {
		t.Errorf("cleanupOnce called %d times, want 1", cleanups.Load())
	}
	fallbackWG.Wait()
}

func TestForwardSignalThenFallbackSkipsWhenRunDone(t *testing.T) {
	var cleanups atomic.Int32
	var fallbackWG sync.WaitGroup
	runDone := make(chan struct{})
	forwardSignalThenFallback(&fallbackWG, time.Second, runDone,
		func() {},
		func() { cleanups.Add(1) },
	)
	close(runDone)
	time.Sleep(200 * time.Millisecond)
	if cleanups.Load() != 0 {
		t.Errorf("cleanupOnce called %d times, want 0", cleanups.Load())
	}
	fallbackWG.Wait()
}
