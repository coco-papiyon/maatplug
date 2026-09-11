package catalog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"maatplug/internal/attribution"
	"maatplug/internal/journal"
	"maatplug/internal/model"
	"maatplug/internal/pricing"
	"maatplug/internal/store"
)

type fixtureExpectations struct {
	Cases []struct {
		ID               string   `json:"id"`
		KnownUsageTokens int64    `json:"knownUsageTokens"`
		UnknownUsage     bool     `json:"unknownUsage"`
		SelectedEventIDs []string `json:"selectedEventIds"`
		ExcludedEventIDs []string `json:"excludedEventIds"`
		OperationState   string   `json:"operationState"`
	} `json:"cases"`
}

func TestProjectReconciliationAndAttributionDoNotSumRunDisplayRows(t *testing.T) {
	root := t.TempDir()
	j := journal.New(root)
	included := true
	parent := catalogUsageEvent("parent-event", "parent", "agent-parent", 60, "parent-run")
	parent.Relations = []model.Relation{
		{Kind: model.RelationContains, From: catalogUsageRef("parent"), To: catalogUsageRef("child-a"), Certainty: model.CertaintyReported, Includes: &included, Reason: "provider states inclusion"},
		{Kind: model.RelationContains, From: catalogUsageRef("parent"), To: catalogUsageRef("child-b"), Certainty: model.CertaintyReported, Includes: &included, Reason: "provider states inclusion"},
	}
	for _, item := range []struct {
		run   string
		event model.EventEnvelope
	}{
		{"parent-run", parent},
		{"child-a-run", catalogUsageEvent("child-a-event", "child-a", "call-a", 10, "child-a-run")},
		{"child-b-run", catalogUsageEvent("child-b-event", "child-b", "call-b", 30, "child-b-run")},
	} {
		if _, err := j.Append(journal.Route{ProjectID: "project", Provider: "fixture", SessionID: "session", RunID: item.run}, item.event); err != nil {
			t.Fatal(err)
		}
	}
	a := model.EntityRef{Kind: model.EntityInstruction, ID: model.NamespacedID{Namespace: "fixture/instruction", Value: "a"}}
	b := model.EntityRef{Kind: model.EntityInstruction, ID: model.NamespacedID{Namespace: "fixture/instruction", Value: "b"}}
	policy := attribution.Policy{Version: "user-v1", BasedOn: attribution.DefaultPolicyVersion, Corrections: []attribution.Correction{{
		Usage: catalogUsageRef("parent"), Reason: "user splits the shared work equally",
		Allocations: []attribution.Allocation{{Instruction: a, Units: 1, TotalUnits: 2}, {Instruction: b, Units: 1, TotalUnits: 2}},
	}}}
	if err := (attribution.PolicyStore{Root: root}).SaveAndActivate("project", policy); err != nil {
		t.Fatal(err)
	}

	projection, err := New(root).RebuildProject("project")
	if err != nil {
		t.Fatal(err)
	}
	if projection.KnownUsageTokens != 60 || projection.UndecomposedTokens != 20 {
		t.Fatalf("project usage = %+v", projection.Derivations.Usage)
	}
	var periodTotal int64
	for _, period := range projection.Periods {
		periodTotal += period.KnownUsageTokens
	}
	if periodTotal != 60 {
		t.Fatalf("period total = %d, want 60", periodTotal)
	}
	active := projection.Derivations.ActiveAttribution
	if active.WorkTotalTokens != 60 || active.AllocatedEstimate != 60 || active.UnattributedTokens != 0 || active.Work.Undecomposed != 20 {
		t.Fatalf("active attribution = %+v", active)
	}
	if len(projection.Derivations.AttributionVersions) != 2 {
		t.Fatalf("versions = %+v", projection.Derivations.AttributionVersions)
	}
	change := projection.Derivations.AttributionVersions[1].ChangeFromBase
	if change.WorkTotalDelta != 0 || len(change.ChangedUsage) != 1 {
		t.Fatalf("attribution change = %+v", change)
	}
}

