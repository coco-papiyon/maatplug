package collector

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"maatplug/internal/contentstore"
	"maatplug/internal/ingest"
	"maatplug/internal/journal"
	"maatplug/internal/model"
	"maatplug/internal/store"
)

func TestRunOncePersistsContentThenJournalAndCatalog(t *testing.T) {
	root := t.TempDir()
	worker := New(root)
	content := []byte("captured prompt")
	ref := contentstore.Describe(content, model.ContentRaw, "text/plain", "test-policy")
	event := instructionEvent("receipt-event", ref)
	route := ingest.Route{ProjectID: "project-a", Provider: "test", SessionID: "session-a", RunID: "run-a"}
	_, err := worker.Inbox.EnqueueEvent(route, event, []ingest.ContentItem{{Reference: ref, Policy: "test-policy", DataBase64: base64.StdEncoding.EncodeToString(content)}})
	if err != nil {
		t.Fatal(err)
	}

	stats, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Completed != 1 || stats.Catalogs != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if got, err := worker.Content.Get(ref); err != nil || string(got) != string(content) {
		t.Fatalf("content = %q, %v", got, err)
	}
	events, err := worker.Journal.Read(journal.Route{ProjectID: "project-a", Provider: "test", SessionID: "session-a", RunID: "run-a"})
	if err != nil || len(events) != 1 {
		t.Fatalf("journal events = %d, %v", len(events), err)
	}
	if _, err := os.Stat((&store.Store{Root: root}).ProjectCatalogPath("project-a")); err != nil {
		t.Fatal(err)
	}
	diagnostics, err := worker.Inbox.Diagnostics()
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics.Pending != 0 || diagnostics.Processing != 0 {
		t.Fatalf("inbox not completed: %+v", diagnostics)
	}
}

type testMaintenanceJob struct{ calls int }

func (job *testMaintenanceJob) RunDue(context.Context) (bool, error) {
	job.calls++
	return true, nil
}

type retryingMaintenanceJob struct {
	calls  int
	cancel context.CancelFunc
	err    error
}

func (job *retryingMaintenanceJob) RunDue(context.Context) (bool, error) {
	job.calls++
	if job.calls == 1 {
		return true, job.err
	}
	job.cancel()
	return false, nil
}

func TestWatchReportsCycleErrorAndRetriesUntilCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cycleErr := errors.New("temporary catalog lock contention")
	job := &retryingMaintenanceJob{cancel: cancel, err: cycleErr}
	worker := New(t.TempDir())
	worker.Jobs = []MaintenanceJob{job}
	var reported []error
	worker.OnError = func(err error) { reported = append(reported, err) }

	err := worker.Watch(ctx, time.Millisecond, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Watch() error = %v, want context cancellation", err)
	}
	if job.calls != 2 {
		t.Fatalf("maintenance calls = %d, want 2", job.calls)
	}
	if len(reported) != 1 || !errors.Is(reported[0], cycleErr) {
		t.Fatalf("reported errors = %v, want [%v]", reported, cycleErr)
	}
}

func TestRunOnceRebuildsProjectCatalogsAfterMaintenance(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "projects", "project-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	job := &testMaintenanceJob{}
	worker := New(root)
	worker.Jobs = []MaintenanceJob{job}
	stats, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if job.calls != 1 || stats.JobsRun != 1 || stats.Catalogs != 1 {
		t.Fatalf("stats = %+v, calls = %d", stats, job.calls)
	}
	if _, err := os.Stat((&store.Store{Root: root}).ProjectCatalogPath("project-a")); err != nil {
		t.Fatal(err)
	}
}

