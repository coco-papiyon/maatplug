// Package codex adapts Codex lifecycle hook payloads to maatplug's
// provider-independent recorder.
package codex

import (
	"encoding/json"
	"fmt"

	"maatplug/internal/recorder"
)

const ProviderName = "codex"

type hookCommon struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
}

type Handler struct {
	Recorder *recorder.Recorder
}

func NewHandler(r *recorder.Recorder) *Handler { return &Handler{Recorder: r} }

func (h *Handler) Handle(event string, stdin []byte) error {
	switch event {
	case "SessionStart":
		var in hookCommon
		if err := decode(event, stdin, &in); err != nil {
			return err
		}
		return h.Recorder.SessionStart(ProviderName, in.SessionID, in.Cwd)
	case "UserPromptSubmit":
		var in struct {
			hookCommon
			TurnID string `json:"turn_id"`
			Prompt string `json:"prompt"`
		}
		if err := decode(event, stdin, &in); err != nil {
			return err
		}
		_, err := h.Recorder.InstructionWithID(ProviderName, in.SessionID, in.Cwd, in.TurnID, in.Prompt)
		return err
	case "Stop":
		var in struct {
			hookCommon
			TurnID string `json:"turn_id"`
		}
		if err := decode(event, stdin, &in); err != nil {
			return err
		}
		// Codex's hook payload does not expose token usage or model directly.
		// Its transcript currently does, but the transcript format is
		// explicitly not a stable hook API, so parsing is best-effort and
		// must never prevent the instruction/diff record from being written.
		tokens, err := turnTokens(in.TranscriptPath, in.TurnID)
		if err != nil {
			tokens = nil
		}
		modelUsed, err := turnModel(in.TranscriptPath, in.TurnID)
		if err != nil {
			modelUsed = ""
		}
		return h.Recorder.TurnEnd(ProviderName, in.SessionID, in.Cwd, in.TurnID, modelUsed, tokens)
	case "SessionEnd":
		var in hookCommon
		if err := decode(event, stdin, &in); err != nil {
			return err
		}
		return h.Recorder.SessionEnd(ProviderName, in.SessionID, in.Cwd)
	default:
		return fmt.Errorf("codex: unsupported hook event %q", event)
	}
}

func decode(event string, stdin []byte, dst any) error {
	if err := json.Unmarshal(stdin, dst); err != nil {
		return fmt.Errorf("decode %s payload: %w", event, err)
	}
	return nil
}
