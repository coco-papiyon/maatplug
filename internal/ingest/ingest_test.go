package ingest

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"maatplug/internal/store"
)

func TestEnqueueHookIsAtomicAndUnique(t *testing.T) {
	root := t.TempDir()
	inbox := New(root)
	const count = 32
	ids := make(chan string, count)
	var wg sync.WaitGroup
	for index := 0; index < count; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := inbox.EnqueueHook("codex", "Stop", []byte(`{"session_id":"s1"}`))
			if err != nil {
				t.Errorf("EnqueueHook: %v", err)
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Errorf("duplicate receipt ID %q", id)
		}
		seen[id] = true
	}
	entries, err := os.ReadDir((&store.Store{Root: root}).InboxDir(store.InboxPending))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != count {
		t.Fatalf("pending files = %d, want %d", len(entries), count)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			t.Errorf("non-committed inbox file is visible: %s", entry.Name())
		}
	}
}

func TestInboxCapacityAndDiagnostics(t *testing.T) {
	inbox := New(t.TempDir())
	inbox.Limits.MaxActiveRecords = 1
	if _, err := inbox.EnqueueHook("claude", "SessionStart", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if _, err := inbox.EnqueueHook("claude", "Stop", []byte("{}")); err == nil {
		t.Fatal("second record exceeded capacity without error")
	}
	diagnostics, err := inbox.Diagnostics()
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics.Pending != 1 || diagnostics.IngestErrors != 1 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

func TestRecoverStaleProcessing(t *testing.T) {
	inbox := New(t.TempDir())
	inbox.StaleAfter = time.Second
	if _, err := inbox.EnqueueHook("codex", "Stop", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	claim, err := inbox.ClaimNext()
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(claim.Path, old, old); err != nil {
		t.Fatal(err)
	}
	recovered, err := inbox.RecoverStale()
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("recovered = %d, want 1", recovered)
	}
	claim, err = inbox.ClaimNext()
	if err != nil || claim == nil {
		t.Fatalf("reclaimed record = %#v, %v", claim, err)
	}
}

func TestRecoverStaleInterruptedAtomicWrite(t *testing.T) {
	root := t.TempDir()
	inbox := New(root)
	inbox.StaleAfter = time.Second
	layout := &store.Store{Root: root}
	if err := layout.EnsureEventLayout(); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(layout.InboxDir(store.InboxPending), ".receipt.json.tmp-orphan")
	if err := os.WriteFile(temporary, []byte(`{"partial":`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(temporary, old, old); err != nil {
		t.Fatal(err)
	}
	recovered, err := inbox.RecoverStale()
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("recovered = %d, want 1", recovered)
	}
	diagnostics, err := inbox.Diagnostics()
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics.Temporary != 0 || diagnostics.IngestErrors != 1 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	failed, err := os.ReadDir(layout.InboxDir(store.InboxFailed))
	if err != nil || len(failed) != 1 {
		t.Fatalf("failed entries = %v, %v", failed, err)
	}
}
