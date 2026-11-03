package cost

import (
	"encoding/json"
	"testing"
	"time"

	"maatplug/internal/model"
	"maatplug/internal/pricing"
	"maatplug/internal/reconcile"
)

func TestBuildUsesEachCallsModelAndKeepsMissingRateUnknown(t *testing.T) {
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	events := []model.EventEnvelope{modelCall("call-a", "model-a", now), modelCall("call-b", "model-b", now), modelCall("call-c", "unknown", now)}
	usage := reconcile.Result{InputEventIDs: []model.NamespacedID{{Namespace: "fixture", Value: "source"}}, Decisions: []reconcile.Decision{
		usageDecision("usage-a", "call-a", now, 1_000_000), usageDecision("usage-b", "call-b", now, 1_000_000), usageDecision("usage-c", "call-c", now, 1_000_000),
	}}
	version := pricing.Version{SchemaVersion: pricing.VersionSchema, Version: "v1", CreatedAt: now, Reason: "fixture", Sources: []pricing.SourceDocument{{ID: "source", Kind: "manual", RetrievedAt: now}}, Rates: []pricing.Rate{
		{ID: "model-a-input", Provider: "codex", ModelPattern: "model-a", UsageClass: "input_uncached", Currency: "USD", Amount: "2", UnitScale: 1_000_000, EffectiveFrom: now.Add(-time.Hour), SourceID: "source"},
		{ID: "model-b-input", Provider: "codex", ModelPattern: "model-b", UsageClass: "input_uncached", Currency: "USD", Amount: "5", UnitScale: 1_000_000, EffectiveFrom: now.Add(-time.Hour), SourceID: "source"},
	}}
	result, err := Build(events, usage, &version)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 3 || len(result.KnownTotals) != 1 || result.KnownTotals[0].Amount != "7" || !result.MissingCost {
		t.Fatalf("result = %+v", result)
	}
	if result.Records[2].Amount != nil || result.Records[2].Status != "incomplete" {
		t.Fatalf("missing rate became a price: %+v", result.Records[2])
	}
}

func TestBuildReproducesOldVersionAndComparesCurrentVersion(t *testing.T) {
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	events := []model.EventEnvelope{modelCall("call", "model", now)}
	usage := reconcile.Result{Decisions: []reconcile.Decision{usageDecision("usage", "call", now, 1_000_000)}}
	old := costVersion("old", "2", now)
	current := costVersion("current", "3", now)
	oldResult, err := Build(events, usage, &old)
	if err != nil {
		t.Fatal(err)
	}
	currentResult, err := Build(events, usage, &current)
	if err != nil {
		t.Fatal(err)
	}
	if *oldResult.Records[0].Amount != "2" || *currentResult.Records[0].Amount != "3" || len(Compare(oldResult, currentResult).ChangedUsage) != 1 {
		t.Fatalf("old=%+v current=%+v", oldResult, currentResult)
	}
}

