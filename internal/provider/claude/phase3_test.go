package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
	"time"

	"maatplug/internal/model"
	provideradapter "maatplug/internal/provider/adapter"
)

var phase3TestTime = time.Date(2026, 9, 10, 1, 2, 3, 0, time.UTC)

func TestPhase3HookFixtureDecodesAndNormalizes(t *testing.T) {
	for _, line := range fixtureLines(t, "testdata/phase0/hook-lifecycle.jsonl") {
		event := hookEventName(t, line.Data)
		hook, err := DecodeHook(event, line.Data)
		if err != nil {
			t.Fatalf("DecodeHook(%s): %v", event, err)
		}
		batch, err := NormalizeHook("project", "receipt-"+event, phase3TestTime, hook)
		if err != nil {
			t.Fatalf("NormalizeHook(%s): %v", event, err)
		}
		if len(batch.Events) != 1 {
			t.Fatalf("NormalizeHook(%s) events = %d", event, len(batch.Events))
		}
		if event == "UserPromptSubmit" && len(batch.Contents) != 1 {
			t.Fatalf("prompt content = %d", len(batch.Contents))
		}
	}
}

func TestPhase3TranscriptDeduplicatesResponseUsageAndCorrelatesTool(t *testing.T) {
	lines := fixtureLines(t, "testdata/phase0/transcript-duplicate-response.jsonl")
	hook := provideradapter.Hook{SessionID: "session-claude-001", RunID: "prompt-claude-001", PromptID: "prompt-claude-001"}
	chunk := provideradapter.Transcript{Path: "fixture.jsonl", Generation: "fixture-v1", Lines: lines, NextLine: uint64(len(lines))}

	batch, err := NormalizeTranscript("project", "receipt", phase3TestTime, hook, chunk)
	if err != nil {
		t.Fatal(err)
	}
	counts := eventCounts(batch.Events)
	if counts[model.EventModelCall] != 1 || counts[model.EventUsageReported] != 1 {
		t.Fatalf("response counts = %+v", counts)
	}
	if counts[model.EventOperationRequested] != 1 || counts[model.EventOperationResult] != 1 {
		t.Fatalf("tool counts = %+v", counts)
	}
	if counts[model.EventCollectionState] != len(lines) {
		t.Fatalf("raw line count = %+v", counts)
	}
	var usage model.UsagePayload
	for _, event := range batch.Events {
		if event.EventType == model.EventUsageReported {
			if err := json.Unmarshal(event.Payload, &usage); err != nil {
				t.Fatal(err)
			}
			if event.Adoption == nil || event.Adoption.Status != model.AdoptionAdopted {
				t.Fatalf("usage adoption = %+v", event.Adoption)
			}
		}
	}
	if usage.Normalized == nil || len(usage.Normalized.Totals) != 1 || usage.Normalized.Totals[0].Tokens.Value == nil || *usage.Normalized.Totals[0].Tokens.Value != 107 {
		t.Fatalf("normalized usage = %+v", usage.Normalized)
	}
	if len(usage.Normalized.Subsets) != 0 {
		t.Fatalf("Claude usage acquired unsupported subsets: %+v", usage.Normalized.Subsets)
	}
}

func TestPhase3BashFixtureProducesShellOperation(t *testing.T) {
	lines := fixtureLines(t, "testdata/phase0/bash-tool-use.jsonl")
	hook := provideradapter.Hook{SessionID: "session-claude-101", RunID: "prompt-claude-101", PromptID: "prompt-claude-101"}
	batch, err := NormalizeTranscript("project", "receipt", phase3TestTime, hook, provideradapter.Transcript{Path: "bash.jsonl", Generation: "fixture-v1", Lines: lines})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range batch.Events {
		if event.EventType != model.EventOperationRequested {
			continue
		}
		var payload model.OperationPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Kind != "shell" {
			t.Fatalf("operation kind = %q", payload.Kind)
		}
		return
	}
	t.Fatal("shell operation was not normalized")
}

func TestPhase3ToolRequestPreservesOperationEnvironment(t *testing.T) {
	raw := []byte(`{"type":"assistant","uuid":"row","requestId":"request","sessionId":"session","promptId":"prompt","timestamp":"2026-09-10T00:00:01Z","cwd":"C:\\workspace","permissionMode":"default","message":{"id":"message","model":"claude","role":"assistant","content":[{"type":"tool_use","id":"tool","name":"Edit","input":{"file_path":"C:\\workspace\\main.go"}}]}}`)
	hook := provideradapter.Hook{SessionID: "session", RunID: "prompt"}
	batch, err := NormalizeTranscript("project", "receipt", phase3TestTime, hook, provideradapter.Transcript{Path: "fixture.jsonl", Generation: "g", Lines: []provideradapter.Line{{Data: raw}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range batch.Events {
		if event.EventType != model.EventOperationRequested {
			continue
		}
		var payload model.OperationPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Kind != "edit" || len(payload.Input) != 1 {
			t.Fatalf("payload = %+v", payload)
		}
		for _, content := range batch.Contents {
			if content.Reference.ID != payload.Input[0].ID {
				continue
			}
			var wrapped struct {
				Environment map[string]any `json:"environment"`
				ToolInput   map[string]any `json:"toolInput"`
			}
			if err := json.Unmarshal(content.Data, &wrapped); err != nil {
				t.Fatal(err)
			}
			if wrapped.Environment["cwd"] != `C:\workspace` || wrapped.Environment["permissionMode"] != "default" || wrapped.ToolInput["file_path"] != `C:\workspace\main.go` {
				t.Fatalf("wrapped input = %+v", wrapped)
			}
			return
		}
		t.Fatal("operation input content missing")
	}
	t.Fatal("operation request missing")
}

func TestPhase3TranscriptMarksUnknownAndPartialRecords(t *testing.T) {
	lines := []provideradapter.Line{{Number: 0, Data: []byte(`{"type":"future-record"}`)}}
	hook := provideradapter.Hook{SessionID: "session", RunID: "run"}
	batch, err := NormalizeTranscript("project", "receipt", phase3TestTime, hook, provideradapter.Transcript{Path: "future.jsonl", Generation: "g", Lines: lines, IncompleteTail: true})
	if err != nil {
		t.Fatal(err)
	}
	if eventCounts(batch.Events)[model.EventCollectionState] != 2 {
		t.Fatalf("events = %+v", batch.Events)
	}
}

func fixtureLines(t *testing.T, path string) []provideradapter.Line {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var result []provideradapter.Line
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		result = append(result, provideradapter.Line{Number: uint64(len(result)), Data: append([]byte(nil), scanner.Bytes()...)})
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func hookEventName(t *testing.T, raw []byte) string {
	t.Helper()
	var value struct {
		Event string `json:"hook_event_name"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value.Event
}

func eventCounts(events []model.EventEnvelope) map[model.EventKind]int {
	result := map[model.EventKind]int{}
	for _, event := range events {
		result[event.EventType]++
	}
	return result
}
