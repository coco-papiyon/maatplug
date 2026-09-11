package pricing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	maatstore "maatplug/internal/store"
)

type SourceSnapshot struct {
	SchemaVersion int            `json:"schemaVersion"`
	SourceID      string         `json:"sourceId"`
	Provider      string         `json:"provider"`
	Kind          string         `json:"kind"`
	URL           string         `json:"url,omitempty"`
	RetrievedAt   time.Time      `json:"retrievedAt"`
	Models        []ModelPricing `json:"models"`
}

type SourceStore struct{ Root string }

// Save stores the provider fetcher's parsed acquisition result before it is
// transformed into common rates. The returned digest identifies the exact
// source snapshot referenced by a pricing version.
func (store SourceStore) Save(snapshot SourceSnapshot) (string, error) {
	if snapshot.SchemaVersion != VersionSchema || !safeIdentifier(snapshot.SourceID) || snapshot.Provider == "" || snapshot.Kind == "" || snapshot.RetrievedAt.IsZero() || len(snapshot.Models) == 0 {
		return "", errors.New("invalid pricing source snapshot")
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	digestBytes := sha256.Sum256(data)
	digest := hex.EncodeToString(digestBytes[:])
	path := filepath.Join(store.Root, "pricing", "sources", snapshot.SourceID, digest+".json")
	err = maatstore.WithFileLock(path, func() error {
		if existing, err := os.ReadFile(path); err == nil {
			if string(existing) != string(data) {
				return errors.New("pricing source digest collision")
			}
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return maatstore.AtomicWriteFile(path, data, 0o600)
	})
	if err != nil {
		return "", err
	}
	return digest, nil
}
