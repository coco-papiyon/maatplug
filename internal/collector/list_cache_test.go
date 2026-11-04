package collector

import (
	"context"
	"encoding/base64"
	"os"
	"testing"

	"maatplug/internal/catalog"
	"maatplug/internal/contentstore"
	"maatplug/internal/ingest"
	"maatplug/internal/model"
)

func TestCollectorPublishesRepairsAndDoesNotRebuildIdleList(t *testing.T) {
	root := t.TempDir()
	worker := New(root)
	data := []byte("prompt")
	ref := contentstore.Describe(data, model.ContentRaw, "text/plain", "test-policy")
	route := ingest.Route{ProjectID: "project-a", Provider: "test", SessionID: "session-a", RunID: "run-a"}
	_, err := worker.Inbox.EnqueueEvent(route, instructionEvent("input", ref), []ingest.ContentItem{{Reference: ref, Policy: "test-policy", DataBase64: base64.StdEncoding.EncodeToString(data)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	list, err := catalog.ReadProjectList(root)
	if err != nil || len(list.Projects) != 1 {
		t.Fatalf("project list = %+v, %v", list, err)
	}
	if !worker.Catalog.ListSummariesReady("project-a") {
		t.Fatal("new list summary was not ready")
	}
	stats, err := worker.RunOnce(context.Background())
	if err != nil || stats.Catalogs != 0 {
		t.Fatalf("idle cycle rebuilt catalogs: %+v %v", stats, err)
	}
	if err := os.Remove(catalog.SessionListPath(root, "project-a")); err != nil {
		t.Fatal(err)
	}
	stats, err = worker.RunOnce(context.Background())
	if err != nil || stats.Catalogs != 1 || !worker.Catalog.ListSummariesReady("project-a") {
		t.Fatalf("missing session list not repaired: %+v %v", stats, err)
	}
	if err := os.Remove(catalog.ProjectListPath(root)); err != nil {
		t.Fatal(err)
	}
	stats, err = worker.RunOnce(context.Background())
	if err != nil || stats.Catalogs != 0 {
		t.Fatalf("missing index unnecessarily rebuilt catalogs: %+v %v", stats, err)
	}
	if _, err := catalog.ReadProjectList(root); err != nil {
		t.Fatal(err)
	}
}
