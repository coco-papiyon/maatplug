package claude

import (
	"encoding/json"
	"testing"
)

func usageLine(input, output, cacheRead, cacheCreation int) transcriptLine {
	l := transcriptLine{Type: "assistant"}
	l.Message.Usage.InputTokens = input
	l.Message.Usage.OutputTokens = output
	l.Message.Usage.CacheReadInputTokens = cacheRead
	l.Message.Usage.CacheCreationInputTokens = cacheCreation
	return l
}

func keyedUsageLine(id string, input, output, cacheRead, cacheCreation int) transcriptLine {
	l := usageLine(input, output, cacheRead, cacheCreation)
	l.Message.ID = id
	return l
}

// toolResultLine simulates a tool_result message, which Claude Code records
// as type:"user" (it's a user-role message per the Anthropic Messages API)
// but tags with a toolUseResult field the real human prompt never has.
func toolResultLine() transcriptLine {
	return transcriptLine{Type: "user", ToolUseResult: json.RawMessage(`{"stdout":"ok"}`)}
}

func realPromptLine() transcriptLine {
	return transcriptLine{Type: "user"}
}

func TestSumSinceLastRealPrompt_SumsMultipleAssistantTurns(t *testing.T) {
	// A single instruction can bounce through several tool-use round trips
	// before Stop fires, each its own assistant transcript line.
	lines := []transcriptLine{
		realPromptLine(),
		usageLine(10, 5, 0, 0),
		usageLine(2, 1, 100, 50), // e.g. a follow-up after a tool call
	}

	got := sumSinceLastRealPrompt(lines)
	if got == nil {
		t.Fatal("sumSinceLastRealPrompt() = nil, want a usage total")
	}
	if got.Input != 12 || got.Output != 6 || got.CacheRead != 100 || got.CacheCreation != 50 {
		t.Fatalf("got %+v", got)
	}
	if got.Total != 168 {
		t.Fatalf("Total = %d, want 168", got.Total)
	}
}

func TestSumSinceLastRealPrompt_DeduplicatesContentBlockLinesByMessageID(t *testing.T) {
	lines := []transcriptLine{
		realPromptLine(),
		keyedUsageLine("msg-1", 2, 278, 36_341, 12_566), // thinking block
		keyedUsageLine("msg-1", 2, 278, 36_341, 12_566), // tool_use block from the same response
		keyedUsageLine("msg-2", 2, 186, 48_907, 518),    // a genuinely separate API response
	}

	got := sumSinceLastRealPrompt(lines)
	if got == nil {
		t.Fatal("sumSinceLastRealPrompt() = nil, want a usage total")
	}
	if got.Input != 4 || got.Output != 464 || got.CacheRead != 85_248 || got.CacheCreation != 13_084 {
		t.Fatalf("got %+v, want each API message counted exactly once", got)
	}
	if got.Total != 98_800 {
		t.Fatalf("Total = %d, want 98800", got.Total)
	}
}

func TestSumSinceLastRealPrompt_DeduplicatesObservedTranscriptJSONShape(t *testing.T) {
	rawLines := []string{
		`{"type":"user","message":{"role":"user","content":"fix it"}}`,
		`{"type":"assistant","uuid":"row-thinking","requestId":"req-1","message":{"id":"msg-1","role":"assistant","content":[{"type":"thinking","thinking":"..."}],"usage":{"input_tokens":2,"cache_creation_input_tokens":12566,"cache_read_input_tokens":36341,"output_tokens":278}}}`,
		`{"type":"assistant","uuid":"row-tool","requestId":"req-1","message":{"id":"msg-1","role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Bash"}],"usage":{"input_tokens":2,"cache_creation_input_tokens":12566,"cache_read_input_tokens":36341,"output_tokens":278}}}`,
	}
	lines := make([]transcriptLine, 0, len(rawLines))
	for _, raw := range rawLines {
		var line transcriptLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}

	got := sumSinceLastRealPrompt(lines)
	if got == nil || got.Input != 2 || got.Output != 278 || got.CacheRead != 36_341 || got.CacheCreation != 12_566 || got.Total != 49_187 {
		t.Fatalf("got %+v, want the repeated observed API response counted once", got)
	}
}

func TestSumSinceLastRealPrompt_DoesNotDeduplicateDistinctMessageIDs(t *testing.T) {
	lines := []transcriptLine{
		realPromptLine(),
		keyedUsageLine("msg-1", 10, 5, 100, 50),
		keyedUsageLine("msg-2", 10, 5, 100, 50),
	}

	got := sumSinceLastRealPrompt(lines)
	if got == nil || got.Total != 330 {
		t.Fatalf("got %+v, want two distinct responses totaling 330", got)
	}
}

func TestSumSinceLastRealPrompt_UsesLatestUsageForSameMessageID(t *testing.T) {
	lines := []transcriptLine{
		realPromptLine(),
		keyedUsageLine("msg-1", 1, 2, 3, 4),
		keyedUsageLine("msg-1", 10, 20, 30, 40),
	}

	got := sumSinceLastRealPrompt(lines)
	if got == nil || got.Input != 10 || got.Output != 20 || got.CacheRead != 30 || got.CacheCreation != 40 || got.Total != 100 {
		t.Fatalf("got %+v, want the latest complete usage for msg-1", got)
	}
}

