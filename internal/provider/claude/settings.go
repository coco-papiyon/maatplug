package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// hookEvents are the Claude Code hook events maatplug registers itself
// against. See docs/design.md section 6.1.
var hookEvents = []string{
	"SessionStart",
	"UserPromptSubmit",
	"PreToolUse",
	"PermissionRequest",
	"PermissionDenied",
	"PostToolUse",
	"PostToolUseFailure",
	"SubagentStart",
	"SubagentStop",
	"FileChanged",
	"PreCompact",
	"PostCompact",
	"Stop",
	"SessionEnd",
}

// commandFor returns the exact command maatplug registers for a hook event,
// so registration and duplicate-detection agree on the same string.
func commandFor(binary, event string) string {
	return fmt.Sprintf("%s hook claude --event %s", quoteIfNeeded(binary), event)
}

// quoteIfNeeded double-quotes binary if it contains whitespace (e.g. a
// Windows "Program Files" install path), so Claude Code's shell parses it as
// one argument instead of splitting at the space and trying to run a
// truncated, nonexistent path. Left unquoted otherwise, so the common case
// (a bare "maatplug" resolved via PATH) stays exactly as before.
func quoteIfNeeded(binary string) string {
	if !strings.ContainsAny(binary, " \t") {
		return binary
	}
	return `"` + strings.ReplaceAll(binary, `"`, `\"`) + `"`
}

// MergeHooks adds maatplug's hook commands into a Claude Code settings.json
// document (already decoded into a generic map, to preserve any fields/hooks
// this tool doesn't know about) and reports whether anything changed. It is
// idempotent: running it twice with the same binary does not duplicate
// entries.
func MergeHooks(settings map[string]any, binary string) (changed bool) {
	hooksAny, ok := settings["hooks"]
	var hooks map[string]any
	if ok {
		hooks, ok = hooksAny.(map[string]any)
	}
	if !ok {
		hooks = map[string]any{}
	}

	for _, event := range hookEvents {
		command := commandFor(binary, event)
		matchers, _ := hooks[event].([]any)

		if hasCommand(matchers, command) {
			continue
		}

		matchers = append(matchers, map[string]any{
			"hooks": []any{
				map[string]any{"type": "command", "command": command},
			},
		})
		hooks[event] = matchers
		changed = true
	}

	settings["hooks"] = hooks
	return changed
}

// hasCommand reports whether any matcher entry already registers command.
func hasCommand(matchers []any, command string) bool {
	for _, m := range matchers {
		matcher, ok := m.(map[string]any)
		if !ok {
			continue
		}
		entries, ok := matcher["hooks"].([]any)
		if !ok {
			continue
		}
		for _, e := range entries {
			entry, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if cmd, _ := entry["command"].(string); cmd == command {
				return true
			}
		}
	}
	return false
}

// LoadSettings reads a Claude Code settings.json file into a generic map,
// treating a missing file as an empty document (settings.json is created
// lazily by Claude Code itself).
func LoadSettings(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	settings := map[string]any{}
	if err := json.Unmarshal(b, &settings); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return settings, nil
}

// MarshalSettings renders settings the way Claude Code itself writes
// settings.json: pretty-printed with a trailing newline.
func MarshalSettings(settings map[string]any) ([]byte, error) {
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
