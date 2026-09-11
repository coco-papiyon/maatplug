package reconcile

import (
	"encoding/json"
	"testing"
	"time"

	"maatplug/internal/model"
)

func TestBuildSelectsFinalObservationOnceAndKeepsDistinctCalls(t *testing.T) {
	events := []model.EventEnvelope{
		usageEvent("provisional", "usage-1", "call-1", 10, model.CounterDelta, model.MeasurementProvisional, "g", 1),
		usageEvent("final", "usage-1", "call-1", 20, model.CounterDelta, model.MeasurementFinal, "g", 2),
		usageEvent("same-content-other-call", "usage-2", "call-2", 20, model.CounterDelta, model.MeasurementFinal, "g", 3),
		usageEvent("run-total", "run-total", "run-1", 40, model.CounterCumulative, model.MeasurementFinal, "g", 4),
	}
	result, err := Build(events)
	if err != nil {
		t.Fatal(err)
	}
	if result.KnownSubtotal != 40 || len(result.SelectedEventIDs) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if !hasSelected(result, "final") || !hasSelected(result, "same-content-other-call") {
		t.Fatalf("selected = %+v", result.SelectedEventIDs)
	}
	if hasSelected(result, "run-total") || hasSelected(result, "provisional") {
		t.Fatalf("selected = %+v", result.SelectedEventIDs)
	}
}

func TestBuildReconcilesContainingParentAndChildren(t *testing.T) {
	parent := usageEvent("parent", "parent", "run-1", 60, model.CounterDelta, model.MeasurementFinal, "g", 1)
	childA := usageEvent("child-a", "child-a", "call-a", 10, model.CounterDelta, model.MeasurementFinal, "g", 2)
	childB := usageEvent("child-b", "child-b", "call-b", 30, model.CounterDelta, model.MeasurementFinal, "g", 3)
	included := true
	parent.Relations = []model.Relation{
		{Kind: model.RelationContains, From: usageRef("parent"), To: usageRef("child-a"), Certainty: model.CertaintyReported, Includes: &included, Reason: "fixture parent includes child"},
		{Kind: model.RelationContains, From: usageRef("parent"), To: usageRef("child-b"), Certainty: model.CertaintyReported, Includes: &included, Reason: "fixture parent includes child"},
	}
	result, err := Build([]model.EventEnvelope{parent, childA, childB})
	if err != nil {
		t.Fatal(err)
	}
	if result.KnownSubtotal != 60 || result.Undecomposed != 20 || len(result.Residuals) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if !hasSelected(result, "parent") || hasSelected(result, "child-a") || hasSelected(result, "child-b") {
		t.Fatalf("selected = %+v", result.SelectedEventIDs)
	}

	parent = usageEvent("parent", "parent", "run-1", 40, model.CounterDelta, model.MeasurementFinal, "g", 1)
	parent.Relations = []model.Relation{
		{Kind: model.RelationContains, From: usageRef("parent"), To: usageRef("child-a"), Certainty: model.CertaintyReported, Includes: &included, Reason: "fixture parent includes child"},
		{Kind: model.RelationContains, From: usageRef("parent"), To: usageRef("child-b"), Certainty: model.CertaintyReported, Includes: &included, Reason: "fixture parent includes child"},
	}
	result, err = Build([]model.EventEnvelope{parent, childA, childB})
	if err != nil {
		t.Fatal(err)
	}
	if result.KnownSubtotal != 40 || hasSelected(result, "parent") {
		t.Fatalf("equal detail result = %+v", result)
	}
}

