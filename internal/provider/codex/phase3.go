package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	TurnID         string `json:"turn_id"`
	RootTurnID     string `json:"root_turn_id"`
	Prompt         string `json:"prompt"`
	Model          string `json:"model"`
}

func DecodeHook(event string, raw []byte) (provideradapter.Hook, error) {
	var common phase3HookPayload
	if err := json.Unmarshal(raw, &common); err != nil {
		return provideradapter.Hook{}, fmt.Errorf("codex decode %s: %w", event, err)
	}
	if common.SessionID == "" || common.Cwd == "" {
		return provideradapter.Hook{}, errors.New("codex hook requires session_id and cwd")
	}
	if common.HookEventName != "" && common.HookEventName != event {
		return provideradapter.Hook{}, fmt.Errorf("codex hook event mismatch: command=%s payload=%s", event, common.HookEventName)
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return provideradapter.Hook{}, err
	}
	runID := common.RootTurnID
	if runID == "" {
		runID = common.TurnID
	}
	if runID == "" {
		runID = common.SessionID
	}
	return provideradapter.Hook{Event: event, SessionID: common.SessionID, RunID: runID, Cwd: common.Cwd, TranscriptPath: common.TranscriptPath, TurnID: common.TurnID, Model: common.Model, Prompt: common.Prompt, Raw: raw, Fields: fields}, nil
}

func NormalizeHook(projectID, receiptID string, receivedAt time.Time, hook provideradapter.Hook) (provideradapter.Batch, error) {
	return provideradapter.NormalizeHook(ProviderName, projectID, receiptID, receivedAt, hook)
}

type tokenUsage struct {
	InputTokens                int64           `json:"input_tokens"`
	CachedInputTokens          int64           `json:"cached_input_tokens"`
	CacheWriteInputTokens      int64           `json:"cache_write_input_tokens"`
	OutputTokens               int64           `json:"output_tokens"`
	ReasoningOutputTokens      int64           `json:"reasoning_output_tokens"`
	TotalTokens                int64           `json:"total_tokens"`
	InputTokensCamel           int64           `json:"inputTokens"`
	CachedInputTokensCamel     int64           `json:"cachedInputTokens"`
	CacheWriteInputTokensCamel int64           `json:"cacheWriteInputTokens"`
	OutputTokensCamel          int64           `json:"outputTokens"`
	ReasoningOutputTokensCamel int64           `json:"reasoningOutputTokens"`
	TotalTokensCamel           int64           `json:"totalTokens"`
	Present                    map[string]bool `json:"-"`
}

func (usage *tokenUsage) UnmarshalJSON(data []byte) error {
	type alias tokenUsage
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*usage = tokenUsage(decoded)
	usage.Present = map[string]bool{}
	aliases := map[string]string{
		"input_tokens": "input_tokens", "inputTokens": "input_tokens",
		"cached_input_tokens": "cached_input_tokens", "cachedInputTokens": "cached_input_tokens",
		"cache_write_input_tokens": "cache_write_input_tokens", "cacheWriteInputTokens": "cache_write_input_tokens",
		"output_tokens": "output_tokens", "outputTokens": "output_tokens",
		"reasoning_output_tokens": "reasoning_output_tokens", "reasoningOutputTokens": "reasoning_output_tokens",
		"total_tokens": "total_tokens", "totalTokens": "total_tokens",
	}
	for name := range raw {
		if canonical, ok := aliases[name]; ok {
			usage.Present[canonical] = true
		}
	}
	return nil
}

func (usage tokenUsage) canonical() tokenUsage {
	if usage.InputTokens == 0 {
		usage.InputTokens = usage.InputTokensCamel
	}
	if usage.CachedInputTokens == 0 {
		usage.CachedInputTokens = usage.CachedInputTokensCamel
	}
	if usage.CacheWriteInputTokens == 0 {
		usage.CacheWriteInputTokens = usage.CacheWriteInputTokensCamel
	}
	if usage.OutputTokens == 0 {
		usage.OutputTokens = usage.OutputTokensCamel
	}
	if usage.ReasoningOutputTokens == 0 {
		usage.ReasoningOutputTokens = usage.ReasoningOutputTokensCamel
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.TotalTokensCamel
	}
	if usage.Present == nil {
		usage.Present = map[string]bool{}
	}
	return usage
}

type codexRecord struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Method    string          `json:"method"`
	Payload   json.RawMessage `json:"payload"`
	Params    json.RawMessage `json:"params"`
	Item      json.RawMessage `json:"item"`
	Usage     tokenUsage      `json:"usage"`
	ThreadID  string          `json:"thread_id"`
}

