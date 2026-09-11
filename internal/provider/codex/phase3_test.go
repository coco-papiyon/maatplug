package codex

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
	"time"

	"maatplug/internal/model"
	provideradapter "maatplug/internal/provider/adapter"
	"maatplug/internal/reconcile"
)

var phase3TestTime = time.Date(2026, 9, 10, 1, 2, 3, 0, time.UTC)

func TestPhase3HookFixtureDecodesAndNormalizes(t *testing.T) {
	for _, line := range phase3FixtureLines(t, "testdata/phase0/hook-lifecycle.jsonl") {
		event := phase3HookEventName(t, line.Data)
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
	}
}

func TestPhase3TranscriptUsageAdoptsOnlyPerResponseDeltas(t *testing.T) {
	batch := normalizePhase3Fixture(t, "transcript-usage.jsonl", "turn-codex-001")
	counts := phase3EventCounts(batch.Events)
	if counts[model.EventModelCall] != 2 || counts[model.EventUsageReported] != 6 {
		t.Fatalf("counts = %+v", counts)
	}
	adopted, candidates, total := 0, 0, int64(0)
	for _, event := range batch.Events {
		if event.EventType != model.EventUsageReported {
			continue
		}
		if event.Adoption == nil {
			t.Fatalf("missing adoption: %+v", event)
		}
		if event.Adoption.Status == model.AdoptionAdopted {
			adopted++
			var payload model.UsagePayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Normalized == nil || payload.Normalized.Totals[0].Tokens.Value == nil {
				t.Fatalf("normalized = %+v", payload.Normalized)
			}
			total += *payload.Normalized.Totals[0].Tokens.Value
		} else if event.Adoption.Status == model.AdoptionCandidate {
			candidates++
		}
	}
	if adopted != 2 || candidates != 4 || total != 2800 {
		t.Fatalf("adopted=%d candidates=%d total=%d", adopted, candidates, total)
	}
}

func TestPhase3AllCodexFixturesNormalize(t *testing.T) {
	tests := []struct {
		name    string
		kind    model.EventKind
		minimum int
	}{
		{"command-execution-legacy-0.151.jsonl", model.EventOperationResult, 1},
		{"file-change-0.153.jsonl", model.EventArtifactObserved, 2},
		{"exec-events.jsonl", model.EventUsageReported, 1},
		{"app-server-schema-events.jsonl", model.EventUsageReported, 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			batch := normalizePhase3Fixture(t, test.name, "turn-codex-001")
			if got := phase3EventCounts(batch.Events)[test.kind]; got < test.minimum {
				t.Fatalf("%s count = %d, want >= %d; all=%+v", test.kind, got, test.minimum, phase3EventCounts(batch.Events))
			}
			lineCount := len(phase3FixtureLines(t, "testdata/phase0/"+test.name))
			if got := phase3EventCounts(batch.Events)[model.EventCollectionState]; got != lineCount {
				t.Fatalf("raw line events = %d, want %d", got, lineCount)
			}
		})
	}
}

func TestPhase3CommandCarriesExitCodeAndDuration(t *testing.T) {
	batch := normalizePhase3Fixture(t, "command-execution-legacy-0.151.jsonl", "turn-codex-legacy-001")
	requestFound := false
	for _, event := range batch.Events {
		if event.EventType == model.EventOperationRequested {
			requestFound = true
			if event.Time.ProviderAt == nil || !event.Time.ProviderAt.Before(phase3TestTime) {
				t.Fatalf("request start was not derived from duration: %+v", event.Time)
			}
		}
		if event.EventType != model.EventOperationResult {
			continue
		}
		var payload model.OperationResultPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.ExitCode == nil || *payload.ExitCode != 0 {
			t.Fatalf("exitCode = %v", payload.ExitCode)
		}
		if payload.DurationMillis == nil || *payload.DurationMillis != 626 {
			t.Fatalf("durationMillis = %v", payload.DurationMillis)
		}
		if !requestFound {
			t.Fatal("operation request missing")
		}
		return
	}
	t.Fatal("operation result missing")
}

func TestPhase3PatchCreatesEditOperationAndCorrelatesArtifacts(t *testing.T) {
	batch := normalizePhase3Fixture(t, "file-change-0.153.jsonl", "turn-codex-001")
	counts := phase3EventCounts(batch.Events)
	if counts[model.EventOperationRequested] != 1 || counts[model.EventOperationResult] != 1 || counts[model.EventArtifactObserved] != 2 {
		t.Fatalf("counts = %+v", counts)
	}
	for _, event := range batch.Events {
		if event.EventType != model.EventArtifactObserved {
			continue
		}
		if len(event.Relations) != 1 || event.Relations[0].Kind != model.RelationResultOf || event.Relations[0].To.Kind != model.EntityOperation {
			t.Fatalf("artifact relation = %+v", event.Relations)
		}
	}
}

func TestPhase3UnknownAndPartialRecordsRemainDiagnostic(t *testing.T) {
	hook := provideradapter.Hook{SessionID: "session", RunID: "turn", TurnID: "turn"}
	chunk := provideradapter.Transcript{Path: "future.jsonl", Generation: "g", Lines: []provideradapter.Line{{Number: 0, Data: []byte(`{"type":"future-record"}`)}}, IncompleteTail: true}
	batch, err := NormalizeTranscript("project", "receipt", phase3TestTime, hook, chunk)
	if err != nil {
		t.Fatal(err)
	}
	if phase3EventCounts(batch.Events)[model.EventCollectionState] != 2 {
		t.Fatalf("events = %+v", batch.Events)
	}
}

