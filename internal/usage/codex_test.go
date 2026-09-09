package usage

import (
	"encoding/json"
	"testing"
)

// Fixture JSON is the actual `result` field observed live from Codex CLI
// 0.153.4's `account/rateLimits/read` app-server response (accountId
// redacted).
const codexRateLimitsFixture = `{
	"rateLimits": {
		"limitId": "codex",
		"primary": {"usedPercent": 0, "windowDurationMins": 300, "resetsAt": 1788894436},
		"secondary": {"usedPercent": 4, "windowDurationMins": 10080, "resetsAt": 1789463236},
		"planType": "plus"
	}
}`

func TestCodexRateLimitsResponse_Windows(t *testing.T) {
	var response codexRateLimitsResponse
	if err := json.Unmarshal([]byte(codexRateLimitsFixture), &response); err != nil {
		t.Fatal(err)
	}

	windows := response.windows()
	if len(windows) != 2 {
		t.Fatalf("windows = %+v, want 2", windows)
	}
	if windows[0].Name != "primary" || windows[0].UsedPercent != 0 || windows[0].RemainingPercent != 100 {
		t.Errorf("primary window = %+v", windows[0])
	}
	if windows[1].Name != "secondary" || windows[1].UsedPercent != 4 || windows[1].RemainingPercent != 96 {
		t.Errorf("secondary window = %+v", windows[1])
	}
	if windows[1].ResetLabel == "" {
		t.Error("secondary ResetLabel is empty, want a formatted date from resetsAt")
	}
}

func TestCodexRateLimitsResponse_NoRateLimitsIsEmpty(t *testing.T) {
	var response codexRateLimitsResponse
	if err := json.Unmarshal([]byte(`{}`), &response); err != nil {
		t.Fatal(err)
	}
	if windows := response.windows(); len(windows) != 0 {
		t.Errorf("windows = %+v, want none when rateLimits is absent", windows)
	}
}

func TestCodexRateLimitsResponse_ClampsOutOfRangePercent(t *testing.T) {
	var response codexRateLimitsResponse
	if err := json.Unmarshal([]byte(`{"rateLimits":{"primary":{"usedPercent":150}}}`), &response); err != nil {
		t.Fatal(err)
	}
	windows := response.windows()
	if len(windows) != 1 || windows[0].UsedPercent != 100 || windows[0].RemainingPercent != 0 {
		t.Errorf("windows = %+v, want usedPercent clamped to 100", windows)
	}
}
