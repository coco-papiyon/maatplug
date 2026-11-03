package query

import (
	"encoding/json"
	"sort"
	"strings"

	"maatplug/internal/contentstore"
	"maatplug/internal/model"
	"maatplug/internal/provider/claude"
	"maatplug/internal/provider/codex"
)

// attachInstructionAnswers derives answers from immutable Stop hook evidence.
// Explicit turn/prompt IDs are required so overlapping or missing prompts cannot
// silently attach a different conversation's answer to an instruction.
func attachInstructionAnswers(root string, instructions []InstructionView, events []model.EventEnvelope) []InstructionView {
	contents := contentstore.New(root)
	sessions := map[string]string{}
	for _, event := range events {
		if event.EventType == model.EventInstructionRecorded {
			sessions[event.EventID.Namespace+"\x00"+event.EventID.Value] = scopeValue(event.Scope, model.EntitySession)
		}
	}
	stops := []model.EventEnvelope{}
	for _, event := range events {
		if event.EventType == model.EventCollectionState && event.Source.Kind == model.SourceHook && (event.Source.Surface == "Stop" || event.Source.Surface == "hook/Stop") {
			stops = append(stops, event)
		}
	}
	sort.SliceStable(stops, func(i, j int) bool { return stops[i].Time.ReceivedAt.Before(stops[j].Time.ReceivedAt) })
	answers := map[string]string{}
	for _, event := range stops {
		var state model.CollectionStatePayload
		if json.Unmarshal(event.Payload, &state) != nil {
			continue
		}
		for _, ref := range state.Evidence {
			if ref.Variant != model.ContentRaw || ref.Availability != model.AvailabilityObserved {
				continue
			}
			raw, err := contents.Get(ref)
			if err != nil {
				continue
			}
			var instruction model.EntityRef
			var text string
			switch event.Source.Provider {
			case "codex":
				instruction, text, err = codex.DecodeFinalAnswer(raw)
			case "claude":
				instruction, text, err = claude.DecodeFinalAnswer(raw)
			default:
				continue
			}
			if err != nil || instruction.ID.Value == "" || strings.TrimSpace(text) == "" {
				continue
			}
			key := scopeValue(event.Scope, model.EntitySession) + "\x00" + instructionStatsKey(instruction)
			answers[key] = text
		}
	}
	for i := range instructions {
		view := &instructions[i]
		session := sessions[view.EventID.Namespace+"\x00"+view.EventID.Value]
		view.FinalAnswer = answers[session+"\x00"+instructionStatsKey(view.Instruction)]
	}
	return instructions
}
