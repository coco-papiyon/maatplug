package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DataArea is a maatplug data directory that can be included in the web
// dashboard. The default area is always the Store's own root.
type DataArea struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Default bool   `json:"default"`
}

type dataAreasFile struct {
	Areas []DataArea `json:"areas"`
}

const dataAreasFileName = "data-areas.json"

func (s *Store) defaultDataArea() DataArea {
	return DataArea{ID: "default", Name: "Default", Path: filepath.Clean(s.Root), Default: true}
}

// ListDataAreas returns the configured dashboard data areas. Older installs
// have no settings file, so they transparently get the same single area they
// used before data-area support was added.
func (s *Store) ListDataAreas() ([]DataArea, error) {
	defaultArea := s.defaultDataArea()
	b, err := os.ReadFile(filepath.Join(s.Root, dataAreasFileName))
	if errors.Is(err, os.ErrNotExist) {
		return []DataArea{defaultArea}, nil
	}
	if err != nil {
		return nil, err
	}
	var file dataAreasFile
	if err := json.Unmarshal(b, &file); err != nil {
		return nil, fmt.Errorf("decode %s: %w", dataAreasFileName, err)
	}

	areas := make([]DataArea, 0, len(file.Areas)+1)
	areas = append(areas, defaultArea)
	seen := map[string]bool{defaultArea.Path: true}
	for _, area := range file.Areas {
		if area.Default || area.ID == "default" {
			continue
		}
		area.Path = filepath.Clean(area.Path)
		if area.ID == "" || area.Name == "" || area.Path == "" || seen[area.Path] {
			continue
		}
		seen[area.Path] = true
		areas = append(areas, area)
	}
	return areas, nil
}

// AddDataArea adds an existing directory to the dashboard's read-only view.
// The directory may be local, a mounted volume, or a UNC/network path.
func (s *Store) AddDataArea(name, path string) (DataArea, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return DataArea{}, errors.New("data area path is required")
	}
	var err error
	path, err = filepath.Abs(filepath.Clean(path))
	if err != nil {
		return DataArea{}, fmt.Errorf("resolve data area path: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return DataArea{}, fmt.Errorf("stat data area: %w", err)
	}
	if !info.IsDir() {
		return DataArea{}, errors.New("data area path is not a directory")
	}

	areas, err := s.ListDataAreas()
	if err != nil {
		return DataArea{}, err
	}
	for _, area := range areas {
		if filepath.Clean(area.Path) == path {
			return DataArea{}, errors.New("data area is already configured")
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = filepath.Base(filepath.Clean(path))
		if name == "" || name == "." || name == string(filepath.Separator) {
			name = path
		}
	}
	area := DataArea{ID: dataAreaID(path), Name: name, Path: path}
	if err := s.saveDataAreas(append(areas[1:], area)); err != nil {
		return DataArea{}, err
	}
	return area, nil
}

// RemoveDataArea removes an additional configured area. The default area is
// intentionally protected because it is the current maatplug data location.
func (s *Store) RemoveDataArea(id string) error {
	if id == "" || id == "default" {
		return errors.New("the default data area cannot be removed")
	}
	areas, err := s.ListDataAreas()
	if err != nil {
		return err
	}
	filtered := make([]DataArea, 0, len(areas)-1)
	found := false
	for _, area := range areas {
		if area.ID == id {
			found = true
			continue
		}
		if !area.Default {
			filtered = append(filtered, area)
		}
	}
	if !found {
		return os.ErrNotExist
	}
	return s.saveDataAreas(filtered)
}

func (s *Store) saveDataAreas(areas []DataArea) error {
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(dataAreasFile{Areas: areas}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Root, dataAreasFileName), append(b, '\n'), 0o644)
}

func dataAreaID(path string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(path)))
	return "area-" + hex.EncodeToString(sum[:])[:12]
}
