package adapter

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"maatplug/internal/model"
)

var exitCodePattern = regexp.MustCompile(`(?i)^exit code\s+(-?\d+)`)

// NormalizeHook maps hook fields common to both providers. Provider-specific
// decoders decide which identifiers are available and preserve all unknown
// fields in Hook.Fields and the raw inbox evidence.
func NormalizeHook(provider, projectID, receiptID string, receivedAt time.Time, hook Hook) (Batch, error) {
	var batch Batch
	scope := Scope(provider, projectID, hook.SessionID, hook.RunID)
	stream := EntityRef(model.EntityStream, provider+"/hooks", hook.SessionID)
	makeEvent := func(suffix string, kind model.EventKind, payload any) error {
		event, err := Event(provider, "hook/"+hook.Event, receivedAt, ID("maatplug/hook-event", receiptID, suffix), kind, scope, payload)
		if err != nil {
			return err
		}
		batch.Events = append(batch.Events, event)
		return nil
	}

	switch hook.Event {
	case "UserPromptSubmit":
		if hook.Prompt == "" {
			return batch, errorsNew("prompt is required")
		}
		ref := batch.AddContent([]byte(hook.Prompt), model.ContentRaw, "text/plain; charset=utf-8", provider+"-prompt-v1")
		instructionID := first(hook.PromptID, hook.TurnID, receiptID)
		payload := model.InstructionPayload{
			Instruction: EntityRef(model.EntityInstruction, provider+"/instruction", instructionID),
			Actor:       EntityRef(model.EntityActor, "maatplug/actor", "user"), Content: []model.ContentRef{ref}, State: "submitted",
		}
		return batch, makeEvent("instruction", model.EventInstructionRecorded, payload)
	case "PreToolUse":
		toolID := fieldString(hook.Fields, "tool_use_id")
		if toolID == "" {
			toolID = receiptID
		}
		toolName := fieldString(hook.Fields, "tool_name")
		if toolName == "" {
			toolName = "unknown"
		}
		input := hook.Fields["tool_input"]
		var refs []model.ContentRef
		wrappedInput := any(nil)
		if len(input) > 0 && string(input) != "null" {
			wrappedInput = json.RawMessage(input)
		}
		wrapped, _ := json.Marshal(map[string]any{
			"toolInput": wrappedInput,
			"environment": map[string]any{
				"cwd":            hook.Cwd,
				"permissionMode": first(fieldString(hook.Fields, "permission_mode"), fieldString(hook.Fields, "permissionMode"), fieldString(hook.Fields, "approval_policy")),
				"sandbox":        rawField(hook.Fields, "sandbox_policy"),
			},
		})
		refs = append(refs, batch.AddContent(wrapped, model.ContentRaw, "application/json", provider+"-operation-request-v1"))
		payload := model.OperationPayload{Operation: EntityRef(model.EntityOperation, provider+"/tool", toolID), Actor: hookActor(provider, hook), Kind: toolKind(toolName), Name: toolName, Input: refs}
		return batch, makeEvent("operation-requested", model.EventOperationRequested, payload)
	case "PostToolUse", "PostToolUseFailure":
		toolID := fieldString(hook.Fields, "tool_use_id")
		if toolID == "" {
			toolID = receiptID
		}
		status := "completed"
		output := hook.Fields["tool_response"]
		if hook.Event == "PostToolUseFailure" {
			status = "failed"
			output = hook.Fields["error"]
		}
		var refs []model.ContentRef
		if len(output) > 0 && string(output) != "null" {
			refs = append(refs, batch.AddContent(output, model.ContentRaw, mediaType(output), provider+"-tool-output-v1"))
		}
		exitCode, reportedStatus, nestedDuration := resultMetadata(output)
		if hook.Event == "PostToolUse" {
			if reportedStatus != "" {
				status = reportedStatus
			}
			if exitCode != nil && *exitCode != 0 {
				status = "failed"
			}
		}
		duration := fieldInt64Pointer(hook.Fields, "duration_ms")
		if duration == nil {
			duration = nestedDuration
		}
		payload := model.OperationResultPayload{Operation: EntityRef(model.EntityOperation, provider+"/tool", toolID), Status: status, ExitCode: exitCode, Output: refs, DurationMillis: duration}
		event, err := Event(provider, "hook/"+hook.Event, receivedAt, ID("maatplug/hook-event", receiptID, "operation-result"), model.EventOperationResult, scope, payload)
		if err != nil {
			return batch, err
		}
		if status == "failed" {
			if code, ok := parseExitCode(fieldString(hook.Fields, "error")); ok {
				payload.ExitCode = &code
				encoded, _ := json.Marshal(payload)
				event.Payload = encoded
				event.Availability = append(event.Availability, model.FieldAvailability{Path: "/payload/exitCode", Status: model.AvailabilityInferred, Reason: "parsed from provider error prefix"})
			}
		}
		batch.Events = append(batch.Events, event)
		return batch, event.Validate()
	case "PermissionRequest", "PermissionDenied":
		decision := "requested"
		if hook.Event == "PermissionDenied" {
			decision = "denied"
		}
		toolID := fieldString(hook.Fields, "tool_use_id")
		payload := model.ApprovalPayload{Approval: EntityRef(model.EntityApproval, provider+"/approval", receiptID), Decision: decision}
		if toolID != "" {
			operation := EntityRef(model.EntityOperation, provider+"/tool", toolID)
			payload.Operation = &operation
		}
		event, err := Event(provider, "hook/"+hook.Event, receivedAt, ID("maatplug/hook-event", receiptID, "approval"), model.EventApprovalResolved, scope, payload)
		if err != nil {
			return batch, err
		}
		if toolID == "" {
			event.Availability = append(event.Availability, model.FieldAvailability{Path: "/payload/operation", Status: model.AvailabilityUnknown, Reason: "provider hook did not supply a tool use ID"})
		}
		batch.Events = append(batch.Events, event)
		return batch, event.Validate()
	case "SubagentStart":
		agentID := fieldString(hook.Fields, "agent_id")
		if agentID == "" {
			agentID = receiptID
		}
		child := EntityRef(model.EntityAgent, provider+"/agent", agentID)
		parent := EntityRef(model.EntityAgent, provider+"/agent", "main")
		payload := model.ActorPayload{Actor: child, Kind: first(fieldString(hook.Fields, "agent_type"), "subagent"), Parent: &parent}
		event, err := Event(provider, "hook/SubagentStart", receivedAt, ID("maatplug/hook-event", receiptID, "actor"), model.EventActorObserved, scope, payload)
		if err != nil {
			return batch, err
		}
		event.Relations = []model.Relation{{Kind: model.RelationParentOf, From: parent, To: child, Certainty: model.CertaintyReported, Includes: nil, Reason: "provider subagent lifecycle hook"}}
		batch.Events = append(batch.Events, event)
		return batch, event.Validate()
	case "SubagentStop":
		agentID := fieldString(hook.Fields, "agent_id")
		if agentID == "" {
			agentID = receiptID
		}
		payload := model.StateTransitionPayload{Subject: EntityRef(model.EntityAgent, provider+"/agent", agentID), To: "completed"}
		return batch, makeEvent("subagent-stop", model.EventStateChanged, payload)
	case "FileChanged":
		path := fieldString(hook.Fields, "file_path")
		change := fieldString(hook.Fields, "event")
		if change == "" {
			change = "observed"
		}
		var locator *model.ContentRef
		if path != "" {
			ref := batch.AddContent([]byte(path), model.ContentRaw, "text/plain; charset=utf-8", provider+"-path-v1")
			locator = &ref
		}
		payload := model.ArtifactPayload{Artifact: EntityRef(model.EntityArtifact, provider+"/file", first(path, receiptID)), Kind: "file", Locator: locator, Change: change}
		return batch, makeEvent("file", model.EventArtifactObserved, payload)
	case "PostCompact":
		summary := fieldString(hook.Fields, "compact_summary")
		var content *model.ContentRef
		if summary != "" {
			ref := batch.AddContent([]byte(summary), model.ContentSummary, "text/plain; charset=utf-8", provider+"-compact-v1")
			content = &ref
		}
		payload := model.ArtifactPayload{Artifact: EntityRef(model.EntityArtifact, provider+"/compact", receiptID), Kind: "context_summary", Content: content, Change: "created"}
		return batch, makeEvent("compact", model.EventArtifactObserved, payload)
	case "Stop":
		payload := model.StateTransitionPayload{Subject: EntityRef(model.EntityRun, provider+"/run", hook.RunID), From: "in_progress", To: "completed"}
		return batch, makeEvent("run-stop", model.EventStateChanged, payload)
	case "SessionStart", "SessionEnd", "PreCompact":
		state := map[string]string{"SessionStart": "session_started", "SessionEnd": "session_ended", "PreCompact": "compaction_started"}[hook.Event]
		payload := model.CollectionStatePayload{Stream: stream, State: state, Generation: "hook-v1", Cursor: receiptID}
		return batch, makeEvent("state", model.EventCollectionState, payload)
	default:
		payload := model.CollectionStatePayload{Stream: stream, State: "unknown_hook", Generation: "hook-v1", Cursor: receiptID, Coverage: []model.FieldAvailability{{Path: "/hook/event", Status: model.AvailabilityUnknown, Reason: "unsupported hook event " + hook.Event}}}
		return batch, makeEvent("unknown", model.EventCollectionState, payload)
	}
}

