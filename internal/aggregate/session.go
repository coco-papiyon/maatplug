package aggregate

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"maatplug/internal/model"
	"maatplug/internal/store"
)

// SessionSummary totals one recorded provider session, for the session list
// under a project.
type SessionSummary struct {
	Provider  string    `json:"provider"`
	SessionID string    `json:"sessionId"`
	StartedAt time.Time `json:"startedAt"`
	// FirstInstruction is the text of this session's earliest instruction
	// event, for the session list to show something more meaningful than a
	// raw session ID. Empty if the session has no recorded instruction yet.
	FirstInstruction      string `json:"firstInstruction"`
	Instructions          int    `json:"instructions"`
	AbandonedInstructions int    `json:"abandonedInstructions"`
	TotalTokens           int    `json:"totalTokens"`
	// TokensInput/Output/CacheRead/CacheCreation break TotalTokens down by
	// bucket (see model.TokenUsage), like ProjectSummary's equivalents.
	TokensInput         int `json:"tokensInput"`
	TokensOutput        int `json:"tokensOutput"`
	TokensCacheRead     int `json:"tokensCacheRead"`
	TokensCacheCreation int `json:"tokensCacheCreation"`
	LinesAdded          int `json:"linesAdded"`
	LinesRemoved        int `json:"linesRemoved"`
	// CostUSD/CostUnpricedInstructions mirror ProjectSummary's fields of the
	// same name (see aggregate.go): a per-model USD total priced from
	// internal/pricing, plus a count of turns whose model had no matching
	// pricing entry.
	CostUSD                  float64 `json:"costUsd"`
	CostUnpricedInstructions int     `json:"costUnpricedInstructions"`
}

// Sessions lists every session recorded for a project, most recently
// started first, restricted to activity matching filter (see Filter's doc
// comment; the zero Filter matches everything).
func Sessions(s *store.Store, projectID string, filter Filter) ([]SessionSummary, error) {
	files, err := s.ListSessionFiles(projectID)
	if err != nil {
		return nil, err
	}

	pricingDoc := loadPricing()
	summaries := make([]SessionSummary, 0, len(files))
	for _, f := range files {
		provider, sessionID, ok := store.ParseSessionFileName(filepath.Base(f))
		if !ok {
			continue // not a session file this store wrote; ignore rather than fail the whole listing
		}
		if !filter.matchesProvider(provider) {
			continue
		}

		events, err := store.ReadEvents(f)
		if err != nil {
			return nil, err
		}

		summary := SessionSummary{Provider: provider, SessionID: sessionID}
		for _, ev := range events {
			switch ev.Type {
			case model.EventSessionStart:
				summary.StartedAt = ev.Timestamp
			case model.EventInstruction:
				if summary.Instructions == 0 {
					summary.FirstInstruction = ev.Text
				}
				summary.Instructions++
			case model.EventTurnEnd:
				if ev.Abandoned {
					summary.AbandonedInstructions++
					continue
				}
				if ev.MergedInto != "" {
					// Its diff/tokens live on the instruction named by
					// MergedInto instead (see model.TurnEndEvent.MergedInto);
					// counting this one too would double-count them.
					continue
				}
				summary.TotalTokens += ev.Tokens.Total
				summary.TokensInput += ev.Tokens.Input
				summary.TokensOutput += ev.Tokens.Output
				summary.TokensCacheRead += ev.Tokens.CacheRead
				summary.TokensCacheCreation += ev.Tokens.CacheCreation
				summary.LinesAdded += ev.Diff.Added
				summary.LinesRemoved += ev.Diff.Removed
				if !ev.TokensUnavailable {
					if cost, ok := costFor(pricingDoc, provider, ev.Model, ev.Tokens); ok {
						summary.CostUSD += cost
					} else {
						summary.CostUnpricedInstructions++
					}
				}
			}
		}
		if !filter.matchesSince(summary.StartedAt) {
			continue
		}
		summaries = append(summaries, summary)
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].StartedAt.After(summaries[j].StartedAt)
	})
	return summaries, nil
}

