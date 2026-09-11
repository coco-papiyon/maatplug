package pricing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	maatstore "maatplug/internal/store"
)

type FetchSource struct {
	ID       string
	Provider string
	Kind     string
	URL      string
	Fetch    func(context.Context) ([]ModelPricing, error)
}

type SourceState struct {
	SourceID      string     `json:"sourceId"`
	Provider      string     `json:"provider"`
	LastAttempt   *time.Time `json:"lastAttempt,omitempty"`
	LastSuccess   *time.Time `json:"lastSuccess,omitempty"`
	FreshUntil    *time.Time `json:"freshUntil,omitempty"`
	Complete      bool       `json:"complete"`
	LastError     string     `json:"lastError,omitempty"`
	ActiveVersion string     `json:"activeVersion,omitempty"`
}

type RefreshStats struct {
	Attempted int `json:"attempted"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Skipped   int `json:"skipped"`
}

type RefreshWorker struct {
	Root     string
	Interval time.Duration
	Timeout  time.Duration
	Sources  []FetchSource
	Now      func() time.Time
}

func DefaultRefreshWorker(root string) *RefreshWorker {
	return &RefreshWorker{Root: root, Interval: 24 * time.Hour, Timeout: 20 * time.Second, Now: time.Now, Sources: []FetchSource{
		{ID: "anthropic-public-pricing", Provider: "claude", Kind: "public_page", URL: ClaudeSourceURL, Fetch: FetchClaude},
		{ID: "openai-public-pricing", Provider: "codex", Kind: "public_page", URL: CodexSourceURL, Fetch: FetchCodex},
	}}
}

// RunDue implements collector.MaintenanceJob. Provider fetch failures are
// durable source state, not collection failures. Filesystem failures return.
func (worker *RefreshWorker) RunDue(ctx context.Context) (bool, error) {
	stats, err := worker.RefreshDue(ctx)
	return stats.Attempted > 0, err
}

func (worker *RefreshWorker) RefreshDue(ctx context.Context) (RefreshStats, error) {
	var stats RefreshStats
	if worker.Now == nil {
		worker.Now = time.Now
	}
	if worker.Interval <= 0 {
		worker.Interval = 24 * time.Hour
	}
	if worker.Timeout <= 0 {
		worker.Timeout = 20 * time.Second
	}
	store := VersionStore{Root: worker.Root}
	for _, source := range worker.Sources {
		if !safeIdentifier(source.ID) || source.Provider == "" || source.Kind == "" || source.Fetch == nil {
			return stats, fmt.Errorf("invalid pricing source %q", source.ID)
		}
		state, err := worker.loadState(source)
		if err != nil {
			return stats, err
		}
		now := worker.Now().UTC()
		if state.FreshUntil != nil && now.Before(*state.FreshUntil) {
			stats.Skipped++
			continue
		}
		stats.Attempted++
		state.LastAttempt = &now
		fetchCtx, cancel := context.WithTimeout(ctx, worker.Timeout)
		models, fetchErr := source.Fetch(fetchCtx)
		cancel()
		if fetchErr != nil || len(models) == 0 {
			stats.Failed++
			state.Complete = false
			if fetchErr != nil {
				state.LastError = fetchErr.Error()
			} else {
				state.LastError = "source returned no rates"
			}
			if err := worker.saveState(state); err != nil {
				return stats, err
			}
			continue
		}
		digest, err := (SourceStore{Root: worker.Root}).Save(SourceSnapshot{SchemaVersion: VersionSchema, SourceID: source.ID, Provider: source.Provider, Kind: source.Kind, URL: source.URL, RetrievedAt: now, Models: models})
		if err != nil {
			return stats, err
		}
		if missing := MissingRateFields(models, source.Provider); len(missing) > 0 {
			stats.Failed++
			state.Complete = false
			state.LastError = "incomplete pricing fields: " + strings.Join(missing, ", ")
			if err := worker.saveState(state); err != nil {
				return stats, err
			}
			continue
		}
		active, hasActive, err := store.LoadActive()
		if err != nil {
			return stats, err
		}
		version := Version{SchemaVersion: VersionSchema, CreatedAt: now, Reason: "periodic refresh from " + source.ID}
		if hasActive {
			version.BasedOn = active.Version
			for _, existingSource := range active.Sources {
				if existingSource.ID != source.ID {
					version.Sources = append(version.Sources, existingSource)
				}
			}
			for _, rate := range active.Rates {
				if rate.SourceID == source.ID && rate.Provider == source.Provider && rate.EffectiveTo == nil {
					closedAt := now
					rate.EffectiveTo = &closedAt
				}
				version.Rates = append(version.Rates, rate)
			}
		}
		version.Sources = append(version.Sources, SourceDocument{ID: source.ID, Kind: source.Kind, URL: source.URL, RetrievedAt: now, Digest: digest})
		version.Rates = append(version.Rates, RatesFromModels(models, source.ID, now)...)
		version.Version = generatedVersionID(now, source.ID, version.Rates)
		if err := store.SaveAndActivate(version); err != nil {
			return stats, err
		}
		freshUntil := now.Add(worker.Interval)
		state.LastSuccess, state.FreshUntil = &now, &freshUntil
		state.Complete, state.LastError, state.ActiveVersion = true, "", version.Version
		if err := worker.saveState(state); err != nil {
			return stats, err
		}
		stats.Succeeded++
	}
	return stats, nil
}

func (worker *RefreshWorker) loadState(source FetchSource) (SourceState, error) {
	state := SourceState{SourceID: source.ID, Provider: source.Provider}
	data, err := os.ReadFile(worker.statePath(source.ID))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	return state, nil
}

func (worker *RefreshWorker) saveState(state SourceState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return maatstore.AtomicWriteFile(worker.statePath(state.SourceID), append(data, '\n'), 0o600)
}

func ListSourceStates(root string) ([]SourceState, error) {
	dir := filepath.Join(root, "pricing", "fetch-state")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	states := []SourceState{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var state SourceState
		if err := json.Unmarshal(data, &state); err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].SourceID < states[j].SourceID })
	return states, nil
}

func (worker *RefreshWorker) statePath(sourceID string) string {
	if !safeIdentifier(sourceID) {
		return filepath.Join(worker.Root, "pricing", "fetch-state", "invalid.json")
	}
	return filepath.Join(worker.Root, "pricing", "fetch-state", sourceID+".json")
}

func generatedVersionID(now time.Time, source string, rates []Rate) string {
	sort.Slice(rates, func(i, j int) bool { return rates[i].ID < rates[j].ID })
	data, _ := json.Marshal(rates)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("auto-%s-%s-%s", now.Format("20060102T150405Z"), source, hex.EncodeToString(sum[:])[:8])
}
