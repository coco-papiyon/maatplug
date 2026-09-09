package aggregate

import (
	"testing"
	"time"

	"maatplug/internal/model"
	"maatplug/internal/store"
)

func TestBreakdown_ByProviderSplitsIntoOneSeriesPerProvider(t *testing.T) {
	dir := t.TempDir()
	s := &store.Store{Root: dir}

	const projectID = "proj1"
	if err := s.EnsureProject(projectID, "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}

	today := truncateToDate(time.Now())
	appendEvent := func(path string, ev any) {
		t.Helper()
		if err := s.AppendEvent(path, ev); err != nil {
			t.Fatal(err)
		}
	}

	claudePath := s.SessionPath(projectID, "claude", "session1")
	appendEvent(claudePath, model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i1"})
	appendEvent(claudePath, model.TurnEndEvent{Type: model.EventTurnEnd, InstructionID: "i1", Timestamp: today, Tokens: &model.TokenUsage{Total: 500}})

	codexPath := s.SessionPath(projectID, "codex", "session2")
	appendEvent(codexPath, model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i2"})
	appendEvent(codexPath, model.TurnEndEvent{Type: model.EventTurnEnd, InstructionID: "i2", Timestamp: today, Tokens: &model.TokenUsage{Total: 300}})

	got, err := Breakdown(s, projectID, Filter{Since: today.AddDate(0, 0, -1)}, BreakdownProvider)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Series) != 2 {
		t.Fatalf("len(Series) = %d, want 2 (claude, codex)", len(got.Series))
	}
	if got.Series[0].Key != "claude" || got.Series[1].Key != "codex" {
		t.Errorf("Series keys = [%s, %s], want [claude, codex] (fixed order)", got.Series[0].Key, got.Series[1].Key)
	}
	lastClaude := got.Series[0].Points[len(got.Series[0].Points)-1]
	lastCodex := got.Series[1].Points[len(got.Series[1].Points)-1]
	if lastClaude.TotalTokens != 500 || lastCodex.TotalTokens != 300 {
		t.Errorf("today's totals = claude:%d codex:%d, want 500/300", lastClaude.TotalTokens, lastCodex.TotalTokens)
	}
}

func TestBreakdown_ByProviderOmitsAProviderWithNoMatchingActivity(t *testing.T) {
	dir := t.TempDir()
	s := &store.Store{Root: dir}
	const projectID = "proj1"
	if err := s.EnsureProject(projectID, "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}
	path := s.SessionPath(projectID, "claude", "session1")
	if err := s.AppendEvent(path, model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(path, model.TurnEndEvent{Type: model.EventTurnEnd, InstructionID: "i1", Timestamp: time.Now(), Tokens: &model.TokenUsage{Total: 500}}); err != nil {
		t.Fatal(err)
	}

	got, err := Breakdown(s, projectID, Filter{}, BreakdownProvider)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Series) != 1 || got.Series[0].Key != "claude" {
		t.Errorf("Series = %+v, want exactly one series (claude) — codex never appears, so it must not be an all-zero series", got.Series)
	}
}

func TestBreakdown_ByTokenTypeSplitsInCacheOutFromOneEvent(t *testing.T) {
	dir := t.TempDir()
	s := &store.Store{Root: dir}
	const projectID = "proj1"
	if err := s.EnsureProject(projectID, "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}
	path := s.SessionPath(projectID, "claude", "session1")
	if err := s.AppendEvent(path, model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(path, model.TurnEndEvent{
		Type: model.EventTurnEnd, InstructionID: "i1", Timestamp: time.Now(),
		Tokens: &model.TokenUsage{Input: 10, CacheRead: 20, CacheCreation: 5, Output: 7, Total: 42},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := Breakdown(s, projectID, Filter{}, BreakdownTokenType)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Series) != 3 {
		t.Fatalf("len(Series) = %d, want 3 (in, cache, out — always all three)", len(got.Series))
	}
	want := map[string]int{"in": 10, "cache": 25, "out": 7}
	for _, series := range got.Series {
		last := series.Points[len(series.Points)-1]
		if last.TotalTokens != want[series.Key] {
			t.Errorf("series %q today's TotalTokens = %d, want %d", series.Key, last.TotalTokens, want[series.Key])
		}
	}
}

func TestBreakdown_ByModelFoldsOverflowIntoOther(t *testing.T) {
	dir := t.TempDir()
	s := &store.Store{Root: dir}
	const projectID = "proj1"
	if err := s.EnsureProject(projectID, "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}
	path := s.SessionPath(projectID, "claude", "session1")

	// 9 distinct models, each with a different, decreasing token total, so
	// ranking is unambiguous: model-0 has the most tokens, model-8 the
	// fewest. breakdownMaxCategories is 7, so the bottom 2 (model-7,
	// model-8) must fold into "other".
	for i := 0; i < 9; i++ {
		instrID := "i" + string(rune('a'+i))
		if err := s.AppendEvent(path, model.InstructionEvent{Type: model.EventInstruction, InstructionID: instrID}); err != nil {
			t.Fatal(err)
		}
		tokens := 100 - i*10
		modelName := "model-" + string(rune('0'+i))
		if err := s.AppendEvent(path, model.TurnEndEvent{
			Type: model.EventTurnEnd, InstructionID: instrID, Timestamp: time.Now(), Model: modelName,
			Tokens: &model.TokenUsage{Total: tokens},
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := Breakdown(s, projectID, Filter{}, BreakdownModel)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Series) != breakdownMaxCategories+1 {
		t.Fatalf("len(Series) = %d, want %d (%d top models + other)", len(got.Series), breakdownMaxCategories+1, breakdownMaxCategories)
	}
	last := got.Series[len(got.Series)-1]
	if last.Key != "other" {
		t.Fatalf("last series key = %q, want %q", last.Key, "other")
	}
	otherTotal := last.Points[len(last.Points)-1].TotalTokens
	// model-7 (30 tokens) + model-8 (20 tokens) folded together.
	if otherTotal != 50 {
		t.Errorf("other's today TotalTokens = %d, want 50 (30+20 from the two folded models)", otherTotal)
	}
}
