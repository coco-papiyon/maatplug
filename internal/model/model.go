// Package model defines maatplug's provider-independent event contract.
// EventEnvelope in event.go is the version 1 contract. The projection types
// in this file keep the current recorder and reports building until their
// Phase 2 and Phase 3 migrations; they are not part of event.schema.json and
// no compatibility reader or dual writer is provided for them.
package model

import "time"

// EventType identifies which shape a JSONL line follows.
type EventType string

const (
	EventSessionStart EventType = "session_start"
	EventInstruction  EventType = "instruction"
	EventTurnEnd      EventType = "turn_end"
	EventSessionEnd   EventType = "session_end"
)

// ClocResult mirrors the totals maatplug keeps from `cloc --json`.
type ClocResult struct {
	Files   int            `json:"files"`
	Blank   int            `json:"blank"`
	Comment int            `json:"comment"`
	Code    int            `json:"code"`
	ByLang  map[string]int `json:"byLang,omitempty"`
}

// DiffStat is a git-diff-derived (or cloc-fallback-derived) line count.
type DiffStat struct {
	FilesChanged int `json:"filesChanged"`
	Added        int `json:"added"`
	Removed      int `json:"removed"`
}

// DiffBaseline is the point-in-time reference an instruction's TurnEndEvent
// is measured against. Exactly one field is set, depending on whether cwd
// was a git repository at the time: GitTree holds a tree object hash (see
// internal/gitdiff), letting the eventual diff be computed exactly via `git
// diff <before> <after>` regardless of how many times a file is touched
// across turns; Cloc holds a full line-count snapshot for the non-git
// fallback, which can only report coarse net totals.
type DiffBaseline struct {
	GitTree string      `json:"gitTree,omitempty"`
	Cloc    *ClocResult `json:"cloc,omitempty"`
}

// TokenUsage aggregates provider API usage across the model responses that
// answered a single instruction. The input/cache buckets are exclusive.
type TokenUsage struct {
	Input         int `json:"input"`
	Output        int `json:"output"`
	CacheRead     int `json:"cacheRead"`
	CacheCreation int `json:"cacheCreation"`
	Total         int `json:"total"`
}

// SessionStartEvent is written once when a provider session begins.
type SessionStartEvent struct {
	Type        EventType   `json:"type"`
	Timestamp   time.Time   `json:"ts"`
	Provider    string      `json:"provider"`
	SessionID   string      `json:"sessionId"`
	ProjectID   string      `json:"projectId"`
	Cwd         string      `json:"cwd"`
	GitHead     string      `json:"gitHead,omitempty"`
	Cloc        *ClocResult `json:"cloc"` // null (not omitted) when ClocSkipped
	ClocSkipped bool        `json:"clocSkipped,omitempty"`
}

// InstructionEvent is written when the user submits an instruction, before
// the provider has produced a response. Baseline is the snapshot TurnEnd
// will diff against once the turn finishes.
type InstructionEvent struct {
	Type          EventType    `json:"type"`
	Timestamp     time.Time    `json:"ts"`
	InstructionID string       `json:"instructionId"`
	Text          string       `json:"text"`
	Baseline      DiffBaseline `json:"baseline"`
}

// TurnEndEvent is written once the provider finishes responding to an
// instruction.
type TurnEndEvent struct {
	Type              EventType   `json:"type"`
	Timestamp         time.Time   `json:"ts"`
	InstructionID     string      `json:"instructionId"`
	Diff              DiffStat    `json:"diff"`
	Tokens            *TokenUsage `json:"tokens"` // null (not omitted) when TokensUnavailable
	TokensUnavailable bool        `json:"tokensUnavailable,omitempty"`
	// Model is the underlying model name the provider used to answer this
	// turn (e.g. "claude-sonnet-5", "gpt-5.6-sol"), best-effort extracted
	// from the same provider-specific transcript each adapter already reads
	// for token usage (internal/provider/claude, internal/provider/codex).
	// Empty when the adapter could not determine it — this is not
	// distinguished from "unavailable" with a separate bool the way tokens
	// is, since an empty string is already an unambiguous "unknown" for a
	// model name (unlike a token count, where 0 is a valid real value).
	Model string `json:"model,omitempty"`
	// MergedInto, when set, names another instructionId in the same session
	// whose turn_end carries the real Diff/Tokens/Model for this instruction
	// too. This happens when a user queues a follow-up message while Claude
	// is still mid-turn (an ordinary Claude Code feature, not just
	// concurrent multi-terminal session resume — see
	// internal/store.FindUnresolvedInstructions): Claude Code resolves both
	// in one continuous turn with a single Stop, so there is no way to
	// separately attribute diff/tokens between them. Diff is zero and Tokens
	// is absent (TokensUnavailable true) on a turn_end with MergedInto set;
	// consumers should follow the reference rather than treat that as a
	// genuine zero measurement, and must not add its Diff/Tokens into any
	// sum (they would double-count MergedInto's own numbers).
	MergedInto string `json:"mergedInto,omitempty"`
	// Abandoned marks a turn_end written not because this instruction's
	// outcome was actually measured, but because it had to be closed out
	// unmeasured to recover from an unresolvable correlation state. Diff/
	// Tokens are always zero/absent on an abandoned turn_end; consumers
	// should treat it as "unknown", not "zero change". Nothing in the
	// current recorder produces this anymore (see MergedInto, which replaced
	// the abandon-on-ambiguity path this field originally existed for) — it
	// stays part of the schema so historical session files that still have
	// abandoned:true turn_end events on disk keep reading correctly.
	Abandoned bool `json:"abandoned,omitempty"`
}

// SessionEndEvent is written once when a provider session terminates.
type SessionEndEvent struct {
	Type             EventType   `json:"type"`
	Timestamp        time.Time   `json:"ts"`
	InstructionCount int         `json:"instructionCount"`
	TotalTokens      int         `json:"totalTokens"`
	Cloc             *ClocResult `json:"cloc"` // null (not omitted) when unavailable
}

// RawEvent is used when reading events back without committing to one of the
// concrete shapes above (e.g. to find the last unresolved instruction, or to
// build the web app's session timeline). It unions the fields of every event
// type; a given line only populates the ones its EventType actually writes.
type RawEvent struct {
	Type      EventType `json:"type"`
	Timestamp time.Time `json:"ts"`

	// session_start
	Provider    string      `json:"provider,omitempty"`
	SessionID   string      `json:"sessionId,omitempty"`
	Cwd         string      `json:"cwd,omitempty"`
	GitHead     string      `json:"gitHead,omitempty"`
	ClocSkipped bool        `json:"clocSkipped,omitempty"`
	Cloc        *ClocResult `json:"cloc,omitempty"` // session_start, session_end

	// instruction, turn_end
	InstructionID     string       `json:"instructionId,omitempty"`
	Text              string       `json:"text,omitempty"`              // instruction
	Baseline          DiffBaseline `json:"baseline,omitempty"`          // instruction
	Diff              DiffStat     `json:"diff,omitempty"`              // turn_end
	Tokens            TokenUsage   `json:"tokens,omitempty"`            // turn_end
	Abandoned         bool         `json:"abandoned,omitempty"`         // turn_end
	TokensUnavailable bool         `json:"tokensUnavailable,omitempty"` // turn_end
	Model             string       `json:"model,omitempty"`             // turn_end
	MergedInto        string       `json:"mergedInto,omitempty"`        // turn_end

	// session_end
	InstructionCount int `json:"instructionCount,omitempty"`
	TotalTokens      int `json:"totalTokens,omitempty"`
}
