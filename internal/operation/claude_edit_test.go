package operation

import (
	"testing"
	"time"

	"maatplug/internal/contentstore"
	"maatplug/internal/model"
)

func TestBuildMeasuresSavedClaudeEditResultOnce(t *testing.T) {
	root := t.TempDir()
	ref, err := contentstore.New(root).Put([]byte(`{"filePath":"a.go","structuredPatch":[{"oldLines":1,"newLines":2,"lines":["-old","+new","+extra"]}]}`), model.ContentRaw, "application/json", "test-output")
	if err != nil {
		t.Fatal(err)
	}
	op := model.EntityRef{Kind: model.EntityOperation, ID: model.NamespacedID{Namespace: "claude/tool", Value: "edit-1"}}
	t0 := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	for _, status := range []string{"completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			events := []model.EventEnvelope{
				event(t, "request", model.EventOperationRequested, t0, model.OperationPayload{Operation: op, Actor: actorRef(), Kind: "edit", Name: "Edit"}),
				event(t, "hook-result", model.EventOperationResult, t0.Add(time.Second), model.OperationResultPayload{Operation: op, Status: status, Output: []model.ContentRef{ref}}),
				event(t, "transcript-result", model.EventOperationResult, t0.Add(2*time.Second), model.OperationResultPayload{Operation: op, Status: status, Output: []model.ContentRef{ref}}),
			}
			for i := range events {
				events[i].Source.Provider = "claude"
			}
			result, err := Build(root, events)
			if err != nil {
				t.Fatal(err)
			}
			got := result.Operations[0]
			if status == "failed" {
				if len(got.Artifacts) != 0 {
					t.Fatal("failed edit counted")
				}
				return
			}
			if len(got.Artifacts) != 1 || len(got.ObservedEffects) != 1 {
				t.Fatalf("duplicate or missing measurements: %+v", got.Artifacts)
			}
			m := got.Artifacts[0].Measurement
			if *m.Added != 2 || *m.Removed != 1 || *m.FilesChanged != 1 {
				t.Fatalf("measurement = %+v", m)
			}
		})
	}
}
