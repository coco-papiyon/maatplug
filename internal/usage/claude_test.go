package usage

import "testing"

// Fixture text is the actual `result` field observed live from `claude
// --print /usage --output-format json` (Claude Code 2.1.263).
const claudeUsageFixture = `You are currently using your subscription to power your Claude Code usage

Current session: 49% used · resets Sep 9, 1:39am (Asia/Tokyo)
Current week (all models): 11% used · resets Sep 14, 12:59pm (Asia/Tokyo)

What's contributing to your limits usage?
Approximate, based on local sessions on this machine — does not include other devices or claude.ai.`

func TestParseClaudeUsageWindows_ParsesSessionAndWeek(t *testing.T) {
	windows, err := parseClaudeUsageWindows(claudeUsageFixture)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("windows = %+v, want 2", windows)
	}
	if windows[0].Name != "session" || windows[0].UsedPercent != 49 || windows[0].RemainingPercent != 51 {
		t.Errorf("session window = %+v", windows[0])
	}
	if windows[0].ResetLabel != "Sep 9, 1:39am (Asia/Tokyo)" {
		t.Errorf("session ResetLabel = %q", windows[0].ResetLabel)
	}
	if windows[1].Name != "week" || windows[1].UsedPercent != 11 || windows[1].RemainingPercent != 89 {
		t.Errorf("week window = %+v", windows[1])
	}
}

func TestParseClaudeUsageWindows_NoWindowsIsAnError(t *testing.T) {
	// What an API-key-only (no Claude Code plan) account's /usage output
	// looks like in practice: no "Current session/week" lines at all. This
	// is what FetchClaude treats as "no contract to poll" (see Poller).
	_, err := parseClaudeUsageWindows("You are using API billing for Claude Code. Usage-based billing has no fixed limits to report.")
	if err == nil {
		t.Error("parseClaudeUsageWindows: err = nil, want an error when no usage windows are present")
	}
}
