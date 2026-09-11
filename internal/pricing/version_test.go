package pricing

import (
	"testing"
	"time"
)

func TestResolveEvaluatesPeriodThresholdAndConditions(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	minimum := int64(1000)
	version := testVersion("v1", now, []Rate{
		{ID: "standard", Provider: "codex", Service: "api", Contract: "on-demand", ModelPattern: "gpt-*", UsageClass: "input_uncached", Currency: "USD", Amount: "2", UnitScale: 1_000_000, EffectiveFrom: now, MinQuantity: &minimum, SourceID: "manual"},
	})
	if got := version.Resolve(Query{Provider: "codex", Model: "gpt-x", UsageClass: "input_uncached", Quantity: 2000, At: now}); got.Status != ResolutionConditionUnknown {
		t.Fatalf("missing conditions = %+v", got)
	}
	if got := version.Resolve(Query{Provider: "codex", Service: "api", Contract: "on-demand", Model: "gpt-x", UsageClass: "input_uncached", Quantity: 999, At: now}); got.Status != ResolutionUnregistered {
		t.Fatalf("below threshold = %+v", got)
	}
	if got := version.Resolve(Query{Provider: "codex", Service: "api", Contract: "on-demand", Model: "gpt-x", UsageClass: "input_uncached", Quantity: 2000, At: now}); got.Status != ResolutionResolved || got.Rate.ID != "standard" {
		t.Fatalf("resolved = %+v", got)
	}
}

func TestResolveReportsEqualSpecificityConflict(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	version := testVersion("v1", now, []Rate{
		{ID: "a", Provider: "claude", ModelPattern: "model", UsageClass: "output", Currency: "USD", Amount: "1", UnitScale: 1, EffectiveFrom: now, SourceID: "manual"},
		{ID: "b", Provider: "claude", ModelPattern: "model", UsageClass: "output", Currency: "USD", Amount: "2", UnitScale: 1, EffectiveFrom: now, SourceID: "manual"},
	})
	if got := version.Resolve(Query{Provider: "claude", Model: "model", UsageClass: "output", Quantity: 1, At: now}); got.Status != ResolutionConflict || len(got.Candidates) != 2 {
		t.Fatalf("conflict = %+v", got)
	}
}

func TestVersionStoreIsImmutableAndTracksActiveVersion(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := VersionStore{Root: t.TempDir()}
	version := testVersion("v1", now, []Rate{{ID: "rate", Provider: "codex", ModelPattern: "model", UsageClass: "output", Currency: "USD", Amount: "1", UnitScale: 1, EffectiveFrom: now, SourceID: "manual"}})
	if err := store.SaveAndActivate(version); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := store.LoadActive()
	if err != nil || !ok || loaded.Version != "v1" {
		t.Fatalf("active = %+v, %v, %v", loaded, ok, err)
	}
	version.Rates[0].Amount = "2"
	if err := store.Save(version); err == nil {
		t.Fatal("overwrote immutable version")
	}
	missingBase := testVersion("v2", now, version.Rates)
	missingBase.BasedOn = "missing"
	if err := store.Save(missingBase); err == nil {
		t.Fatal("saved version with missing base")
	}
	if err := store.Deactivate(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.LoadActive(); err != nil || ok {
		t.Fatalf("active remained after deactivate: %v, %v", ok, err)
	}
}

func testVersion(id string, now time.Time, rates []Rate) Version {
	return Version{SchemaVersion: VersionSchema, Version: id, CreatedAt: now, Reason: "test", Sources: []SourceDocument{{ID: "manual", Kind: "manual", RetrievedAt: now}}, Rates: rates}
}
