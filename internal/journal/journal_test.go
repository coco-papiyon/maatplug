package journal

import (
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"maatplug/internal/model"
	"maatplug/internal/store"
)

func testEvent(id string) model.EventEnvelope {
	payload, _ := json.Marshal(model.CollectionStatePayload{
		Stream:     model.EntityRef{Kind: model.EntityStream, ID: model.NamespacedID{Namespace: "test", Value: "stream"}},
		State:      "active",
		Generation: "g1",
	})
	return model.EventEnvelope{
		SchemaVersion: model.SchemaVersion,
		EventID:       model.NamespacedID{Namespace: "test", Value: id},
		EventType:     model.EventCollectionState,
		Source:        model.EventSource{Provider: "test", Surface: "test", Kind: model.SourceCollector},
		Time:          model.EventTime{ReceivedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		Scope:         []model.EntityRef{{Kind: model.EntityRun, ID: model.NamespacedID{Namespace: "test", Value: "run"}}},
		Payload:       payload,
	}
}

func TestAppendIsIdempotentAndDetectsConflict(t *testing.T) {
	j := New(t.TempDir())
	route := Route{ProjectID: "project", Provider: "test", SessionID: "session/with/slash", RunID: "run:1"}
	event := testEvent("event-1")
	first, err := j.Append(route, event)
	if err != nil {
		t.Fatal(err)
	}
	second, err := j.Append(route, event)
	if err != nil {
		t.Fatal(err)
	}
	if first.Position != 0 || second.Position != 0 || !second.Duplicate {
		t.Fatalf("append results = %+v, %+v", first, second)
	}
	events, err := j.Read(route)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	conflict := event
	conflict.Source.Surface = "different"
	if _, err := j.Append(route, conflict); err == nil {
		t.Fatal("conflicting duplicate event ID was accepted")
	}
	parsed, err := j.RouteFromPath(first.Path)
	if err != nil || parsed != route {
		t.Fatalf("route round trip = %+v, %v", parsed, err)
	}
}

func TestAppendTreatsReceivedAtOnlyChangeAsRedelivery(t *testing.T) {
	j := New(t.TempDir())
	route := Route{ProjectID: "project", Provider: "test", SessionID: "session", RunID: "run"}
	event := testEvent("redelivery")
	if _, err := j.Append(route, event); err != nil {
		t.Fatal(err)
	}
	redelivered := event
	redelivered.Time.ReceivedAt = event.Time.ReceivedAt.Add(time.Minute)
	result, err := j.Append(route, redelivered)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Duplicate || result.Position != 0 {
		t.Fatalf("result = %+v", result)
	}
	events, err := j.Read(route)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
}

func TestAppendRecoversIncompleteTail(t *testing.T) {
	root := t.TempDir()
	j := New(root)
	route := Route{ProjectID: "project", Provider: "test", SessionID: "session", RunID: "run"}
	first, err := j.Append(route, testEvent("event-1"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(first.Path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{\"schemaVersion\":"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err := j.Append(route, testEvent("event-2")); err != nil {
		t.Fatal(err)
	}
	events, err := j.Read(route)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	recovery, err := os.ReadDir((&store.Store{Root: root}).StateDir("journal-recovery"))
	if err != nil || len(recovery) != 1 {
		t.Fatalf("recovery files = %v, %v", recovery, err)
	}
}

func TestConcurrentAppendSerializesPerRun(t *testing.T) {
	j := New(t.TempDir())
	route := Route{ProjectID: "project", Provider: "test", SessionID: "session", RunID: "run"}
	const count = 24
	var wg sync.WaitGroup
	for index := 0; index < count; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := j.Append(route, testEvent("event-"+time.Unix(int64(index), 0).Format("150405"))); err != nil {
				t.Errorf("Append: %v", err)
			}
		}()
	}
	wg.Wait()
	events, err := j.Read(route)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != count {
		t.Fatalf("events = %d, want %d", len(events), count)
	}
}

func TestDifferentRunsUseIndependentLocks(t *testing.T) {
	j := New(t.TempDir())
	routeA := Route{ProjectID: "project", Provider: "test", SessionID: "session", RunID: "run-a"}
	routeB := Route{ProjectID: "project", Provider: "test", SessionID: "session", RunID: "run-b"}
	pathA, err := j.StreamPath(routeA)
	if err != nil {
		t.Fatal(err)
	}
	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- store.WithFileLock(pathA, func() error { close(locked); <-release; return nil })
	}()
	<-locked
	appendDone := make(chan error, 1)
	go func() { _, err := j.Append(routeB, testEvent("event-run-b")); appendDone <- err }()
	select {
	case err := <-appendDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("append to run-b waited for run-a lock")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
