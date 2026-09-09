package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	lockRetryInterval  = 50 * time.Millisecond
	lockAcquireTimeout = 10 * time.Second // generous vs. how long a Stop handler normally takes
	lockStaleAfter     = 30 * time.Second // longer than any legitimate hold; assume a crashed holder past this
)

// WithSessionLock runs fn while holding an exclusive, cross-process lock
// scoped to sessionPath.
//
// maatplug hook processes are short-lived (docs/design.md section 3.1), and
// under concurrent multi-terminal session resume (or a queued follow-up
// message resolved together with an earlier instruction, see
// internal/store.FindUnresolvedInstructions) more than one of them can be
// acting on the same session file around the same time. Without this lock,
// two Stop processes that both read the session file before either has
// written back could both decide the same instruction(s) need a turn_end and
// both append one, breaking the "at most one turn_end per instructionId"
// invariant the rest of the schema relies on. Any caller that reads the
// session file to decide what to append should go through this lock.
//
// The lock is a plain file created with O_EXCL, which is atomic on both
// Windows and POSIX (no cgo/syscall-specific locking needed). A lock file
// older than lockStaleAfter is assumed abandoned by a crashed process and is
// broken rather than waited on forever.
func WithSessionLock(sessionPath string, fn func() error) error {
	path := lockPath(sessionPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create lock dir: %w", err)
	}

	if err := acquireLock(path); err != nil {
		return err
	}
	defer os.Remove(path)

	return fn()
}

func lockPath(sessionPath string) string {
	return sessionPath + ".lock"
}

func acquireLock(path string) error {
	deadline := time.Now().Add(lockAcquireTimeout)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create lock file %s: %w", path, err)
		}

		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > lockStaleAfter {
			os.Remove(path) // best-effort: break a stale lock left by a crashed process
			continue
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for lock on %s", path)
		}
		time.Sleep(lockRetryInterval)
	}
}
