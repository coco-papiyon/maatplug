package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureEventLayout(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if err := s.EnsureEventLayout(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		s.InboxDir(InboxPending),
		s.InboxDir(InboxProcessing),
		s.InboxDir(InboxFailed),
		s.ObjectsDir(),
		s.StateDir("checkpoints"),
	} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Errorf("%s was not created as a directory: %v", path, err)
		}
	}
	if got := s.ProjectCatalogPath("p1"); got != filepath.Join(s.Root, "projects", "p1", "catalog.json") {
		t.Errorf("ProjectCatalogPath = %q", got)
	}
}

func TestAtomicWriteFileReplacesCompleteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "value.json")
	if err := AtomicWriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteFile(path, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" {
		t.Fatalf("content = %q, want second", data)
	}
}