func TestBuildMarksContainingTotalConflictWhenChildrenExceedParent(t *testing.T) {
	parent := usageEvent("parent", "parent", "run-1", 30, model.CounterDelta, model.MeasurementFinal, "g", 1)
	childA := usageEvent("child-a", "child-a", "call-a", 10, model.CounterDelta, model.MeasurementFinal, "g", 2)
	childB := usageEvent("child-b", "child-b", "call-b", 30, model.CounterDelta, model.MeasurementFinal, "g", 3)
	included := true
	parent.Relations = []model.Relation{
		{Kind: model.RelationContains, From: usageRef("parent"), To: usageRef("child-a"), Certainty: model.CertaintyReported, Includes: &included, Reason: "fixture parent includes child"},
		{Kind: model.RelationContains, From: usageRef("parent"), To: usageRef("child-b"), Certainty: model.CertaintyReported, Includes: &included, Reason: "fixture parent includes child"},
	}
	result, err := Build([]model.EventEnvelope{parent, childA, childB})
	if err != nil {
		t.Fatal(err)
	}
	if result.KnownSubtotal != 0 || !result.Unknown || len(result.Conflicts) != 1 || len(result.SelectedEventIDs) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestBuildDoesNotCountEitherSideOfUnknownContainment(t *testing.T) {
	parent := usageEvent("parent", "parent", "run-1", 100, model.CounterDelta, model.MeasurementFinal, "g", 1)
	unknown := usageEvent("unknown", "unknown", "call-a", 0, model.CounterDelta, model.MeasurementFinal, "g", 2)
	var payload model.UsagePayload
	if err := json.Unmarshal(unknown.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Normalized = nil
	unknown.Payload, _ = json.Marshal(payload)
	known := usageEvent("known", "known", "call-b", 30, model.CounterDelta, model.MeasurementFinal, "g", 3)
	included := true
	parent.Relations = []model.Relation{
		{Kind: model.RelationContains, From: usageRef("parent"), To: usageRef("unknown"), Certainty: model.CertaintyReported, Includes: &included},
		{Kind: model.RelationContains, From: usageRef("parent"), To: usageRef("known"), Certainty: model.CertaintyReported, Includes: &included},
	}
	result, err := Build([]model.EventEnvelope{parent, unknown, known})
	if err != nil {
		t.Fatal(err)
	}
	if result.KnownSubtotal != 0 || !result.Unknown || len(result.SelectedEventIDs) != 0 || len(result.Conflicts) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestCumulativeDifferenceRejectsResetAndGenerationMismatch(t *testing.T) {
	previous := decisionForDifference("target", "g", 100)
	current := decisionForDifference("target", "g", 140)
	difference := CumulativeDifference(previous, current)
	if !difference.Available || difference.Usage.ExclusiveBuckets[0].Tokens.Value == nil || *difference.Usage.ExclusiveBuckets[0].Tokens.Value != 40 {
		t.Fatalf("difference = %+v", difference)
	}
	current.Normalized = normalized(90)
	if got := CumulativeDifference(previous, current); got.Available {
		t.Fatalf("reset difference = %+v", got)
	}
	current = decisionForDifference("target", "other", 140)
	if got := CumulativeDifference(previous, current); got.Available {
		t.Fatalf("generation difference = %+v", got)
	}
}

func usageEvent(eventID, usageID, targetID string, tokens int64, counter model.CounterKind, state model.MeasurementState, generation string, second int) model.EventEnvelope {
	payload := model.UsagePayload{Usage: usageRef(usageID), AppliesTo: model.EntityRef{Kind: model.EntityModelCall, ID: model.NamespacedID{Namespace: "fixture/call", Value: targetID}}, Raw: model.RawUsage{Unit: "tokens", Fields: map[string]json.RawMessage{"total": json.RawMessage("1")}}, Normalized: normalized(tokens), Counter: model.CounterSemantics{Kind: counter}, State: state}
	if counter == model.CounterCumulative {
		payload.Counter.Generation = generation
		payload.AppliesTo = model.EntityRef{Kind: model.EntityRun, ID: model.NamespacedID{Namespace: "fixture/run", Value: targetID}}
	}
	data, _ := json.Marshal(payload)
	return model.EventEnvelope{SchemaVersion: model.SchemaVersion, EventID: model.NamespacedID{Namespace: "fixture/event", Value: eventID}, EventType: model.EventUsageReported, Source: model.EventSource{Provider: "fixture", Surface: "test", Kind: model.SourceCollector}, Time: model.EventTime{ReceivedAt: time.Date(2026, 1, 1, 0, 0, second, 0, time.UTC)}, Scope: []model.EntityRef{{Kind: model.EntityRun, ID: model.NamespacedID{Namespace: "fixture/run", Value: "run-1"}}}, Payload: data}
}

func usageRef(id string) model.EntityRef {
	return model.EntityRef{Kind: model.EntityUsage, ID: model.NamespacedID{Namespace: "fixture/usage", Value: id}}
}
func normalized(tokens int64) *model.NormalizedUsage {
	value := tokens
	return &model.NormalizedUsage{ExclusiveBuckets: []model.UsageBucket{{Name: "input", Tokens: model.Quantity{Value: &value, Availability: model.AvailabilityObserved}}}, Totals: []model.UsageBucket{{Name: "all", Tokens: model.Quantity{Value: &value, Availability: model.AvailabilityObserved}}}}
}
func hasSelected(result Result, id string) bool {
	for _, candidate := range result.SelectedEventIDs {
		if candidate.Value == id {
			return true
		}
	}
	return false
}
func decisionForDifference(target, generation string, tokens int64) Decision {
	return Decision{AppliesTo: model.EntityRef{Kind: model.EntityRun, ID: model.NamespacedID{Namespace: "fixture/run", Value: target}}, Counter: model.CounterSemantics{Kind: model.CounterCumulative, Generation: generation}, Normalized: normalized(tokens)}
}