func TestPhase3MissingUsageComponentsRemainUnavailable(t *testing.T) {
	line := []byte(`{"timestamp":"2026-09-10T00:00:00Z","type":"token_usage_record","payload":{"response_id":"response","turn_id":"turn","usage":{"input_tokens":3,"output_tokens":1}}}`)
	hook := provideradapter.Hook{SessionID: "session", RunID: "turn", TurnID: "turn"}
	batch, err := NormalizeTranscript("project", "receipt", phase3TestTime, hook, provideradapter.Transcript{Path: "missing.jsonl", Generation: "g", Lines: []provideradapter.Line{{Data: line}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range batch.Events {
		if event.EventType != model.EventUsageReported {
			continue
		}
		var payload model.UsagePayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Normalized != nil {
			t.Fatalf("missing components were normalized: %+v", payload.Normalized)
		}
		if _, found := payload.Raw.Fields["cached_input_tokens"]; found {
			t.Fatal("missing cached_input_tokens became a zero value")
		}
		if len(event.Availability) == 0 || event.Availability[0].Status != model.AvailabilityUnavailable {
			t.Fatalf("availability = %+v", event.Availability)
		}
		return
	}
	t.Fatal("usage event missing")
}

func TestPhase3TurnCompletedUsageIsCumulativeFallback(t *testing.T) {
	lines := []provideradapter.Line{
		{Number: 1, Data: []byte(`{"type":"turn.completed","usage":{"input_tokens":90,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":10}}`)},
		{Number: 2, Data: []byte(`{"type":"turn.completed","usage":{"input_tokens":220,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":30}}`)},
	}
	hook := provideradapter.Hook{SessionID: "thread", RunID: "turn", TurnID: "turn"}
	batch, err := NormalizeTranscript("project", "receipt", phase3TestTime, hook, provideradapter.Transcript{Path: "cli.jsonl", Generation: "g", Lines: lines})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reconcile.Build(batch.Events)
	if err != nil {
		t.Fatal(err)
	}
	if result.KnownSubtotal != 250 || len(result.SelectedEventIDs) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestPhase3TurnCompletedIsSuppressedWhenResponseDeltaExists(t *testing.T) {
	lines := []provideradapter.Line{
		{Number: 1, Data: []byte(`{"type":"turn.completed","usage":{"input_tokens":220,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":30}}`)},
		{Number: 2, Data: []byte(`{"timestamp":"2026-09-10T00:00:01Z","type":"token_usage_record","payload":{"response_id":"response","turn_id":"turn","usage":{"input_tokens":90,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":10,"total_tokens":100}}}`)},
	}
	hook := provideradapter.Hook{SessionID: "thread", RunID: "turn", TurnID: "turn"}
	batch, err := NormalizeTranscript("project", "receipt", phase3TestTime, hook, provideradapter.Transcript{Path: "mixed.jsonl", Generation: "g", Lines: lines})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reconcile.Build(batch.Events)
	if err != nil {
		t.Fatal(err)
	}
	if result.KnownSubtotal != 100 || len(result.SelectedEventIDs) != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestPhase3ConflictingProviderTotalIsNotNormalized(t *testing.T) {
	line := []byte(`{"timestamp":"2026-09-10T00:00:00Z","type":"token_usage_record","payload":{"response_id":"response","turn_id":"turn","usage":{"input_tokens":3,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"total_tokens":10}}}`)
	hook := provideradapter.Hook{SessionID: "session", RunID: "turn", TurnID: "turn"}
	batch, err := NormalizeTranscript("project", "receipt", phase3TestTime, hook, provideradapter.Transcript{Path: "conflict.jsonl", Generation: "g", Lines: []provideradapter.Line{{Data: line}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range batch.Events {
		if event.EventType != model.EventUsageReported {
			continue
		}
		var payload model.UsagePayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Normalized != nil || len(event.Availability) == 0 || event.Availability[0].Status != model.AvailabilityConflict {
			t.Fatalf("event = %+v payload=%+v", event, payload)
		}
		return
	}
	t.Fatal("usage event missing")
}

func normalizePhase3Fixture(t *testing.T, name, turnID string) provideradapter.Batch {
	t.Helper()
	lines := phase3FixtureLines(t, "testdata/phase0/"+name)
	hook := provideradapter.Hook{SessionID: "session-codex-001", RunID: turnID, TurnID: turnID, Model: "gpt-5.6-sol"}
	batch, err := NormalizeTranscript("project", "receipt", phase3TestTime, hook, provideradapter.Transcript{Path: name, Generation: "fixture-v1", Lines: lines, NextLine: uint64(len(lines))})
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func phase3FixtureLines(t *testing.T, path string) []provideradapter.Line {
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

func phase3HookEventName(t *testing.T, raw []byte) string {
	t.Helper()
	var value struct {
		Event string `json:"hook_event_name"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value.Event
}

func phase3EventCounts(events []model.EventEnvelope) map[model.EventKind]int {
	result := map[model.EventKind]int{}
	for _, event := range events {
		result[event.EventType]++
	}
	return result
}
