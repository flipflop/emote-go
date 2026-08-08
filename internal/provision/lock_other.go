//go:build !unix

package provision

import (
	"fmt"
	"os"
)

// acquireLock on non-unix platforms falls back to an O_EXCL lockfile.
// Weaker than flock (a crash leaves the file behind); emote ships for
// macOS/Linux first, so this path is best-effort only.
func acquireLock(path string) (func(), error) {
	f, err := os.OpenFile(path+".excl", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("provision: opening lock: %w", err)
	}
	f.Close()
	return func() { os.Remove(path + ".excl") }, nil
}