func TestCatalogKeepsCostResultsForHistoricalPricingVersions(t *testing.T) {
	root := t.TempDir()
	j := journal.New(root)
	route := journal.Route{ProjectID: "project", Provider: "codex", SessionID: "session", RunID: "run"}
	for _, event := range []model.EventEnvelope{catalogModelCallEvent("call", "model", "run"), catalogCostUsageEvent("usage-event", "usage", "call", 1_000_000, "run")} {
		if _, err := j.Append(route, event); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	pricingStore := pricing.VersionStore{Root: root}
	v1 := catalogPricingVersion("price-v1", "", "2", now)
	v2 := catalogPricingVersion("price-v2", "price-v1", "3", now.Add(time.Hour))
	if err := pricingStore.Save(v1); err != nil {
		t.Fatal(err)
	}
	if err := pricingStore.SaveAndActivate(v2); err != nil {
		t.Fatal(err)
	}
	projection, err := New(root).RebuildProject("project")
	if err != nil {
		t.Fatal(err)
	}
	if projection.Derivations.ActiveCost.PricingVersion != "price-v2" || len(projection.Derivations.ActiveCost.KnownTotals) != 1 || projection.Derivations.ActiveCost.KnownTotals[0].Amount != "3" {
		t.Fatalf("active cost = %+v", projection.Derivations.ActiveCost)
	}
	if len(projection.Derivations.CostVersions) != 2 || len(projection.Derivations.CostVersions[1].ChangeFromBase.ChangedUsage) != 1 {
		t.Fatalf("cost versions = %+v", projection.Derivations.CostVersions)
	}
}

func catalogUsageEvent(eventID, usageID, targetID string, tokens int64, runID string) model.EventEnvelope {
	value := tokens
	payload, _ := json.Marshal(model.UsagePayload{
		Usage:      catalogUsageRef(usageID),
		AppliesTo:  model.EntityRef{Kind: model.EntityAgent, ID: model.NamespacedID{Namespace: "fixture/target", Value: targetID}},
		Raw:        model.RawUsage{Unit: "tokens", Fields: map[string]json.RawMessage{"total": json.RawMessage(fmt.Sprintf("%d", tokens))}},
		Normalized: &model.NormalizedUsage{ExclusiveBuckets: []model.UsageBucket{{Name: "input", Tokens: model.Quantity{Value: &value, Availability: model.AvailabilityObserved}}}, Totals: []model.UsageBucket{{Name: "all", Tokens: model.Quantity{Value: &value, Availability: model.AvailabilityObserved}}}},
		Counter:    model.CounterSemantics{Kind: model.CounterDelta}, State: model.MeasurementFinal,
	})
	return model.EventEnvelope{SchemaVersion: model.SchemaVersion, EventID: model.NamespacedID{Namespace: "fixture/event", Value: eventID}, EventType: model.EventUsageReported, Source: model.EventSource{Provider: "fixture", Surface: "test", Kind: model.SourceCollector}, Time: model.EventTime{ReceivedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}, Scope: []model.EntityRef{{Kind: model.EntityRun, ID: model.NamespacedID{Namespace: "fixture/run", Value: runID}}}, Payload: payload}
}

func catalogUsageRef(id string) model.EntityRef {
	return model.EntityRef{Kind: model.EntityUsage, ID: model.NamespacedID{Namespace: "fixture/usage", Value: id}}
}

func catalogModelCallEvent(id, actualModel, runID string) model.EventEnvelope {
	call := model.EntityRef{Kind: model.EntityModelCall, ID: model.NamespacedID{Namespace: "fixture/call", Value: id}}
	payload, _ := json.Marshal(model.ModelCallPayload{Call: call, Instruction: model.EntityRef{Kind: model.EntityInstruction, ID: model.NamespacedID{Namespace: "fixture/instruction", Value: "instruction"}}, Actor: model.EntityRef{Kind: model.EntityAgent, ID: model.NamespacedID{Namespace: "fixture/agent", Value: "agent"}}, ActualModel: actualModel, State: "completed"})
	return model.EventEnvelope{SchemaVersion: model.SchemaVersion, EventID: model.NamespacedID{Namespace: "fixture/event", Value: "call-" + id}, EventType: model.EventModelCall, Source: model.EventSource{Provider: "codex", Surface: "fixture", Kind: model.SourceTranscript}, Time: model.EventTime{ReceivedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}, Scope: []model.EntityRef{{Kind: model.EntityRun, ID: model.NamespacedID{Namespace: "fixture/run", Value: runID}}}, Payload: payload}
}

func catalogCostUsageEvent(eventID, usageID, callID string, tokens int64, runID string) model.EventEnvelope {
	value := tokens
	payload, _ := json.Marshal(model.UsagePayload{Usage: catalogUsageRef(usageID), AppliesTo: model.EntityRef{Kind: model.EntityModelCall, ID: model.NamespacedID{Namespace: "fixture/call", Value: callID}}, Raw: model.RawUsage{Unit: "tokens", Fields: map[string]json.RawMessage{"input_tokens": json.RawMessage(fmt.Sprintf("%d", tokens))}}, Normalized: &model.NormalizedUsage{ExclusiveBuckets: []model.UsageBucket{{Name: "input_uncached", Tokens: model.Quantity{Value: &value, Availability: model.AvailabilityObserved}}}}, Counter: model.CounterSemantics{Kind: model.CounterDelta}, State: model.MeasurementFinal})
	return model.EventEnvelope{SchemaVersion: model.SchemaVersion, EventID: model.NamespacedID{Namespace: "fixture/event", Value: eventID}, EventType: model.EventUsageReported, Source: model.EventSource{Provider: "codex", Surface: "fixture", Kind: model.SourceTranscript}, Time: model.EventTime{ReceivedAt: time.Date(2026, 1, 2, 0, 1, 0, 0, time.UTC)}, Scope: []model.EntityRef{{Kind: model.EntityRun, ID: model.NamespacedID{Namespace: "fixture/run", Value: runID}}}, Payload: payload}
}

func catalogPricingVersion(id, basedOn, amount string, created time.Time) pricing.Version {
	return pricing.Version{SchemaVersion: pricing.VersionSchema, Version: id, BasedOn: basedOn, CreatedAt: created, Reason: "catalog test", Sources: []pricing.SourceDocument{{ID: "manual", Kind: "manual", RetrievedAt: created}}, Rates: []pricing.Rate{{ID: "rate-" + id, Provider: "codex", ModelPattern: "model", UsageClass: "input_uncached", Currency: "USD", Amount: amount, UnitScale: 1_000_000, EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), SourceID: "manual"}}}
}

