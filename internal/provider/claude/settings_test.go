package claude

import (
	"encoding/json"
	"testing"
)

func TestMergeHooksAddsAllEvents(t *testing.T) {
	settings := map[string]any{}

	changed := MergeHooks(settings, "maatplug")
	if !changed {
		t.Fatal("MergeHooks() = false on empty settings, want true")
	}

	hooks := settings["hooks"].(map[string]any)
	for _, event := range hookEvents {
		if _, ok := hooks[event]; !ok {
			t.Errorf("hooks[%q] missing after merge", event)
		}
	}
}

func TestMergeHooksIsIdempotent(t *testing.T) {
	settings := map[string]any{}
	MergeHooks(settings, "maatplug")

	// Round-trip through JSON, as init would between two separate process
	// runs, so the second merge sees []any/map[string]any like a real reload.
	b, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := map[string]any{}
	if err := json.Unmarshal(b, &reloaded); err != nil {
		t.Fatal(err)
	}

	changed := MergeHooks(reloaded, "maatplug")
	if changed {
		t.Fatal("MergeHooks() = true on already-registered settings, want false")
	}
}

func TestMergeHooksPreservesExistingHooks(t *testing.T) {
	settings := map[string]any{
		"model": "custom-model",
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{
						map[string]any{"type": "command", "command": "echo existing"},
					},
				},
			},
		},
	}

	MergeHooks(settings, "maatplug")

	if settings["model"] != "custom-model" {
		t.Errorf("model field was clobbered: %v", settings["model"])
	}
	hooks := settings["hooks"].(map[string]any)
	if _, ok := hooks["PreToolUse"]; !ok {
		t.Fatal("pre-existing PreToolUse hook was dropped")
	}
	if _, ok := hooks["Stop"]; !ok {
		t.Fatal("maatplug's Stop hook was not added")
	}
}

func TestMergeHooksUsesBinaryOverride(t *testing.T) {
	settings := map[string]any{}
	MergeHooks(settings, "/opt/maatplug/bin/maatplug")

	hooks := settings["hooks"].(map[string]any)
	matchers := hooks["Stop"].([]any)
	entry := matchers[0].(map[string]any)
	cmds := entry["hooks"].([]any)
	cmd := cmds[0].(map[string]any)["command"].(string)

	want := "/opt/maatplug/bin/maatplug hook claude --event Stop"
	if cmd != want {
		t.Errorf("command = %q, want %q", cmd, want)
	}
}

func TestMergeHooksQuotesBinaryPathWithSpaces(t *testing.T) {
	settings := map[string]any{}
	MergeHooks(settings, `C:\Program Files\maatplug\maatplug.exe`)

	hooks := settings["hooks"].(map[string]any)
	matchers := hooks["Stop"].([]any)
	entry := matchers[0].(map[string]any)
	cmds := entry["hooks"].([]any)
	cmd := cmds[0].(map[string]any)["command"].(string)

	want := `"C:\Program Files\maatplug\maatplug.exe" hook claude --event Stop`
	if cmd != want {
		t.Errorf("command = %q, want %q (unquoted, this would run \"C:\\Program\" with \"Files\\...\" as a stray argument)", cmd, want)
	}
}

func TestMergeHooksIsIdempotentWithSpacesInBinaryPath(t *testing.T) {
	// Regression guard: commandFor's quoting must be applied identically by
	// both MergeHooks and hasCommand, or a path with spaces would never be
	// recognized as already-registered and would keep duplicating on every
	// `maatplug init --write`.
	settings := map[string]any{}
	binary := `C:\Program Files\maatplug\maatplug.exe`
	MergeHooks(settings, binary)

	if changed := MergeHooks(settings, binary); changed {
		t.Fatal("MergeHooks() = true on second call with the same spaced binary path, want false (duplicate registration)")
	}
}
