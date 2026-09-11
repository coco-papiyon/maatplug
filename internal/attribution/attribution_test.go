package attribution

import (
	"encoding/json"
	"testing"
	"time"

	"maatplug/internal/model"
	"maatplug/internal/reconcile"
)

func TestBuildDirectSharedAndCorrectedAllocationPreservesWorkTotal(t *testing.T) {
	a, b := instruction("a"), instruction("b")
	call1 := callEvent("call-1", a)
	call2 := callEvent("call-2", a)
	callRef2 := model.EntityRef{Kind: model.EntityModelCall, ID: model.NamespacedID{Namespace: "fixture/call", Value: "call-2"}}
	call2.Relations = []model.Relation{{Kind: model.RelationAttributedTo, From: callRef2, To: b, Certainty: model.CertaintyReported, Reason: "fixture shared instruction"}}
	reconciled := reconcile.Result{RuleVersion: reconcile.RuleVersion, KnownSubtotal: 60, Decisions: []reconcile.Decision{
		decision("usage-1", "event-1", "call-1", 10), decision("usage-2", "event-2", "call-2", 20), decision("usage-3", "event-3", "call-3", 30),
	}}
	policy := Policy{Version: "user-v1", Corrections: []Correction{{Usage: usage("usage-3"), Allocations: []Allocation{{Instruction: a, Units: 1, TotalUnits: 2}, {Instruction: b, Units: 1, TotalUnits: 2}}, Reason: "split equally by user"}}}
	result, err := Build([]model.EventEnvelope{call1, call2}, reconciled, policy)
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkTotalTokens != 60 || result.DirectTokens != 10 || result.SharedUnallocated != 20 || result.AllocatedEstimate != 30 {
		t.Fatalf("result = %+v", result)
	}
	if result.Work.KnownSubtotal != 60 || len(result.Calls) != 3 || len(result.Instructions) != 2 {
		t.Fatalf("summaries = work:%+v calls:%+v instructions:%+v", result.Work, result.Calls, result.Instructions)
	}
	if result.Instructions[0].Summary.KnownSubtotal+result.Instructions[1].Summary.KnownSubtotal != 40 {
		t.Fatalf("instruction exclusive subtotals = %+v", result.Instructions)
	}
	var allocated Record
	for _, record := range result.Records {
		if record.Usage.ID.Value == "usage-3" {
			allocated = record
		}
	}
	if len(allocated.Assigned) != 2 || allocated.Assigned[0].Tokens+allocated.Assigned[1].Tokens+allocated.UnallocatedTokens != 30 {
		t.Fatalf("allocated = %+v", allocated)
	}
	automatic, err := Build([]model.EventEnvelope{call1, call2}, reconciled, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	change := Compare(automatic, result)
	if change.WorkTotalDelta != 0 {
		t.Fatalf("change = %+v", change)
	}
	changedPolicy := Policy{Version: "user-v2", BasedOn: "user-v1", Corrections: []Correction{{Usage: usage("usage-3"), Allocations: []Allocation{{Instruction: a, Units: 1, TotalUnits: 3}, {Instruction: b, Units: 2, TotalUnits: 3}}, Reason: "user revised the split"}}}
	changed, err := Build([]model.EventEnvelope{call1, call2}, reconciled, changedPolicy)
	if err != nil {
		t.Fatal(err)
	}
	change = Compare(result, changed)
	if change.WorkTotalDelta != 0 || len(change.ChangedUsage) != 1 || change.ChangedUsage[0].ID.Value != "usage-3" {
		t.Fatalf("allocation change = %+v", change)
	}
}

func TestPolicyStoreKeepsVersionsImmutableAndLoadsActive(t *testing.T) {
	store := PolicyStore{Root: t.TempDir()}
	policy := Policy{Version: "user-v1", Corrections: []Correction{{Usage: usage("u"), Reason: "leave unattributed by user"}}}
	if err := store.SaveAndActivate("project", policy); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadActive("project")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != "user-v1" || len(loaded.Corrections) != 1 {
		t.Fatalf("loaded = %+v", loaded)
	}
	policy.Corrections[0].Reason = "changed"
	if err := store.Save("project", policy); err == nil {
		t.Fatal("same policy version was overwritten")
	}
	if err := store.Activate("project", DefaultPolicyVersion); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.LoadActive("project")
	if err != nil || loaded.Version != DefaultPolicyVersion {
		t.Fatalf("default policy = %+v, %v", loaded, err)
	}
}

func TestPolicyStoreRejectsReservedVersionAndMissingBase(t *testing.T) {
	store := PolicyStore{Root: t.TempDir()}
	if err := store.Save("project", Policy{Version: DefaultPolicyVersion}); err == nil {
		t.Fatal("saved reserved automatic policy version")
	}
	if err := store.Save("project", Policy{Version: "user-v2", BasedOn: "missing-v1"}); err == nil {
		t.Fatal("saved policy with missing base")
	}
}

func decision(usageID, eventID, callID string, tokens int64) reconcile.Decision {
	return reconcile.Decision{EventID: model.NamespacedID{Namespace: "fixture/event", Value: eventID}, Usage: usage(usageID), AppliesTo: model.EntityRef{Kind: model.EntityModelCall, ID: model.NamespacedID{Namespace: "fixture/call", Value: callID}}, Tokens: &tokens, Role: reconcile.RoleCounted}
}
func usage(id string) model.EntityRef {
	return model.EntityRef{Kind: model.EntityUsage, ID: model.NamespacedID{Namespace: "fixture/usage", Value: id}}
}
func instruction(id string) model.EntityRef {
	return model.EntityRef{Kind: model.EntityInstruction, ID: model.NamespacedID{Namespace: "fixture/instruction", Value: id}}
}
func callEvent(id string, target model.EntityRef) model.EventEnvelope {
	payload, _ := json.Marshal(model.ModelCallPayload{Call: model.EntityRef{Kind: model.EntityModelCall, ID: model.NamespacedID{Namespace: "fixture/call", Value: id}}, Instruction: target, Actor: model.EntityRef{Kind: model.EntityAgent, ID: model.NamespacedID{Namespace: "fixture/agent", Value: "main"}}, State: "completed"})
	return model.EventEnvelope{SchemaVersion: model.SchemaVersion, EventID: model.NamespacedID{Namespace: "fixture/event", Value: "call-" + id}, EventType: model.EventModelCall, Source: model.EventSource{Provider: "fixture", Surface: "test", Kind: model.SourceCollector}, Time: model.EventTime{ReceivedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, Scope: []model.EntityRef{{Kind: model.EntityRun, ID: model.NamespacedID{Namespace: "fixture/run", Value: "run"}}}, Payload: payload}
}