func TestBuildCanCompareHistoricalUsageAtCurrentConditions(t *testing.T) {
	usedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	currentAt := usedAt.Add(24 * time.Hour)
	events := []model.EventEnvelope{modelCall("call", "model", usedAt)}
	usage := reconcile.Result{Decisions: []reconcile.Decision{usageDecision("usage", "call", usedAt, 1_000_000)}}
	// An actual price change (old rate closed out when new took over at
	// currentAt) — not just "no earlier rate exists yet" (see
	// TestBuild_FallsBackToEarliestRateForUsageOlderThanAnyPricingHistory
	// for that case), so this only exercises the deliberate as-then vs.
	// as-now comparison the PricingAt override is for.
	version := pricing.Version{SchemaVersion: pricing.VersionSchema, Version: "v1", CreatedAt: currentAt, Reason: "test", Sources: []pricing.SourceDocument{{ID: "source", Kind: "manual", RetrievedAt: currentAt}}, Rates: []pricing.Rate{
		{ID: "old-rate", Provider: "codex", ModelPattern: "model", UsageClass: "input_uncached", Currency: "USD", Amount: "2", UnitScale: 1_000_000, EffectiveFrom: usedAt.Add(-time.Hour), EffectiveTo: &currentAt, SourceID: "source"},
		{ID: "new-rate", Provider: "codex", ModelPattern: "model", UsageClass: "input_uncached", Currency: "USD", Amount: "4", UnitScale: 1_000_000, EffectiveFrom: currentAt, SourceID: "source"},
	}}
	withoutOverride, err := Build(events, usage, &version)
	if err != nil {
		t.Fatal(err)
	}
	if withoutOverride.Records[0].Amount == nil || *withoutOverride.Records[0].Amount != "2" {
		t.Fatalf("historical calculation should use the rate that actually covered usedAt: %+v", withoutOverride)
	}
	withOverride, err := BuildWithOptions(events, usage, &version, Options{PricingAt: &currentAt})
	if err != nil || withOverride.Records[0].Amount == nil || *withOverride.Records[0].Amount != "4" {
		t.Fatalf("current comparison = %+v, %v", withOverride, err)
	}
	if withoutOverride.CalculationID == withOverride.CalculationID {
		t.Fatal("historical and current-condition calculations share an ID")
	}
}

func TestBuild_FallsBackToEarliestRateForUsageOlderThanAnyPricingHistory(t *testing.T) {
	// Usage from before maatplug ever fetched a price for this model (e.g.
	// right after the data directory was reset and pricing was fetched for
	// the first time) has no earlier rate to apply — Build falls back to
	// the earliest one on record (see pricing.earliestApplicable) instead
	// of leaving it permanently unpriced.
	firstFetch := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	usedAt := firstFetch.Add(-48 * time.Hour)
	events := []model.EventEnvelope{modelCall("call", "model", usedAt)}
	usage := reconcile.Result{Decisions: []reconcile.Decision{usageDecision("usage", "call", usedAt, 1_000_000)}}
	version := costVersion("v1", "4", firstFetch.Add(time.Hour))
	result, err := Build(events, usage, &version)
	if err != nil {
		t.Fatal(err)
	}
	if result.Records[0].Status != "calculated" || result.Records[0].Amount == nil || *result.Records[0].Amount != "4" {
		t.Fatalf("result = %+v, want the earliest rate applied to pre-history usage", result)
	}
}

func TestBuildDoesNotRequireRateForZeroQuantityBucket(t *testing.T) {
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	events := []model.EventEnvelope{modelCall("call", "model", now)}
	zero := int64(0)
	million := int64(1_000_000)
	decision := reconcile.Decision{
		EventID:   model.NamespacedID{Namespace: "fixture/event", Value: "usage"},
		Usage:     model.EntityRef{Kind: model.EntityUsage, ID: model.NamespacedID{Namespace: "fixture/usage", Value: "usage"}},
		AppliesTo: model.EntityRef{Kind: model.EntityModelCall, ID: model.NamespacedID{Namespace: "fixture/call", Value: "call"}},
		Role:      reconcile.RoleCounted,
		Normalized: &model.NormalizedUsage{ExclusiveBuckets: []model.UsageBucket{
			{Name: "input_uncached", Tokens: model.Quantity{Value: &million, Availability: model.AvailabilityObserved}},
			{Name: "cache_write_input", Tokens: model.Quantity{Value: &zero, Availability: model.AvailabilityObserved}},
		}},
	}
	usage := reconcile.Result{Decisions: []reconcile.Decision{decision}}
	version := costVersion("v1", "2", now)
	result, err := Build(events, usage, &version)
	if err != nil || result.Records[0].Status != "calculated" || result.Records[0].Amount == nil || *result.Records[0].Amount != "2" {
		t.Fatalf("result = %+v, %v", result, err)
	}
}

