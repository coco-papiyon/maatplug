package operation

import (
	"encoding/json"
	"testing"
	"time"

	"maatplug/internal/contentstore"
	"maatplug/internal/model"
)

func TestBuildSeparatesRequestedTargetsObservedEffectsAndUnionsParallelTime(t *testing.T) {
	root := t.TempDir()
	objects := contentstore.New(root)
	input, err := objects.Put([]byte(`{"command":["/bin/bash","-lc","curl https://api.example.test/v1"],"parsedCommand":[{"type":"read","path":"src/main.go"}],"environment":{"cwd":"/workspace","approvalPolicy":"on-request","sandboxPolicy":"workspace-write","networkAccess":true}}`), model.ContentRaw, "application/json", "test-input")
	if err != nil {
		t.Fatal(err)
	}
	locator, err := objects.Put([]byte("src/main.go"), model.ContentRaw, "text/plain", "test-path")
	if err != nil {
		t.Fatal(err)
	}
	diff, err := objects.Put([]byte("--- a/src/main.go\n+++ b/src/main.go\n-old\n+new\n+more\n"), model.ContentRaw, "text/x-diff", "test-diff")
	if err != nil {
		t.Fatal(err)
	}

	t0 := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	op1, op2 := operationRef("one"), operationRef("two")
	artifact := model.EntityRef{Kind: model.EntityArtifact, ID: model.NamespacedID{Namespace: "test/artifact", Value: "main"}}
	instruction := model.EntityRef{Kind: model.EntityInstruction, ID: model.NamespacedID{Namespace: "test/instruction", Value: "instruction"}}
	events := []model.EventEnvelope{
		event(t, "instruction", model.EventInstructionRecorded, t0.Add(-time.Second), model.InstructionPayload{Instruction: instruction, Actor: actorRef(), State: "submitted"}),
		event(t, "request-1", model.EventOperationRequested, t0, model.OperationPayload{Operation: op1, Actor: actorRef(), Kind: "shell", Name: "CommandExecution", Input: []model.ContentRef{input}}),
		event(t, "request-2", model.EventOperationRequested, t0.Add(time.Second), model.OperationPayload{Operation: op2, Actor: actorRef(), Kind: "shell", Name: "CommandExecution"}),
		event(t, "approval-request", model.EventApprovalResolved, t0.Add(time.Second), model.ApprovalPayload{Approval: approvalRef("request"), Operation: &op1, Decision: "requested"}),
		event(t, "approval-allow", model.EventApprovalResolved, t0.Add(2*time.Second), model.ApprovalPayload{Approval: approvalRef("allow"), Operation: &op1, Decision: "allowed"}),
		event(t, "result-2", model.EventOperationResult, t0.Add(7*time.Second), model.OperationResultPayload{Operation: op2, Status: "failed", DurationMillis: int64ptr(4000)}),
		event(t, "result-1", model.EventOperationResult, t0.Add(10*time.Second), model.OperationResultPayload{Operation: op1, Status: "completed", DurationMillis: int64ptr(8000)}),
	}
	artifactEvent := event(t, "artifact", model.EventArtifactObserved, t0.Add(9*time.Second), model.ArtifactPayload{Artifact: artifact, Kind: "file", Locator: &locator, Content: &diff, Change: "updated"})
	artifactEvent.Relations = []model.Relation{{Kind: model.RelationResultOf, From: artifact, To: op1, Certainty: model.CertaintyReported, Reason: "fixture relation"}}
	events = append(events, artifactEvent)

	result, err := Build(root, events)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Operations) != 2 {
		t.Fatalf("operations = %d", len(result.Operations))
	}
	first := result.Operations[0]
	if len(first.Instructions) != 1 || first.Instructions[0].Entity != instruction || first.Instructions[0].Availability != model.AvailabilityInferred {
		t.Fatalf("instructions = %+v", first.Instructions)
	}
	if first.Environment.WorkingDirectory.Value != "/workspace" || first.Environment.PermissionMode.Value != "on-request" || first.Environment.NetworkAccess.Value != "true" {
		t.Fatalf("environment = %+v", first.Environment)
	}
	if !hasTarget(first.RequestedTargets, "file", "src/main.go") || !hasTarget(first.RequestedTargets, "network_endpoint", "api.example.test") {
		t.Fatalf("requested targets = %+v", first.RequestedTargets)
	}
	if len(first.ObservedEffects) != 1 || first.ObservedEffects[0].Value != "src/main.go" {
		t.Fatalf("observed effects = %+v", first.ObservedEffects)
	}
	if first.Artifacts[0].Measurement.Added == nil || *first.Artifacts[0].Measurement.Added != 2 || first.Artifacts[0].Measurement.Removed == nil || *first.Artifacts[0].Measurement.Removed != 1 || first.Artifacts[0].Measurement.ByLanguage["Go"] != 1 {
		t.Fatalf("measurement = %+v", first.Artifacts[0].Measurement)
	}
	if first.States[1].State != "approval_pending" || first.States[2].State != "allowed" {
		t.Fatalf("states = %+v", first.States)
	}
	if len(result.Operations[1].ObservedEffects) != 0 || len(result.Operations[1].Missing) == 0 {
		t.Fatalf("unobserved effects were not explicit: %+v", result.Operations[1])
	}
	if elapsed(result.Elapsed, "tool") != 8000 || elapsed(result.Elapsed, "operation_wall") != 10000 {
		t.Fatalf("elapsed = %+v", result.Elapsed)
	}
	if elapsed(result.Elapsed, "approval_wait") != 1000 {
		t.Fatalf("approval elapsed = %+v", result.Elapsed)
	}
}

