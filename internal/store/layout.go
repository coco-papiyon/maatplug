package store

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	InboxPending    = "pending"
	InboxProcessing = "processing"
	InboxFailed     = "failed"
)

func (s *Store) InboxDir(state string) string {
	return filepath.Join(s.Root, "inbox", state)
}

func (s *Store) StateDir(parts ...string) string {
	all := append([]string{s.Root, "state"}, parts...)
	return filepath.Join(all...)
}

func (s *Store) ObjectsDir() string {
	return filepath.Join(s.Root, "objects", "sha256")
}

func (s *Store) ProjectStreamsDir(projectID string) string {
	return filepath.Join(s.Root, "projects", projectID, "streams")
}

func (s *Store) ProjectCatalogPath(projectID string) string {
	return filepath.Join(s.Root, "projects", projectID, "catalog.json")
}

func (s *Store) ProjectSummariesDir(projectID string) string {
	return filepath.Join(s.Root, "projects", projectID, "summaries")
}

// EnsureEventLayout creates only the durable roots shared by the phase 2
// components. Individual project and object directories are created lazily.
func (s *Store) EnsureEventLayout() error {
	dirs := []string{
		s.InboxDir(InboxPending),
		s.InboxDir(InboxProcessing),
		s.InboxDir(InboxFailed),
		s.ObjectsDir(),
		s.StateDir("checkpoints"),
		s.StateDir("ingest-errors"),
		s.StateDir("journal-recovery"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}
