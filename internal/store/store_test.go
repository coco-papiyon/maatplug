package store

import (
	"path/filepath"
	"testing"
	"time"

	"maatplug/internal/model"
)

func TestAppendAndReadEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-session-1.jsonl")

	s := &Store{Root: dir}
	if err := s.AppendEvent(path, model.SessionStartEvent{
		Type: model.EventSessionStart, Timestamp: time.Now(), SessionID: "session-1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(path, model.InstructionEvent{
		Type: model.EventInstruction, Timestamp: time.Now(),
		InstructionID: "i1", Text: "do the thing",
		Baseline: model.DiffBaseline{GitTree: "abc123"},
	}); err != nil {
		t.Fatal(err)
	}

	events, err := ReadEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	if events[0].Type != model.EventSessionStart {
		t.Errorf("events[0].Type = %q", events[0].Type)
	}
	if events[1].InstructionID != "i1" {
		t.Errorf("events[1].InstructionID = %q", events[1].InstructionID)
	}
}

func TestReadEventsMissingFile(t *testing.T) {
	events, err := ReadEvents(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if err != nil {
		t.Fatalf("ReadEvents() on missing file returned error: %v", err)
	}
	if events != nil {
		t.Fatalf("ReadEvents() on missing file = %v, want nil", events)
	}
}

func TestFindInstructionBaseline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-session-1.jsonl")
	s := &Store{Root: dir}

	want := model.DiffBaseline{GitTree: "tree-i1"}
	if err := s.AppendEvent(path, model.InstructionEvent{
		Type: model.EventInstruction, InstructionID: "i1", Baseline: want,
	}); err != nil {
		t.Fatal(err)
	}
	// A second instruction in the same session must not shadow the first
	// when looked up by its own id.
	if err := s.AppendEvent(path, model.InstructionEvent{
		Type: model.EventInstruction, InstructionID: "i2", Baseline: model.DiffBaseline{GitTree: "tree-i2"},
	}); err != nil {
		t.Fatal(err)
	}

	got, found, err := FindInstructionBaseline(path, "i1")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("FindInstructionBaseline() found = false, want true")
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}

	_, found, err = FindInstructionBaseline(path, "does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("FindInstructionBaseline() found = true for unknown instructionId")
	}
}

func TestFindUnresolvedInstructions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-session-1.jsonl")
	s := &Store{Root: dir}

	appendEvent := func(ev any) {
		t.Helper()
		if err := s.AppendEvent(path, ev); err != nil {
			t.Fatal(err)
		}
	}

	// i1 is resolved, i2 is not yet.
	appendEvent(model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i1", Baseline: model.DiffBaseline{GitTree: "t1"}})
	appendEvent(model.TurnEndEvent{Type: model.EventTurnEnd, InstructionID: "i1"})
	appendEvent(model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i2", Baseline: model.DiffBaseline{GitTree: "t2"}})

	ids, baseline, found, err := FindUnresolvedInstructions(path)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("found = false, want true (i2 has no turn_end yet)")
	}
	if len(ids) != 1 || ids[0] != "i2" || baseline.GitTree != "t2" {
		t.Errorf("got ids=%v baseline=%+v, want [i2]/t2", ids, baseline)
	}

	// Resolve i2 too; nothing should be left pending.
	appendEvent(model.TurnEndEvent{Type: model.EventTurnEnd, InstructionID: "i2"})
	_, _, found, err = FindUnresolvedInstructions(path)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Errorf("found = %v after every instruction was resolved, want false", found)
	}
}

func TestFindUnresolvedInstructions_QueuedFollowUpReturnsBothInSubmissionOrder(t *testing.T) {
	// A user queuing a follow-up message while Claude is still mid-turn (an
	// ordinary Claude Code feature) fires a second UserPromptSubmit before
	// the first Stop — the same on-disk shape as the same session_id being
	// resumed concurrently from two terminals (see FindUnresolvedInstructions'
	// doc comment: the two are indistinguishable from the hook payload
	// alone). Either way, both instructions come back, oldest first, with
	// the oldest one's baseline — the span a single Stop covering both
	// should be measured against.
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-session-1.jsonl")
	s := &Store{Root: dir}

	appendEvent := func(ev any) {
		t.Helper()
		if err := s.AppendEvent(path, ev); err != nil {
			t.Fatal(err)
		}
	}

	appendEvent(model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i1", Baseline: model.DiffBaseline{GitTree: "t1"}})
	appendEvent(model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i2", Baseline: model.DiffBaseline{GitTree: "t2"}})
	// A single Stop fires now, covering the combined work of both.

	ids, baseline, found, err := FindUnresolvedInstructions(path)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("found = false, want true (two unresolved instructions)")
	}
	if len(ids) != 2 || ids[0] != "i1" || ids[1] != "i2" {
		t.Fatalf("ids = %v, want [i1 i2]", ids)
	}
	if baseline.GitTree != "t1" {
		t.Errorf("baseline = %+v, want the OLDEST instruction's (t1)", baseline)
	}
}

func TestProjectIDStableForSamePath(t *testing.T) {
	dir := t.TempDir()
	a := ProjectID(dir)
	b := ProjectID(dir)
	if a != b {
		t.Errorf("ProjectID not stable: %q != %q", a, b)
	}
	if len(a) != 12 {
		t.Errorf("len(ProjectID()) = %d, want 12", len(a))
	}
}
