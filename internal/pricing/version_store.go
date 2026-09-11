package pricing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	maatstore "maatplug/internal/store"
)

type VersionStore struct{ Root string }

func (store VersionStore) Save(version Version) error {
	version.Sort()
	if err := version.Validate(); err != nil {
		return err
	}
	if version.BasedOn != "" {
		if version.BasedOn == version.Version {
			return errors.New("pricing version cannot be based on itself")
		}
		if _, err := os.Stat(store.versionPath(version.BasedOn)); err != nil {
			return fmt.Errorf("load basedOn pricing version %q: %w", version.BasedOn, err)
		}
	}
	data, err := json.MarshalIndent(version, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := store.versionPath(version.Version)
	return maatstore.WithFileLock(path, func() error {
		existing, err := os.ReadFile(path)
		if err == nil {
			if bytes.Equal(existing, data) {
				return nil
			}
			return fmt.Errorf("pricing version %q is immutable", version.Version)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return maatstore.AtomicWriteFile(path, data, 0o600)
	})
}

func (store VersionStore) Activate(version string) error {
	if !safeIdentifier(version) {
		return errors.New("pricing version must be a safe identifier")
	}
	activePath := store.activePath()
	return maatstore.WithFileLock(activePath, func() error {
		if _, err := os.Stat(store.versionPath(version)); err != nil {
			return err
		}
		data, _ := json.Marshal(struct {
			Version string `json:"version"`
		}{version})
		return maatstore.AtomicWriteFile(activePath, append(data, '\n'), 0o600)
	})
}

func (store VersionStore) SaveAndActivate(version Version) error {
	if err := store.Save(version); err != nil {
		return err
	}
	return store.Activate(version.Version)
}

func (store VersionStore) Deactivate() error {
	activePath := store.activePath()
	return maatstore.WithFileLock(activePath, func() error {
		if err := os.Remove(activePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

func (store VersionStore) Load(version string) (Version, error) {
	if !safeIdentifier(version) {
		return Version{}, errors.New("pricing version must be a safe identifier")
	}
	data, err := os.ReadFile(store.versionPath(version))
	if err != nil {
		return Version{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result Version
	if err := decoder.Decode(&result); err != nil {
		return Version{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Version{}, errors.New("pricing version contains trailing JSON")
	}
	return result, result.Validate()
}

func (store VersionStore) LoadActive() (Version, bool, error) {
	data, err := os.ReadFile(store.activePath())
	if errors.Is(err, os.ErrNotExist) {
		return Version{}, false, nil
	}
	if err != nil {
		return Version{}, false, err
	}
	var active struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &active); err != nil {
		return Version{}, false, err
	}
	version, err := store.Load(active.Version)
	return version, err == nil, err
}

func (store VersionStore) List() ([]Version, error) {
	entries, err := os.ReadDir(store.versionsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, strings.TrimSuffix(entry.Name(), ".json"))
		}
	}
	sort.Strings(names)
	result := make([]Version, 0, len(names))
	for _, name := range names {
		version, err := store.Load(name)
		if err != nil {
			return nil, err
		}
		result = append(result, version)
	}
	return result, nil
}

func (store VersionStore) versionsDir() string {
	return filepath.Join(store.Root, "pricing", "versions")
}
func (store VersionStore) versionPath(version string) string {
	return filepath.Join(store.versionsDir(), version+".json")
}
func (store VersionStore) activePath() string {
	return filepath.Join(store.Root, "pricing", "active.json")
}
