package codex

import (
	"encoding/json"

	"maatplug/internal/model"
	"maatplug/internal/provider/adapter"
)

// DecodeFinalAnswer reads the final assistant message preserved in Stop evidence.
// A missing turn ID must not be replaced by the session/root-turn identifier.
func DecodeFinalAnswer(raw []byte) (model.EntityRef, string, error) {
	hook, err := DecodeHook("Stop", raw)
	if err != nil {
		return model.EntityRef{}, "", err
	}
	var text string
	if field := hook.Fields["last_assistant_message"]; len(field) > 0 {
		if err := json.Unmarshal(field, &text); err != nil {
			return model.EntityRef{}, "", err
		}
	}
	return adapter.EntityRef(model.EntityInstruction, "codex/instruction", hook.TurnID), text, nil
}
