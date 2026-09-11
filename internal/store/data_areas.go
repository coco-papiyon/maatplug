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
	Default bool   `json:"default"`
	// Path is always a LOCAL directory this area's data can be read
	// straight off disk (a *store.Store{Root: Path} works for every kind).
	// For Kind == DataAreaKindLocal it's exactly what AddDataArea was given
	// (an existing local/mounted directory); for Kind == DataAreaKindSSH
	// it's an auto-computed local cache directory
	// (remoteCacheDir(root, id)) that internal/remotesync keeps mirrored
	// from RemotePath — callers reading project data never need to
	// distinguish the two.
	Path string `json:"path"`
	// Kind selects how this area's data gets onto local disk: "" or
	// DataAreaKindLocal (the historical, only behavior before remote areas
	// existed) reads Path directly; DataAreaKindSSH additionally has
	// internal/remotesync.Poller periodically mirror RemotePath (on
	// SSHHost) into Path.
	Kind string `json:"kind,omitempty"`
	// SSHHost is a Host alias from the user's own ~/.ssh/config — maatplug
	// deliberately never accepts a username or password/key here, only an
	// alias, so auth is whatever that config entry already resolves to
	// (see internal/remotesync's doc comment for why this is the only
	// supported connection method).
	SSHHost string `json:"sshHost,omitempty"`
	// RemotePath is the maatplug data directory on the remote host (i.e.
	// the directory containing ITS OWN data-areas.json/projects/, not a
	// path to a specific project).
	RemotePath string `json:"remotePath,omitempty"`
}

const (
	// DataAreaKindLocal is the default Kind: Path is a directly-readable
	// local (or mounted/UNC) directory. Matches the empty string too, so
	// data-areas.json files written before Kind existed still load as
	// local areas.
	DataAreaKindLocal = "local"
	// DataAreaKindSSH is a remote area synced over SSH — see DataArea's
	// SSHHost/RemotePath doc comments and internal/remotesync.
	DataAreaKindSSH = "ssh"
)

// IsSSH reports whether this area is remote-synced (as opposed to reading
// Path directly).
func (a DataArea) IsSSH() bool {
	return a.Kind == DataAreaKindSSH
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
	area := DataArea{ID: dataAreaID(path), Name: name, Path: path, Kind: DataAreaKindLocal}
	if err := s.saveDataAreas(append(areas[1:], area)); err != nil {
		return DataArea{}, err
	}
	return area, nil
}

// AddSSHDataArea adds a remote maatplug data directory, synced into a local
// cache directory under this Store's own root (see DataArea's Path doc
// comment). It does not itself touch the network — it only reserves the
// cache directory and saves the config; internal/remotesync.Poller (or a
// caller's own immediate internal/remotesync.Sync, for "does this actually
// work" feedback right after adding) does the syncing. sshHost must be a
// Host alias the user has already set up in their own ~/.ssh/config
// (username and key are deliberately not accepted here — see DataArea's
// SSHHost doc comment).
func (s *Store) AddSSHDataArea(name, sshHost, remotePath string) (DataArea, error) {
	sshHost = strings.TrimSpace(sshHost)
	remotePath = strings.TrimSpace(remotePath)
	if sshHost == "" {
		return DataArea{}, errors.New("SSH host is required")
	}
	if remotePath == "" {
		return DataArea{}, errors.New("remote path is required")
	}

	areas, err := s.ListDataAreas()
	if err != nil {
		return DataArea{}, err
	}
	for _, area := range areas {
		if area.IsSSH() && area.SSHHost == sshHost && area.RemotePath == remotePath {
			return DataArea{}, errors.New("data area is already configured")
		}
	}

	name = strings.TrimSpace(name)
	if name == "" {
		name = sshHost + ":" + remotePath
	}

	id := dataAreaID("ssh:" + sshHost + ":" + remotePath)
	cacheDir := remoteCacheDir(s.Root, id)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return DataArea{}, fmt.Errorf("create local cache dir: %w", err)
	}

	area := DataArea{ID: id, Name: name, Path: cacheDir, Kind: DataAreaKindSSH, SSHHost: sshHost, RemotePath: remotePath}
	if err := s.saveDataAreas(append(areas[1:], area)); err != nil {
		return DataArea{}, err
	}
	return area, nil
}

// remoteCacheDir is where an SSH data area's synced copy lives on local
// disk — a fixed, deterministic location under the store's own root (never
// user-chosen, unlike a local area's Path), so it survives process
// restarts and needs no separate bookkeeping to find again.
func remoteCacheDir(root, id string) string {
	return filepath.Join(root, "remote-cache", id)
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

// dataAreaID derives a stable, filesystem-safe id from a unique string —
// AddDataArea passes an already filepath.Abs+Clean'd local path;
// AddSSHDataArea passes an "ssh:<host>:<remotePath>" composite that isn't a
// filesystem path at all, so no path-cleaning happens here (any needed
// there is the caller's job).
func dataAreaID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "area-" + hex.EncodeToString(sum[:])[:12]
}
