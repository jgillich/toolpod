package approval

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lockable is implemented by stores that back onto a filesystem state file
// and can name its advisory lock file. WithLock requires it, so a store or
// decorator that cannot lock never silently runs a transaction unlocked.
type Lockable interface {
	// LockPath returns the advisory lock file path for fullName's state
	// file: a stable sibling of the state file, which Save replaces via
	// rename.
	LockPath(fullName string) (string, error)
}

// LockPath returns the advisory lock file path for fullName's state file.
func (s *FSStore) LockPath(fullName string) (string, error) {
	path, err := s.pathFor(fullName)
	if err != nil {
		return "", err
	}
	return path + ".lock", nil
}

// WithLock runs fn while holding an exclusive advisory lock on the
// profile's approval state file, so concurrent tpd processes cannot lose
// each other's approvals. The lock file is a stable sibling of the state
// file — never the state file itself — so locking the renamed path could
// not serialize writers. A store that does not implement Lockable (an
// in-memory overlay, or a decorator that fails to forward LockPath) is
// rejected rather than run unlocked.
func WithLock(store Store, fullName string, fn func() error) error {
	lockable, ok := store.(Lockable)
	if !ok {
		return fmt.Errorf("approval store %T does not support locking; refusing to run the approval transaction unlocked", store)
	}
	lockPath, err := lockable.LockPath(fullName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("locking approval state for %q: %w", fullName, err)
	}
	defer f.Close()
	// flock locks attach to the open file description, so two separate
	// opens of the same lock file contend even within one process.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("locking approval state for %q: %w", fullName, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
