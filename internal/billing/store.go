package billing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	maatstore "maatplug/internal/store"
)

type Store struct{ Root string }

func (store Store) Save(record Record) error {
	if err := record.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := store.path(record.ID)
	return maatstore.WithFileLock(path, func() error {
		existing, err := os.ReadFile(path)
		if err == nil {
			if bytes.Equal(existing, data) {
				return nil
			}
			return fmt.Errorf("billing record %q is immutable", record.ID)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return maatstore.AtomicWriteFile(path, data, 0o600)
	})
}

func (store Store) SaveAll(records []Record) error {
	seen := map[string]bool{}
	for _, record := range records {
		if seen[record.ID] {
			return fmt.Errorf("duplicate billing record %q in import", record.ID)
		}
		seen[record.ID] = true
		if err := store.Save(record); err != nil {
			return err
		}
	}
	return nil
}

func (store Store) List() ([]Record, error) {
	entries, err := os.ReadDir(store.dir())
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
	result := make([]Record, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(store.path(name))
		if err != nil {
			return nil, err
		}
		var record Record
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, err
		}
		if err := record.Validate(); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, nil
}

func (store Store) dir() string           { return filepath.Join(store.Root, "billing", "records") }
func (store Store) path(id string) string { return filepath.Join(store.dir(), id+".json") }
