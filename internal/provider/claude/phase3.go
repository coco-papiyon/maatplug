package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"maatplug/internal/model"
	provideradapter "maatplug/internal/provider/adapter"
)

type phase3HookPayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	PromptID       string `json:"prompt_id"`
	Prompt         string `json:"prompt"`
	Model          string `json:"model"`
}

func DecodeHook(event string, raw []byte) (provideradapter.Hook, error) {
	var common phase3HookPayload
	if err := json.Unmarshal(raw, &common); err != nil {
		return provideradapter.Hook{}, fmt.Errorf("claude decode %s: %w", event, err)
	}
	if common.SessionID == "" || common.Cwd == "" {
		return provideradapter.Hook{}, errors.New("claude hook requires session_id and cwd")
	}
	if common.HookEventName != "" && common.HookEventName != event {
		return provideradapter.Hook{}, fmt.Errorf("claude hook event mismatch: command=%s payload=%s", event, common.HookEventName)
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return provideradapter.Hook{}, err
	}
	runID := common.PromptID
	if runID == "" {
		runID = fieldString(fields, "agent_id")
	}
	if runID == "" {
		runID = common.SessionID
	}
	return provideradapter.Hook{Event: event, SessionID: common.SessionID, RunID: runID, Cwd: common.Cwd, TranscriptPath: common.TranscriptPath, PromptID: common.PromptID, Model: common.Model, Prompt: common.Prompt, Raw: raw, Fields: fields}, nil
}

func NormalizeHook(projectID, receiptID string, receivedAt time.Time, hook provideradapter.Hook) (provideradapter.Batch, error) {
	return provideradapter.NormalizeHook(ProviderName, projectID, receiptID, receivedAt, hook)
}