func TestRestartAfterAppendDoesNotDuplicateEvent(t *testing.T) {
	root := t.TempDir()
	worker := New(root)
	worker.Inbox.StaleAfter = 0
	event := collectionEvent("stable-event")
	route := ingest.Route{ProjectID: "project-a", Provider: "test", SessionID: "session-a", RunID: "run-a"}
	if _, err := worker.Inbox.EnqueueEvent(route, event, nil); err != nil {
		t.Fatal(err)
	}
	claim, err := worker.Inbox.ClaimNext()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Journal.Append(journal.Route{ProjectID: route.ProjectID, Provider: route.Provider, SessionID: route.SessionID, RunID: route.RunID}, *claim.Record.Event); err != nil {
		t.Fatal(err)
	}
	// Simulate a stop after journal fsync but before deleting processing state.
	stats, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Recovered != 1 || stats.Duplicates != 1 || stats.Completed != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	events, err := worker.Journal.Read(journal.Route{ProjectID: route.ProjectID, Provider: route.Provider, SessionID: route.SessionID, RunID: route.RunID})
	if err != nil || len(events) != 1 {
		t.Fatalf("journal events = %d, %v", len(events), err)
	}
}

func TestMissingContentIsQuarantinedAndVisibleToDoctor(t *testing.T) {
	root := t.TempDir()
	worker := New(root)
	worker.MaxAttempts = 1
	missing := contentstore.Describe([]byte("missing"), model.ContentRaw, "text/plain", "test-policy")
	event := instructionEvent("missing-content", missing)
	route := ingest.Route{ProjectID: "project-a", Provider: "test", SessionID: "session-a", RunID: "run-a"}
	if _, err := worker.Inbox.EnqueueEvent(route, event, nil); err != nil {
		t.Fatal(err)
	}
	stats, err := worker.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected quarantined record error")
	}
	if stats.Quarantined != 1 || stats.Completed != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	report, err := worker.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	if report.Inbox.Failed != 1 || report.Healthy() {
		t.Fatalf("doctor = %+v", report)
	}
}