func TestRebuildProjectMatchesAcceptanceFixtureAndIsDeterministic(t *testing.T) {
	root := t.TempDir()
	j := journal.New(root)
	fixture, err := os.Open(filepath.Join("..", "..", "testdata", "events", "acceptance.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	scanner := bufio.NewScanner(fixture)
	for scanner.Scan() {
		var event model.EventEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		runID := scopeID(event.Scope, model.EntityRun)
		if runID == "" {
			t.Fatalf("event %s has no run scope", event.EventID.Value)
		}
		_, err := j.Append(journal.Route{ProjectID: "fixture", Provider: "fixture", SessionID: "acceptance", RunID: runID}, event)
		if err != nil {
			t.Fatalf("append %s: %v", event.EventID.Value, err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}

	expectationData, err := os.ReadFile(filepath.Join("..", "..", "testdata", "events", "expectations.json"))
	if err != nil {
		t.Fatal(err)
	}
	var expectations fixtureExpectations
	if err := json.Unmarshal(expectationData, &expectations); err != nil {
		t.Fatal(err)
	}

	builder := New(root)
	first, err := builder.RebuildProject("fixture")
	if err != nil {
		t.Fatal(err)
	}
	runs := map[string]RunSummary{}
	for _, session := range first.Sessions {
		for _, run := range session.Runs {
			runs[run.RunID] = run
		}
	}
	for _, expected := range expectations.Cases {
		run, ok := runs[expected.ID]
		if !ok {
			t.Errorf("missing run %s", expected.ID)
			continue
		}
		if run.KnownUsageTokens != expected.KnownUsageTokens || run.UnknownUsage != expected.UnknownUsage {
			t.Errorf("%s usage = (%d, %v), want (%d, %v)", expected.ID, run.KnownUsageTokens, run.UnknownUsage, expected.KnownUsageTokens, expected.UnknownUsage)
		}
		if got := idValues(run.SelectedEventIDs); !reflect.DeepEqual(got, expected.SelectedEventIDs) {
			t.Errorf("%s selected = %v, want %v", expected.ID, got, expected.SelectedEventIDs)
		}
		if expected.OperationState != "" && run.OperationState != expected.OperationState {
			t.Errorf("%s operation state = %q, want %q", expected.ID, run.OperationState, expected.OperationState)
		}
		gotExcluded := map[string]bool{}
		for _, id := range run.ExcludedEventIDs {
			gotExcluded[id.Value] = true
		}
		for _, id := range expected.ExcludedEventIDs {
			if !gotExcluded[id] {
				t.Errorf("%s missing excluded ID %s", expected.ID, id)
			}
		}
	}
	if first.KnownUsageTokens != 415 || !first.UnknownUsage || first.Derivations.ActiveAttribution.WorkTotalTokens != 415 {
		t.Fatalf("project totals = usage:%d unknown:%v attribution:%d", first.KnownUsageTokens, first.UnknownUsage, first.Derivations.ActiveAttribution.WorkTotalTokens)
	}
	if first.Derivations.Operations.RuleVersion != "operation-projection-v1" || len(first.Derivations.Operations.Operations) == 0 {
		t.Fatalf("operation audit projection missing: %+v", first.Derivations.Operations)
	}
	var truncatedInputFound bool
	for _, call := range first.Derivations.InputComposition.Calls {
		if call.Call.ID.Value != "call-06" {
			continue
		}
		if len(call.Elements) == 1 && call.Elements[0].OriginalContent != nil && call.Elements[0].OriginalContent.Value == "tool-output-raw" && call.Elements[0].Transformation == "truncated" && call.Gap.Kind == "missing_provider_usage" {
			truncatedInputFound = true
		}
	}
	if !truncatedInputFound {
		t.Fatalf("truncated input composition missing: %+v", first.Derivations.InputComposition)
	}

	before := projectionFiles(t, root, "fixture")
	layout := &store.Store{Root: root}
	if err := os.Remove(layout.ProjectCatalogPath("fixture")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(layout.ProjectSummariesDir("fixture")); err != nil {
		t.Fatal(err)
	}
	second, err := builder.RebuildProject("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("logical catalog changed after delete and rebuild")
	}
	after := projectionFiles(t, root, "fixture")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("catalog files changed after deterministic rebuild")
	}
}

func scopeID(scope []model.EntityRef, kind model.EntityKind) string {
	for _, ref := range scope {
		if ref.Kind == kind {
			return ref.ID.Value
		}
	}
	return ""
}

func idValues(ids []model.NamespacedID) []string {
	result := make([]string, len(ids))
	for index, id := range ids {
		result[index] = id.Value
	}
	return result
}

func projectionFiles(t *testing.T, root, projectID string) map[string]string {
	t.Helper()
	base := filepath.Join(root, "projects", projectID)
	result := map[string]string{}
	for _, target := range []string{"catalog.json", filepath.Join("summaries", "runs")} {
		path := filepath.Join(base, target)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			result[target] = string(data)
			continue
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			data, err := os.ReadFile(filepath.Join(path, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			result[filepath.Join(target, entry.Name())] = string(data)
		}
	}
	return result
}
