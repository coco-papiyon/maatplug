package query

import (
	"encoding/json"
	"strings"

	"maatplug/internal/model"
	"maatplug/internal/operation"
)

// Missing diffs do not imply no edits. Only completed instructions whose
// correlated operations are absent or explicitly read-only get that display state.
func markInstructionsWithoutEdits(instructions []InstructionView, ops operation.Result, events []model.EventEnvelope) []InstructionView {
	route := func(scope []model.EntityRef) string {
		return scopeValue(scope, model.EntitySession) + "\x00" + scopeValue(scope, model.EntityRun)
	}
	inputRoutes := map[model.NamespacedID]string{}
	completed := map[string]bool{}
	unsafeRoutes := map[string]bool{}
	for _, event := range events {
		key := route(event.Scope)
		switch event.EventType {
		case model.EventInstructionRecorded:
			inputRoutes[event.EventID] = key
		case model.EventStateChanged:
			var state model.StateTransitionPayload
			if json.Unmarshal(event.Payload, &state) == nil && state.Subject.Kind == model.EntityRun && state.To == "completed" {
				completed[key] = true
			}
		case model.EventCollectionState:
			var state model.CollectionStatePayload
			if json.Unmarshal(event.Payload, &state) == nil && (state.State == "unavailable" || state.State == "partial_tail") {
				unsafeRoutes[key] = true
			}
		}
	}
	unsafeInputs := map[string]bool{}
	for _, op := range ops.Operations {
		if len(op.Instructions) == 0 {
			// An uncorrelated operation could be the edit belonging to this input.
			unsafeRoutes[route(op.Scope)] = true
		}
		if !readOnlyOperation(op) {
			for _, related := range op.Instructions {
				unsafeInputs[instructionStatsKey(related.Entity)] = true
			}
		}
	}
	for i := range instructions {
		view := &instructions[i]
		key, hasRoute := inputRoutes[view.EventID]
		changes := &view.Activity.Changes
		if changes.Availability != model.AvailabilityUnknown || !hasRoute || !completed[key] || unsafeRoutes[key] || unsafeInputs[instructionStatsKey(view.Instruction)] {
			continue
		}
		*changes = ChangeSummary{NoEdits: true, Availability: model.AvailabilityInferred, Reason: "completed instruction has no recorded editing operations"}
	}
	return instructions
}

func readOnlyOperation(op operation.Operation) bool {
	// A generic tool, connector or shell can write files even when it has no
	// file target. Never infer no edits from a zero file count alone.
	if op.Kind != "tool" && op.Kind != "read" {
		return false
	}
	if len(op.Artifacts) > 0 || len(op.ObservedEffects) > 0 {
		return false
	}
	switch strings.ToLower(op.Name) {
	case "read", "glob", "grep", "ls", "read_file", "list_directory", "list_dir":
		return true
	default:
		return false
	}
}
