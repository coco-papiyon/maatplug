package codex

import (
	"bufio"
	"encoding/json"
	"os"
	"time"

	"maatplug/internal/model"
)

const (
	transcriptPollInterval = 200 * time.Millisecond
	transcriptPollAttempts = 15
	maxTranscriptLineSize  = 16 * 1024 * 1024
)

// codexUsage mirrors both the current token_usage_record payload and the
// older event_msg/token_count payload. Cached and cache-write input are
// subsets of input_tokens in Codex, unlike the mutually exclusive fields in
// maatplug's provider-independent TokenUsage.
type codexUsage struct {
	Input           int `json:"input_tokens"`
	CachedInput     int `json:"cached_input_tokens"`
	CacheWrite      int `json:"cache_write_input_tokens"`
	Output          int `json:"output_tokens"`
	ReasoningOutput int `json:"reasoning_output_tokens"`
	Total           int `json:"total_tokens"`
}

type transcriptLine struct {
	Type    string `json:"type"`
	Payload struct {
		Type           string      `json:"type"`
		RootTurnID     string      `json:"root_turn_id"`
		TurnID         string      `json:"turn_id"`
		Model          string      `json:"model"` // turn_context lines only
		TurnTokenUsage *codexUsage `json:"turn_token_usage"`
		Info           struct {
			TotalTokenUsage *codexUsage `json:"total_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

// turnTokens waits briefly for Codex's asynchronous transcript writer, then
// returns the completed turn's usage. The transcript format is not a stable
// Codex hook API, so callers treat every error or unknown shape as
// tokens-unavailable rather than failing the Stop hook.
func turnTokens(transcriptPath, turnID string) (*model.TokenUsage, error) {
	if transcriptPath == "" || turnID == "" {
		return nil, nil
	}

	var latest *codexUsage
	for attempt := 0; attempt < transcriptPollAttempts; attempt++ {
		usage, complete, err := findTurnUsage(transcriptPath, turnID)
		if err != nil {
			return nil, err
		}
		if usage != nil {
			latest = usage
		}
		if complete {
			return normalizeUsage(usage), nil
		}
		time.Sleep(transcriptPollInterval)
	}

	// A future Codex version may remove the task_complete marker while still
	// writing keyed usage records. After the bounded wait, retain the latest
	// keyed value instead of discarding otherwise useful data.
	return normalizeUsage(latest), nil
}

// findTurnUsage scans one transcript snapshot. Current Codex versions write
// token_usage_record with an exact root_turn_id and cumulative usage for that
// turn. Older versions only wrote thread-cumulative token_count events; for
// those, subtract the usage at the preceding task_complete marker.
func findTurnUsage(path, turnID string) (*codexUsage, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	var (
		keyedUsage       *codexUsage
		lastCumulative   *codexUsage
		previousComplete *codexUsage
		legacyUsage      *codexUsage
		complete         bool
	)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxTranscriptLineSize)
	for scanner.Scan() {
		var line transcriptLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue // tolerate partial/unknown transcript lines
		}

		switch {
		case line.Type == "token_usage_record" && line.Payload.RootTurnID == turnID && line.Payload.TurnTokenUsage != nil:
			keyedUsage = cloneUsage(line.Payload.TurnTokenUsage)
		case line.Type == "event_msg" && line.Payload.Type == "token_count" && line.Payload.Info.TotalTokenUsage != nil:
			lastCumulative = cloneUsage(line.Payload.Info.TotalTokenUsage)
		case line.Type == "event_msg" && line.Payload.Type == "task_complete":
			if line.Payload.TurnID == turnID {
				complete = true
				if lastCumulative != nil {
					legacyUsage, _ = subtractUsage(lastCumulative, previousComplete)
				}
			} else if !complete && lastCumulative != nil {
				previousComplete = cloneUsage(lastCumulative)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, false, err
	}
	if keyedUsage != nil {
		return keyedUsage, complete, nil
	}
	return legacyUsage, complete, nil
}

// turnModel returns the model name Codex used for a turn, read from that
// turn's "turn_context" transcript line (payload.model, keyed by
// root_turn_id like token_usage_record — see findTurnUsage). Like
// turnTokens, the transcript format is not a stable hook API, so any read
// failure or missing record degrades to an empty string rather than failing
// the Stop hook. Unlike token usage, there is no separate "unavailable" flag
// for the model — an empty string is already an unambiguous "unknown".
func turnModel(transcriptPath, turnID string) (string, error) {
	if transcriptPath == "" || turnID == "" {
		return "", nil
	}

	f, err := os.Open(transcriptPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var modelUsed string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxTranscriptLineSize)
	for scanner.Scan() {
		var line transcriptLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue // tolerate partial/unknown transcript lines
		}
		if line.Type == "turn_context" && line.Payload.RootTurnID == turnID && line.Payload.Model != "" {
			modelUsed = line.Payload.Model // latest matching record wins, same as findTurnUsage's keyedUsage
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return modelUsed, nil
}

func cloneUsage(usage *codexUsage) *codexUsage {
	if usage == nil {
		return nil
	}
	copy := *usage
	return &copy
}

func subtractUsage(current, previous *codexUsage) (*codexUsage, bool) {
	if current == nil {
		return nil, false
	}
	if previous == nil {
		return cloneUsage(current), true
	}
	if current.Input < previous.Input ||
		current.CachedInput < previous.CachedInput ||
		current.CacheWrite < previous.CacheWrite ||
		current.Output < previous.Output ||
		current.ReasoningOutput < previous.ReasoningOutput ||
		current.Total < previous.Total {
		return nil, false // cumulative counters reset; a safe delta is impossible
	}
	return &codexUsage{
		Input:           current.Input - previous.Input,
		CachedInput:     current.CachedInput - previous.CachedInput,
		CacheWrite:      current.CacheWrite - previous.CacheWrite,
		Output:          current.Output - previous.Output,
		ReasoningOutput: current.ReasoningOutput - previous.ReasoningOutput,
		Total:           current.Total - previous.Total,
	}, true
}

func normalizeUsage(usage *codexUsage) *model.TokenUsage {
	if usage == nil {
		return nil
	}
	if usage.Input < 0 || usage.CachedInput < 0 || usage.CacheWrite < 0 || usage.Output < 0 || usage.Total < 0 {
		return nil
	}
	if usage.CachedInput+usage.CacheWrite > usage.Input {
		return nil
	}

	// Codex reports cached/cache-write tokens as subsets of input_tokens and
	// reasoning tokens as a subset of output_tokens. Reject an internally
	// inconsistent record instead of persisting a plausible-looking but
	// inaccurate total when an unknown transcript shape appears.
	computedTotal := usage.Input + usage.Output
	if usage.Total != 0 && usage.Total != computedTotal {
		return nil
	}

	// cached_input_tokens/cache_write_input_tokens are included in
	// input_tokens. Store exclusive buckets so TokenUsage retains the same
	// meaning it has for the Claude adapter.
	nonCachedInput := usage.Input - usage.CachedInput - usage.CacheWrite
	total := usage.Total
	if total == 0 {
		total = computedTotal
	}
	return &model.TokenUsage{
		Input:         nonCachedInput,
		Output:        usage.Output,
		CacheRead:     usage.CachedInput,
		CacheCreation: usage.CacheWrite,
		Total:         total,
	}
}
