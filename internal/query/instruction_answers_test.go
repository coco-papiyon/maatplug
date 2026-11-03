package query

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"maatplug/internal/contentstore"
	"maatplug/internal/model"
)

func TestInstructionAnswersFromSavedStopEvidence(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			root := t.TempDir()
			base := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
			field := "turn_id"
			if provider == "claude" {
				field = "prompt_id"
			}
			instruction := model.EntityRef{Kind: model.EntityInstruction, ID: model.NamespacedID{Namespace: provider + "/instruction", Value: "same-id"}}
			scope := func(session string) []model.EntityRef {
				return []model.EntityRef{{Kind: model.EntitySession, ID: model.NamespacedID{Namespace: provider, Value: session}}}
			}
			inputEvent := func(id, session string) model.EventEnvelope {
				return model.EventEnvelope{EventType: model.EventInstructionRecorded, EventID: model.NamespacedID{Namespace: "input", Value: id}, Scope: scope(session)}
			}
			inputA, inputB := inputEvent("a", "session-a"), inputEvent("b", "session-b")
			stop := func(session, text string, seconds int, idField string) model.EventEnvelope {
				raw := []byte(fmt.Sprintf(`{"session_id":%q,"cwd":"/repo","hook_event_name":"Stop",%q:"same-id","last_assistant_message":%q}`, session, idField, text))
				ref, err := contentstore.New(root).Put(raw, model.ContentRaw, "application/json", "test-stop")
				if err != nil {
					t.Fatal(err)
				}
				payload, _ := json.Marshal(model.CollectionStatePayload{Evidence: []model.ContentRef{ref}})
				return model.EventEnvelope{EventType: model.EventCollectionState, Source: model.EventSource{Provider: provider, Surface: "Stop", Kind: model.SourceHook}, Scope: scope(session), Time: model.EventTime{ReceivedAt: base.Add(time.Duration(seconds) * time.Second)}, Payload: payload}
			}
			latest := "最終回答\n<script>plain text</script>"
			events := []model.EventEnvelope{inputA, inputB, stop("session-a", latest, 20, field), stop("session-b", "other session", 10, field), stop("session-a", "older answer", 5, field), stop("session-a", "do not guess missing identifier", 30, "unrecognized_id"), stop("session-a", "", 40, field)}
			subagent := stop("session-a", "subagent answer", 50, field)
			subagent.Source.Surface = "SubagentStop"
			events = append(events, subagent)
			got := attachInstructionAnswers(root, []InstructionView{{Instruction: instruction, EventID: inputA.EventID}, {Instruction: instruction, EventID: inputB.EventID}}, events)
			if got[0].FinalAnswer != latest || got[1].FinalAnswer != "other session" {
				t.Fatalf("answers = %q / %q", got[0].FinalAnswer, got[1].FinalAnswer)
			}
			missing := attachInstructionAnswers(t.TempDir(), []InstructionView{{Instruction: instruction, EventID: inputA.EventID}}, events)
			if missing[0].FinalAnswer != "" {
				t.Fatal("missing content should not produce an answer")
			}
		})
	}
}
