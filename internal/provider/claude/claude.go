// Package claude implements maatplug's Claude Code adapter: it decodes the
// JSON Claude Code's hooks feed on stdin and turns each event into a call
// against internal/recorder. See docs/design.md section 6.1.
//
// Field names below (session_id, transcript_path, cwd, prompt,
// last_assistant_message, ...) are taken from Claude Code's hooks reference.
// This adapter does not rely on any id field to correlate a UserPromptSubmit
// with its matching Stop — no such field is documented on those two events'
// payloads — so UserPromptSubmit and Stop correlate purely through
// internal/store.FindUnresolvedInstructions instead (see
// internal/recorder's Instruction/TurnEndForLastInstruction doc comments).
// These field names are Claude-specific and intentionally do not leak into
// internal/recorder or internal/model, which stay provider-agnostic for
// Codex/Copilot to reuse.
package claude

import (
	"encoding/json"
	"fmt"

	"maatplug/internal/recorder"
)

// ProviderName is the value recorded in every event's "provider" field and
// used to name session files (claude-<sessionID>.jsonl).
const ProviderName = "claude"

// hookCommon holds the fields present on every Claude Code hook event.
type hookCommon struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
}

// Handler dispatches decoded hook payloads to a Recorder.
type Handler struct {
	Recorder *recorder.Recorder
}

// NewHandler wraps a Recorder for Claude Code hook events.
func NewHandler(r *recorder.Recorder) *Handler {
	return &Handler{Recorder: r}
}

// Handle decodes stdin according to event (one of SessionStart,
// UserPromptSubmit, Stop, SessionEnd) and records it.
func (h *Handler) Handle(event string, stdin []byte) error {
	switch event {
	case "SessionStart":
		return h.handleSessionStart(stdin)
	case "UserPromptSubmit":
		return h.handleUserPromptSubmit(stdin)
	case "Stop":
		return h.handleStop(stdin)
	case "SessionEnd":
		return h.handleSessionEnd(stdin)
	default:
		return fmt.Errorf("claude: unsupported hook event %q", event)
	}
}

func (h *Handler) handleSessionStart(stdin []byte) error {
	var in hookCommon
	if err := json.Unmarshal(stdin, &in); err != nil {
		return fmt.Errorf("decode SessionStart payload: %w", err)
	}
	return h.Recorder.SessionStart(ProviderName, in.SessionID, in.Cwd)
}

func (h *Handler) handleUserPromptSubmit(stdin []byte) error {
	var in struct {
		hookCommon
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(stdin, &in); err != nil {
		return fmt.Errorf("decode UserPromptSubmit payload: %w", err)
	}
	// The returned instructionId is discarded: Stop recovers it from the
	// store (FindUnresolvedInstructions) rather than needing it handed
	// back, since UserPromptSubmit and Stop are separate short-lived
	// processes with nothing to pass an id through.
	_, err := h.Recorder.Instruction(ProviderName, in.SessionID, in.Cwd, in.Prompt)
	return err
}

func (h *Handler) handleStop(stdin []byte) error {
	var in struct {
		hookCommon
		LastAssistantMessage string `json:"last_assistant_message"`
	}
	if err := json.Unmarshal(stdin, &in); err != nil {
		return fmt.Errorf("decode Stop payload: %w", err)
	}

	tokens, modelUsed, err := sumTurnTokens(in.TranscriptPath, in.LastAssistantMessage)
	if err != nil {
		// transcript_path is best-effort: token usage/model are not present
		// in the hook payload itself, so a read failure degrades to
		// tokensUnavailable/an empty model rather than blocking the
		// diff/instruction record.
		tokens = nil
		modelUsed = ""
	}

	return h.Recorder.TurnEndForLastInstruction(ProviderName, in.SessionID, in.Cwd, modelUsed, tokens)
}

func (h *Handler) handleSessionEnd(stdin []byte) error {
	var in hookCommon
	if err := json.Unmarshal(stdin, &in); err != nil {
		return fmt.Errorf("decode SessionEnd payload: %w", err)
	}
	return h.Recorder.SessionEnd(ProviderName, in.SessionID, in.Cwd)
}
