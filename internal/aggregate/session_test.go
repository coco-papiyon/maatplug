package aggregate

import (
	"errors"
	"testing"
	"time"

	"maatplug/internal/model"
	"maatplug/internal/store"
)

func setupSession(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s := &store.Store{Root: dir}
	const projectID = "proj1"
	if err := s.EnsureProject(projectID, "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}
	return s, projectID
}

func TestSessions_ListsAndSortsByMostRecentlyStarted(t *testing.T) {
	s, projectID := setupSession(t)

	older := s.SessionPath(projectID, "claude", "session-older")
	newer := s.SessionPath(projectID, "claude", "session-newer")

	if err := s.AppendEvent(older, model.SessionStartEvent{
		Type: model.EventSessionStart, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(newer, model.SessionStartEvent{
		Type: model.EventSessionStart, Timestamp: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	sessions, err := Sessions(s, projectID, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("len(sessions) = %d, want 2", len(sessions))
	}
	if sessions[0].SessionID != "session-newer" || sessions[1].SessionID != "session-older" {
		t.Errorf("order = [%s, %s], want [session-newer, session-older]", sessions[0].SessionID, sessions[1].SessionID)
	}
	if sessions[0].Provider != "claude" {
		t.Errorf("Provider = %q, want claude", sessions[0].Provider)
	}
}

func TestSessionDetailFor_BuildsFullTimeline(t *testing.T) {
	s, projectID := setupSession(t)
	path := s.SessionPath(projectID, "claude", "session1")

	appendEvent := func(ev any) {
		t.Helper()
		if err := s.AppendEvent(path, ev); err != nil {
			t.Fatal(err)
		}
	}

	appendEvent(model.SessionStartEvent{
		Type: model.EventSessionStart, Cwd: "/repo",
		Cloc: &model.ClocResult{Code: 100},
	})
	appendEvent(model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i1", Text: "fix the bug"})
	appendEvent(model.TurnEndEvent{
		Type: model.EventTurnEnd, InstructionID: "i1",
		Diff:   model.DiffStat{Added: 5, Removed: 1, FilesChanged: 1},
		Tokens: &model.TokenUsage{Total: 200},
	})
	// A second instruction that never got a Stop (session still open).
	appendEvent(model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i2", Text: "in progress"})
	appendEvent(model.SessionEndEvent{Type: model.EventSessionEnd, Cloc: &model.ClocResult{Code: 104}})

	detail, err := SessionDetailFor(s, projectID, "claude", "session1")
	if err != nil {
		t.Fatal(err)
	}

	if detail.Cwd != "/repo" {
		t.Errorf("Cwd = %q, want /repo", detail.Cwd)
	}
	if detail.ClocStart == nil || detail.ClocStart.Code != 100 {
		t.Errorf("ClocStart = %+v, want Code=100", detail.ClocStart)
	}
	if detail.ClocEnd == nil || detail.ClocEnd.Code != 104 {
		t.Errorf("ClocEnd = %+v, want Code=104", detail.ClocEnd)
	}
	if len(detail.Turns) != 2 {
		t.Fatalf("len(Turns) = %d, want 2", len(detail.Turns))
	}

	t1 := detail.Turns[0]
	if t1.InstructionID != "i1" || t1.Text != "fix the bug" || !t1.Resolved {
		t.Errorf("Turns[0] = %+v, want i1/fix the bug/resolved", t1)
	}
	if t1.Tokens == nil || t1.Tokens.Total != 200 {
		t.Errorf("Turns[0].Tokens = %+v, want Total=200", t1.Tokens)
	}

	t2 := detail.Turns[1]
	if t2.InstructionID != "i2" || t2.Resolved {
		t.Errorf("Turns[1] = %+v, want i2/unresolved (no turn_end was written)", t2)
	}
}

func TestSessionDetailFor_NotFound(t *testing.T) {
	s, projectID := setupSession(t)

	_, err := SessionDetailFor(s, projectID, "claude", "does-not-exist")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("err = %v, want ErrSessionNotFound", err)
	}
}
