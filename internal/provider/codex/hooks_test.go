package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPluginRegistersPhase3HookSurfaces(t *testing.T) {
	path := filepath.Join("..", "..", "..", "plugins", "maatplug", "hooks", "hooks.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PermissionRequest", "PostToolUse", "SubagentStart", "SubagentStop", "PreCompact", "Stop", "SessionEnd"} {
		if len(document.Hooks[event]) == 0 {
			t.Errorf("hook %q is not registered", event)
		}
	}
}