func TestSumSinceLastRealPrompt_DeduplicatesByRequestIDWhenMessageIDMissing(t *testing.T) {
	first := usageLine(3, 4, 5, 6)
	first.RequestID = "req-1"
	second := first
	lines := []transcriptLine{realPromptLine(), first, second}

	got := sumSinceLastRealPrompt(lines)
	if got == nil || got.Total != 18 {
		t.Fatalf("got %+v, want requestId fallback counted once", got)
	}
}

func TestSumSinceLastRealPrompt_CountsUnkeyedLinesSeparately(t *testing.T) {
	lines := []transcriptLine{
		realPromptLine(),
		usageLine(1, 2, 3, 4),
		usageLine(1, 2, 3, 4),
	}

	got := sumSinceLastRealPrompt(lines)
	if got == nil || got.Total != 20 {
		t.Fatalf("got %+v, want unidentifiable responses retained rather than under-counted", got)
	}
}

func TestSumSinceLastRealPrompt_IncludesUsageAroundToolResults(t *testing.T) {
	// This is the shape a real tool-using turn takes: assistant calls a
	// tool, the tool result comes back as a type:"user" line (NOT a new
	// human prompt), then the assistant responds again. Every assistant
	// line in this stretch belongs to the same instruction and must be
	// summed — including the ones before the last tool_result line, which a
	// naive "since the last type:user line" scan would miss entirely.
	lines := []transcriptLine{
		realPromptLine(),
		usageLine(10, 5, 0, 0), // assistant decides to call a tool
		toolResultLine(),       // tool result comes back as type:"user"
		usageLine(3, 2, 0, 0),  // assistant calls another tool
		toolResultLine(),
		usageLine(1, 1, 0, 0), // assistant's final answer
	}

	got := sumSinceLastRealPrompt(lines)
	if got == nil {
		t.Fatal("sumSinceLastRealPrompt() = nil, want a usage total")
	}
	wantInput, wantOutput := 14, 8
	if got.Input != wantInput || got.Output != wantOutput {
		t.Fatalf("got input=%d output=%d, want input=%d output=%d (all three assistant messages, not just the last one)",
			got.Input, got.Output, wantInput, wantOutput)
	}
}

func TestSumSinceLastRealPrompt_OnlyCountsAfterMostRecentRealPrompt(t *testing.T) {
	lines := []transcriptLine{
		realPromptLine(),
		usageLine(999, 999, 0, 0), // belongs to an earlier turn
		realPromptLine(),
		usageLine(1, 2, 0, 0),
	}

	got := sumSinceLastRealPrompt(lines)
	if got == nil || got.Input != 1 || got.Output != 2 {
		t.Fatalf("got %+v, want only the usage after the second real prompt", got)
	}
}

func TestSumSinceLastRealPrompt_SkipsSidechain(t *testing.T) {
	sidechain := usageLine(1000, 1000, 0, 0)
	sidechain.IsSidechain = true
	lines := []transcriptLine{
		realPromptLine(),
		sidechain,
		usageLine(3, 4, 0, 0),
	}

	got := sumSinceLastRealPrompt(lines)
	if got == nil || got.Input != 3 || got.Output != 4 {
		t.Fatalf("got %+v, want sidechain usage excluded", got)
	}
}

func TestSumSinceLastRealPrompt_NoPromptLine(t *testing.T) {
	lines := []transcriptLine{usageLine(1, 1, 0, 0)}
	if got := sumSinceLastRealPrompt(lines); got != nil {
		t.Fatalf("got %+v, want nil with no prompt line to anchor on", got)
	}
}

// textLine builds an assistant transcript line whose message.content holds
// a single text block, matching how a real assistant response with text
// looks in the transcript.
func textLine(text string) transcriptLine {
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": text}})
	if err != nil {
		panic(err)
	}
	l := transcriptLine{Type: "assistant"}
	l.Message.Content = content
	return l
}

func TestTurnLooksComplete_NoAssistantLineYet(t *testing.T) {
	lines := []transcriptLine{realPromptLine()}
	if turnLooksComplete(lines, "the final answer") {
		t.Error("turnLooksComplete() = true before any assistant line landed")
	}
}

func TestTurnLooksComplete_MatchesLastAssistantMessage(t *testing.T) {
	lines := []transcriptLine{
		realPromptLine(),
		textLine("still working on it"), // an intermediate response, e.g. before a tool call
		toolResultLine(),
	}
	// The final text hasn't landed yet: the last assistant text doesn't
	// match what Stop told us the real final answer was.
	if turnLooksComplete(lines, "the final answer") {
		t.Fatal("turnLooksComplete() = true, but the transcript's last assistant text doesn't match last_assistant_message")
	}

	lines = append(lines, textLine("the final answer"))
	if !turnLooksComplete(lines, "the final answer") {
		t.Fatal("turnLooksComplete() = false once the transcript's last assistant text matches last_assistant_message")
	}
}

