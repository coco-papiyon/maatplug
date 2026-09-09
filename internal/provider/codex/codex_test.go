package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"maatplug/internal/model"
	"maatplug/internal/recorder"
	"maatplug/internal/store"
)

func TestUserPromptAndStopUseTurnID(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	s := &store.Store{Root: root}
	h := NewHandler(recorder.New(s))

	prompt := fmt.Sprintf(`{"session_id":"s1","cwd":%q,"turn_id":"turn-42","prompt":"implement it"}`, cwd)
	if err := h.Handle("UserPromptSubmit", []byte(prompt)); err != nil {
		t.Fatal(err)
	}
	stop := fmt.Sprintf(`{"session_id":"s1","cwd":%q,"turn_id":"turn-42","last_assistant_message":"done"}`, cwd)
	if err := h.Handle("Stop", []byte(stop)); err != nil {
		t.Fatal(err)
	}

	events, err := store.ReadEvents(s.SessionPath(store.ProjectID(cwd), ProviderName, "s1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Type != model.EventInstruction || events[0].InstructionID != "turn-42" {
		t.Fatalf("unexpected instruction: %+v", events[0])
	}
	if events[1].Type != model.EventTurnEnd || events[1].InstructionID != "turn-42" {
		t.Fatalf("unexpected turn end: %+v", events[1])
	}
	if !events[1].TokensUnavailable {
		t.Fatal("Codex turn should mark tokens unavailable")
	}
}

func TestStopRecordsTranscriptTokens(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	s := &store.Store{Root: root}
	h := NewHandler(recorder.New(s))

	const turnID = "turn-42"
	transcriptPath := filepath.Join(t.TempDir(), "rollout.jsonl")
	transcript := `{"type":"token_usage_record","payload":{"root_turn_id":"turn-42","turn_token_usage":{"input_tokens":100,"cached_input_tokens":40,"cache_write_input_tokens":10,"output_tokens":20,"reasoning_output_tokens":5,"total_tokens":120}}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-42"}}` + "\n"
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}

	prompt := fmt.Sprintf(`{"session_id":"s1","cwd":%q,"turn_id":%q,"prompt":"implement it"}`, cwd, turnID)
	if err := h.Handle("UserPromptSubmit", []byte(prompt)); err != nil {
		t.Fatal(err)
	}
	stop := fmt.Sprintf(`{"session_id":"s1","transcript_path":%q,"cwd":%q,"turn_id":%q}`, transcriptPath, cwd, turnID)
	if err := h.Handle("Stop", []byte(stop)); err != nil {
		t.Fatal(err)
	}

	events, err := store.ReadEvents(s.SessionPath(store.ProjectID(cwd), ProviderName, "s1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	got := events[1]
	if got.TokensUnavailable {
		t.Fatal("tokens unexpectedly unavailable")
	}
	want := model.TokenUsage{Input: 50, Output: 20, CacheRead: 40, CacheCreation: 10, Total: 120}
	if got.Tokens != want {
		t.Fatalf("Tokens = %+v, want %+v", got.Tokens, want)
	}
}

func TestPromptRequiresTurnID(t *testing.T) {
	h := NewHandler(recorder.New(&store.Store{Root: t.TempDir()}))
	payload := fmt.Sprintf(`{"session_id":"s1","cwd":%q,"prompt":"missing id"}`, t.TempDir())
	if err := h.Handle("UserPromptSubmit", []byte(payload)); err == nil {
		t.Fatal("expected missing turn_id error")
	}
}

func TestDuplicateStopDoesNotDuplicateTurnEnd(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	s := &store.Store{Root: root}
	h := NewHandler(recorder.New(s))
	prompt := fmt.Sprintf(`{"session_id":"s1","cwd":%q,"turn_id":"turn-42","prompt":"once"}`, cwd)
	stop := fmt.Sprintf(`{"session_id":"s1","cwd":%q,"turn_id":"turn-42"}`, cwd)
	if err := h.Handle("UserPromptSubmit", []byte(prompt)); err != nil {
		t.Fatal(err)
	}
	if err := h.Handle("Stop", []byte(stop)); err != nil {
		t.Fatal(err)
	}
	if err := h.Handle("Stop", []byte(stop)); err == nil {
		t.Fatal("duplicate Stop should fail")
	}

	events, err := store.ReadEvents(s.SessionPath(store.ProjectID(cwd), ProviderName, "s1"))
	if err != nil {
		t.Fatal(err)
	}
	var count int
	for _, event := range events {
		if event.Type == model.EventTurnEnd {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("got %d turn_end events, want 1", count)
	}
}
