package claude

import (
	"encoding/json"

	"maatplug/internal/model"
	"maatplug/internal/provider/adapter"
)

// DecodeFinalAnswer reads Stop evidence using the same prompt ID as prompt hooks.
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
	return adapter.EntityRef(model.EntityInstruction, "claude/instruction", hook.PromptID), text, nil
}
