package collector

import (
	"context"
	"testing"
)

func TestRunOnceRecordsWorkerAndStableCutoverState(t *testing.T) {
	worker := New(t.TempDir())
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := worker.LoadWorkerState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "idle" || state.LastAttempt == nil || state.LastSuccess == nil {
		t.Fatalf("worker state = %+v", state)
	}
	first, err := worker.LoadCutoverState()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := worker.LoadCutoverState()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("cutover changed: first=%+v second=%+v", first, second)
	}
}
