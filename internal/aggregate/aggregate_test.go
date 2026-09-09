package aggregate

import (
	"path/filepath"
	"testing"
	"time"

	"maatplug/internal/model"
	"maatplug/internal/store"
)

func TestProject_AbandonedTurnsAreCountedSeparatelyFromMeasuredTotals(t *testing.T) {
	dir := t.TempDir()
	s := &store.Store{Root: dir}

	const projectID = "proj1"
	if err := s.EnsureProject(projectID, "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}
	path := s.SessionPath(projectID, "claude", "session1")

	appendEvent := func(ev any) {
		t.Helper()
		if err := s.AppendEvent(path, ev); err != nil {
			t.Fatal(err)
		}
	}

	// One normally-measured instruction...
	appendEvent(model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i1"})
	appendEvent(model.TurnEndEvent{
		Type: model.EventTurnEnd, InstructionID: "i1",
		Diff:   model.DiffStat{Added: 10, Removed: 2, FilesChanged: 1},
		Tokens: &model.TokenUsage{Total: 500},
	})
	// ...and two abandoned ones (e.g. from a concurrent-resume ambiguity).
	appendEvent(model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i2"})
	appendEvent(model.TurnEndEvent{Type: model.EventTurnEnd, InstructionID: "i2", Abandoned: true, TokensUnavailable: true})
	appendEvent(model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i3"})
	appendEvent(model.TurnEndEvent{Type: model.EventTurnEnd, InstructionID: "i3", Abandoned: true, TokensUnavailable: true})

	summary, err := Project(s, projectID, Filter{})
	if err != nil {
		t.Fatal(err)
	}

	if summary.Instructions != 3 {
		t.Errorf("Instructions = %d, want 3 (abandoned ones still count as instructions the user issued)", summary.Instructions)
	}
	if summary.AbandonedInstructions != 2 {
		t.Errorf("AbandonedInstructions = %d, want 2", summary.AbandonedInstructions)
	}
	if summary.TotalTokens != 500 {
		t.Errorf("TotalTokens = %d, want 500 (only i1's real measurement, not zero-filled abandoned turns)", summary.TotalTokens)
	}
	if summary.LinesAdded != 10 || summary.LinesRemoved != 2 {
		t.Errorf("LinesAdded/Removed = %d/%d, want 10/2", summary.LinesAdded, summary.LinesRemoved)
	}
}

func TestProject_MergedInstructionsAreNotDoubleCounted(t *testing.T) {
	dir := t.TempDir()
	s := &store.Store{Root: dir}

	const projectID = "proj1"
	if err := s.EnsureProject(projectID, "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}
	path := s.SessionPath(projectID, "claude", "session1")

	appendEvent := func(ev any) {
		t.Helper()
		if err := s.AppendEvent(path, ev); err != nil {
			t.Fatal(err)
		}
	}

	// i1 and i2 were queued together and resolved by a single Stop: i1
	// (the oldest) carries the real measurement, i2 points at it via
	// MergedInto instead of repeating the same numbers.
	appendEvent(model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i1"})
	appendEvent(model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i2"})
	appendEvent(model.TurnEndEvent{
		Type: model.EventTurnEnd, InstructionID: "i1",
		Diff:   model.DiffStat{Added: 10, Removed: 2, FilesChanged: 1},
		Tokens: &model.TokenUsage{Total: 500},
	})
	appendEvent(model.TurnEndEvent{
		Type: model.EventTurnEnd, InstructionID: "i2",
		TokensUnavailable: true, MergedInto: "i1",
	})

	summary, err := Project(s, projectID, Filter{})
	if err != nil {
		t.Fatal(err)
	}

	if summary.Instructions != 2 {
		t.Errorf("Instructions = %d, want 2 (both the merged instruction and its primary count as issued)", summary.Instructions)
	}
	if summary.TotalTokens != 500 {
		t.Errorf("TotalTokens = %d, want 500 (i1's measurement counted once, not doubled by i2's merged turn_end)", summary.TotalTokens)
	}
	if summary.LinesAdded != 10 || summary.LinesRemoved != 2 {
		t.Errorf("LinesAdded/Removed = %d/%d, want 10/2 (not doubled)", summary.LinesAdded, summary.LinesRemoved)
	}
}

func TestProject_PricesTokensByModelAndFlagsUnrecognizedOnes(t *testing.T) {
	dir := t.TempDir()
	s := &store.Store{Root: dir}

	const projectID = "proj1"
	if err := s.EnsureProject(projectID, "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}
	path := s.SessionPath(projectID, "claude", "session1")

	appendEvent := func(ev any) {
		t.Helper()
		if err := s.AppendEvent(path, ev); err != nil {
			t.Fatal(err)
		}
	}

	// i1 uses a model on record in internal/pricing/pricing.json...
	appendEvent(model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i1"})
	appendEvent(model.TurnEndEvent{
		Type: model.EventTurnEnd, InstructionID: "i1", Model: "claude-sonnet-5",
		Tokens: &model.TokenUsage{Input: 1_000_000, Total: 1_000_000},
	})
	// ...i2 uses one that isn't.
	appendEvent(model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i2"})
	appendEvent(model.TurnEndEvent{
		Type: model.EventTurnEnd, InstructionID: "i2", Model: "claude-nonexistent-model",
		Tokens: &model.TokenUsage{Input: 1_000_000, Total: 1_000_000},
	})

	summary, err := Project(s, projectID, Filter{})
	if err != nil {
		t.Fatal(err)
	}

	rate, ok := loadPricing().Lookup("claude", "claude-sonnet-5")
	if !ok {
		t.Fatal("claude-sonnet-5 is not on record in the built-in pricing.json; update this test's model id or the fixture")
	}
	wantCost := rate.Cost(1_000_000, 0, 0, 0)
	if summary.CostUSD != wantCost {
		t.Errorf("CostUSD = %v, want %v (only i1, priced at claude-sonnet-5's on-record rate)", summary.CostUSD, wantCost)
	}
	if summary.CostUnpricedInstructions != 1 {
		t.Errorf("CostUnpricedInstructions = %d, want 1 (i2's model has no pricing entry)", summary.CostUnpricedInstructions)
	}
}

// TestProject_ProviderFilterRestrictsToOneProvidersSessions covers the web
// dashboard's provider (Claude/Codex) filter: passing a non-empty
// providerFilter should count only that provider's session files, as if the
// other provider's sessions didn't exist.
func TestProject_ProviderFilterRestrictsToOneProvidersSessions(t *testing.T) {
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

	claudePath := s.SessionPath(projectID, "claude", "session1")
	appendEvent(claudePath, model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i1"})
	appendEvent(claudePath, model.TurnEndEvent{
		Type: model.EventTurnEnd, InstructionID: "i1",
		Tokens: &model.TokenUsage{Total: 500},
	})

	codexPath := s.SessionPath(projectID, "codex", "session2")
	appendEvent(codexPath, model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i2"})
	appendEvent(codexPath, model.TurnEndEvent{
		Type: model.EventTurnEnd, InstructionID: "i2",
		Tokens: &model.TokenUsage{Total: 300},
	})

	claudeOnly, err := Project(s, projectID, Filter{Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if claudeOnly.Sessions != 1 || claudeOnly.Instructions != 1 || claudeOnly.TotalTokens != 500 {
		t.Errorf("Project(..., \"claude\") = %+v, want 1 session/1 instruction/500 tokens (codex session excluded)", claudeOnly)
	}

	both, err := Project(s, projectID, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if both.Sessions != 2 || both.Instructions != 2 || both.TotalTokens != 800 {
		t.Errorf("Project(..., \"\") = %+v, want both sessions counted", both)
	}
}

// TestProject_SinceFilterExcludesSessionsThatStartedEarlier covers the web
// dashboard's date-range filter: a session is included or excluded as a
// whole based on its session_start timestamp, not per-instruction.
func TestProject_SinceFilterExcludesSessionsThatStartedEarlier(t *testing.T) {
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

	now := time.Now().UTC()
	oldPath := s.SessionPath(projectID, "claude", "old-session")
	appendEvent(oldPath, model.SessionStartEvent{Type: model.EventSessionStart, Timestamp: now.AddDate(0, 0, -60)})
	appendEvent(oldPath, model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i1"})
	appendEvent(oldPath, model.TurnEndEvent{Type: model.EventTurnEnd, InstructionID: "i1", Tokens: &model.TokenUsage{Total: 500}})

	recentPath := s.SessionPath(projectID, "claude", "recent-session")
	appendEvent(recentPath, model.SessionStartEvent{Type: model.EventSessionStart, Timestamp: now.AddDate(0, 0, -1)})
	appendEvent(recentPath, model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i2"})
	appendEvent(recentPath, model.TurnEndEvent{Type: model.EventTurnEnd, InstructionID: "i2", Tokens: &model.TokenUsage{Total: 300}})

	last30, err := Project(s, projectID, Filter{Since: now.AddDate(0, 0, -30)})
	if err != nil {
		t.Fatal(err)
	}
	if last30.Sessions != 1 || last30.Instructions != 1 || last30.TotalTokens != 300 {
		t.Errorf("Project(..., Since: -30d) = %+v, want only recent-session counted", last30)
	}

	allTime, err := Project(s, projectID, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if allTime.Sessions != 2 || allTime.TotalTokens != 800 {
		t.Errorf("Project(..., Filter{}) = %+v, want both sessions counted", allTime)
	}
}

// TestAll_ExcludesProjectsRootedInsideTheStore covers the usage-check probe
// directory (internal/usage.Poller, run via store.UsageCheckDir) showing up
// as an ordinary project: any project recorded under the store's own data
// directory is excluded, including under a stale probe dirname left over
// from an earlier version (e.g. "claude-usage-probe-dir",
// "usage-probe-scratch") — not just today's exact store.UsageCheckDir path.
func TestAll_ExcludesProjectsRootedInsideTheStore(t *testing.T) {
	dir := t.TempDir()
	s := &store.Store{Root: dir}

	if err := s.EnsureProject("realproj", "myproject", filepath.Join(dir, "..", "myproject")); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureProject("probecurrent", "usage-check", store.UsageCheckDir(dir)); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureProject("probelegacy", "claude-usage-probe-dir", filepath.Join(dir, "claude-usage-probe-dir")); err != nil {
		t.Fatal(err)
	}

	summaries, err := All(s, Filter{})
	if err != nil {
		t.Fatal(err)
	}

	if len(summaries) != 1 || summaries[0].ProjectID != "realproj" {
		t.Errorf("All() = %+v, want only realproj (probe directories under the store root must be excluded)", summaries)
	}
}
