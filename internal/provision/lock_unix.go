//go:build unix

package provision

import (
	"fmt"
	"os"
	"syscall"
)

// acquireLock takes an exclusive non-blocking flock on path. It returns a
// release func, or ErrAlreadyRunning if another process holds the lock.
// flock (not lockfiles) so a crashed provisioner never wedges the cache.
func acquireLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("provision: opening lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("provision: locking: %w", err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