func TestTurnLooksComplete_IntermediateResponseIsNotMistakenForFinal(t *testing.T) {
	// An intermediate status message that happens to be a prefix of the
	// eventual final answer must not be treated as "the turn is done" —
	// only an exact match (once the real final message lands) should.
	lines := []transcriptLine{
		realPromptLine(),
		textLine("Running tests"),
	}
	if turnLooksComplete(lines, "Running tests completed successfully") {
		t.Fatal("turnLooksComplete() = true, but only an intermediate message has landed so far")
	}

	lines = append(lines, textLine("Running tests completed successfully"))
	if !turnLooksComplete(lines, "Running tests completed successfully") {
		t.Fatal("turnLooksComplete() = false once the exact final text has landed")
	}
}

func TestTurnLooksComplete_EmptyLastAssistantMessageAcceptsAnyResponse(t *testing.T) {
	// e.g. a turn that ends in a tool_use with no closing text: there is
	// nothing to compare against, so any landed response is accepted.
	lines := []transcriptLine{realPromptLine(), usageLine(1, 1, 0, 0)}
	if !turnLooksComplete(lines, "") {
		t.Error("turnLooksComplete() = false with an empty last_assistant_message and a landed response")
	}
}

func TestTextMatches(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"hello", "hello", true},
		{"  hello  ", "hello", true}, // whitespace-insensitive
		{"hello", "goodbye", false},
		// Substring containment in EITHER direction must not count as a
		// match: transcript lines are separate complete messages within a
		// turn, so an earlier, genuinely different message can be a prefix
		// or superset of a later one by pure topical coincidence — treating
		// that as "the final answer landed" would stop polling too early
		// and undercount the turn's real final response's tokens.
		{"hello world", "hello", false},
		{"hello", "hello world", false},
		{"Running tests", "Running tests completed successfully", false},
		{"Running tests completed successfully", "Running tests", false},
		{"", "hello", false},
		{"hello", "", false},
	}
	for _, tc := range cases {
		if got := textMatches(tc.a, tc.b); got != tc.want {
			t.Errorf("textMatches(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// modelLine builds an assistant transcript line reporting the given model,
// matching how a real assistant response's message.model field looks (see
// transcriptLine.Message.Model).
func modelLine(model string) transcriptLine {
	l := transcriptLine{Type: "assistant"}
	l.Message.Model = model
	return l
}

func TestLastModelSinceLastRealPrompt_ReturnsLastAssistantModel(t *testing.T) {
	lines := []transcriptLine{
		realPromptLine(),
		modelLine("claude-sonnet-5"),
		modelLine("claude-sonnet-5"),
	}
	if got := lastModelSinceLastRealPrompt(lines); got != "claude-sonnet-5" {
		t.Errorf("lastModelSinceLastRealPrompt() = %q, want claude-sonnet-5", got)
	}
}

func TestLastModelSinceLastRealPrompt_PrefersLatestOverEarlierModel(t *testing.T) {
	// A turn can in principle bounce through more than one model (e.g. a
	// model switch mid-turn); the last response's model is the one that
	// actually produced the answer the user saw.
	lines := []transcriptLine{
		realPromptLine(),
		modelLine("claude-haiku-4-5"),
		modelLine("claude-sonnet-5"),
	}
	if got := lastModelSinceLastRealPrompt(lines); got != "claude-sonnet-5" {
		t.Errorf("lastModelSinceLastRealPrompt() = %q, want claude-sonnet-5", got)
	}
}

func TestLastModelSinceLastRealPrompt_OnlyCountsAfterMostRecentRealPrompt(t *testing.T) {
	lines := []transcriptLine{
		realPromptLine(),
		modelLine("claude-haiku-4-5"), // belongs to an earlier turn
		realPromptLine(),
		modelLine("claude-sonnet-5"),
	}
	if got := lastModelSinceLastRealPrompt(lines); got != "claude-sonnet-5" {
		t.Errorf("lastModelSinceLastRealPrompt() = %q, want claude-sonnet-5", got)
	}
}

func TestLastModelSinceLastRealPrompt_NoPromptLine(t *testing.T) {
	lines := []transcriptLine{modelLine("claude-sonnet-5")}
	if got := lastModelSinceLastRealPrompt(lines); got != "" {
		t.Errorf("lastModelSinceLastRealPrompt() = %q, want empty with no prompt line to anchor on", got)
	}
}

func TestAssistantText(t *testing.T) {
	l := textLine("hello there")
	if got := assistantText(l); got != "hello there" {
		t.Errorf("assistantText() = %q, want %q", got, "hello there")
	}

	// A response with no content set at all (e.g. usageLine's zero-value
	// Content) must not error, just yield "".
	if got := assistantText(usageLine(1, 1, 0, 0)); got != "" {
		t.Errorf("assistantText() on a line with no content = %q, want empty", got)
	}
}
