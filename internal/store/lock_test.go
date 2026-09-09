package store

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWithSessionLock_MutualExclusion(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "claude-session-1.jsonl")

	const goroutines = 20
	var (
		wg         sync.WaitGroup
		holders    int32
		maxHolders int32
		counter    int
		lockErrs   int32
	)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := WithSessionLock(sessionPath, func() error {
				n := atomic.AddInt32(&holders, 1)
				for {
					old := atomic.LoadInt32(&maxHolders)
					if n <= old || atomic.CompareAndSwapInt32(&maxHolders, old, n) {
						break
					}
				}
				// A tiny critical section a real race would corrupt if two
				// goroutines were inside it at once.
				counter++
				time.Sleep(2 * time.Millisecond)
				atomic.AddInt32(&holders, -1)
				return nil
			})
			if err != nil {
				atomic.AddInt32(&lockErrs, 1)
			}
		}()
	}
	wg.Wait()

	if lockErrs != 0 {
		t.Fatalf("%d goroutines failed to acquire the lock within the timeout", lockErrs)
	}
	if maxHolders != 1 {
		t.Errorf("max concurrent holders = %d, want 1 (mutual exclusion violated)", maxHolders)
	}
	if counter != goroutines {
		t.Errorf("counter = %d, want %d", counter, goroutines)
	}
	if _, err := os.Stat(lockPath(sessionPath)); !os.IsNotExist(err) {
		t.Errorf("lock file still exists after all holders released it")
	}
}

func TestWithSessionLock_BreaksStaleLock(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "claude-session-1.jsonl")

	// Simulate a lock left behind by a crashed process: create the lock
	// file and backdate its mtime past lockStaleAfter.
	path := lockPath(sessionPath)
	if err := os.WriteFile(path, []byte("12345\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-2 * lockStaleAfter)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatal(err)
	}

	ran := false
	err := WithSessionLock(sessionPath, func() error {
		ran = true
		return nil
	})
	if err != nil {
		t.Fatalf("WithSessionLock() with a stale lock present = %v, want it to break the stale lock and succeed", err)
	}
	if !ran {
		t.Error("fn was not run")
	}
}
