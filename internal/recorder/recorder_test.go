package recorder

import (
	"sync"
	"testing"

	"maatplug/internal/model"
	"maatplug/internal/store"
)

// TestTurnEndForLastInstruction_QueuedFollowUpMergesInsteadOfLosingData
// reproduces a normal Claude Code interaction: the user sends a second
// message while Claude is still working on the first, so a second
// UserPromptSubmit fires before the first Stop, then Claude addresses both
// in one continuous turn and Stop fires once. Both instructions must come
// back with a turn_end: the first (oldest) carries the real diff/tokens/
// model, and the second points at it via MergedInto rather than either being
// abandoned (the old behavior, which lost this data entirely) or getting a
// duplicate copy of the same numbers (which would double-count in any sum).
func TestTurnEndForLastInstruction_QueuedFollowUpMergesInsteadOfLosingData(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir() // non-git: diff computation stays a pure cloc-fallback zero-diff, not under test here
	s := &store.Store{Root: dir}
	r := New(s)

	const provider, sessionID = "claude", "queued-followup-session"

	id1, err := r.Instruction(provider, sessionID, cwd, "first message")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := r.Instruction(provider, sessionID, cwd, "queued follow-up, sent before Claude stopped on the first")
	if err != nil {
		t.Fatal(err)
	}

	// A single Stop, covering both, with real token usage and a model name.
	tokens := &model.TokenUsage{Input: 100, Output: 50, Total: 150}
	if err := r.TurnEndForLastInstruction(provider, sessionID, cwd, "claude-sonnet-5", tokens); err != nil {
		t.Fatal(err)
	}

	projectID := store.ProjectID(cwd)
	path := s.SessionPath(projectID, provider, sessionID)
	events, err := store.ReadEvents(path)
	if err != nil {
		t.Fatal(err)
	}

	turnEnds := map[string]model.RawEvent{}
	for _, ev := range events {
		if ev.Type == model.EventTurnEnd {
			turnEnds[ev.InstructionID] = ev
		}
	}
	if len(turnEnds) != 2 {
		t.Fatalf("got %d turn_end events, want 2 (one per instruction, neither lost)", len(turnEnds))
	}

	primary, ok := turnEnds[id1]
	if !ok {
		t.Fatalf("no turn_end for the first (oldest) instruction %q", id1)
	}
	if primary.Abandoned || primary.MergedInto != "" {
		t.Errorf("primary turn_end = %+v, want Abandoned=false and MergedInto empty (it carries the real data)", primary)
	}
	if primary.Model != "claude-sonnet-5" || primary.Tokens.Total != 150 {
		t.Errorf("primary turn_end model/tokens = %q/%+v, want claude-sonnet-5/150", primary.Model, primary.Tokens)
	}

	secondary, ok := turnEnds[id2]
	if !ok {
		t.Fatalf("no turn_end for the queued instruction %q — its data was lost, not merged", id2)
	}
	if secondary.Abandoned {
		t.Error("secondary turn_end is Abandoned, want it merged (not lost) via MergedInto")
	}
	if secondary.MergedInto != id1 {
		t.Errorf("secondary turn_end MergedInto = %q, want %q", secondary.MergedInto, id1)
	}
	if secondary.TokensUnavailable != true || secondary.Diff != (model.DiffStat{}) {
		t.Errorf("secondary turn_end = %+v, want TokensUnavailable=true and zero Diff (its real numbers live on the primary, to avoid double-counting)", secondary)
	}
}

// TestTurnEndForLastInstruction_ConcurrentAmbiguousDoesNotDuplicate
// reproduces the race a code review caught: two Stop hook processes for a
// concurrently-resumed session (see store.FindUnresolvedInstructions) both
// find the same unresolved instructions and, without the session lock in
// TurnEndForLastInstruction, could each independently decide to resolve
// them — writing up to N turn_end events per instructionId instead of
// exactly one. cwd is a non-git temp dir so this stays a pure store/
// locking test, not a git-tree-snapshot test.
func TestTurnEndForLastInstruction_ConcurrentAmbiguousDoesNotDuplicate(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	s := &store.Store{Root: dir}
	r := New(s)

	const provider, sessionID = "claude", "concurrent-stop-session"

	if _, err := r.Instruction(provider, sessionID, cwd, "from terminal A"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Instruction(provider, sessionID, cwd, "from terminal B"); err != nil {
		t.Fatal(err)
	}

	const attempts = 10
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Errors are expected here (ambiguous, or "nothing left to
			// close out" once another goroutine already resolved it) — the
			// invariant under test is the resulting event count, not these
			// calls' return values.
			_ = r.TurnEndForLastInstruction(provider, sessionID, cwd, "", nil)
		}()
	}
	wg.Wait()

	projectID := store.ProjectID(cwd)
	path := s.SessionPath(projectID, provider, sessionID)
	events, err := store.ReadEvents(path)
	if err != nil {
		t.Fatal(err)
	}

	turnEndCount := map[string]int{}
	for _, ev := range events {
		if ev.Type == model.EventTurnEnd {
			turnEndCount[ev.InstructionID]++
		}
	}

	if len(turnEndCount) != 2 {
		t.Fatalf("turn_end written for %d distinct instructions, want 2: %v", len(turnEndCount), turnEndCount)
	}
	for id, count := range turnEndCount {
		if count != 1 {
			t.Errorf("instruction %q has %d turn_end events, want exactly 1", id, count)
		}
	}
}
