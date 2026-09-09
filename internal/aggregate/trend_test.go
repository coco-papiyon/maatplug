package aggregate

import (
	"testing"
	"time"

	"maatplug/internal/model"
	"maatplug/internal/store"
)

func TestTrend_BucketsByEventDayAndZeroFillsGaps(t *testing.T) {
	dir := t.TempDir()
	s := &store.Store{Root: dir}

	const projectID = "proj1"
	if err := s.EnsureProject(projectID, "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}

	appendEvent := func(path string, ev any) {
		t.Helper()
		if err := s.AppendEvent(path, ev); err != nil {
			t.Fatal(err)
		}
	}

	today := truncateToDate(time.Now())
	path := s.SessionPath(projectID, "claude", "session1")
	// One long-running session: session_start 3 days ago, but its two
	// turn_ends land on different days — Trend should bucket each by its
	// OWN timestamp, not the session's start day.
	appendEvent(path, model.SessionStartEvent{Type: model.EventSessionStart, Timestamp: today.AddDate(0, 0, -3)})
	appendEvent(path, model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i1"})
	appendEvent(path, model.TurnEndEvent{
		Type: model.EventTurnEnd, InstructionID: "i1", Timestamp: today.AddDate(0, 0, -3),
		Tokens: &model.TokenUsage{Total: 100},
	})
	appendEvent(path, model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i2"})
	appendEvent(path, model.TurnEndEvent{
		Type: model.EventTurnEnd, InstructionID: "i2", Timestamp: today,
		Tokens: &model.TokenUsage{Total: 250},
	})
	// An abandoned turn on the in-between day should not contribute.
	appendEvent(path, model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i3"})
	appendEvent(path, model.TurnEndEvent{
		Type: model.EventTurnEnd, InstructionID: "i3", Timestamp: today.AddDate(0, 0, -1),
		Abandoned: true, TokensUnavailable: true,
	})

	points, err := Trend(s, projectID, Filter{Since: today.AddDate(0, 0, -3)})
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 4 {
		t.Fatalf("Trend(...) returned %d points, want 4 (one per day from -3d to today)", len(points))
	}

	want := map[string]int{
		today.AddDate(0, 0, -3).Format("2006-01-02"): 100,
		today.AddDate(0, 0, -2).Format("2006-01-02"): 0,
		today.AddDate(0, 0, -1).Format("2006-01-02"): 0, // abandoned turn contributes nothing
		today.Format("2006-01-02"):                   250,
	}
	for _, p := range points {
		if want[p.Date] != p.TotalTokens {
			t.Errorf("Trend(...) day %s TotalTokens = %d, want %d", p.Date, p.TotalTokens, want[p.Date])
		}
	}
}

func TestTrend_DefaultsToBoundedWindowWhenSinceIsUnset(t *testing.T) {
	dir := t.TempDir()
	s := &store.Store{Root: dir}

	const projectID = "proj1"
	if err := s.EnsureProject(projectID, "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}

	points, err := Trend(s, projectID, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != trendDefaultWindowDays+1 {
		t.Errorf("Trend(..., Filter{}) returned %d points, want %d (default %d-day window, inclusive of today)", len(points), trendDefaultWindowDays+1, trendDefaultWindowDays)
	}
}
