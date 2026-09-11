package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"time"

	"maatplug/internal/model"
)

// transcriptLine covers the fields maatplug needs from a Claude Code
// transcript JSONL line. "user", "assistant", and other line kinds share
// this shape; unused fields for a given kind simply stay zero.
//
// Claude Code represents tool results as type:"user" lines too (the
// Anthropic Messages API encodes a tool_result as a user-role message), so
// type=="user" alone does not mean "the human's prompt". Those tool-result
// lines carry a "toolUseResult" field the real prompt line never has; see
// isRealPrompt.
type transcriptLine struct {
	Type          string          `json:"type"`
	IsSidechain   bool            `json:"isSidechain"`
	ToolUseResult json.RawMessage `json:"toolUseResult,omitempty"`
	RequestID     string          `json:"requestId"`
	Message       struct {
		ID      string          `json:"id"`
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func isRealPrompt(l transcriptLine) bool {
	return l.Type == "user" && !l.IsSidechain && len(l.ToolUseResult) == 0
}

func isAssistantResponse(l transcriptLine) bool {
	return l.Type == "assistant" && !l.IsSidechain
}

// assistantText concatenates every text content block in an assistant
// message (skipping thinking/tool_use blocks), matching how Claude Code's
// Stop hook payload reports last_assistant_message as plain text.
func assistantText(l transcriptLine) string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(l.Message.Content, &blocks); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

const (
	transcriptPollInterval = 200 * time.Millisecond
	transcriptPollAttempts = 15 // ~3s total, per docs/design.md section 12 (async transcript write lag)
)

// sumTurnTokens sums the usage of every distinct non-sidechain assistant API
// response after the most recent real human prompt line in the transcript.
// Claude Code can serialize one API response as several assistant lines (for
// example separate thinking and tool_use content blocks). Those lines carry
// the same message.id/requestId and repeat the response's complete usage, so
// they must be counted once rather than once per content block. Distinct API
// responses around tool calls are still all included.
//
// Claude Code writes the transcript file asynchronously, so lines for the
// turn that just finished may not have fully landed yet when Stop fires.
// lastAssistantMessage is Stop's own last_assistant_message field — the
// authoritative final response text, supplied by Claude Code itself rather
// than inferred — and this polls until the transcript's own last assistant
// line's text matches it, which is a real completion signal rather than
// "the transcript stopped changing for one polling interval" (a fixed
// pause between two of the turn's own assistant messages, e.g. while a tool
// call is running, can by coincidence look identical to "finished" under a
// silence-based check). If lastAssistantMessage is empty (e.g. the turn
// ended in a tool_use with no closing text) or never matches within the
// attempt budget, this falls back to whatever the transcript holds once
// attempts are exhausted — never worse than the old silence-based check.
func sumTurnTokens(transcriptPath, lastAssistantMessage string) (*model.TokenUsage, string, error) {
	if transcriptPath == "" {
		return nil, "", nil
	}

	var lines []transcriptLine
	for attempt := 0; attempt < transcriptPollAttempts; attempt++ {
		var err error
		lines, err = readTranscriptLines(transcriptPath)
		if err != nil {
			return nil, "", err
		}
		if turnLooksComplete(lines, lastAssistantMessage) {
			break
		}
		time.Sleep(transcriptPollInterval)
	}

	return sumSinceLastRealPrompt(lines), lastModelSinceLastRealPrompt(lines), nil
}

// turnLooksComplete reports whether the transcript's last assistant
// response (since the last real prompt) matches lastAssistantMessage. See
// sumTurnTokens for why this, rather than "no new lines for one interval",
// is the completion signal.
func turnLooksComplete(lines []transcriptLine, lastAssistantMessage string) bool {
	idx := lastRealPromptLineIndex(lines)
	if idx == -1 {
		return false
	}

	tail := lines[idx+1:]
	for i := len(tail) - 1; i >= 0; i-- {
		if !isAssistantResponse(tail[i]) {
			continue
		}
		if lastAssistantMessage == "" {
			return true // nothing to compare against; at least a response landed
		}
		return textMatches(assistantText(tail[i]), lastAssistantMessage)
	}
	return false
}

// textMatches compares two response texts after trimming surrounding
// whitespace, and nothing more: exact equality only, not substring
// containment in either direction. A one-directional "does the transcript's
// current text contain/get contained by the target" check was tried and
// rejected — transcript lines are separate, complete messages within the
// same turn (e.g. an intermediate status line like "Running tests" followed
// later by an unrelated final answer like "Running tests completed
// successfully"), so an earlier, still-not-final message can easily be a
// substring of — or a superset overlapping — the eventual real final
// message's text purely by topical coincidence, not because the turn has
// actually finished. Requiring exact equality means a mismatch just falls
// back to polling until the attempt budget in sumTurnTokens runs out, same
// as if this check didn't exist at all — never worse, just potentially
// slower when the join formatting genuinely differs.
func textMatches(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return a == b
}

func readTranscriptLines(path string) ([]transcriptLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []transcriptLine
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var l transcriptLine
		if err := json.Unmarshal(scanner.Bytes(), &l); err != nil {
			continue // tolerate non-message lines (queue-operation, system, ...)
		}
		lines = append(lines, l)
	}
	return lines, scanner.Err()
}

func lastRealPromptLineIndex(lines []transcriptLine) int {
	idx := -1
	for i, l := range lines {
		if isRealPrompt(l) {
			idx = i
		}
	}
	return idx
}

func sumSinceLastRealPrompt(lines []transcriptLine) *model.TokenUsage {
	idx := lastRealPromptLineIndex(lines)
	if idx == -1 {
		return nil
	}

	// Later copies of the same API message are authoritative. Real Claude
	// transcripts normally repeat identical usage, but replacing rather than
	// adding also handles a future/older writer that emits an early partial
	// record followed by a completed one.
	keyed := make(map[string]model.TokenUsage)
	var unkeyed []model.TokenUsage
	found := false
	for _, l := range lines[idx+1:] {
		if !isAssistantResponse(l) {
			continue
		}
		found = true
		lineUsage := model.TokenUsage{
			Input:         l.Message.Usage.InputTokens,
			Output:        l.Message.Usage.OutputTokens,
			CacheRead:     l.Message.Usage.CacheReadInputTokens,
			CacheCreation: l.Message.Usage.CacheCreationInputTokens,
		}
		if key := assistantUsageKey(l); key != "" {
			keyed[key] = lineUsage
		} else {
			// Without a provider identifier there is no safe way to prove two
			// lines are the same response. Count both to avoid under-reporting
			// real calls in old or changed transcript formats.
			unkeyed = append(unkeyed, lineUsage)
		}
	}
	if !found {
		return nil
	}

	var usage model.TokenUsage
	add := func(u model.TokenUsage) {
		usage.Input += u.Input
		usage.Output += u.Output
		usage.CacheRead += u.CacheRead
		usage.CacheCreation += u.CacheCreation
	}
	for _, u := range keyed {
		add(u)
	}
	for _, u := range unkeyed {
		add(u)
	}
	usage.Total = usage.Input + usage.Output + usage.CacheRead + usage.CacheCreation
	return &usage
}

// assistantUsageKey identifies the API response whose complete usage is
// repeated on each serialized content-block line. message.id is the primary
// Anthropic response identifier; requestId is a best-effort fallback observed
// in Claude Code transcripts. Prefixes keep the two namespaces distinct.
func assistantUsageKey(l transcriptLine) string {
	if l.Message.ID != "" {
		return "message:" + l.Message.ID
	}
	if l.RequestID != "" {
		return "request:" + l.RequestID
	}
	return ""
}

// lastModelSinceLastRealPrompt returns the model field of the turn's last
// assistant response (mirroring sumSinceLastRealPrompt's scan range), which
// is the model that actually produced the answer the user saw. A turn can in
// principle bounce through several assistant messages on different models
// (e.g. a model switch mid-turn), so the last one — not the first — is the
// one worth recording.
func lastModelSinceLastRealPrompt(lines []transcriptLine) string {
	idx := lastRealPromptLineIndex(lines)
	if idx == -1 {
		return ""
	}

	for i := len(lines) - 1; i > idx; i-- {
		if isAssistantResponse(lines[i]) && lines[i].Message.Model != "" {
			return lines[i].Message.Model
		}
	}
	return ""
}
