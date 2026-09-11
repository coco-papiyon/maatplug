package inputcomposition

import (
	"encoding/json"
	"testing"
	"time"

	"maatplug/internal/model"
	"maatplug/internal/reconcile"
)

func TestBuildTracksRepeatedTransformedContentPerCallAndUsageGap(t *testing.T) {
	original := model.NamespacedID{Namespace: "sha256", Value: "original"}
	content := model.ContentRef{ID: model.NamespacedID{Namespace: "sha256", Value: "summary"}, Variant: model.ContentSummary, Availability: model.AvailabilityObserved, DerivedFrom: &original}
	inferred := quantity(6, model.AvailabilityInferred, "fixture estimate")
	observed := quantity(6, model.AvailabilityObserved, "")
	events := []model.EventEnvelope{
		inputEvent("input-a", "element-a", "call-a", content, &inferred, "truncated"),
		inputEvent("input-b", "element-b", "call-b", content, &observed, "truncated"),
	}
	usage := reconcile.Result{InputEventIDs: []model.NamespacedID{{Namespace: "fixture", Value: "input-a"}, {Namespace: "fixture", Value: "input-b"}}, Decisions: []reconcile.Decision{
		usageDecision("usage-a", "call-a", 10), usageDecision("usage-b", "call-b", 10),
	}}
	result, err := Build(events, usage)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Calls) != 2 {
		t.Fatalf("calls = %+v", result.Calls)
	}
	for _, call := range result.Calls {
		if len(call.Elements) != 1 || call.Elements[0].OriginalContent == nil || *call.Elements[0].OriginalContent != original {
			t.Fatalf("call = %+v", call)
		}
		if call.AccountedTokens != 6 || call.Gap.Value == nil || *call.Gap.Value != 4 {
			t.Fatalf("call = %+v", call)
		}
	}
	if result.Calls[0].Gap.Kind != "estimation_error" {
		t.Fatalf("first gap = %+v", result.Calls[0].Gap)
	}
	if result.Calls[1].Gap.Kind != "non_displayed_elements" {
		t.Fatalf("second gap = %+v", result.Calls[1].Gap)
	}
}

func TestBuildAddsReferencedInstructionWithoutInventingTokens(t *testing.T) {
	call := model.EntityRef{Kind: model.EntityModelCall, ID: model.NamespacedID{Namespace: "fixture/call", Value: "call"}}
	instruction := model.EntityRef{Kind: model.EntityInstruction, ID: model.NamespacedID{Namespace: "fixture/instruction", Value: "instruction"}}
	content := model.ContentRef{ID: model.NamespacedID{Namespace: "sha256", Value: "content"}, Variant: model.ContentRaw, Availability: model.AvailabilityObserved}
	events := []model.EventEnvelope{modelCallEvent(call, instruction), instructionEvent(instruction, content)}
	usage := reconcile.Result{Decisions: []reconcile.Decision{usageDecision("usage", "call", 10)}}
	result, err := Build(events, usage)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Calls) != 1 || len(result.Calls[0].Elements) != 1 {
		t.Fatalf("result = %+v", result)
	}
	element := result.Calls[0].Elements[0]
	if element.Kind != "instruction" || element.Availability != model.AvailabilityInferred || element.Tokens != nil {
		t.Fatalf("element = %+v", element)
	}
	if result.Calls[0].Gap.Kind != "non_displayed_or_missing_elements" {
		t.Fatalf("gap = %+v", result.Calls[0].Gap)
	}
}

func usageDecision(id, callID string, input int64) reconcile.Decision {
	output := int64(2)
	total := input + output
	return reconcile.Decision{EventID: model.NamespacedID{Namespace: "fixture/event", Value: id}, Usage: model.EntityRef{Kind: model.EntityUsage, ID: model.NamespacedID{Namespace: "fixture/usage", Value: id}}, AppliesTo: model.EntityRef{Kind: model.EntityModelCall, ID: model.NamespacedID{Namespace: "fixture/call", Value: callID}}, Role: reconcile.RoleCounted, Normalized: &model.NormalizedUsage{ExclusiveBuckets: []model.UsageBucket{{Name: "input_uncached", Tokens: quantity(input, model.AvailabilityObserved, "")}, {Name: "output", Tokens: quantity(output, model.AvailabilityObserved, "")}}, Totals: []model.UsageBucket{{Name: "all", Tokens: quantity(total, model.AvailabilityObserved, "")}}}}
}
func inputEvent(eventID, elementID, callID string, content model.ContentRef, tokens *model.Quantity, transformation string) model.EventEnvelope {
	payload, _ := json.Marshal(model.InputElementPayload{Element: model.EntityRef{Kind: model.EntityInputElement, ID: model.NamespacedID{Namespace: "fixture/element", Value: elementID}}, Call: model.EntityRef{Kind: model.EntityModelCall, ID: model.NamespacedID{Namespace: "fixture/call", Value: callID}}, Kind: "tool_result", Content: &content, Tokens: tokens, Transformation: transformation})
	return envelope(eventID, model.EventInputObserved, payload)
}
func modelCallEvent(call, instruction model.EntityRef) model.EventEnvelope {
	payload, _ := json.Marshal(model.ModelCallPayload{Call: call, Instruction: instruction, Actor: model.EntityRef{Kind: model.EntityAgent, ID: model.NamespacedID{Namespace: "fixture/agent", Value: "main"}}, State: "completed"})
	return envelope("call", model.EventModelCall, payload)
}
func instructionEvent(instruction model.EntityRef, content model.ContentRef) model.EventEnvelope {
	payload, _ := json.Marshal(model.InstructionPayload{Instruction: instruction, Actor: model.EntityRef{Kind: model.EntityActor, ID: model.NamespacedID{Namespace: "fixture/actor", Value: "user"}}, Content: []model.ContentRef{content}, State: "submitted"})
	return envelope("instruction", model.EventInstructionRecorded, payload)
}
func envelope(id string, kind model.EventKind, payload json.RawMessage) model.EventEnvelope {
	return model.EventEnvelope{SchemaVersion: model.SchemaVersion, EventID: model.NamespacedID{Namespace: "fixture", Value: id}, EventType: kind, Source: model.EventSource{Provider: "fixture", Surface: "test", Kind: model.SourceCollector}, Time: model.EventTime{ReceivedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, Scope: []model.EntityRef{{Kind: model.EntityRun, ID: model.NamespacedID{Namespace: "fixture/run", Value: "run"}}}, Payload: payload}
}
func quantity(value int64, status model.AvailabilityStatus, reason string) model.Quantity {
	return model.Quantity{Value: &value, Availability: status, Reason: reason}
}
