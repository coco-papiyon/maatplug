package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"maatplug/internal/pricing"
)

func fakeProvider(name string, models []pricing.ModelPricing, err error) provider {
	return provider{
		name: name,
		fetch: func(context.Context) ([]pricing.ModelPricing, error) {
			return models, err
		},
	}
}

// TestRun_SuccessfulFetchReplacesProviderData is the ordinary path: a fetch
// that succeeds replaces whatever was previously on record for that
// provider.
func TestRun_SuccessfulFetchReplacesProviderData(t *testing.T) {
	out := filepath.Join(t.TempDir(), "pricing.json")
	fresh := []pricing.ModelPricing{{Provider: "claude", Model: "claude-sonnet-5", InputPerMillion: 2}}

	if err := run(out, time.Second, []provider{fakeProvider("claude", fresh, nil)}); err != nil {
		t.Fatal(err)
	}

	doc, err := pricing.Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Models) != 1 || doc.Models[0].Model != "claude-sonnet-5" || doc.Models[0].InputPerMillion != 2 {
		t.Errorf("doc.Models = %+v, want the freshly fetched entry", doc.Models)
	}
	if doc.GeneratedAt.IsZero() {
		t.Error("GeneratedAt is zero after a successful fetch")
	}
}

func TestRun_FreshPricingSkipsFetch(t *testing.T) {
	out := filepath.Join(t.TempDir(), "pricing.json")
	generatedAt := time.Now().UTC().Add(-time.Hour)
	previous := pricing.Document{
		GeneratedAt: generatedAt,
		Models:      []pricing.ModelPricing{{Provider: "claude", Model: "old-model"}},
	}
	if err := pricing.Save(out, previous); err != nil {
		t.Fatal(err)
	}

	called := false
	providers := []provider{{name: "claude", fetch: func(context.Context) ([]pricing.ModelPricing, error) {
		called = true
		return []pricing.ModelPricing{{Provider: "claude", Model: "new-model"}}, nil
	}}}
	if err := run(out, time.Second, providers); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("fetch was called for pricing generated less than 24 hours ago")
	}
	doc, err := pricing.Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if !doc.GeneratedAt.Equal(generatedAt) || len(doc.Models) != 1 || doc.Models[0].Model != "old-model" {
		t.Errorf("doc = %+v, want the unchanged fresh document", doc)
	}
}

func TestRun_StalePricingUpdatesGeneratedAtAfterFetch(t *testing.T) {
	out := filepath.Join(t.TempDir(), "pricing.json")
	old := time.Now().UTC().Add(-25 * time.Hour)
	if err := pricing.Save(out, pricing.Document{GeneratedAt: old}); err != nil {
		t.Fatal(err)
	}

	if err := run(out, time.Second, []provider{fakeProvider("claude", []pricing.ModelPricing{{Provider: "claude", Model: "fresh-model"}}, nil)}); err != nil {
		t.Fatal(err)
	}
	doc, err := pricing.Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if !doc.GeneratedAt.After(old) {
		t.Errorf("GeneratedAt = %v, want it updated after stale pricing was fetched", doc.GeneratedAt)
	}
}

// TestRun_FailedFetchKeepsPreviousData is the user-specified fallback
// policy: if a fetch fails, keep whatever pricing was already on record for
// that provider rather than wiping it out.
func TestRun_FailedFetchKeepsPreviousData(t *testing.T) {
	out := filepath.Join(t.TempDir(), "pricing.json")
	previous := pricing.Document{
		Models: []pricing.ModelPricing{{Provider: "claude", Model: "claude-sonnet-5", InputPerMillion: 2}},
	}
	if err := pricing.Save(out, previous); err != nil {
		t.Fatal(err)
	}

	err := run(out, time.Second, []provider{fakeProvider("claude", nil, errors.New("page structure changed"))})
	if err != nil {
		t.Fatal(err) // a fetch failure must not fail the whole run
	}

	doc, err := pricing.Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Models) != 1 || doc.Models[0].Model != "claude-sonnet-5" || doc.Models[0].InputPerMillion != 2 {
		t.Errorf("doc.Models = %+v, want the previous entry preserved", doc.Models)
	}
}

// TestRun_FailedFetchWithNoPreviousDataStaysEmpty covers the other half of
// the user-specified policy: if there was never any previous data for a
// provider either, it stays empty rather than erroring out the run.
func TestRun_FailedFetchWithNoPreviousDataStaysEmpty(t *testing.T) {
	out := filepath.Join(t.TempDir(), "pricing.json") // no pre-existing file

	err := run(out, time.Second, []provider{fakeProvider("claude", nil, errors.New("network error"))})
	if err != nil {
		t.Fatal(err)
	}

	doc, err := pricing.Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Models) != 0 {
		t.Errorf("doc.Models = %+v, want empty", doc.Models)
	}
}

// TestRun_MixedProviders_OneSucceedsOneFails confirms each provider's
// success/failure is independent — one provider's fetch failure must not
// affect another provider's freshly fetched data.
func TestRun_MixedProviders_OneSucceedsOneFails(t *testing.T) {
	out := filepath.Join(t.TempDir(), "pricing.json")
	previous := pricing.Document{
		Models: []pricing.ModelPricing{{Provider: "codex", Model: "gpt-5.6-sol", InputPerMillion: 4}},
	}
	if err := pricing.Save(out, previous); err != nil {
		t.Fatal(err)
	}

	fresh := []pricing.ModelPricing{{Provider: "claude", Model: "claude-sonnet-5", InputPerMillion: 2}}
	providers := []provider{
		fakeProvider("claude", fresh, nil),
		fakeProvider("codex", nil, errors.New("page structure changed")),
	}
	if err := run(out, time.Second, providers); err != nil {
		t.Fatal(err)
	}

	doc, err := pricing.Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Models) != 2 {
		t.Fatalf("doc.Models = %+v, want 2 entries (fresh claude + preserved codex)", doc.Models)
	}
	if doc.Models[0].Model != "claude-sonnet-5" {
		t.Errorf("Models[0] = %+v, want the freshly fetched claude entry", doc.Models[0])
	}
	if doc.Models[1].Model != "gpt-5.6-sol" {
		t.Errorf("Models[1] = %+v, want the preserved codex entry", doc.Models[1])
	}
}