type tokenRecordPayload struct {
	Type              string          `json:"type"`
	ResponseID        string          `json:"response_id"`
	RootTurnID        string          `json:"root_turn_id"`
	TurnID            string          `json:"turn_id"`
	ThreadID          string          `json:"thread_id"`
	State             string          `json:"state"`
	Model             string          `json:"model"`
	ApprovalPolicy    string          `json:"approval_policy"`
	Cwd               string          `json:"cwd"`
	SandboxPolicy     json.RawMessage `json:"sandbox_policy"`
	PermissionProfile json.RawMessage `json:"permission_profile"`
	Usage             tokenUsage      `json:"usage"`
	TurnTokenUsage    tokenUsage      `json:"turn_token_usage"`
	ThreadTokenUsage  tokenUsage      `json:"thread_token_usage"`
	Item              json.RawMessage `json:"item"`
	Changes           map[string]struct {
		Type        string  `json:"type"`
		UnifiedDiff string  `json:"unified_diff"`
		Content     string  `json:"content"`
		MovePath    *string `json:"move_path"`
	} `json:"changes"`
	CallID      string `json:"call_id"`
	Status      string `json:"status"`
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	Success     *bool  `json:"success"`
	Reason      string `json:"reason"`
	CompletedAt string `json:"completed_at"`
}

type codexOperationContext struct {
	Cwd               string
	Model             string
	ApprovalPolicy    string
	SandboxPolicy     json.RawMessage
	PermissionProfile json.RawMessage
}

type commandItem struct {
	Type             string            `json:"type"`
	ID               string            `json:"id"`
	ProcessID        string            `json:"process_id"`
	Command          []string          `json:"command"`
	Cwd              string            `json:"cwd"`
	ParsedCommand    []json.RawMessage `json:"parsed_cmd"`
	Source           string            `json:"source"`
	Status           string            `json:"status"`
	Stdout           string            `json:"stdout"`
	Stderr           string            `json:"stderr"`
	AggregatedOutput string            `json:"aggregated_output"`
	FormattedOutput  string            `json:"formatted_output"`
	ExitCode         *int              `json:"exit_code"`
	Duration         struct {
		Seconds int64 `json:"secs"`
		Nanos   int64 `json:"nanos"`
	} `json:"duration"`
	Text    string `json:"text"`
	Changes map[string]struct {
		Type        string  `json:"type"`
		UnifiedDiff string  `json:"unified_diff"`
		Content     string  `json:"content"`
		MovePath    *string `json:"move_path"`
	} `json:"changes"`
}

