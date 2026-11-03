package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDataAreas_DefaultAddAndRemove(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}

	areas, err := s.ListDataAreas()
	if err != nil {
		t.Fatal(err)
	}
	if len(areas) != 1 || !areas[0].Default || areas[0].Path != filepath.Clean(root) {
		t.Fatalf("default areas = %+v", areas)
	}

	other := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	added, err := s.AddDataArea("Shared server", other)
	if err != nil {
		t.Fatal(err)
	}
	if added.Default || added.Name != "Shared server" || added.Path != other {
		t.Errorf("added area = %+v", added)
	}

	areas, err = s.ListDataAreas()
	if err != nil {
		t.Fatal(err)
	}
	if len(areas) != 2 || areas[1].ID != added.ID {
		t.Fatalf("areas after add = %+v", areas)
	}
	if err := s.RemoveDataArea(added.ID); err != nil {
		t.Fatal(err)
	}
	areas, err = s.ListDataAreas()
	if err != nil {
		t.Fatal(err)
	}
	if len(areas) != 1 || !areas[0].Default {
		t.Fatalf("areas after remove = %+v", areas)
	}
}

func TestDataAreas_DefaultCannotBeRemoved(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if err := s.RemoveDataArea("default"); err == nil {
		t.Fatal("RemoveDataArea(default) succeeded, want an error")
	}
}

func TestDataAreaMetadataPersistsAcrossAddRemoveAndRestart(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if _, err := s.UpdateDataAreaMetadata("default", " My PC ", " Alice "); err != nil {
		t.Fatal(err)
	}
	local, err := s.AddDataArea("Shared", t.TempDir(), " Bob ")
	if err != nil {
		t.Fatal(err)
	}
	ssh, err := s.AddSSHDataArea("Server", "existing-alias", "/data/maatplug", "Carol")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.UpdateDataAreaMetadata(ssh.ID, "Build server", "Dave")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Path != ssh.Path || updated.SSHHost != ssh.SSHHost || updated.RemotePath != ssh.RemotePath {
		t.Fatal("metadata update changed connection settings")
	}
	if err := s.RemoveDataArea(local.ID); err != nil {
		t.Fatal(err)
	}
	areas, err := (&Store{Root: s.Root}).ListDataAreas()
	if err != nil {
		t.Fatal(err)
	}
	if len(areas) != 2 || areas[0].Name != "My PC" || areas[0].UserName != "Alice" || areas[0].Path != s.Root || areas[1].UserName != "Dave" {
		t.Fatalf("areas = %+v", areas)
	}
	if _, err := s.UpdateDataAreaMetadata("default", "My PC", ""); err != nil {
		t.Fatal(err)
	}
	areas, err = s.ListDataAreas()
	if err != nil || areas[0].UserName != "" {
		t.Fatal("username could not be cleared")
	}
}

func TestDataAreas_AddSSH(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}

	added, err := s.AddSSHDataArea("Build box", "myserver", "/home/me/.config/maatplug")
	if err != nil {
		t.Fatal(err)
	}
	if !added.IsSSH() || added.SSHHost != "myserver" || added.RemotePath != "/home/me/.config/maatplug" {
		t.Fatalf("added area = %+v", added)
	}
	// Path must be a real, existing LOCAL directory under our own root —
	// aggregate.All etc. read every area the same way via *store.Store{Root:
	// area.Path}, regardless of Kind.
	wantCacheDir := filepath.Join(root, "remote-cache", added.ID)
	if added.Path != wantCacheDir {
		t.Errorf("Path = %q, want %q", added.Path, wantCacheDir)
	}
	if info, err := os.Stat(added.Path); err != nil || !info.IsDir() {
		t.Errorf("cache dir %q does not exist as a directory: %v", added.Path, err)
	}

	areas, err := s.ListDataAreas()
	if err != nil {
		t.Fatal(err)
	}
	if len(areas) != 2 || areas[1].ID != added.ID {
		t.Fatalf("areas after add = %+v", areas)
	}

	// Adding the exact same host+remote path again must be rejected.
	if _, err := s.AddSSHDataArea("Build box again", "myserver", "/home/me/.config/maatplug"); err == nil {
		t.Error("AddSSHDataArea duplicate host+remotePath succeeded, want an error")
	}
}

func TestDataAreas_AddSSHRequiresHostAndPath(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if _, err := s.AddSSHDataArea("x", "", "/remote"); err == nil {
		t.Error("AddSSHDataArea with empty host succeeded, want an error")
	}
	if _, err := s.AddSSHDataArea("x", "myserver", ""); err == nil {
		t.Error("AddSSHDataArea with empty remote path succeeded, want an error")
	}
}
