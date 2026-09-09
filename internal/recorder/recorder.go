// Package recorder is the provider-agnostic write path: given an
// instruction's text and (from the provider adapter) its token usage, it
// snapshots git/cloc state, and appends events through internal/store.
// Every provider adapter (today: Claude Code's hook handler; later:
// Codex/Copilot's exec-wrap handler) calls into this package instead of
// touching internal/store, internal/gitdiff, or internal/cloc directly, so
// that adding a provider means writing a new thin adapter, not new
// bookkeeping logic. See docs/design.md section 6.
package recorder

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"maatplug/internal/cloc"
	"maatplug/internal/gitdiff"
	"maatplug/internal/gitutil"
	"maatplug/internal/model"
	"maatplug/internal/store"
)

// Recorder binds a Store to the git/cloc side effects every provider
// adapter needs around session-start, instruction, and turn-end events.
type Recorder struct {
	Store *store.Store
}

// New wraps a Store.
func New(s *store.Store) *Recorder {
	return &Recorder{Store: s}
}

// SessionStart records that a provider session began in cwd: it registers
// the project (first time only), runs cloc (skipped silently if cloc is not
// installed), and appends a session_start event.
func (r *Recorder) SessionStart(provider, sessionID, cwd string) error {
	projectID := store.ProjectID(cwd)
	if err := r.Store.EnsureProject(projectID, projectName(cwd), cwd); err != nil {
		return fmt.Errorf("ensure project: %w", err)
	}

	result, err := cloc.Run(cwd)
	if err != nil {
		return fmt.Errorf("run cloc: %w", err)
	}

	event := model.SessionStartEvent{
		Type:        model.EventSessionStart,
		Timestamp:   time.Now(),
		Provider:    provider,
		SessionID:   sessionID,
		ProjectID:   projectID,
		Cwd:         cwd,
		GitHead:     gitutil.HeadCommit(cwd),
		Cloc:        result,
		ClocSkipped: result == nil,
	}

	path := r.Store.SessionPath(projectID, provider, sessionID)
	return r.Store.AppendEvent(path, event)
}

// Instruction records that the user submitted text and snapshots the
// current source state as the baseline the matching TurnEnd call will diff
// against. It generates and returns the instructionId itself — callers
// don't need (and, for Claude Code hooks, can't reliably get) a
// caller-supplied correlation id shared between the instruction and its
// eventual turn-end; see docs/design.md section 6.1 and internal/store's
// FindUnresolvedInstructions, which is how a separate, later process
// (e.g. the Stop hook) recovers this id without it being handed back.
func (r *Recorder) Instruction(provider, sessionID, cwd, text string) (instructionID string, err error) {
	return r.InstructionWithID(provider, sessionID, cwd, newInstructionID(), text)
}

// InstructionWithID records an instruction using a provider-supplied
// correlation id. Providers such as Codex expose the same turn_id to both
// UserPromptSubmit and Stop hooks, which is more reliable than recovering the
// latest unresolved instruction from the session log.
func (r *Recorder) InstructionWithID(provider, sessionID, cwd, instructionID, text string) (string, error) {
	if instructionID == "" {
		return "", fmt.Errorf("instruction id is required")
	}
	baseline, err := r.snapshotBaseline(cwd)
	if err != nil {
		return "", fmt.Errorf("snapshot baseline: %w", err)
	}

	event := model.InstructionEvent{
		Type:          model.EventInstruction,
		Timestamp:     time.Now(),
		InstructionID: instructionID,
		Text:          text,
		Baseline:      baseline,
	}

	projectID := store.ProjectID(cwd)
	path := r.Store.SessionPath(projectID, provider, sessionID)
	if err := r.Store.AppendEvent(path, event); err != nil {
		return "", err
	}
	return instructionID, nil
}

// TurnEndForLastInstruction records that the provider finished responding:
// it resolves which instruction(s) just finished via
// store.FindUnresolvedInstructions (see Instruction's doc comment for why),
// re-snapshots source state, diffs it against the oldest of them's baseline,
// and appends a turn_end event carrying that delta plus whatever token usage
// the adapter could determine (nil is fine — the schema allows tokens to be
// absent per docs/design.md section 5.2).
//
// Normally there is exactly one unresolved instruction. But
// FindUnresolvedInstructions can return more than one — most commonly
// because the user queued a follow-up message while this turn was still in
// progress (an ordinary Claude Code feature), which Claude Code then
// resolves together in one continuous turn with a single Stop; the same
// shape on disk is also what concurrent multi-terminal session resume looks
// like, and the two cannot be told apart from the hook payload alone (see
// that function's doc comment). Either way there is only one real
// diff/tokens/model to report for the whole span, computed once here: the
// oldest instruction's turn_end carries it, and every other unresolved
// instruction gets its own turn_end pointing at that one via MergedInto
// (Diff zero, Tokens absent) rather than either being silently dropped
// (losing real data, the old ambiguous/abandon behavior) or getting the same
// numbers copied onto every id (which would double-count them in any sum
// over the session).
//
// The whole read-decide-append sequence runs under store.WithSessionLock:
// two Stop processes racing on the same session file could otherwise both
// read the same unresolved instruction(s) before either writes back, and
// both append turn_ends for them — breaking the "at most one turn_end per
// instructionId" invariant.
func (r *Recorder) TurnEndForLastInstruction(provider, sessionID, cwd, modelUsed string, tokens *model.TokenUsage) error {
	projectID := store.ProjectID(cwd)
	path := r.Store.SessionPath(projectID, provider, sessionID)

	return store.WithSessionLock(path, func() error {
		instructionIDs, baseline, found, err := store.FindUnresolvedInstructions(path)
		if err != nil {
			return fmt.Errorf("find unresolved instructions: %w", err)
		}
		if !found {
			return fmt.Errorf("no unresolved instruction to close out in %s", path)
		}

		diff, err := r.diffAgainstBaseline(cwd, baseline)
		if err != nil {
			return fmt.Errorf("compute diff: %w", err)
		}

		primary := instructionIDs[0]
		if err := r.appendTurnEnd(path, primary, diff, modelUsed, tokens, ""); err != nil {
			return err
		}
		for _, id := range instructionIDs[1:] {
			if err := r.appendTurnEnd(path, id, model.DiffStat{}, "", nil, primary); err != nil {
				return err
			}
		}
		return nil
	})
}

