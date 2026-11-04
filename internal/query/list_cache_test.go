package query

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"maatplug/internal/catalog"
	"maatplug/internal/journal"
	"maatplug/internal/store"
)

func TestListSnapshotsMatchCatalogAndIgnoreHeavyFiles(t *testing.T) {
	root := t.TempDir()
	service := New(root)
	loadAcceptance(t, root)
	builder := catalog.New(root)
	projection, err := builder.RebuildProject("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.RefreshProjectList(); err != nil {
		t.Fatal(err)
	}
	projects, err := service.ListProjects(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	wantProject, err := service.summaryFromCatalog(projection)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || !reflect.DeepEqual(projects[0], wantProject) {
		t.Fatalf("project snapshot differs: %+v", projects)
	}
	sessions, err := service.Sessions("fixture", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != len(projection.Sessions) {
		t.Fatal("sessions missing")
	}
	for i, session := range projection.Sessions {
		want := sessionSummaryFromCatalog(session)
		got := sessions[i]
		want.RunSummaries, got.RunSummaries = nil, nil
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("session snapshot differs: %+v / %+v", got, want)
		}
	}
	provider := projection.Sessions[0].Provider
	byProvider, err := service.ListProjects(Filter{Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	live, err := service.Project("fixture", Filter{Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	if len(byProvider) != 1 || byProvider[0].TotalTokens != live.TotalTokens || byProvider[0].Instructions != wantProject.Instructions || !reflect.DeepEqual(byProvider[0].EstimatedCost, live.EstimatedCost) {
		t.Fatalf("provider summary differs: %+v / %+v", byProvider, live)
	}
	filter := Filter{Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)}
	trend, err := service.Trend("", filter)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := journal.New(root).ListProjectStreams("fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("invalid journal"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile((&store.Store{Root: root}).ProjectCatalogPath("fixture"), []byte("invalid heavy catalog"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := service.ListProjects(Filter{}); err != nil || !reflect.DeepEqual(got, projects) {
		t.Fatalf("fast projects read heavy files: %v", err)
	}
	if got, err := service.ListProjects(Filter{Provider: provider}); err != nil || !reflect.DeepEqual(got, byProvider) {
		t.Fatalf("fast provider projects read heavy files: %v", err)
	}
	if got, err := service.Sessions("fixture", Filter{}); err != nil || !reflect.DeepEqual(got, sessions) {
		t.Fatalf("fast sessions read heavy files: %v", err)
	}
	if got, err := service.Trend("", filter); err != nil || !reflect.DeepEqual(got, trend) {
		t.Fatalf("fast trend read heavy files: %v", err)
	}
	if got, err := service.Sessions("fixture", Filter{Provider: "missing"}); err != nil || len(got) != 0 {
		t.Fatal("provider filter was not applied")
	}
	index, _ := os.ReadFile(catalog.ProjectListPath(root))
	if !json.Valid(index) {
		t.Fatal("invalid index")
	}
	t.Logf("Project index: %d bytes; sessions: %d", len(index), len(sessions))
}

func BenchmarkProjectListSnapshot(b *testing.B) {
	root := b.TempDir()
	projects := []catalog.ProjectListEntry{}
	for i := 0; i < 100; i++ {
		projects = append(projects, catalog.ProjectListEntry{Project: catalog.ProjectSummary{ProjectID: "project", Name: "Project", Path: "/repo", Instructions: 1000, TotalTokens: 1000000}})
	}
	raw, _ := json.Marshal(catalog.ProjectList{Version: catalog.ListVersion, AggregationVersion: catalog.AggregationVersion, Projects: projects})
	if err := os.WriteFile(catalog.ProjectListPath(root), raw, 0o600); err != nil {
		b.Fatal(err)
	}
	service := New(root)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := service.ListProjects(Filter{}); err != nil {
			b.Fatal(err)
		}
	}
}