type appServerParams struct {
	ThreadID   string     `json:"threadId"`
	TurnID     string     `json:"turnId"`
	ResponseID string     `json:"responseId"`
	Usage      tokenUsage `json:"usage"`
	TokenUsage struct {
		Last  tokenUsage `json:"last"`
		Total tokenUsage `json:"total"`
	} `json:"tokenUsage"`
	Turn struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"turn"`
}

func NormalizeTranscript(projectID, receiptID string, receivedAt time.Time, hook provideradapter.Hook, chunk provideradapter.Transcript) (provideradapter.Batch, error) {
	var batch provideradapter.Batch
	scope := provideradapter.Scope(ProviderName, projectID, hook.SessionID, hook.RunID)
	if chunk.Unavailable != "" {
		return codexTranscriptDiagnostic(batch, scope, receiptID, receivedAt, chunk, "unavailable", chunk.Unavailable)
	}
	if chunk.Rotated {
		var err error
		batch, err = codexTranscriptDiagnostic(batch, scope, receiptID, receivedAt, chunk, "rotated", "transcript generation changed or file shrank")
		if err != nil {
			return batch, err
		}
	}
	contexts := map[string]codexOperationContext{}
	seenResponses := make(map[string]bool, len(chunk.KnownIDs))
	for id := range chunk.KnownIDs {
		seenResponses[id] = true
	}
	// Public CLI turn.completed records may appear before or after the richer
	// token_usage_record in a collected chunk. Discover response deltas first
	// so line ordering cannot cause an overlapping cumulative fallback to be
	// emitted for the same run.
	for _, sourceLine := range chunk.Lines {
		var record codexRecord
		if json.Unmarshal(sourceLine.Data, &record) != nil || record.Type != "token_usage_record" {
			continue
		}
		var payload tokenRecordPayload
		if json.Unmarshal(record.Payload, &payload) != nil || !hasUsage(payload.Usage) {
			continue
		}
		turnID := firstNonEmptyCodex(payload.RootTurnID, payload.TurnID, hook.RunID)
		if turnID != "" {
			key := "delta-run:" + turnID
			if !seenResponses[key] {
				seenResponses[key] = true
				batch.SeenIDs = append(batch.SeenIDs, key)
			}
		}
	}
	for _, sourceLine := range chunk.Lines {
		ref := batch.AddContent(sourceLine.Data, model.ContentRaw, "application/json", "codex-transcript-line-v1")
		var record codexRecord
		if err := json.Unmarshal(sourceLine.Data, &record); err != nil {
			if err := addCodexRawLine(&batch, scope, receivedAt, chunk, sourceLine, ref, "unknown_record", "invalid JSON: "+err.Error()); err != nil {
				return batch, err
			}
			continue
		}
		at := codexTime(record.Timestamp, receivedAt)
		lineRunID := codexRecordRunID(record)
		lineScope := provideradapter.ScopeWithRun(scope, ProviderName, lineRunID)
		recognized, err := normalizeCodexRecord(&batch, lineScope, hook, chunk, sourceLine, record, at, contexts, seenResponses)
		if err != nil {
			return batch, err
		}
		state, reason := "observed", ""
		if !recognized {
			state, reason = "unknown_record", "record shape is not recognized by this adapter version"
		}
		rawIndex := len(batch.Events)
		if err := addCodexRawLine(&batch, lineScope, at, chunk, sourceLine, ref, state, reason); err != nil {
			return batch, err
		}
		if lineRunID != "" && hook.RunID != "" && lineRunID != hook.RunID {
			batch.Events[rawIndex].Availability = append(batch.Events[rawIndex].Availability, model.FieldAvailability{Path: "/scope/run", Status: model.AvailabilityConflict, Reason: "transcript turn ID differs from the Stop hook run; record was routed by transcript turn ID"})
			if err := batch.Events[rawIndex].Validate(); err != nil {
				return batch, err
			}
		}
	}
	if chunk.IncompleteTail {
		var err error
		batch, err = codexTranscriptDiagnostic(batch, scope, receiptID, receivedAt, chunk, "partial_tail", "last transcript line is incomplete")
		if err != nil {
			return batch, err
		}
	}
	return batch, nil
}

func normalizeCodexRecord(batch *provideradapter.Batch, scope []model.EntityRef, hook provideradapter.Hook, chunk provideradapter.Transcript, source provideradapter.Line, record codexRecord, at time.Time, contexts map[string]codexOperationContext, seen map[string]bool) (bool, error) {
	var payload tokenRecordPayload
	_ = json.Unmarshal(record.Payload, &payload)
	if record.Method != "" {
		return normalizeAppServerRecord(batch, scope, hook, chunk, source, record, at, seen)
	}
	switch record.Type {
	case "session_meta":
		return true, nil
	case "turn_context":
		turnID := firstNonEmptyCodex(payload.RootTurnID, payload.TurnID)
		contexts[turnID] = codexOperationContext{Cwd: payload.Cwd, Model: payload.Model, ApprovalPolicy: payload.ApprovalPolicy, SandboxPolicy: payload.SandboxPolicy, PermissionProfile: payload.PermissionProfile}
		return true, nil
	case "token_usage_record":
		return true, normalizeCodexTokenRecord(batch, scope, hook, chunk, source, payload, at, contexts, seen)
	case "event_msg":
		if payload.Type == "item_completed" {
			return true, normalizeCommandItem(batch, scope, at, payload.Item, chunk.Generation, contexts[firstNonEmptyCodex(payload.RootTurnID, payload.TurnID)], seen)
		}
		if payload.Type == "patch_apply_end" {
			return true, normalizePatch(batch, scope, at, payload, chunk.Generation, contexts[firstNonEmptyCodex(payload.RootTurnID, payload.TurnID)], seen)
		}
		return false, nil
	case "thread.started", "turn.started":
		return true, nil
	case "item.completed":
		return true, normalizePublicItem(batch, scope, at, record.Item, chunk.Generation, seen)
	case "turn.completed":
		return true, addCodFinalUsage(batch, scope, hook, record.Usage, chunk.Path, chunk.Generation, source.Number, at, seen)
	case "compacted":
		return true, normalizeCompaction(batch, scope, at, record.Payload, chunk.Generation, source.Number)
	default:
		return false, nil
	}
}

func normalizeCodexTokenRecord(batch *provideradapter.Batch, scope []model.EntityRef, hook provideradapter.Hook, chunk provideradapter.Transcript, source provideradapter.Line, payload tokenRecordPayload, at time.Time, contexts map[string]codexOperationContext, seen map[string]bool) error {
	turnID := firstNonEmptyCodex(payload.RootTurnID, payload.TurnID)
	lineHook := hook
	if turnID != "" {
		lineHook.TurnID, lineHook.RunID = turnID, turnID
	}
	responseID := firstNonEmptyCodex(payload.ResponseID, fmt.Sprintf("line-%d", source.Number))
	callKey := "response-call:" + responseID
	if !seen[callKey] {
		if err := addCodexCall(batch, scope, lineHook, responseID, firstNonEmptyCodex(payload.Model, contexts[turnID].Model, hook.Model), at); err != nil {
			return err
		}
		seen[callKey] = true
		batch.SeenIDs = append(batch.SeenIDs, callKey)
	}
	usageKey := "response-usage:" + responseID
	if hasUsage(payload.Usage) && !seen[usageKey] {
		if err := addCodexUsage(batch, scope, responseID, responseID, model.EntityModelCall, payload.Usage, model.CounterDelta, model.AdoptionAdopted, chunk.Generation, at); err != nil {
			return err
		}
		if hasUsage(payload.TurnTokenUsage) {
			if err := addCodexUsage(batch, scope, responseID+"-turn", firstNonEmptyCodex(turnID, hook.RunID), model.EntityRun, payload.TurnTokenUsage, model.CounterCumulative, model.AdoptionCandidate, chunk.Generation, at); err != nil {
				return err
			}
		}
		if hasUsage(payload.ThreadTokenUsage) {
			if err := addCodexUsage(batch, scope, responseID+"-thread", firstNonEmptyCodex(payload.ThreadID, hook.SessionID), model.EntityThread, payload.ThreadTokenUsage, model.CounterCumulative, model.AdoptionCandidate, chunk.Generation, at); err != nil {
				return err
			}
		}
		seen[usageKey] = true
		batch.SeenIDs = append(batch.SeenIDs, usageKey)
		deltaKey := "delta-run:" + firstNonEmptyCodex(turnID, hook.RunID)
		seen[deltaKey] = true
		batch.SeenIDs = append(batch.SeenIDs, deltaKey)
	}
	return nil
}

func codexRecordRunID(record codexRecord) string {
	var payload tokenRecordPayload
	_ = json.Unmarshal(record.Payload, &payload)
	runID := firstNonEmptyCodex(payload.RootTurnID, payload.TurnID)
	if runID == "" && record.Method != "" {
		var params appServerParams
		_ = json.Unmarshal(record.Params, &params)
		runID = firstNonEmptyCodex(params.Turn.ID, params.TurnID)
	}
	return runID
}

func addCodexCall(batch *provideradapter.Batch, scope []model.EntityRef, hook provideradapter.Hook, responseID, actualModel string, at time.Time) error {
	response := provideradapter.EntityRef(model.EntityResponse, "codex/response", responseID)
	payload := model.ModelCallPayload{
		Call:        provideradapter.EntityRef(model.EntityModelCall, "codex/response", responseID),
		Instruction: provideradapter.EntityRef(model.EntityInstruction, "codex/instruction", firstNonEmptyCodex(hook.TurnID, hook.RunID)),
		Actor:       provideradapter.EntityRef(model.EntityAgent, "codex/agent", "main"), ActualModel: actualModel, State: "completed", Response: &response,
	}
	event, err := provideradapter.NewEvent(ProviderName, "transcript", at, &at, provideradapter.ID("codex/model-call", responseID), model.EventModelCall, scope, payload)
	if err == nil {
		if actualModel == "" {
			event.Availability = append(event.Availability, model.FieldAvailability{Path: "/payload/actualModel", Status: model.AvailabilityUnknown, Reason: "Codex record and hook did not include a model"})
		}
		batch.Events = append(batch.Events, event)
	}
	return err
}

// CanonicalInstructionRef recognizes the instruction namespace emitted by older
// transcript collectors. Run entities also use codex/turn and must remain intact.
// This conversion is for derived views; original journal evidence is unchanged.
func CanonicalInstructionRef(ref model.EntityRef) model.EntityRef {
	if ref.Kind == model.EntityInstruction && ref.ID.Namespace == "codex/turn" {
		ref.ID.Namespace = "codex/instruction"
	}
	return ref
}

func addCodexUsage(batch *provideradapter.Batch, scope []model.EntityRef, usageID, targetID string, targetKind model.EntityKind, rawUsage tokenUsage, counter model.CounterKind, adoption model.AdoptionStatus, generation string, at time.Time) error {
	usage := rawUsage.canonical()
	namespace := "codex/turn"
	if targetKind == model.EntityModelCall {
		namespace = "codex/response"
	}
	if targetKind == model.EntityThread {
		namespace = "codex/thread"
	}
	raw := usageFields(usage)
	uncached := usage.InputTokens - usage.CachedInputTokens - usage.CacheWriteInputTokens
	var normalized *model.NormalizedUsage
	normalizationReason := ""
	normalizationConflict := false
	required := []string{"input_tokens", "cached_input_tokens", "cache_write_input_tokens", "output_tokens"}
	for _, name := range required {
		if !usage.Present[name] {
			normalizationReason = "required exclusive usage component was not reported"
			break
		}
	}
	if usage.Present["total_tokens"] && usage.TotalTokens != usage.InputTokens+usage.OutputTokens {
		normalizationReason = "provider total_tokens conflicts with input_tokens plus output_tokens"
		normalizationConflict = true
	}
	if normalizationReason == "" && uncached >= 0 {
		total := usage.TotalTokens
		if !usage.Present["total_tokens"] {
			total = usage.InputTokens + usage.OutputTokens
		}
		subsets := []model.UsageSubset{}
		if usage.Present["reasoning_output_tokens"] {
			subsets = append(subsets, model.UsageSubset{Name: "reasoning_output", Of: "output", Tokens: provideradapter.ObservedQuantity(usage.ReasoningOutputTokens)})
		}
		normalized = &model.NormalizedUsage{
			ExclusiveBuckets: []model.UsageBucket{
				{Name: "input_uncached", Tokens: provideradapter.ObservedQuantity(uncached)},
				{Name: "cache_read_input", Tokens: provideradapter.ObservedQuantity(usage.CachedInputTokens)},
				{Name: "cache_write_input", Tokens: provideradapter.ObservedQuantity(usage.CacheWriteInputTokens)},
				{Name: "output", Tokens: provideradapter.ObservedQuantity(usage.OutputTokens)},
			},
			Subsets: subsets,
			Totals:  []model.UsageBucket{{Name: "all", Tokens: provideradapter.ObservedQuantity(total)}},
		}
	}
	if normalizationReason == "" && uncached < 0 {
		normalizationReason = "cached input components exceed provider input total"
		normalizationConflict = true
	}
	counterValue := model.CounterSemantics{Kind: counter}
	if counter == model.CounterCumulative {
		counterValue.Generation = generation
	}
	payload := model.UsagePayload{Usage: provideradapter.EntityRef(model.EntityUsage, "codex/usage", usageID), AppliesTo: provideradapter.EntityRef(targetKind, namespace, targetID), Raw: model.RawUsage{Unit: "tokens", Fields: raw}, Normalized: normalized, Counter: counterValue, State: model.MeasurementFinal}
	event, err := provideradapter.NewEvent(ProviderName, "transcript", at, &at, provideradapter.ID("codex/usage", usageID), model.EventUsageReported, scope, payload)
	if err != nil {
		return err
	}
	event.Adoption = &model.Adoption{Status: adoption}
	if adoption == model.AdoptionAdopted {
		event.Adoption.RuleVersion = "codex-response-usage-v1"
		event.Adoption.Reason = "provider response usage is the non-cumulative measurement"
	} else if adoption == model.AdoptionCandidate {
		event.Adoption.RuleVersion = "codex-cumulative-fallback-v1"
		event.Adoption.Reason = "measurement is retained as a fallback until a non-cumulative response usage is available"
	}
	if normalized == nil {
		status := model.AvailabilityUnavailable
		if normalizationConflict {
			status = model.AvailabilityConflict
		}
		event.Availability = append(event.Availability, model.FieldAvailability{Path: "/payload/normalized", Status: status, Reason: normalizationReason})
	}
	if err := event.Validate(); err != nil {
		return err
	}
	batch.Events = append(batch.Events, event)
	return nil
}

func usageFields(usage tokenUsage) map[string]json.RawMessage {
	result := map[string]json.RawMessage{}
	values := map[string]int64{"input_tokens": usage.InputTokens, "cached_input_tokens": usage.CachedInputTokens, "cache_write_input_tokens": usage.CacheWriteInputTokens, "output_tokens": usage.OutputTokens, "reasoning_output_tokens": usage.ReasoningOutputTokens, "total_tokens": usage.TotalTokens}
	for name, value := range values {
		if usage.Present[name] {
			result[name] = json.RawMessage(fmt.Sprintf("%d", value))
		}
	}
	return result
}

func hasUsage(usage tokenUsage) bool {
	usage = usage.canonical()
	return len(usage.Present) > 0
}

func normalizeCommandItem(batch *provideradapter.Batch, scope []model.EntityRef, at time.Time, raw json.RawMessage, generation string, context codexOperationContext, seen map[string]bool) error {
	var item commandItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return err
	}
	if item.Type == "FileChange" {
		payload := tokenRecordPayload{CallID: firstNonEmptyCodex(item.ID, item.ProcessID), Changes: item.Changes, Status: item.Status}
		return normalizePatch(batch, scope, at, payload, generation, context, seen)
	}
	if item.Type != "CommandExecution" {
		return nil
	}
	operationID := firstNonEmptyCodex(item.ID, item.ProcessID)
	if operationID == "" {
		operationID = string(provideradapter.ID("codex/command", generation, string(raw)).Value)
	}
	seenKey := "operation:" + operationID
	if seen[seenKey] {
		return nil
	}
	environment := codexEnvironment(context)
	if item.Cwd != "" {
		environment["cwd"] = item.Cwd
	}
	input, _ := json.Marshal(map[string]any{"command": item.Command, "cwd": item.Cwd, "parsedCommand": item.ParsedCommand, "source": item.Source, "environment": environment})
	inputRef := batch.AddContent(input, model.ContentRaw, "application/json", "codex-command-input-v1")
	request := model.OperationPayload{Operation: provideradapter.EntityRef(model.EntityOperation, "codex/operation", operationID), Actor: provideradapter.EntityRef(model.EntityAgent, "codex/agent", "main"), Kind: "shell", Name: "CommandExecution", Input: []model.ContentRef{inputRef}}
	requestAt := at
	if duration := item.DurationMilli(); duration != nil {
		requestAt = at.Add(-time.Duration(*duration) * time.Millisecond)
	}
	event, err := provideradapter.NewEvent(ProviderName, "transcript", at, &requestAt, provideradapter.ID("codex/operation-request", operationID), model.EventOperationRequested, scope, request)
	if err != nil {
		return err
	}
	batch.Events = append(batch.Events, event)
	output, _ := json.Marshal(map[string]string{"stdout": item.Stdout, "stderr": item.Stderr, "aggregatedOutput": item.AggregatedOutput, "formattedOutput": item.FormattedOutput})
	outputRef := batch.AddContent(output, model.ContentRaw, "application/json", "codex-command-output-v1")
	duration := item.DurationMilli()
	result := model.OperationResultPayload{Operation: request.Operation, Status: firstNonEmptyCodex(item.Status, "completed"), ExitCode: item.ExitCode, Output: []model.ContentRef{outputRef}, DurationMillis: duration}
	event, err = provideradapter.NewEvent(ProviderName, "transcript", at, &at, provideradapter.ID("codex/operation-result", operationID), model.EventOperationResult, scope, result)
	if err != nil {
		return err
	}
	batch.Events = append(batch.Events, event)
	batch.SeenIDs = append(batch.SeenIDs, seenKey)
	seen[seenKey] = true
	return nil
}

func (item commandItem) DurationMilli() *int64 {
	value := item.Duration.Seconds*1000 + item.Duration.Nanos/1_000_000
	if value == 0 {
		return nil
	}
	return &value
}

func normalizePatch(batch *provideradapter.Batch, scope []model.EntityRef, at time.Time, payload tokenRecordPayload, generation string, context codexOperationContext, seen map[string]bool) error {
	paths := make([]string, 0, len(payload.Changes))
	for path := range payload.Changes {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	operationID := firstNonEmptyCodex(payload.CallID, string(provideradapter.ID("codex/patch-operation", generation, strings.Join(paths, "\x00")).Value))
	operation := provideradapter.EntityRef(model.EntityOperation, "codex/operation", operationID)
	operationKey := "patch-operation:" + operationID
	if !seen[operationKey] {
		input, _ := json.Marshal(map[string]any{"paths": paths, "environment": codexEnvironment(context)})
		inputRef := batch.AddContent(input, model.ContentRaw, "application/json", "codex-edit-request-v1")
		request := model.OperationPayload{Operation: operation, Actor: provideradapter.EntityRef(model.EntityAgent, "codex/agent", "main"), Kind: "edit", Name: "apply_patch", Input: []model.ContentRef{inputRef}}
		event, err := provideradapter.NewEvent(ProviderName, "transcript", at, &at, provideradapter.ID("codex/operation-request", operationID), model.EventOperationRequested, scope, request)
		if err != nil {
			return err
		}
		batch.Events = append(batch.Events, event)
		status := firstNonEmptyCodex(payload.Status, "completed")
		if payload.Success != nil && !*payload.Success && status == "completed" {
			status = "failed"
		}
		result := model.OperationResultPayload{Operation: operation, Status: status}
		event, err = provideradapter.NewEvent(ProviderName, "transcript", at, &at, provideradapter.ID("codex/operation-result", operationID), model.EventOperationResult, scope, result)
		if err != nil {
			return err
		}
		batch.Events = append(batch.Events, event)
		batch.SeenIDs = append(batch.SeenIDs, operationKey)
		seen[operationKey] = true
	}
	for _, path := range paths {
		change := payload.Changes[path]
		seenKey := "file-change:" + payload.CallID + ":" + path
		if seen[seenKey] {
			continue
		}
		locator := batch.AddContent([]byte(path), model.ContentRaw, "text/plain; charset=utf-8", "codex-path-v1")
		contentText := change.UnifiedDiff
		mediaType := "text/x-diff; charset=utf-8"
		if contentText == "" {
			contentText = change.Content
			mediaType = "text/plain; charset=utf-8"
		}
		var content *model.ContentRef
		if contentText != "" {
			ref := batch.AddContent([]byte(contentText), model.ContentRaw, mediaType, "codex-file-change-v1")
			content = &ref
		}
		artifact := model.ArtifactPayload{Artifact: provideradapter.EntityRef(model.EntityArtifact, "codex/file", path), Kind: "file", Locator: &locator, Content: content, Change: firstNonEmptyCodex(change.Type, "modified")}
		event, err := provideradapter.NewEvent(ProviderName, "transcript", at, &at, provideradapter.ID("codex/file-change", generation, payload.CallID, path), model.EventArtifactObserved, scope, artifact)
		if err != nil {
			return err
		}
		event.Relations = append(event.Relations, model.Relation{Kind: model.RelationResultOf, From: artifact.Artifact, To: operation, Certainty: model.CertaintyReported, Reason: "provider file-change record carries the originating tool call ID"})
		if err := event.Validate(); err != nil {
			return err
		}
		batch.Events = append(batch.Events, event)
		batch.SeenIDs = append(batch.SeenIDs, seenKey)
		seen[seenKey] = true
	}
	return nil
}

func codexEnvironment(context codexOperationContext) map[string]any {
	result := map[string]any{"cwd": context.Cwd, "approvalPolicy": context.ApprovalPolicy}
	if len(context.SandboxPolicy) > 0 {
		var sandbox any
		_ = json.Unmarshal(context.SandboxPolicy, &sandbox)
		result["sandboxPolicy"] = sandbox
		if object, ok := sandbox.(map[string]any); ok {
			if network, exists := object["network_access"]; exists {
				result["networkAccess"] = network
			}
		}
	}
	if len(context.PermissionProfile) > 0 {
		var profile any
		_ = json.Unmarshal(context.PermissionProfile, &profile)
		result["permissionProfile"] = profile
	}
	return result
}

func addCodexRawLine(batch *provideradapter.Batch, scope []model.EntityRef, at time.Time, chunk provideradapter.Transcript, line provideradapter.Line, ref model.ContentRef, state, reason string) error {
	position := line.Number
	payload := model.CollectionStatePayload{
		Stream:           provideradapter.EntityRef(model.EntityStream, "codex/transcript", chunk.Path),
		State:            state,
		Cursor:           fmt.Sprintf("%d", line.ByteOffset),
		Generation:       chunk.Generation,
		LastGoodPosition: &position,
		Evidence:         []model.ContentRef{ref},
	}
	if reason != "" {
		payload.Coverage = []model.FieldAvailability{{Path: "/transcript/record", Status: model.AvailabilityUnknown, Reason: reason}}
	}
	event, err := provideradapter.NewEvent(ProviderName, "transcript", at, &at, provideradapter.ID("codex/transcript-row", chunk.Generation, fmt.Sprint(line.Number), string(line.Data)), model.EventCollectionState, scope, payload)
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

func codexTranscriptDiagnostic(batch provideradapter.Batch, scope []model.EntityRef, receiptID string, at time.Time, chunk provideradapter.Transcript, state, reason string) (provideradapter.Batch, error) {
	generation := chunk.Generation
	if generation == "" {
		generation = "unavailable"
	}
	path := chunk.Path
	if path == "" {
		path = "unknown"
	}
	payload := model.CollectionStatePayload{
		Stream:     provideradapter.EntityRef(model.EntityStream, "codex/transcript", path),
		State:      state,
		Cursor:     fmt.Sprint(chunk.StartByteOffset),
		Generation: generation,
		Coverage:   []model.FieldAvailability{{Path: "/transcript", Status: model.AvailabilityUnknown, Reason: reason}},
	}
	event, err := provideradapter.NewEvent(ProviderName, "transcript", at, nil, provideradapter.ID("codex/transcript-state", receiptID, state), model.EventCollectionState, scope, payload)
	if err == nil {
		batch.Events = append(batch.Events, event)
	}
	return batch, err
}

func codexTime(value string, fallback time.Time) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err == nil {
		return parsed
	}
	return fallback
}

func firstNonEmptyCodex(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func normalizePublicItem(batch *provideradapter.Batch, scope []model.EntityRef, at time.Time, raw json.RawMessage, generation string, seen map[string]bool) error {
	var item commandItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return err
	}
	if item.Type != "agent_message" {
		return nil
	}
	seenKey := "item:" + item.ID
	if seen[seenKey] {
		return nil
	}
	ref := batch.AddContent([]byte(item.Text), model.ContentRaw, "text/plain; charset=utf-8", "codex-agent-message-v1")
	payload := model.ArtifactPayload{Artifact: provideradapter.EntityRef(model.EntityArtifact, "codex/item", firstNonEmptyCodex(item.ID, ref.ID.Value)), Kind: "assistant_message", Content: &ref, Change: "created"}
	event, err := provideradapter.NewEvent(ProviderName, "cli-json", at, nil, provideradapter.ID("codex/item", generation, item.ID), model.EventArtifactObserved, scope, payload)
	if err == nil {
		batch.Events = append(batch.Events, event)
		batch.SeenIDs = append(batch.SeenIDs, seenKey)
		seen[seenKey] = true
	}
	return err
}

func addCodFinalUsage(batch *provideradapter.Batch, scope []model.EntityRef, hook provideradapter.Hook, usage tokenUsage, transcriptPath, generation string, line uint64, at time.Time, seen map[string]bool) error {
	if !hasUsage(usage) {
		return nil
	}
	if seen["delta-run:"+hook.RunID] {
		return nil
	}
	if generation == "" {
		generation = "cli-json"
	}
	keyData, _ := json.Marshal(usage)
	key := provideradapter.ID("codex/cli-turn-usage", hook.SessionID, generation, string(keyData)).Value
	seenKey := "cli-turn-usage:" + key
	if seen[seenKey] {
		return nil
	}
	if err := addCodexUsage(batch, withoutScopeKind(scope, model.EntityRun), key, hook.SessionID, model.EntityThread, usage, model.CounterCumulative, model.AdoptionCandidate, generation, at); err != nil {
		return err
	}
	batch.Events[len(batch.Events)-1].Sequence = &model.EventSequence{Stream: provideradapter.ID("codex/transcript", transcriptPath), Generation: generation, Position: &line}
	batch.SeenIDs = append(batch.SeenIDs, seenKey)
	seen[seenKey] = true
	return nil
}

func withoutScopeKind(scope []model.EntityRef, kind model.EntityKind) []model.EntityRef {
	result := make([]model.EntityRef, 0, len(scope))
	for _, ref := range scope {
		if ref.Kind != kind {
			result = append(result, ref)
		}
	}
	return result
}

func normalizeCompaction(batch *provideradapter.Batch, scope []model.EntityRef, at time.Time, raw json.RawMessage, generation string, line uint64) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	ref := batch.AddContent(raw, model.ContentSummary, "application/json", "codex-compaction-v1")
	payload := model.ArtifactPayload{Artifact: provideradapter.EntityRef(model.EntityArtifact, "codex/compaction", fmt.Sprintf("%s-%d", generation, line)), Kind: "context_summary", Content: &ref, Change: "created"}
	event, err := provideradapter.NewEvent(ProviderName, "transcript", at, &at, provideradapter.ID("codex/compaction", generation, fmt.Sprint(line)), model.EventArtifactObserved, scope, payload)
	if err == nil {
		batch.Events = append(batch.Events, event)
	}
	return err
}

func normalizeAppServerRecord(batch *provideradapter.Batch, scope []model.EntityRef, hook provideradapter.Hook, chunk provideradapter.Transcript, source provideradapter.Line, record codexRecord, at time.Time, seen map[string]bool) (bool, error) {
	var params appServerParams
	if err := json.Unmarshal(record.Params, &params); err != nil {
		return false, nil
	}
	switch record.Method {
	case "turn/started", "turn/completed":
		state := params.Turn.Status
		if state == "" {
			state = strings.TrimPrefix(record.Method, "turn/")
		}
		payload := model.StateTransitionPayload{Subject: provideradapter.EntityRef(model.EntityRun, "codex/turn", firstNonEmptyCodex(params.Turn.ID, params.TurnID, hook.RunID)), To: state}
		event, err := provideradapter.NewEvent(ProviderName, "app-server", at, nil, provideradapter.ID("codex/app-turn", chunk.Generation, fmt.Sprint(source.Number), record.Method), model.EventStateChanged, scope, payload)
		if err == nil {
			batch.Events = append(batch.Events, event)
		}
		return true, err
	case "thread/tokenUsage/updated":
		id := fmt.Sprintf("%s-%d", params.TurnID, source.Number)
		err := addCodexUsage(batch, scope, id+"-last", params.TurnID, model.EntityRun, params.TokenUsage.Last, model.CounterSnapshot, model.AdoptionCandidate, chunk.Generation, at)
		if err == nil {
			err = addCodexUsage(batch, scope, id+"-total", params.ThreadID, model.EntityThread, params.TokenUsage.Total, model.CounterCumulative, model.AdoptionCandidate, chunk.Generation, at)
		}
		return true, err
	case "rawResponse/completed":
		responseID := firstNonEmptyCodex(params.ResponseID, fmt.Sprintf("line-%d", source.Number))
		callKey := "response-call:" + responseID
		if !seen[callKey] {
			if err := addCodexCall(batch, scope, hook, responseID, "", at); err != nil {
				return true, err
			}
			batch.SeenIDs = append(batch.SeenIDs, callKey)
			seen[callKey] = true
		}
		usageKey := "response-usage:" + responseID
		if hasUsage(params.Usage) && !seen[usageKey] {
			if err := addCodexUsage(batch, scope, responseID, responseID, model.EntityModelCall, params.Usage, model.CounterDelta, model.AdoptionAdopted, chunk.Generation, at); err != nil {
				return true, err
			}
			batch.SeenIDs = append(batch.SeenIDs, usageKey)
			seen[usageKey] = true
		}
		return true, nil
	default:
		return false, nil
	}
}
