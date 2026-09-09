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
