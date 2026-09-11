package codex

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTranscript(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFindTurnUsagePrefersLatestKeyedRecord(t *testing.T) {
	path := writeTranscript(t, "not-json\n"+
		`{"type":"token_usage_record","payload":{"root_turn_id":"wanted","turn_token_usage":{"input_tokens":50,"cached_input_tokens":20,"output_tokens":5,"total_tokens":55}}}`+"\n"+
		`{"type":"token_usage_record","payload":{"root_turn_id":"other","turn_token_usage":{"input_tokens":999,"output_tokens":1,"total_tokens":1000}}}`+"\n"+
		`{"type":"token_usage_record","payload":{"root_turn_id":"wanted","turn_token_usage":{"input_tokens":100,"cached_input_tokens":40,"cache_write_input_tokens":10,"output_tokens":20,"reasoning_output_tokens":5,"total_tokens":120}}}`+"\n"+
		`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"wanted"}}`+"\n"+
		`{"type":"partial"`)

	usage, complete, err := findTurnUsage(path, "wanted")
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("turn should be complete")
	}
	got := normalizeUsage(usage)
	if got == nil || got.Input != 50 || got.CacheRead != 40 || got.CacheCreation != 10 || got.Output != 20 || got.Total != 120 {
		t.Fatalf("unexpected usage: %+v", got)
	}
}

func TestFindTurnUsageFallsBackToLegacyCumulativeCounts(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10,"total_tokens":110}}}}`+"\n"+
			`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"previous"}}`+"\n"+
			`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":200,"cached_input_tokens":60,"cache_write_input_tokens":5,"output_tokens":25,"total_tokens":225}}}}`+"\n"+
			`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":260,"cached_input_tokens":80,"cache_write_input_tokens":10,"output_tokens":40,"total_tokens":300}}}}`+"\n"+
			`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"wanted"}}`+"\n")

	usage, complete, err := findTurnUsage(path, "wanted")
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("turn should be complete")
	}
	got := normalizeUsage(usage)
	if got == nil || got.Input != 90 || got.CacheRead != 60 || got.CacheCreation != 10 || got.Output != 30 || got.Total != 190 {
		t.Fatalf("unexpected usage: %+v", got)
	}
}

func TestFindTurnUsageRejectsResetLegacyCounters(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110}}}}`+"\n"+
			`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"previous"}}`+"\n"+
			`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":20,"output_tokens":5,"total_tokens":25}}}}`+"\n"+
			`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"wanted"}}`+"\n")

	usage, complete, err := findTurnUsage(path, "wanted")
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("turn should be complete")
	}
	if usage != nil {
		t.Fatalf("usage = %+v, want nil after cumulative counter reset", usage)
	}
}

func TestTurnModelPrefersLatestMatchingRecord(t *testing.T) {
	path := writeTranscript(t, "not-json\n"+
		`{"type":"turn_context","payload":{"root_turn_id":"other","turn_id":"other","model":"gpt-5.6-nova"}}`+"\n"+
		`{"type":"turn_context","payload":{"root_turn_id":"wanted","turn_id":"wanted","model":"gpt-5.6-sol"}}`+"\n"+
		`{"type":"turn_context","payload":{"root_turn_id":"wanted","turn_id":"wanted-subturn","model":"codex-auto-review"}}`+"\n")

	got, err := turnModel(path, "wanted")
	if err != nil {
		t.Fatal(err)
	}
	if got != "codex-auto-review" {
		t.Fatalf("turnModel() = %q, want codex-auto-review (the latest matching turn_context record)", got)
	}
}

func TestTurnModelReturnsEmptyWithoutMatchingRecord(t *testing.T) {
	path := writeTranscript(t, `{"type":"turn_context","payload":{"root_turn_id":"other","turn_id":"other","model":"gpt-5.6-nova"}}`+"\n")

	got, err := turnModel(path, "wanted")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("turnModel() = %q, want empty (no matching root_turn_id)", got)
	}
}

func TestTurnModelReturnsEmptyWithoutTranscript(t *testing.T) {
	got, err := turnModel("", "wanted")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("turnModel() = %q, want empty", got)
	}
}

func TestTurnTokensReturnsNilWithoutTranscript(t *testing.T) {
	usage, err := turnTokens("", "wanted")
	if err != nil {
		t.Fatal(err)
	}
	if usage != nil {
		t.Fatalf("usage = %+v, want nil", usage)
	}
}

func TestNormalizeUsageRejectsOverlappingBucketsLargerThanInput(t *testing.T) {
	got := normalizeUsage(&codexUsage{Input: 100, CachedInput: 80, CacheWrite: 30, Output: 10, Total: 110})
	if got != nil {
		t.Fatalf("normalizeUsage() = %+v, want nil for impossible input buckets", got)
	}
}

func TestNormalizeUsageRejectsInconsistentProviderTotal(t *testing.T) {
	got := normalizeUsage(&codexUsage{Input: 100, CachedInput: 20, Output: 10, Total: 999})
	if got != nil {
		t.Fatalf("normalizeUsage() = %+v, want nil for inconsistent provider total", got)
	}
}

func TestNormalizeUsageIncludesReasoningInsideOutput(t *testing.T) {
	got := normalizeUsage(&codexUsage{
		Input:           283_583,
		CachedInput:     260_608,
		Output:          8_858,
		ReasoningOutput: 160,
		Total:           292_441,
	})
	if got == nil {
		t.Fatal("normalizeUsage() = nil, want valid usage")
	}
	if got.Input != 22_975 || got.CacheRead != 260_608 || got.Output != 8_858 || got.Total != 292_441 {
		t.Fatalf("normalizeUsage() = %+v, want exact observed Codex buckets", got)
	}
}