// TurnEnd records a turn-end for an explicitly known instructionID. It
// exists for providers (e.g. an exec-wrap adapter, or Codex's turn_id) that
// already have the id on hand and don't need
// TurnEndForLastInstruction's store lookup. It still takes the same session
// lock as TurnEndForLastInstruction, for the same reason (see that method's
// doc comment).
func (r *Recorder) TurnEnd(provider, sessionID, cwd, instructionID, modelUsed string, tokens *model.TokenUsage) error {
	projectID := store.ProjectID(cwd)
	path := r.Store.SessionPath(projectID, provider, sessionID)

	return store.WithSessionLock(path, func() error {
		baseline, found, err := store.FindInstructionBaseline(path, instructionID)
		if err != nil {
			return fmt.Errorf("find instruction baseline: %w", err)
		}
		if !found {
			return fmt.Errorf("no unresolved instruction event found for instructionId %q", instructionID)
		}

		diff, err := r.diffAgainstBaseline(cwd, baseline)
		if err != nil {
			return fmt.Errorf("compute diff: %w", err)
		}
		return r.appendTurnEnd(path, instructionID, diff, modelUsed, tokens, "")
	})
}

func (r *Recorder) appendTurnEnd(path, instructionID string, diff model.DiffStat, modelUsed string, tokens *model.TokenUsage, mergedInto string) error {
	event := model.TurnEndEvent{
		Type:              model.EventTurnEnd,
		Timestamp:         time.Now(),
		InstructionID:     instructionID,
		Diff:              diff,
		Tokens:            tokens,
		TokensUnavailable: tokens == nil,
		Model:             modelUsed,
		MergedInto:        mergedInto,
	}
	return r.Store.AppendEvent(path, event)
}

// SessionEnd records that a provider session terminated: it re-runs cloc for
// an end-of-session snapshot and totals up the session's instructions/tokens
// from the events already on disk.
func (r *Recorder) SessionEnd(provider, sessionID, cwd string) error {
	projectID := store.ProjectID(cwd)
	path := r.Store.SessionPath(projectID, provider, sessionID)

	events, err := store.ReadEvents(path)
	if err != nil {
		return fmt.Errorf("read events: %w", err)
	}

	var instructionCount, totalTokens int
	for _, ev := range events {
		switch ev.Type {
		case model.EventInstruction:
			instructionCount++
		case model.EventTurnEnd:
			totalTokens += ev.Tokens.Total
		}
	}

	result, err := cloc.Run(cwd)
	if err != nil {
		return fmt.Errorf("run cloc: %w", err)
	}

	event := model.SessionEndEvent{
		Type:             model.EventSessionEnd,
		Timestamp:        time.Now(),
		InstructionCount: instructionCount,
		TotalTokens:      totalTokens,
		Cloc:             result,
	}
	return r.Store.AppendEvent(path, event)
}

// snapshotBaseline picks the git tree-snapshot path when cwd is a git repo
// and falls back to a cloc snapshot otherwise (docs/design.md section 6.4).
func (r *Recorder) snapshotBaseline(cwd string) (model.DiffBaseline, error) {
	if gitutil.IsRepo(cwd) {
		tree, err := gitdiff.SnapshotGitTree(cwd)
		if err != nil {
			return model.DiffBaseline{}, err
		}
		return model.DiffBaseline{GitTree: tree}, nil
	}

	result, err := cloc.Run(cwd)
	if err != nil {
		return model.DiffBaseline{}, err
	}
	return model.DiffBaseline{Cloc: result}, nil
}

// diffAgainstBaseline re-snapshots cwd with whichever method produced
// baseline and diffs the two.
func (r *Recorder) diffAgainstBaseline(cwd string, baseline model.DiffBaseline) (model.DiffStat, error) {
	if baseline.GitTree != "" {
		after, err := gitdiff.SnapshotGitTree(cwd)
		if err != nil {
			return model.DiffStat{}, err
		}
		return gitdiff.DiffTrees(cwd, baseline.GitTree, after)
	}

	after, err := cloc.Run(cwd)
	if err != nil {
		return model.DiffStat{}, err
	}
	return gitdiff.DiffCloc(baseline.Cloc, after), nil
}

func projectName(cwd string) string {
	// A short, human-readable label; the full path is kept separately.
	base := cwd
	for i := len(cwd) - 1; i >= 0; i-- {
		if cwd[i] == '/' || cwd[i] == '\\' {
			base = cwd[i+1:]
			break
		}
	}
	if base == "" {
		return cwd
	}
	return base
}

// newInstructionID returns a unique, roughly time-sortable id. It doesn't
// need to be a UUID — it only has to be unique within one session's file and
// never collide with a caller-supplied id from TurnEnd — so this avoids
// pulling in an external uuid dependency.
func newInstructionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // crypto/rand.Read on the OS CSPRNG does not fail in practice
	return fmt.Sprintf("%d-%s", time.Now().UnixNano(), hex.EncodeToString(b))
}