func TestBuildKeepsDeniedAndAsyncCompletionDistinct(t *testing.T) {
	t0 := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	denied, async := operationRef("denied"), operationRef("async")
	events := []model.EventEnvelope{
		event(t, "request-denied", model.EventOperationRequested, t0, model.OperationPayload{Operation: denied, Actor: actorRef(), Kind: "shell", Name: "exec"}),
		event(t, "denied", model.EventOperationResult, t0.Add(time.Second), model.OperationResultPayload{Operation: denied, Status: "denied"}),
		event(t, "request-async", model.EventOperationRequested, t0, model.OperationPayload{Operation: async, Actor: actorRef(), Kind: "external_api", Name: "job"}),
		event(t, "async-complete", model.EventOperationResult, t0.Add(2*time.Second), model.OperationResultPayload{Operation: async, Status: "completed", Async: true, DurationMillis: int64ptr(1000)}),
	}
	result, err := Build(t.TempDir(), events)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]bool{}
	for _, operation := range result.Operations {
		statuses[operation.FinalStatus] = true
	}
	if !statuses["denied"] || !statuses["async_completed"] {
		t.Fatalf("statuses = %+v", statuses)
	}
}

func TestBuildClassifiesDangerousAndValidationCommandsWithoutStoringCommandText(t *testing.T) {
	root := t.TempDir()
	objects := contentstore.New(root)
	input, err := objects.Put([]byte(`{"command":"go test ./...; Remove-Item -Recurse -Force C:\\workspace\\tmp"}`), model.ContentRaw, "application/json", "test-input")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	op := operationRef("risk")
	result, err := Build(root, []model.EventEnvelope{
		event(t, "request", model.EventOperationRequested, t0, model.OperationPayload{Operation: op, Actor: actorRef(), Kind: "shell", Name: "PowerShell", Input: []model.ContentRef{input}}),
		event(t, "result", model.EventOperationResult, t0.Add(time.Second), model.OperationResultPayload{Operation: op, Status: "completed"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := result.Operations[0]
	if got.Risk.Level != "high" || got.Risk.RuleVersion != RiskRuleVersion || len(got.Risk.Reasons) == 0 {
		t.Fatalf("risk = %+v", got.Risk)
	}
	if len(got.Validations) != 1 || got.Validations[0].Name != "go test" || got.Validations[0].Status != "completed" || got.Validations[0].ResultEvent == nil {
		t.Fatalf("validations = %+v", got.Validations)
	}
}

func TestBuildDoesNotTurnUnknownDurationIntoObservedZero(t *testing.T) {
	at := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	op := operationRef("same-timestamp")
	result, err := Build(t.TempDir(), []model.EventEnvelope{
		event(t, "request", model.EventOperationRequested, at, model.OperationPayload{Operation: op, Actor: actorRef(), Kind: "edit", Name: "apply_patch"}),
		event(t, "result", model.EventOperationResult, at, model.OperationResultPayload{Operation: op, Status: "completed"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed(result.Elapsed, "tool") != -1 || len(result.Operations[0].Missing) == 0 {
		t.Fatalf("unknown duration became a zero interval: %+v", result)
	}
}

func TestFinishDoesNotUseSyntheticApprovalAsObservedResolution(t *testing.T) {
	t0 := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	b := builder{result: Result{Operations: []Operation{{
		Operation: operationRef("approval-cycles"),
		States: []Transition{
			{State: "approval_pending", At: t0, EventID: model.NamespacedID{Namespace: "test/event", Value: "pending-1"}},
			{State: "requested", At: t0.Add(time.Second), EventID: model.NamespacedID{Namespace: "test/event", Value: "requested"}},
			{State: "approval_pending", At: t0.Add(2 * time.Second), EventID: model.NamespacedID{Namespace: "test/event", Value: "pending-2"}},
		},
	}}}}
	b.finish()
	if len(b.result.Operations[0].Intervals) != 1 || elapsed(b.result.Elapsed, "approval_wait") != 1000 {
		t.Fatalf("result = %+v", b.result)
	}
}

func event(t *testing.T, id string, kind model.EventKind, at time.Time, payload any) model.EventEnvelope {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	result := model.EventEnvelope{SchemaVersion: model.SchemaVersion, EventID: model.NamespacedID{Namespace: "test/event", Value: id}, EventType: kind, Source: model.EventSource{Provider: "test", Surface: "fixture", Kind: model.SourceTranscript}, Time: model.EventTime{ProviderAt: &at, ReceivedAt: at}, Scope: []model.EntityRef{{Kind: model.EntityRun, ID: model.NamespacedID{Namespace: "test/run", Value: "run"}}}, Payload: raw}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	return result
}

func operationRef(id string) model.EntityRef {
	return model.EntityRef{Kind: model.EntityOperation, ID: model.NamespacedID{Namespace: "test/operation", Value: id}}
}

func actorRef() model.EntityRef {
	return model.EntityRef{Kind: model.EntityAgent, ID: model.NamespacedID{Namespace: "test/agent", Value: "main"}}
}

func approvalRef(id string) model.EntityRef {
	return model.EntityRef{Kind: model.EntityApproval, ID: model.NamespacedID{Namespace: "test/approval", Value: id}}
}

func int64ptr(value int64) *int64 { return &value }

func hasTarget(targets []TargetEvidence, kind, value string) bool {
	for _, target := range targets {
		if target.Kind == kind && target.Value == value {
			return true
		}
	}
	return false
}

func elapsed(values []Elapsed, kind string) int64 {
	for _, value := range values {
		if value.Kind == kind {
			return value.WallMillis
		}
	}
	return -1
}