type phase3TranscriptLine struct {
	Type           string `json:"type"`
	UUID           string `json:"uuid"`
	RequestID      string `json:"requestId"`
	SessionID      string `json:"sessionId"`
	PromptID       string `json:"promptId"`
	Timestamp      string `json:"timestamp"`
	IsSidechain    bool   `json:"isSidechain"`
	Cwd            string `json:"cwd"`
	PermissionMode string `json:"permissionMode"`
	Version        string `json:"version"`
	Message        struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Usage   claudeUsage     `json:"usage"`
	} `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type responseObservation struct {
	Line   phase3TranscriptLine
	At     time.Time
	RawRef model.NamespacedID
	Scope  []model.EntityRef
}

func NormalizeTranscript(projectID, receiptID string, receivedAt time.Time, hook provideradapter.Hook, chunk provideradapter.Transcript) (provideradapter.Batch, error) {
	var batch provideradapter.Batch
	scope := provideradapter.Scope(ProviderName, projectID, hook.SessionID, hook.RunID)
	if chunk.Unavailable != "" {
		return transcriptDiagnostic(batch, scope, receiptID, receivedAt, chunk, "unavailable", chunk.Unavailable)
	}
	if chunk.Rotated {
		var err error
		batch, err = transcriptDiagnostic(batch, scope, receiptID, receivedAt, chunk, "rotated", "transcript generation changed or file shrank")
		if err != nil {
			return batch, err
		}
	}
	responses := map[string]responseObservation{}
	order := []string{}
	knownIDs := make(map[string]bool, len(chunk.KnownIDs))
	for id := range chunk.KnownIDs {
		knownIDs[id] = true
	}
	for _, sourceLine := range chunk.Lines {
		ref := batch.AddContent(sourceLine.Data, model.ContentRaw, "application/json", "claude-transcript-line-v1")
		var line phase3TranscriptLine
		if err := json.Unmarshal(sourceLine.Data, &line); err != nil {
			if err := addRawLineEvent(&batch, scope, receivedAt, chunk, sourceLine, ref, "unknown_record", "invalid JSON: "+err.Error()); err != nil {
				return batch, err
			}
			continue
		}
		at := parseTime(line.Timestamp, receivedAt)
		lineScope := provideradapter.ScopeWithRun(scope, ProviderName, line.PromptID)
		state, reason := claudeRecordState(line)
		rawIndex := len(batch.Events)
		if err := addRawLineEvent(&batch, lineScope, at, chunk, sourceLine, ref, state, reason); err != nil {
			return batch, err
		}
		if line.PromptID != "" && hook.RunID != "" && line.PromptID != hook.RunID {
			batch.Events[rawIndex].Availability = append(batch.Events[rawIndex].Availability, model.FieldAvailability{Path: "/scope/run", Status: model.AvailabilityConflict, Reason: "transcript prompt ID differs from the Stop hook run; record was routed by transcript prompt ID"})
			if err := batch.Events[rawIndex].Validate(); err != nil {
				return batch, err
			}
		}
		if line.IsSidechain {
			if err := addSidechainEvent(&batch, lineScope, at, line, sourceLine, knownIDs); err != nil {
				return batch, err
			}
		}
		if line.Type == "assistant" && !line.IsSidechain {
			key := firstNonEmpty(line.Message.ID, line.RequestID)
			if key != "" && (!knownIDs["response-call:"+key] || !knownIDs["response-usage:"+key]) {
				if _, found := responses[key]; !found {
					order = append(order, key)
				}
				responses[key] = responseObservation{Line: line, At: at, RawRef: ref.ID, Scope: lineScope}
			}
			if err := addClaudeToolRequests(&batch, lineScope, at, line, sourceLine, knownIDs); err != nil {
				return batch, err
			}
		}
		if line.Type == "user" {
			if err := addClaudeToolResults(&batch, lineScope, at, line, sourceLine, knownIDs); err != nil {
				return batch, err
			}
		}
	}
	for _, key := range order {
		observation := responses[key]
		if err := addClaudeResponse(&batch, observation.Scope, hook, key, observation, knownIDs); err != nil {
			return batch, err
		}
	}
	if chunk.IncompleteTail {
		var err error
		batch, err = transcriptDiagnostic(batch, scope, receiptID, receivedAt, chunk, "partial_tail", "last transcript line is incomplete")
		if err != nil {
			return batch, err
		}
	}
	return batch, nil
}

func addRawLineEvent(batch *provideradapter.Batch, scope []model.EntityRef, at time.Time, chunk provideradapter.Transcript, line provideradapter.Line, ref model.ContentRef, state, reason string) error {
	position := line.Number
	payload := model.CollectionStatePayload{Stream: provideradapter.EntityRef(model.EntityStream, "claude/transcript", chunk.Path), State: state, Cursor: fmt.Sprintf("%d", line.ByteOffset), Generation: chunk.Generation, LastGoodPosition: &position, Evidence: []model.ContentRef{ref}}
	if reason != "" {
		payload.Coverage = []model.FieldAvailability{{Path: "/transcript/record", Status: model.AvailabilityUnknown, Reason: reason}}
	}
	event, err := provideradapter.NewEvent(ProviderName, "transcript", at, &at, provideradapter.ID("claude/transcript-row", chunk.Generation, fmt.Sprint(line.Number), string(line.Data)), model.EventCollectionState, scope, payload)
	if err != nil {
		return err
	}
	event.Source.RecordID = &ref.ID
	event.Sequence = &model.EventSequence{Stream: payload.Stream.ID, Generation: chunk.Generation, Position: &position}
	if err := event.Validate(); err != nil {
		return err
	}
	batch.Events = append(batch.Events, event)
	return nil
}

func addClaudeResponse(batch *provideradapter.Batch, scope []model.EntityRef, hook provideradapter.Hook, key string, observed responseObservation, known map[string]bool) error {
	line := observed.Line
	call := provideradapter.EntityRef(model.EntityModelCall, "claude/message", key)
	callKey := "response-call:" + key
	if !known[callKey] {
		instruction := provideradapter.EntityRef(model.EntityInstruction, "claude/instruction", firstNonEmpty(line.PromptID, hook.PromptID, hook.RunID))
		response := provideradapter.EntityRef(model.EntityResponse, "claude/message", key)
		callPayload := model.ModelCallPayload{Call: call, Instruction: instruction, Actor: provideradapter.EntityRef(model.EntityAgent, "claude/agent", "main"), ActualModel: line.Message.Model, State: "completed", Response: &response}
		callEvent, err := provideradapter.NewEvent(ProviderName, "transcript", observed.At, &observed.At, provideradapter.ID("claude/model-call", key), model.EventModelCall, scope, callPayload)
		if err != nil {
			return err
		}
		callEvent.Source.RecordID = &observed.RawRef
		if line.Message.Model == "" {
			callEvent.Availability = append(callEvent.Availability, model.FieldAvailability{Path: "/payload/actualModel", Status: model.AvailabilityUnknown, Reason: "Claude transcript record did not include a model"})
		}
		batch.Events = append(batch.Events, callEvent)
		batch.SeenIDs = append(batch.SeenIDs, callKey)
		known[callKey] = true
	}
	usage := line.Message.Usage
	usageKey := "response-usage:" + key
	if !hasClaudeUsage(usage) || known[usageKey] {
		return nil
	}
	raw := map[string]json.RawMessage{}
	putRawInt(raw, "input_tokens", usage.InputTokens)
	putRawInt(raw, "output_tokens", usage.OutputTokens)
	putRawInt(raw, "cache_creation_input_tokens", usage.CacheCreationInputTokens)
	putRawInt(raw, "cache_read_input_tokens", usage.CacheReadInputTokens)
	total := usage.InputTokens + usage.OutputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
	payload := model.UsagePayload{
		Usage: provideradapter.EntityRef(model.EntityUsage, "claude/message-usage", key), AppliesTo: call,
		Raw: model.RawUsage{Unit: "tokens", Fields: raw},
		Normalized: &model.NormalizedUsage{ExclusiveBuckets: []model.UsageBucket{
			{Name: "input_uncached", Tokens: provideradapter.ObservedQuantity(usage.InputTokens)},
			{Name: "cache_write_input", Tokens: provideradapter.ObservedQuantity(usage.CacheCreationInputTokens)},
			{Name: "cache_read_input", Tokens: provideradapter.ObservedQuantity(usage.CacheReadInputTokens)},
			{Name: "output", Tokens: provideradapter.ObservedQuantity(usage.OutputTokens)},
		}, Totals: []model.UsageBucket{{Name: "all", Tokens: provideradapter.ObservedQuantity(total)}}},
		Counter: model.CounterSemantics{Kind: model.CounterDelta}, State: model.MeasurementFinal,
	}
	usageEvent, err := provideradapter.NewEvent(ProviderName, "transcript", observed.At, &observed.At, provideradapter.ID("claude/usage", key), model.EventUsageReported, scope, payload)
	if err != nil {
		return err
	}
	usageEvent.Source.RecordID = &observed.RawRef
	usageEvent.Adoption = &model.Adoption{Status: model.AdoptionAdopted, RuleVersion: "claude-response-id-v1", Reason: "one final usage per distinct Claude message ID"}
	if err := usageEvent.Validate(); err != nil {
		return err
	}
	batch.Events = append(batch.Events, usageEvent)
	batch.SeenIDs = append(batch.SeenIDs, usageKey)
	known[usageKey] = true
	return nil
}

func addClaudeToolRequests(batch *provideradapter.Batch, scope []model.EntityRef, at time.Time, line phase3TranscriptLine, source provideradapter.Line, known map[string]bool) error {
	var blocks []claudeBlock
	if json.Unmarshal(line.Message.Content, &blocks) != nil {
		return nil
	}
	for _, block := range blocks {
		if block.Type != "tool_use" || block.ID == "" {
			continue
		}
		key := "tool-request:" + block.ID
		if known[key] {
			continue
		}
		var refs []model.ContentRef
		if len(block.Input) > 0 {
			wrapped, _ := json.Marshal(map[string]any{
				"toolInput":   json.RawMessage(block.Input),
				"environment": map[string]any{"cwd": line.Cwd, "permissionMode": line.PermissionMode},
			})
			refs = append(refs, batch.AddContent(wrapped, model.ContentRaw, "application/json", "claude-operation-request-v1"))
		}
		payload := model.OperationPayload{Operation: provideradapter.EntityRef(model.EntityOperation, "claude/tool", block.ID), Actor: provideradapter.EntityRef(model.EntityAgent, "claude/agent", "main"), Kind: transcriptToolKind(block.Name), Name: firstNonEmpty(block.Name, "unknown"), Input: refs}
		event, err := provideradapter.NewEvent(ProviderName, "transcript", at, &at, provideradapter.ID("claude/tool-request", block.ID), model.EventOperationRequested, scope, payload)
		if err != nil {
			return err
		}
		batch.Events = append(batch.Events, event)
		batch.SeenIDs = append(batch.SeenIDs, key)
		known[key] = true
	}
	return nil
}

func addClaudeToolResults(batch *provideradapter.Batch, scope []model.EntityRef, at time.Time, line phase3TranscriptLine, source provideradapter.Line, known map[string]bool) error {
	var blocks []claudeBlock
	if json.Unmarshal(line.Message.Content, &blocks) != nil {
		return nil
	}
	for _, block := range blocks {
		if block.Type != "tool_result" || block.ToolUseID == "" {
			continue
		}
		key := "tool-result:" + block.ToolUseID
		if known[key] {
			continue
		}
		data := block.Content
		if len(line.ToolUseResult) > 0 {
			data = line.ToolUseResult
		}
		var refs []model.ContentRef
		if len(data) > 0 {
			refs = append(refs, batch.AddContent(data, model.ContentRaw, "application/json", "claude-tool-output-v1"))
		}
		status := "completed"
		if block.IsError {
			status = "failed"
		}
		payload := model.OperationResultPayload{Operation: provideradapter.EntityRef(model.EntityOperation, "claude/tool", block.ToolUseID), Status: status, Output: refs}
		event, err := provideradapter.NewEvent(ProviderName, "transcript", at, &at, provideradapter.ID("claude/tool-result", block.ToolUseID, status), model.EventOperationResult, scope, payload)
		if err != nil {
			return err
		}
		batch.Events = append(batch.Events, event)
		batch.SeenIDs = append(batch.SeenIDs, key)
		known[key] = true
	}
	return nil
}

func addSidechainEvent(batch *provideradapter.Batch, scope []model.EntityRef, at time.Time, line phase3TranscriptLine, source provideradapter.Line, known map[string]bool) error {
	id := firstNonEmpty(line.UUID, fmt.Sprint(source.Number))
	key := "sidechain:" + id
	if known[key] {
		return nil
	}
	payload := model.ActorPayload{Actor: provideradapter.EntityRef(model.EntityAgent, "claude/sidechain", id), Kind: "subagent"}
	event, err := provideradapter.NewEvent(ProviderName, "transcript", at, &at, provideradapter.ID("claude/sidechain", id), model.EventActorObserved, scope, payload)
	if err != nil {
		return err
	}
	event.Availability = []model.FieldAvailability{{Path: "/payload/parent", Status: model.AvailabilityUnknown, Reason: "transcript sidechain flag does not identify the parent agent"}}
	batch.Events = append(batch.Events, event)
	batch.SeenIDs = append(batch.SeenIDs, key)
	known[key] = true
	return event.Validate()
}

func transcriptDiagnostic(batch provideradapter.Batch, scope []model.EntityRef, receiptID string, at time.Time, chunk provideradapter.Transcript, state, reason string) (provideradapter.Batch, error) {
	generation := chunk.Generation
	if generation == "" {
		generation = "unavailable"
	}
	payload := model.CollectionStatePayload{Stream: provideradapter.EntityRef(model.EntityStream, "claude/transcript", firstNonEmpty(chunk.Path, "unknown")), State: state, Cursor: fmt.Sprint(chunk.StartByteOffset), Generation: generation, Coverage: []model.FieldAvailability{{Path: "/transcript", Status: model.AvailabilityUnknown, Reason: reason}}}
	event, err := provideradapter.NewEvent(ProviderName, "transcript", at, nil, provideradapter.ID("claude/transcript-state", receiptID, state), model.EventCollectionState, scope, payload)
	if err == nil {
		batch.Events = append(batch.Events, event)
	}
	return batch, err
}

func fieldString(fields map[string]json.RawMessage, name string) string {
	var value string
	_ = json.Unmarshal(fields[name], &value)
	return value
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
func parseTime(value string, fallback time.Time) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err == nil {
		return parsed
	}
	return fallback
}
func putRawInt(target map[string]json.RawMessage, name string, value int64) {
	target[name] = json.RawMessage(fmt.Sprintf("%d", value))
}
func transcriptToolKind(name string) string {
	lower := strings.ToLower(name)
	if strings.EqualFold(name, "Bash") {
		return "shell"
	}
	if strings.Contains(lower, "edit") || strings.Contains(lower, "write") || strings.Contains(lower, "patch") {
		return "edit"
	}
	if strings.Contains(lower, "browser") {
		return "browser"
	}
	if strings.Contains(lower, "mcp") || strings.Contains(lower, "connector") {
		return "connector"
	}
	if strings.Contains(lower, "webfetch") || strings.Contains(lower, "web_search") || strings.Contains(lower, "http") {
		return "external_api"
	}
	return "tool"
}

func hasClaudeUsage(usage claudeUsage) bool {
	return usage.InputTokens != 0 || usage.OutputTokens != 0 || usage.CacheCreationInputTokens != 0 || usage.CacheReadInputTokens != 0
}

func claudeRecordState(line phase3TranscriptLine) (string, string) {
	switch line.Type {
	case "assistant", "user", "system", "progress", "summary", "queue-operation", "file-history-snapshot":
		if line.Type == "assistant" && line.Message.ID == "" && line.RequestID == "" {
			return "unknown_record", "assistant record has no response or request ID; call granularity was not normalized"
		}
		return "observed", ""
	default:
		return "unknown_record", "record type is not recognized by this adapter version"
	}
}