// Turn is one instruction and its outcome, for a session's timeline.
type Turn struct {
	InstructionID     string            `json:"instructionId"`
	Text              string            `json:"text"`
	Timestamp         time.Time         `json:"ts"`
	Diff              model.DiffStat    `json:"diff"`
	Tokens            *model.TokenUsage `json:"tokens"`
	TokensUnavailable bool              `json:"tokensUnavailable"`
	// Model is the underlying model name the provider used for this turn
	// (see model.TurnEndEvent.Model). Empty when the adapter could not
	// determine it.
	Model string `json:"model"`
	// MergedInto, when set, names the instructionId that carries this turn's
	// real Diff/Tokens/Model — a follow-up message queued while that other
	// instruction was still being worked (see model.TurnEndEvent.MergedInto).
	MergedInto string `json:"mergedInto"`
	Abandoned  bool   `json:"abandoned"`
	// Resolved is false if this instruction has no turn_end at all yet —
	// e.g. the session is still in progress, or the process recording its
	// Stop crashed before writing one.
	Resolved bool `json:"resolved"`
	// CostUSD is Tokens priced against internal/pricing's reference rates
	// for Model. Zero (and meaningless) whenever Tokens is nil.
	CostUSD float64 `json:"costUsd"`
	// CostUnavailable is true when this turn has real Tokens but Model had
	// no matching entry in the pricing reference data (e.g. an
	// unrecognized/new model id) — consumers should treat CostUSD as
	// unknown, not zero, when this is set. Mirrors TokensUnavailable's
	// contract.
	CostUnavailable bool `json:"costUnavailable"`
}

// SessionDetail is one session's full instruction-by-instruction timeline,
// bracketed by its cloc snapshots.
type SessionDetail struct {
	Provider  string            `json:"provider"`
	SessionID string            `json:"sessionId"`
	Cwd       string            `json:"cwd"`
	StartedAt time.Time         `json:"startedAt"`
	ClocStart *model.ClocResult `json:"clocStart"`
	ClocEnd   *model.ClocResult `json:"clocEnd"`
	Turns     []Turn            `json:"turns"`
}

// ErrSessionNotFound is returned by SessionDetailFor when the requested
// session file doesn't exist (as opposed to existing but being unreadable).
var ErrSessionNotFound = errors.New("session not found")

// SessionDetailFor builds the full timeline for one session.
func SessionDetailFor(s *store.Store, projectID, provider, sessionID string) (SessionDetail, error) {
	path := s.SessionPath(projectID, provider, sessionID)
	events, err := store.ReadEvents(path)
	if err != nil {
		return SessionDetail{}, fmt.Errorf("read session: %w", err)
	}
	if events == nil {
		return SessionDetail{}, ErrSessionNotFound
	}

	pricingDoc := loadPricing()
	detail := SessionDetail{Provider: provider, SessionID: sessionID}
	turnsByID := map[string]*Turn{}
	var order []string

	turnFor := func(instructionID string) *Turn {
		if t, ok := turnsByID[instructionID]; ok {
			return t
		}
		t := &Turn{InstructionID: instructionID}
		turnsByID[instructionID] = t
		order = append(order, instructionID)
		return t
	}

	for _, ev := range events {
		switch ev.Type {
		case model.EventSessionStart:
			detail.StartedAt = ev.Timestamp
			detail.Cwd = ev.Cwd
			detail.ClocStart = ev.Cloc
		case model.EventInstruction:
			t := turnFor(ev.InstructionID)
			t.Text = ev.Text
			t.Timestamp = ev.Timestamp
		case model.EventTurnEnd:
			t := turnFor(ev.InstructionID)
			t.Diff = ev.Diff
			t.TokensUnavailable = ev.TokensUnavailable
			t.Model = ev.Model
			t.MergedInto = ev.MergedInto
			t.Abandoned = ev.Abandoned
			t.Resolved = true
			if !ev.TokensUnavailable {
				tokens := ev.Tokens
				t.Tokens = &tokens
				if !ev.Abandoned && ev.MergedInto == "" {
					if cost, ok := costFor(pricingDoc, provider, ev.Model, ev.Tokens); ok {
						t.CostUSD = cost
					} else {
						t.CostUnavailable = true
					}
				}
			}
		case model.EventSessionEnd:
			detail.ClocEnd = ev.Cloc
		}
	}

	detail.Turns = make([]Turn, 0, len(order))
	for _, id := range order {
		detail.Turns = append(detail.Turns, *turnsByID[id])
	}
	return detail, nil
}
