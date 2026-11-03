package query

import (
	"encoding/json"
	"testing"

	"maatplug/internal/model"
	"maatplug/internal/operation"
)

func TestNoEditsRequiresCompletionAndSafeOperations(t *testing.T) {
	id := model.NamespacedID{Namespace: "event", Value: "input"}
	instruction := model.EntityRef{Kind: model.EntityInstruction, ID: model.NamespacedID{Namespace: "codex/instruction", Value: "turn"}}
	scope := []model.EntityRef{{Kind: model.EntitySession, ID: model.NamespacedID{Namespace: "codex", Value: "session"}}, {Kind: model.EntityRun, ID: model.NamespacedID{Namespace: "codex/run", Value: "turn"}}}
	input := model.EventEnvelope{EventType: model.EventInstructionRecorded, EventID: id, Scope: scope}
	state, _ := json.Marshal(model.StateTransitionPayload{Subject: scope[1], To: "completed"})
	done := model.EventEnvelope{EventType: model.EventStateChanged, Scope: scope, Payload: state}
	partial, _ := json.Marshal(model.CollectionStatePayload{State: "partial_tail"})
	gap := model.EventEnvelope{EventType: model.EventCollectionState, Scope: scope, Payload: partial}
	for _, tt := range []struct {
		name                               string
		kind, nameOfTool                   string
		complete, orphan, gap, wantNoEdits bool
	}{
		{"completed chat only", "", "", true, false, false, true},
		{"still running", "", "", false, false, false, false},
		{"read only", "tool", "Read", true, false, false, true},
		{"shell may write", "shell", "exec", true, false, false, false},
		{"unmeasured edit", "edit", "Edit", true, false, false, false},
		{"unknown tool", "tool", "custom", true, false, false, false},
		{"uncorrelated operation", "tool", "Read", true, true, false, false},
		{"incomplete collection", "", "", true, false, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			events := []model.EventEnvelope{input}
			if tt.complete {
				events = append(events, done)
			}
			if tt.gap {
				events = append(events, gap)
			}
			ops := operation.Result{}
			if tt.kind != "" {
				op := operation.Operation{Kind: tt.kind, Name: tt.nameOfTool, Scope: scope}
				if !tt.orphan {
					op.Instructions = []operation.RelatedEntity{{Entity: instruction}}
				}
				ops.Operations = append(ops.Operations, op)
			}
			views := []InstructionView{{Instruction: instruction, EventID: id, Activity: InstructionActivity{Changes: ChangeSummary{Availability: model.AvailabilityUnknown}}}}
			got := markInstructionsWithoutEdits(views, ops, events)[0].Activity.Changes
			if got.NoEdits != tt.wantNoEdits {
				t.Fatalf("changes = %+v; want no edits %t", got, tt.wantNoEdits)
			}
			if !tt.wantNoEdits && got.Availability != model.AvailabilityUnknown {
				t.Fatal("unknown was converted to known")
			}
		})
	}
}

func TestMeasuredZeroDiffIsNoEdits(t *testing.T) {
	zero, one := 0, 1
	result := operation.Result{Operations: []operation.Operation{{Artifacts: []operation.ArtifactEvidence{{Measurement: operation.Measurement{Added: &zero, Removed: &zero, FilesChanged: &zero}}}}}}
	if got := changeSummary(result); !got.NoEdits {
		t.Fatalf("measured zero = %+v", got)
	}
	result.Operations[0].Artifacts[0].Measurement.FilesChanged = &one
	if got := changeSummary(result); got.NoEdits {
		t.Fatal("changed file without line changes is not no edits")
	}
	if got := changeSummary(operation.Result{}); got.NoEdits {
		t.Fatal("absence of measurements alone is not no edits")
	}
}
