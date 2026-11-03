package pricing

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRefreshWorkerPersistsSuccessFailureAndFreshness(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	price := 2.0
	worker := &RefreshWorker{Root: root, Interval: time.Hour, Timeout: time.Second, Now: func() time.Time { return now }, Sources: []FetchSource{
		{ID: "good", Provider: "codex", Kind: "fixture", Fetch: func(context.Context) ([]ModelPricing, error) {
			return []ModelPricing{{Provider: "codex", Model: "model", InputPerMillion: price, CachedInputPerMillion: 0.5, OutputPerMillion: 10}}, nil
		}},
		{ID: "bad", Provider: "claude", Kind: "fixture", Fetch: func(context.Context) ([]ModelPricing, error) {
			return nil, errors.New("offline")
		}},
	}}
	stats, err := worker.RefreshDue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Attempted != 2 || stats.Succeeded != 1 || stats.Failed != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	active, ok, err := (VersionStore{Root: root}).LoadActive()
	// 3 usage classes (input/cached input/output) x 2 rates each (the bare
	// model pattern plus a dated-snapshot variant — see RatesFromModels).
	if err != nil || !ok || len(active.Rates) != 6 {
		t.Fatalf("active = %+v, %v, %v", active, ok, err)
	}
	bad, err := worker.loadState(worker.Sources[1])
	if err != nil || bad.Complete || bad.LastAttempt == nil || bad.LastError != "offline" {
		t.Fatalf("bad state = %+v, %v", bad, err)
	}
	stats, err = worker.RefreshDue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Skipped != 1 || stats.Attempted != 1 {
		t.Fatalf("freshness stats = %+v", stats)
	}
	if _, err := os.Stat(worker.statePath("good")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	price = 3
	stats, err = worker.RefreshDue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	active, ok, err = (VersionStore{Root: root}).LoadActive()
	closed := 0
	for _, rate := range active.Rates {
		if rate.EffectiveTo != nil {
			closed++
		}
	}
	if err != nil || !ok || len(active.Rates) != 12 || closed != 6 {
		t.Fatalf("historical rates = %+v, %v, %v", active, ok, err)
	}
	if len(active.Sources) != 1 || active.Sources[0].Digest == "" {
		t.Fatalf("source evidence = %+v", active.Sources)
	}
	snapshotPath := filepath.Join(root, "pricing", "sources", "good", active.Sources[0].Digest+".json")
	if _, err := os.Stat(snapshotPath); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshWorkerDoesNotActivateIncompleteAcquisition(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	source := FetchSource{ID: "partial", Provider: "codex", Kind: "fixture", Fetch: func(context.Context) ([]ModelPricing, error) {
		return []ModelPricing{{Provider: "codex", Model: "model", InputPerMillion: 2, OutputPerMillion: 10, AcquisitionIssues: []string{"cache_read_input: fixture failure"}}}, nil
	}}
	worker := &RefreshWorker{Root: root, Interval: time.Hour, Timeout: time.Second, Now: func() time.Time { return now }, Sources: []FetchSource{source}}
	stats, err := worker.RefreshDue(context.Background())
	if err != nil || stats.Failed != 1 || stats.Succeeded != 0 {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
	if _, ok, err := (VersionStore{Root: root}).LoadActive(); err != nil || ok {
		t.Fatalf("incomplete pricing became active: %v, %v", ok, err)
	}
	state, err := worker.loadState(source)
	if err != nil || state.Complete || state.LastError == "" {
		t.Fatalf("state = %+v, %v", state, err)
	}
}
