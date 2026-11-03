package query

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"maatplug/internal/catalog"
	"maatplug/internal/journal"
	"maatplug/internal/model"
	"maatplug/internal/store"
)

func TestCatalogRepairAndJournalDetails(t *testing.T) {
	root := t.TempDir()
	loadAcceptance(t, root)
	if _, err := catalog.New(root).RebuildProject("fixture"); err != nil {
		t.Fatal(err)
	}
	path := (&store.Store{Root: root}).ProjectCatalogPath("fixture")
	if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := New(root)
	summary, err := service.Project("fixture", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Source != "catalog" || summary.Usage.KnownSubtotal != 415 || summary.Runs == 0 {
		t.Fatalf("summary = %+v", summary)
	}
	if data, err := os.ReadFile(path); err != nil || !json.Valid(data) {
		t.Fatalf("catalog was not repaired: %v", err)
	}
	projection, err := service.catalog("fixture")
	if err != nil {
		t.Fatal(err)
	}
	firstRun := projection.Sessions[0].Runs[0]
	summaryPath := catalog.New(root).RunSummaryPath("fixture", firstRun.Provider, firstRun.SessionID, firstRun.RunID)
	if err := os.Remove(summaryPath); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Project("fixture", Filter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(summaryPath); err != nil {
		t.Fatalf("run summary was not restored: %v", err)
	}
	sessions, err := service.Sessions("fixture", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Runs == 0 || sessions[0].Usage.KnownSubtotal != 415 {
		t.Fatalf("sessions = %+v", sessions)
	}
	runID := sessions[0].RunSummaries[0].RunID
	detail, err := service.RunDetail("fixture", "fixture", "acceptance", runID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Source != "journal" || detail.RunID != runID || len(detail.Events) == 0 {
		t.Fatalf("detail = %+v", detail)
	}
	entity, err := service.Entity("fixture", model.EntityInstruction, "fixture", "instruction-05-a", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entity.EventIDs) == 0 || len(entity.PricingEvidence) == 0 {
		t.Fatalf("entity = %+v", entity)
	}
}

func TestPerformanceBaselineIsFlatFile(t *testing.T) {
	root := t.TempDir()
	loadAcceptance(t, root)
	service := New(root)
	result, err := service.RecordBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if result.BaselinePath == "" || result.FileCount == 0 || result.ProjectCount != 1 {
		t.Fatalf("baseline = %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(result.BaselinePath)))
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatal("baseline is not valid JSON")
	}
}

func TestDoctorSnapshotSupportsLightweightStatus(t *testing.T) {
	root := t.TempDir()
	loadAcceptance(t, root)
	service := New(root)
	report, err := service.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	if report.GeneratedAt.IsZero() || report.Coverage.Events == 0 {
		t.Fatalf("report = %+v", report)
	}
	if _, err := os.Stat(filepath.Join(root, "state", "doctor-report.json")); err != nil {
		t.Fatal(err)
	}
	paths, err := journal.New(root).ListProjectStreams("fixture")
	if err != nil || len(paths) == 0 {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	if err := os.WriteFile(paths[0], []byte("broken journal"), 0o600); err != nil {
		t.Fatal(err)
	}
	cached, err := service.CachedDoctor()
	if err != nil {
		t.Fatal(err)
	}
	if !cached.FullScanAvailable || cached.Coverage.Events != report.Coverage.Events || cached.GeneratedAt != report.GeneratedAt || len(cached.Collection.JournalErrors) != len(report.Collection.JournalErrors) {
		t.Fatalf("cached=%+v full=%+v", cached, report)
	}
}

// TestSessionsCatalogPathMatchesLiveFilterPath guards the Sessions()
// fast path (cached catalog.SessionDerived, used when no time filter is
// given) against the slow live path (raw journal re-read + derive(), still
// used when Since/Until narrows the window): with a window wide enough to
// include every event, both paths must agree exactly.
func TestSessionsCatalogPathMatchesLiveFilterPath(t *testing.T) {
	root := t.TempDir()
	loadAcceptance(t, root)
	service := New(root)
	cached, err := service.Sessions("fixture", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	live, err := service.Sessions("fixture", Filter{Since: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if len(cached) != 1 || len(live) != 1 {
		t.Fatalf("cached=%d live=%d sessions, want 1 each", len(cached), len(live))
	}
	if cached[0].Usage.KnownSubtotal == 0 {
		t.Fatal("expected nonzero known usage in fixture")
	}
	if cached[0].Usage.KnownSubtotal != live[0].Usage.KnownSubtotal ||
		cached[0].Runs != live[0].Runs ||
		cached[0].Instructions != live[0].Instructions ||
		cached[0].Calls != live[0].Calls ||
		cached[0].Operations != live[0].Operations ||
		cached[0].Unresolved != live[0].Unresolved ||
		cached[0].CostMissing != live[0].CostMissing ||
		!reflect.DeepEqual(cached[0].EstimatedCost, live[0].EstimatedCost) {
		t.Fatalf("cached = %+v\nlive = %+v", cached[0], live[0])
	}
}

// TestTrendCatalogPathMatchesLiveProviderPath guards Trend()'s catalog-based
// fast path (no provider filter) against its live raw-journal path (used
// whenever a provider filter is given): the fixture has a single journal
// route provider, so filtering by it is a no-op and both paths must produce
// identical daily points.
func TestTrendCatalogPathMatchesLiveProviderPath(t *testing.T) {
	root := t.TempDir()
	loadAcceptance(t, root)
	service := New(root)
	window := Filter{Since: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)}
	cached, err := service.Trend("fixture", window)
	if err != nil {
		t.Fatal(err)
	}
	live, err := service.Trend("fixture", Filter{Provider: "fixture", Since: window.Since, Until: window.Until})
	if err != nil {
		t.Fatal(err)
	}
	var cachedTokens int64
	for _, point := range cached {
		cachedTokens += point.TotalTokens
	}
	if cachedTokens == 0 {
		t.Fatal("expected nonzero trend tokens from fixture")
	}
	if !reflect.DeepEqual(cached, live) {
		t.Fatalf("cached trend = %+v\nlive trend = %+v", cached, live)
	}
}

func loadAcceptance(t *testing.T, root string) {
	t.Helper()
	file, err := os.Open(filepath.Join("..", "..", "testdata", "events", "acceptance.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	j := journal.New(root)
	for scanner.Scan() {
		var event model.EventEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		runID := ""
		for _, ref := range event.Scope {
			if ref.Kind == model.EntityRun {
				runID = ref.ID.Value
			}
		}
		if runID == "" {
			t.Fatalf("event %s has no run scope", event.EventID.Value)
		}
		if _, err := j.Append(journal.Route{ProjectID: "fixture", Provider: "fixture", SessionID: "acceptance", RunID: runID}, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}