func TestProviderHookBecomesRawEvidenceEvent(t *testing.T) {
	root := t.TempDir()
	worker := New(root)
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"session_id":"session-1","cwd":` + quoted(cwd) + `,"turn_id":"turn-1","prompt":"secret"}`)
	if _, err := worker.Inbox.EnqueueHook("codex", "UserPromptSubmit", payload); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	route := journal.Route{ProjectID: store.ProjectID(cwd), Provider: "codex", SessionID: "session-1", RunID: "turn-1"}
	events, err := worker.Journal.Read(route)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].EventType != model.EventCollectionState || events[1].EventType != model.EventInstructionRecorded {
		t.Fatalf("events = %+v", events)
	}
	refs, err := model.ContentReferences(events[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("content refs = %d", len(refs))
	}
	got, err := worker.Content.Get(refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("raw evidence changed: %s", got)
	}
	report, err := worker.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	if !report.Healthy() {
		t.Fatalf("doctor = %+v", report)
	}
}

func TestStopCollectsTranscriptDifferentiallyAndHandlesPartialAndRotation(t *testing.T) {
	root := t.TempDir()
	worker := New(root)
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(root, "codex-session.jsonl")
	initial, err := os.ReadFile(filepath.Join("..", "provider", "codex", "testdata", "phase0", "transcript-usage.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	enqueueStop := func() {
		payload, err := json.Marshal(map[string]any{
			"session_id": "session-codex-001", "cwd": cwd, "hook_event_name": "Stop",
			"turn_id": "turn-codex-001", "transcript_path": transcript,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := worker.Inbox.EnqueueHook("codex", "Stop", payload); err != nil {
			t.Fatal(err)
		}
		if _, err := worker.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	route := journal.Route{ProjectID: store.ProjectID(cwd), Provider: "codex", SessionID: "session-codex-001", RunID: "turn-codex-001"}
	usageCount := func() int {
		events, err := worker.Journal.Read(route)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, event := range events {
			if event.EventType == model.EventUsageReported {
				count++
			}
		}
		return count
	}
	knownUsage := func() int64 {
		projection, err := worker.Catalog.RebuildProject(route.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		var total int64
		for _, session := range projection.Sessions {
			for _, run := range session.Runs {
				total += run.KnownUsageTokens
			}
		}
		return total
	}

	enqueueStop()
	if got := usageCount(); got != 6 {
		t.Fatalf("initial usage count = %d", got)
	}
	if got := knownUsage(); got != 2800 {
		t.Fatalf("initial adopted usage = %d", got)
	}
	enqueueStop()
	if got := usageCount(); got != 6 {
		t.Fatalf("unchanged transcript usage count = %d", got)
	}

	third := `{"timestamp":"2026-09-10T00:00:03Z","type":"token_usage_record","payload":{"response_id":"response-codex-003","root_turn_id":"turn-codex-001","turn_id":"turn-codex-001","usage":{"input_tokens":7,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":2,"total_tokens":9},"turn_token_usage":{"input_tokens":7,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":2,"total_tokens":9},"thread_token_usage":{"input_tokens":7,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":2,"total_tokens":9}}}` + "\n"
	file, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(third); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	enqueueStop()
	if got := usageCount(); got != 9 {
		t.Fatalf("appended transcript usage count = %d", got)
	}
	if got := knownUsage(); got != 2809 {
		t.Fatalf("appended adopted usage = %d", got)
	}
	file, err = os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(third); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	enqueueStop()
	if got := usageCount(); got != 9 {
		t.Fatalf("repeated provider response usage count = %d", got)
	}
	if got := knownUsage(); got != 2809 {
		t.Fatalf("repeated provider response adopted usage = %d", got)
	}

	partial := `{"type":"future-record"}`
	file, err = os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(partial); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	enqueueStop()
	if got := usageCount(); got != 9 {
		t.Fatalf("partial transcript usage count = %d", got)
	}
	file, err = os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	enqueueStop()

	rotated := `{"timestamp":"2026-09-10T00:00:04Z","type":"token_usage_record","payload":{"response_id":"response-codex-004","root_turn_id":"turn-codex-001","turn_id":"turn-codex-001","usage":{"input_tokens":5,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"total_tokens":6},"turn_token_usage":{"input_tokens":5,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"total_tokens":6},"thread_token_usage":{"input_tokens":5,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"total_tokens":6}}}` + "\n"
	if err := os.WriteFile(transcript, []byte(rotated), 0o600); err != nil {
		t.Fatal(err)
	}
	enqueueStop()
	if got := usageCount(); got != 12 {
		t.Fatalf("rotated transcript usage count = %d", got)
	}
	if got := knownUsage(); got != 2815 {
		t.Fatalf("rotated adopted usage = %d", got)
	}
	events, err := worker.Journal.Read(route)
	if err != nil {
		t.Fatal(err)
	}
	foundRotation := false
	for _, event := range events {
		if event.EventType != model.EventCollectionState {
			continue
		}
		var payload model.CollectionStatePayload
		if json.Unmarshal(event.Payload, &payload) == nil && payload.State == "rotated" {
			foundRotation = true
		}
	}
	if !foundRotation {
		t.Fatal("rotation diagnostic missing")
	}
}

func instructionEvent(id string, ref model.ContentRef) model.EventEnvelope {
	payload, _ := json.Marshal(model.InstructionPayload{
		Instruction: model.EntityRef{Kind: model.EntityInstruction, ID: model.NamespacedID{Namespace: "test", Value: id + "-instruction"}},
		Actor:       model.EntityRef{Kind: model.EntityActor, ID: model.NamespacedID{Namespace: "test", Value: "user"}},
		Content:     []model.ContentRef{ref}, State: "submitted",
	})
	return baseEvent(id, model.EventInstructionRecorded, payload)
}

func collectionEvent(id string) model.EventEnvelope {
	payload, _ := json.Marshal(model.CollectionStatePayload{
		Stream: model.EntityRef{Kind: model.EntityStream, ID: model.NamespacedID{Namespace: "test", Value: "stream"}}, State: "active", Generation: "one",
	})
	return baseEvent(id, model.EventCollectionState, payload)
}

func baseEvent(id string, kind model.EventKind, payload json.RawMessage) model.EventEnvelope {
	return model.EventEnvelope{
		SchemaVersion: model.SchemaVersion, EventID: model.NamespacedID{Namespace: "test", Value: id}, EventType: kind,
		Source: model.EventSource{Provider: "test", Surface: "test", Kind: model.SourceCollector}, Time: model.EventTime{ReceivedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		Scope: []model.EntityRef{{Kind: model.EntityRun, ID: model.NamespacedID{Namespace: "test", Value: "run-a"}}}, Payload: payload,
	}
}

func quoted(value string) string { data, _ := json.Marshal(value); return string(data) }