func Event(provider, surface string, receivedAt time.Time, eventID model.NamespacedID, kind model.EventKind, scope []model.EntityRef, payload any) (model.EventEnvelope, error) {
	return NewEvent(provider, surface, receivedAt, nil, eventID, kind, scope, payload)
}

func NewEvent(provider, surface string, receivedAt time.Time, providerAt *time.Time, eventID model.NamespacedID, kind model.EventKind, scope []model.EntityRef, payload any) (model.EventEnvelope, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return model.EventEnvelope{}, err
	}
	event := model.EventEnvelope{SchemaVersion: model.SchemaVersion, EventID: eventID, EventType: kind, Source: model.EventSource{Provider: provider, Surface: surface, Kind: sourceKind(surface)}, Time: model.EventTime{ProviderAt: providerAt, ReceivedAt: receivedAt}, Scope: scope, Payload: encoded}
	return event, event.Validate()
}

func EntityRef(kind model.EntityKind, namespace, value string) model.EntityRef {
	return model.EntityRef{Kind: kind, ID: model.NamespacedID{Namespace: namespace, Value: value}}
}
func fieldString(fields map[string]json.RawMessage, name string) string {
	var value string
	_ = json.Unmarshal(fields[name], &value)
	return value
}
func fieldInt64Pointer(fields map[string]json.RawMessage, name string) *int64 {
	var value int64
	if json.Unmarshal(fields[name], &value) != nil {
		return nil
	}
	return &value
}
func rawField(fields map[string]json.RawMessage, name string) any {
	var value any
	_ = json.Unmarshal(fields[name], &value)
	return value
}
func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
func hookActor(provider string, hook Hook) model.EntityRef {
	id := first(fieldString(hook.Fields, "agent_id"), "main")
	return EntityRef(model.EntityAgent, provider+"/agent", id)
}
func toolKind(name string) string {
	lower := strings.ToLower(name)
	if strings.EqualFold(name, "Bash") || strings.Contains(lower, "shell") || strings.EqualFold(name, "exec") {
		return "shell"
	}
	if strings.Contains(lower, "edit") || strings.Contains(lower, "write") || strings.Contains(lower, "patch") {
		return "edit"
	}
	if strings.Contains(lower, "browser") || strings.Contains(lower, "chrome") {
		return "browser"
	}
	if strings.Contains(lower, "mcp") || strings.Contains(lower, "connector") {
		return "connector"
	}
	if strings.Contains(lower, "webfetch") || strings.Contains(lower, "web_search") || strings.Contains(lower, "web__") || strings.Contains(lower, "http") {
		return "external_api"
	}
	return "tool"
}
func mediaType(data []byte) string {
	if json.Valid(data) {
		return "application/json"
	}
	return "text/plain; charset=utf-8"
}
func parseExitCode(message string) (int, bool) {
	match := exitCodePattern.FindStringSubmatch(message)
	if len(match) != 2 {
		return 0, false
	}
	var code int
	_, err := fmt.Sscanf(match[1], "%d", &code)
	return code, err == nil
}

func resultMetadata(raw json.RawMessage) (*int, string, *int64) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, "", nil
	}
	var value struct {
		ExitCode   *int   `json:"exit_code"`
		Status     string `json:"status"`
		Success    *bool  `json:"success"`
		DurationMS *int64 `json:"duration_ms"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return nil, "", nil
	}
	status := value.Status
	if value.Success != nil {
		if *value.Success {
			status = "completed"
		} else {
			status = "failed"
		}
	}
	return value.ExitCode, status, value.DurationMS
}
func sourceKind(surface string) model.SourceKind {
	if strings.HasPrefix(surface, "hook/") {
		return model.SourceHook
	}
	return model.SourceTranscript
}
func errorsNew(message string) error { return fmt.Errorf("%s", message) }
