package adapter

import (
	"encoding/json"
	"testing"
	"time"

	"maatplug/internal/model"
)

func TestNormalizeHookAuditSurfaces(t *testing.T) {
	at := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		hook Hook
		kind model.EventKind
	}{
		{"tool request", Hook{Event: "PreToolUse", Fields: fields(`{"tool_use_id":"tool-1","tool_name":"Bash","tool_input":{"command":"echo ok"}}`)}, model.EventOperationRequested},
		{"tool failure", Hook{Event: "PostToolUse", Fields: fields(`{"tool_use_id":"tool-1","tool_response":{"exit_code":7,"duration_ms":12}}`)}, model.EventOperationResult},
		{"approval request", Hook{Event: "PermissionRequest", Fields: fields(`{"tool_name":"Bash"}`)}, model.EventApprovalResolved},
		{"approval denied", Hook{Event: "PermissionDenied", Fields: fields(`{"tool_use_id":"tool-1"}`)}, model.EventApprovalResolved},
		{"subagent start", Hook{Event: "SubagentStart", Fields: fields(`{"agent_id":"agent-1","agent_type":"Explore"}`)}, model.EventActorObserved},
		{"subagent stop", Hook{Event: "SubagentStop", Fields: fields(`{"agent_id":"agent-1"}`)}, model.EventStateChanged},
		{"file changed", Hook{Event: "FileChanged", Fields: fields(`{"file_path":"example.txt","event":"updated"}`)}, model.EventArtifactObserved},
		{"compact", Hook{Event: "PostCompact", Fields: fields(`{"compact_summary":"summary"}`)}, model.EventArtifactObserved},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.hook.SessionID, test.hook.RunID = "session", "run"
			batch, err := NormalizeHook("test", "project", "receipt-"+test.name, at, test.hook)
			if err != nil {
				t.Fatal(err)
			}
			if len(batch.Events) != 1 || batch.Events[0].EventType != test.kind {
				t.Fatalf("events = %+v", batch.Events)
			}
			if test.name == "tool failure" {
				var payload model.OperationResultPayload
				if err := json.Unmarshal(batch.Events[0].Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if payload.Status != "failed" || payload.ExitCode == nil || *payload.ExitCode != 7 {
					t.Fatalf("payload = %+v", payload)
				}
			}
		})
	}
}

func fields(raw string) map[string]json.RawMessage {
	result := map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(raw), &result)
	return result
}
