package collector

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"maatplug/internal/catalog"
	"maatplug/internal/model"
	"maatplug/internal/store"
)

type WorkerState struct {
	SchemaVersion  int        `json:"schemaVersion"`
	Status         string     `json:"status"`
	LastAttempt    *time.Time `json:"lastAttempt,omitempty"`
	LastSuccess    *time.Time `json:"lastSuccess,omitempty"`
	LastError      string     `json:"lastError,omitempty"`
	DurationMillis int64      `json:"durationMillis"`
	LastStats      Stats      `json:"lastStats"`
}

type CutoverState struct {
	SchemaVersion        int       `json:"schemaVersion"`
	StartedAt            time.Time `json:"startedAt"`
	EventSchemaVersion   int       `json:"eventSchemaVersion"`
	CatalogSchemaVersion int       `json:"catalogSchemaVersion"`
	StoragePath          string    `json:"storagePath"`
}

func (worker *Collector) workerStatePath() string {
	return filepath.Join(worker.Root, "state", "collector-worker.json")
}
func (worker *Collector) cutoverPath() string {
	return filepath.Join(worker.Root, "state", "format-cutover.json")
}

func (worker *Collector) saveWorkerState(state WorkerState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return store.AtomicWriteFile(worker.workerStatePath(), append(data, '\n'), 0o600)
}

func (worker *Collector) LoadWorkerState() (WorkerState, error) {
	data, err := os.ReadFile(worker.workerStatePath())
	if err != nil {
		return WorkerState{}, err
	}
	var state WorkerState
	if err := json.Unmarshal(data, &state); err != nil {
		return WorkerState{}, err
	}
	return state, nil
}

func (worker *Collector) ensureCutover(now time.Time) error {
	_, err := os.Stat(worker.cutoverPath())
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state := CutoverState{SchemaVersion: 1, StartedAt: now, EventSchemaVersion: model.SchemaVersion, CatalogSchemaVersion: catalog.SchemaVersion, StoragePath: worker.Root}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return store.WithFileLock(worker.cutoverPath(), func() error {
		if _, err := os.Stat(worker.cutoverPath()); err == nil {
			return nil
		}
		return store.AtomicWriteFile(worker.cutoverPath(), append(data, '\n'), 0o600)
	})
}

func (worker *Collector) LoadCutoverState() (CutoverState, error) {
	data, err := os.ReadFile(worker.cutoverPath())
	if err != nil {
		return CutoverState{}, err
	}
	var state CutoverState
	if err := json.Unmarshal(data, &state); err != nil {
		return CutoverState{}, err
	}
	return state, nil
}
