package approval

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func TestStateLockExcludesConcurrentHolder(t *testing.T) {
	dir := t.TempDir()
	s := NewFSStore(dir)
	path, err := s.pathFor("p")
	if err != nil {
		t.Fatal(err)
	}
	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	l1, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer l1.Close()
	if err := syscall.Flock(int(l1.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	// A second, independent open must not acquire while l1 holds the lock.
	l2, err := os.OpenFile(lockPath, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if err := syscall.Flock(int(l2.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Error("second exclusive flock should be denied while the first is held")
		syscall.Flock(int(l2.Fd()), syscall.LOCK_UN)
	}
	// After l1 releases, the same fd can acquire.
	if err := syscall.Flock(int(l1.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(l2.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Errorf("flock after release should succeed, got %v", err)
	}
}

// TestWithLockReportsContention asserts that WithLock fails fast instead of
// blocking when another process holds the approval lock — the interactive
// prompt can hold it for a long time, so a second launch must not hang.
func TestWithLockReportsContention(t *testing.T) {
	dir := t.TempDir()
	s := NewFSStore(dir)
	path, err := s.pathFor("p")
	if err != nil {
		t.Fatal(err)
	}
	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	holder, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	ran := false
	err = WithLock(s, "p", func() error {
		ran = true
		return nil
	})
	if err == nil {
		t.Fatal("WithLock should fail when another process holds the lock, not block")
	}
	if ran {
		t.Error("WithLock must not run fn while the lock is held elsewhere")
	}
	if !strings.Contains(err.Error(), "p") {
		t.Errorf("error should name the profile, got %v", err)
	}
}

// TestWithLockSerializesTransactions models the launch transaction (Load →
// merge → Save) running concurrently for one profile. The lock is now
// non-blocking, so a concurrent transaction either wins the lock and commits
// its decision, or fails fast with the contention error — it never runs
// unlocked. Either way no decision is lost: a failed transaction writes
// nothing, so the final state holds exactly the decisions of the transactions
// that succeeded.
func TestWithLockSerializesTransactions(t *testing.T) {
	dir := t.TempDir()
	s := NewFSStore(dir)
	const fullName = "p"
	var wg sync.WaitGroup
	type result struct {
		key string
		err error
	}
	results := make(chan result, 2)
	for _, key := range []string{"~/.ssh", "~/aws"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			err := WithLock(s, fullName, func() error {
				st, err := s.Load(fullName)
				if err != nil {
					return err
				}
				if st.Approved == nil {
					st.Approved = map[string]ApprovedField{}
				}
				af := st.Approved["mounts"]
				af.Keys = append(af.Keys, key)
				sort.Strings(af.Keys)
				st.Approved["mounts"] = af
				st.Hash = "h"
				return s.Save(fullName, st)
			})
			results <- result{key: key, err: err}
		}(key)
	}
	wg.Wait()
	close(results)
	committed := map[string]bool{}
	ok := 0
	for r := range results {
		if r.err == nil {
			ok++
			committed[r.key] = true
			continue
		}
		if !strings.Contains(r.err.Error(), "another tpd process is awaiting approval") {
			t.Errorf("transaction for %q failed with an unexpected error: %v", r.key, r.err)
		}
	}
	if ok == 0 {
		t.Fatal("at least one transaction should acquire the lock")
	}
	st, err := s.Load(fullName)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"~/.ssh", "~/aws"} {
		if committed[key] && !containsKey(st.Approved["mounts"].Keys, key) {
			t.Errorf("approval for %q lost by a concurrent transaction", key)
		}
		if !committed[key] && containsKey(st.Approved["mounts"].Keys, key) {
			t.Errorf("approval for %q committed by a failed transaction", key)
		}
	}
}

func TestWithLockRejectsNonLockableStore(t *testing.T) {
	ran := false
	err := WithLock(&memStore{}, "p", func() error {
		ran = true
		return nil
	})
	if err == nil {
		t.Fatal("WithLock should reject a store that cannot lock instead of running unlocked")
	}
	if ran {
		t.Error("WithLock must not run fn for a non-lockable store")
	}
	if !strings.Contains(err.Error(), "does not support locking") {
		t.Errorf("error should explain the lock requirement, got %v", err)
	}
}

// lockForwardingStore wraps an FSStore and forwards LockPath, standing in
// for a decorator that must keep the lock contract explicit.
type lockForwardingStore struct {
	*FSStore
}

var _ Lockable = (*lockForwardingStore)(nil)

func TestWithLockWorksThroughLockableDecorator(t *testing.T) {
	s := &lockForwardingStore{FSStore: NewFSStore(t.TempDir())}
	ran := false
	err := WithLock(s, "p", func() error {
		ran = true
		return nil
	})
	if err != nil {
		t.Fatalf("WithLock through a Lockable decorator: %v", err)
	}
	if !ran {
		t.Error("WithLock should run fn through a decorator that forwards LockPath")
	}
	// The lock file was created alongside the (as-yet unwritten) state file.
	if _, err := os.Stat(filepath.Join(s.root, "approvals", "p.yaml.lock")); err != nil {
		t.Errorf("lock file not created: %v", err)
	}
}