func TestBuildKeepsKnownSubtotalForPartiallyPricedUsage(t *testing.T) {
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	events := []model.EventEnvelope{modelCall("call", "model", now)}
	million := int64(1_000_000)
	decision := usageDecision("usage", "call", now, million)
	decision.Normalized.ExclusiveBuckets = append(decision.Normalized.ExclusiveBuckets,
		model.UsageBucket{Name: "cache_write_input", Tokens: model.Quantity{Value: &million, Availability: model.AvailabilityObserved}})
	result, err := Build(events, reconcile.Result{InputEventIDs: []model.NamespacedID{{Namespace: "fixture", Value: "usage-a"}}, Decisions: []reconcile.Decision{decision}}, ptrVersion(costVersion("v1", "2", now)))
	if err != nil {
		t.Fatal(err)
	}
	if !result.MissingCost || len(result.KnownTotals) != 1 || result.KnownTotals[0].Amount != "2" || result.Records[0].Status != "incomplete" {
		t.Fatalf("result = %+v", result)
	}
}

func TestCalculationIDIncludesInputEvents(t *testing.T) {
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	events := []model.EventEnvelope{modelCall("call", "model", now)}
	decision := usageDecision("usage", "call", now, 1_000_000)
	version := costVersion("v1", "2", now)
	first, err := Build(events, reconcile.Result{InputEventIDs: []model.NamespacedID{{Namespace: "fixture", Value: "a"}}, Decisions: []reconcile.Decision{decision}}, &version)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(events, reconcile.Result{InputEventIDs: []model.NamespacedID{{Namespace: "fixture", Value: "b"}}, Decisions: []reconcile.Decision{decision}}, &version)
	if err != nil {
		t.Fatal(err)
	}
	if first.CalculationID == second.CalculationID {
		t.Fatal("different input event snapshots share a calculation ID")
	}
}

func ptrVersion(value pricing.Version) *pricing.Version { return &value }

func modelCall(id, actual string, at time.Time) model.EventEnvelope {
	ref := model.EntityRef{Kind: model.EntityModelCall, ID: model.NamespacedID{Namespace: "fixture/call", Value: id}}
	payload, _ := json.Marshal(model.ModelCallPayload{Call: ref, Instruction: model.EntityRef{Kind: model.EntityInstruction, ID: model.NamespacedID{Namespace: "fixture/instruction", Value: "instruction"}}, Actor: model.EntityRef{Kind: model.EntityAgent, ID: model.NamespacedID{Namespace: "fixture/agent", Value: "agent"}}, ActualModel: actual, State: "completed"})
	return model.EventEnvelope{SchemaVersion: model.SchemaVersion, EventID: model.NamespacedID{Namespace: "fixture/event", Value: "call-" + id}, EventType: model.EventModelCall, Source: model.EventSource{Provider: "codex", Surface: "fixture", Kind: model.SourceTranscript}, Time: model.EventTime{ReceivedAt: at}, Payload: payload}
}

func usageDecision(id, call string, at time.Time, tokens int64) reconcile.Decision {
	quantity := model.Quantity{Value: &tokens, Availability: model.AvailabilityObserved}
	return reconcile.Decision{EventID: model.NamespacedID{Namespace: "fixture/event", Value: id}, Usage: model.EntityRef{Kind: model.EntityUsage, ID: model.NamespacedID{Namespace: "fixture/usage", Value: id}}, AppliesTo: model.EntityRef{Kind: model.EntityModelCall, ID: model.NamespacedID{Namespace: "fixture/call", Value: call}}, Role: reconcile.RoleCounted, Normalized: &model.NormalizedUsage{ExclusiveBuckets: []model.UsageBucket{{Name: "input_uncached", Tokens: quantity}}}}
}

func costVersion(id, amount string, at time.Time) pricing.Version {
	return pricing.Version{SchemaVersion: pricing.VersionSchema, Version: id, CreatedAt: at, Reason: "test", Sources: []pricing.SourceDocument{{ID: "source", Kind: "manual", RetrievedAt: at}}, Rates: []pricing.Rate{{ID: "rate", Provider: "codex", ModelPattern: "model", UsageClass: "input_uncached", Currency: "USD", Amount: amount, UnitScale: 1_000_000, EffectiveFrom: at.Add(-time.Hour), SourceID: "source"}}}
}
