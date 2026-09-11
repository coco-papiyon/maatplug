package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"maatplug/internal/journal"
	"maatplug/internal/model"
	"maatplug/internal/query"
	"maatplug/internal/remotesync"
	"maatplug/internal/store"
	"maatplug/internal/usage"
)

func setupServer(t *testing.T) (*store.Store, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	s := &store.Store{Root: dir}
	return s, NewServer(s, nil, nil)
}

func appendNewEvent(t *testing.T, s *store.Store, projectID, provider, sessionID, runID, eventID string, kind model.EventKind, payload any, at time.Time) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	event := model.EventEnvelope{
		SchemaVersion: model.SchemaVersion,
		EventID:       model.NamespacedID{Namespace: "web-test/event", Value: eventID},
		EventType:     kind,
		Source:        model.EventSource{Provider: provider, Surface: "web-test", Kind: model.SourceCollector},
		Time:          model.EventTime{ReceivedAt: at},
		Scope: []model.EntityRef{
			{Kind: model.EntityProject, ID: model.NamespacedID{Namespace: "web-test/project", Value: projectID}},
			{Kind: model.EntitySession, ID: model.NamespacedID{Namespace: "web-test/session", Value: sessionID}},
			{Kind: model.EntityRun, ID: model.NamespacedID{Namespace: "web-test/run", Value: runID}},
		},
		Payload: data,
	}
	if _, err := journal.New(s.Root).Append(journal.Route{ProjectID: projectID, Provider: provider, SessionID: sessionID, RunID: runID}, event); err != nil {
		t.Fatal(err)
	}
}

func appendNewInstruction(t *testing.T, s *store.Store, projectID, provider, sessionID, runID, suffix string, at time.Time) {
	t.Helper()
	ns := "web-test/" + suffix
	appendNewEvent(t, s, projectID, provider, sessionID, runID, "instruction-"+suffix, model.EventInstructionRecorded, model.InstructionPayload{
		Instruction: model.EntityRef{Kind: model.EntityInstruction, ID: model.NamespacedID{Namespace: ns, Value: "instruction"}},
		Actor:       model.EntityRef{Kind: model.EntityActor, ID: model.NamespacedID{Namespace: ns, Value: "user"}},
		State:       "recorded",
	}, at)
}

func appendNewUsage(t *testing.T, s *store.Store, projectID, provider, sessionID, runID, suffix string, tokens int64, at time.Time) {
	t.Helper()
	ns := "web-test/" + suffix
	value := tokens
	appendNewEvent(t, s, projectID, provider, sessionID, runID, "usage-"+suffix, model.EventUsageReported, model.UsagePayload{
		Usage:     model.EntityRef{Kind: model.EntityUsage, ID: model.NamespacedID{Namespace: ns, Value: "usage"}},
		AppliesTo: model.EntityRef{Kind: model.EntityModelCall, ID: model.NamespacedID{Namespace: ns, Value: "call"}},
		Raw:       model.RawUsage{Unit: "tokens", Fields: map[string]json.RawMessage{"total": json.RawMessage(fmt.Sprintf("%d", tokens))}},
		Normalized: &model.NormalizedUsage{
			ExclusiveBuckets: []model.UsageBucket{{Name: "input", Tokens: model.Quantity{Value: &value, Availability: model.AvailabilityObserved}}},
			Totals:           []model.UsageBucket{{Name: "all", Tokens: model.Quantity{Value: &value, Availability: model.AvailabilityObserved}}},
		},
		Counter: model.CounterSemantics{Kind: model.CounterDelta},
		State:   model.MeasurementFinal,
	}, at)
}

func TestHandleProjects(t *testing.T) {
	s, handler := setupServer(t)
	if err := s.EnsureProject("proj1", "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}
	appendNewInstruction(t, s, "proj1", "claude", "session1", "run1", "p1", time.Now().UTC())

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var got []query.ProjectSummary
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, rr.Body.String())
	}
	if len(got) != 1 || got[0].ProjectID != "proj1" || got[0].Instructions != 1 {
		t.Errorf("got %+v", got)
	}
}

func TestHandleProjects_EmptyStoreReturnsEmptyArrayNotNull(t *testing.T) {
	_, handler := setupServer(t)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	// A frontend doing `for (const p of projects)` should never have to
	// special-case `null` — the query service must return [] (via a non-nil,
	// zero-length slice), not nil, when there is nothing recorded yet.
	if body != "[]\n" {
		t.Errorf("body = %q, want %q (empty JSON array, not null)", body, "[]\n")
	}
}

func TestHandleSessions(t *testing.T) {
	s, handler := setupServer(t)
	if err := s.EnsureProject("proj1", "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}
	appendNewInstruction(t, s, "proj1", "claude", "session1", "run1", "s1", time.Now().UTC())

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects/proj1/sessions", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var got []query.SessionSummary
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 || got[0].Provider != "claude" || got[0].SessionID != "session1" {
		t.Errorf("got %+v", got)
	}
}

func TestHandleSessionDetail(t *testing.T) {
	s, handler := setupServer(t)
	if err := s.EnsureProject("proj1", "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}
	appendNewInstruction(t, s, "proj1", "claude", "session1", "run1", "detail", time.Now().UTC())

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects/proj1/sessions/claude/session1", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var got query.Detail
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Provider != "claude" || got.SessionID != "session1" || len(got.Events) != 1 || got.Source != "journal" {
		t.Errorf("got %+v", got)
	}
}

func TestCanonicalRunAPIReadsNewJournal(t *testing.T) {
	s, handler := setupServer(t)
	if err := s.EnsureProject("proj1", "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}
	value := int64(12)
	payload, _ := json.Marshal(model.UsagePayload{Usage: model.EntityRef{Kind: model.EntityUsage, ID: model.NamespacedID{Namespace: "test/usage", Value: "u1"}}, AppliesTo: model.EntityRef{Kind: model.EntityModelCall, ID: model.NamespacedID{Namespace: "test/call", Value: "c1"}}, Raw: model.RawUsage{Unit: "tokens", Fields: map[string]json.RawMessage{"total": json.RawMessage("12")}}, Normalized: &model.NormalizedUsage{ExclusiveBuckets: []model.UsageBucket{{Name: "input", Tokens: model.Quantity{Value: &value, Availability: model.AvailabilityObserved}}}, Totals: []model.UsageBucket{{Name: "all", Tokens: model.Quantity{Value: &value, Availability: model.AvailabilityObserved}}}}, Counter: model.CounterSemantics{Kind: model.CounterDelta}, State: model.MeasurementFinal})
	event := model.EventEnvelope{SchemaVersion: model.SchemaVersion, EventID: model.NamespacedID{Namespace: "test/event", Value: "e1"}, EventType: model.EventUsageReported, Source: model.EventSource{Provider: "codex", Surface: "test", Kind: model.SourceCollector}, Time: model.EventTime{ReceivedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}, Scope: []model.EntityRef{{Kind: model.EntitySession, ID: model.NamespacedID{Namespace: "test/session", Value: "s1"}}, {Kind: model.EntityRun, ID: model.NamespacedID{Namespace: "test/run", Value: "r1"}}}, Payload: payload}
	if _, err := journal.New(s.Root).Append(journal.Route{ProjectID: "proj1", Provider: "codex", SessionID: "s1", RunID: "r1"}, event); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/projects/proj1/sessions/codex/s1/runs/r1", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		RunID string `json:"runId"`
		Usage struct {
			KnownSubtotal int64 `json:"knownSubtotal"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.RunID != "r1" || got.Usage.KnownSubtotal != 12 {
		t.Fatalf("got=%+v", got)
	}
}

func TestCollectionStatusAPI(t *testing.T) {
	_, handler := setupServer(t)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/collection-status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got []collectionStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Report.Worker.Known {
		t.Fatalf("got=%+v", got)
	}
}

func TestHandleSessionDetail_NotFound(t *testing.T) {
	s, handler := setupServer(t)
	if err := s.EnsureProject("proj1", "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects/proj1/sessions/claude/does-not-exist", nil))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleProjectsTrend(t *testing.T) {
	s, handler := setupServer(t)
	if err := s.EnsureProject("proj1", "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	appendNewUsage(t, s, "proj1", "claude", "session1", "run1", "trend", 500, now)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects/trend?days=7", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var got []query.DailyPoint
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 8 {
		t.Fatalf("len(got) = %d, want 8 (one point per day, -7d through today inclusive)", len(got))
	}
	last := got[len(got)-1]
	if last.TotalTokens != 500 {
		t.Errorf("today's point = %+v, want TotalTokens 500", last)
	}
}

func TestHandleProjectTrend_ScopedToOneProject(t *testing.T) {
	s, handler := setupServer(t)
	if err := s.EnsureProject("proj1", "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureProject("proj2", "otherproject", "/other"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	appendTurn := func(projectID string, tokens int) {
		t.Helper()
		appendNewUsage(t, s, projectID, "claude", "session1", "run1", projectID, int64(tokens), now)
	}
	appendTurn("proj1", 500)
	appendTurn("proj2", 999)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects/proj1/trend?days=1", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var got []query.DailyPoint
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	last := got[len(got)-1]
	if last.TotalTokens != 500 {
		t.Errorf("today's point = %+v, want TotalTokens 500 (proj2's 999 must not leak in)", last)
	}
}

func TestHandleProjectsBreakdown_ByProvider(t *testing.T) {
	s, handler := setupServer(t)
	if err := s.EnsureProject("proj1", "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	appendTurn := func(provider string, tokens int) {
		t.Helper()
		appendNewUsage(t, s, "proj1", provider, "session-"+provider, "run1", provider, int64(tokens), now)
	}
	appendTurn("claude", 500)
	appendTurn("codex", 300)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects/breakdown?days=1&dimension=provider", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var got query.TrendBreakdown
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Series) != 2 || got.Series[0].Key != "claude" || got.Series[1].Key != "codex" {
		t.Fatalf("Series = %+v, want [claude, codex] in that order", got.Series)
	}
	claudeLast := got.Series[0].Points[len(got.Series[0].Points)-1]
	codexLast := got.Series[1].Points[len(got.Series[1].Points)-1]
	if claudeLast.TotalTokens != 500 || codexLast.TotalTokens != 300 {
		t.Errorf("today's totals = claude:%d codex:%d, want 500/300", claudeLast.TotalTokens, codexLast.TotalTokens)
	}
}

func TestHandleProjectBreakdown_DefaultsToProviderDimension(t *testing.T) {
	s, handler := setupServer(t)
	if err := s.EnsureProject("proj1", "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}
	appendNewUsage(t, s, "proj1", "claude", "session1", "run1", "default-dimension", 42, time.Now().UTC())

	// No ?dimension= at all — must fall back to provider, not error.
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects/proj1/breakdown?days=1", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var got query.TrendBreakdown
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Series) != 1 || got.Series[0].Key != "claude" {
		t.Errorf("Series = %+v, want exactly one series keyed \"claude\"", got.Series)
	}
}

// fakeSyncReader stands in for *remotesync.Poller: syncFunc lets each test
// script exactly what a "sync" does (succeed, fail, or record which area
// was asked for) without touching a real ssh binary or network.
type fakeSyncReader struct {
	statuses []remotesync.AreaStatus
	syncFunc func(ctx context.Context, areaID string) (remotesync.AreaStatus, error)
	synced   []string
	interval time.Duration
}

func (f *fakeSyncReader) Statuses() []remotesync.AreaStatus { return f.statuses }

func (f *fakeSyncReader) SyncNow(ctx context.Context, areaID string) (remotesync.AreaStatus, error) {
	f.synced = append(f.synced, areaID)
	if f.syncFunc != nil {
		return f.syncFunc(ctx, areaID)
	}
	return remotesync.AreaStatus{AreaID: areaID}, nil
}

func (f *fakeSyncReader) Interval() time.Duration { return f.interval }

func (f *fakeSyncReader) SetInterval(d time.Duration) { f.interval = d }

func TestHandleAddDataArea_SSH_SyncsImmediatelyAndReturnsTheArea(t *testing.T) {
	s := &store.Store{Root: t.TempDir()}
	sync := &fakeSyncReader{syncFunc: func(_ context.Context, areaID string) (remotesync.AreaStatus, error) {
		return remotesync.AreaStatus{AreaID: areaID, FilesFetched: 3}, nil
	}}
	handler := NewServer(s, nil, sync)

	body := `{"name":"Build box","kind":"ssh","sshHost":"myserver","remotePath":"/home/me/.config/maatplug"}`
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/settings/areas", strings.NewReader(body)))

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var got store.DataArea
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.IsSSH() || got.SSHHost != "myserver" || got.RemotePath != "/home/me/.config/maatplug" {
		t.Errorf("got %+v", got)
	}
	if len(sync.synced) != 1 || sync.synced[0] != got.ID {
		t.Errorf("SyncNow calls = %v, want exactly one call for area %q", sync.synced, got.ID)
	}

	areas, err := s.ListDataAreas()
	if err != nil {
		t.Fatal(err)
	}
	if len(areas) != 2 {
		t.Errorf("ListDataAreas() after add = %+v, want the new area to have been saved", areas)
	}
}

func TestHandleAddDataArea_SSH_RollsBackOnFailedFirstSync(t *testing.T) {
	s := &store.Store{Root: t.TempDir()}
	sync := &fakeSyncReader{syncFunc: func(_ context.Context, areaID string) (remotesync.AreaStatus, error) {
		return remotesync.AreaStatus{}, errors.New("ssh: Could not resolve hostname myserver")
	}}
	handler := NewServer(s, nil, sync)

	body := `{"name":"Build box","kind":"ssh","sshHost":"myserver","remotePath":"/home/me/.config/maatplug"}`
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/settings/areas", strings.NewReader(body)))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}

	areas, err := s.ListDataAreas()
	if err != nil {
		t.Fatal(err)
	}
	if len(areas) != 1 {
		t.Errorf("ListDataAreas() after failed add = %+v, want only the default area (broken area rolled back)", areas)
	}
}

func TestHandleSyncDataArea(t *testing.T) {
	s := &store.Store{Root: t.TempDir()}
	added, err := s.AddSSHDataArea("Build box", "myserver", "/home/me/.config/maatplug")
	if err != nil {
		t.Fatal(err)
	}
	sync := &fakeSyncReader{syncFunc: func(_ context.Context, areaID string) (remotesync.AreaStatus, error) {
		return remotesync.AreaStatus{AreaID: areaID, FilesFetched: 5}, nil
	}}
	handler := NewServer(s, nil, sync)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/settings/areas/"+added.ID+"/sync", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var got remotesync.AreaStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.FilesFetched != 5 {
		t.Errorf("got %+v", got)
	}
}

func TestHandleSettings_IncludesSyncStatus(t *testing.T) {
	s := &store.Store{Root: t.TempDir()}
	sync := &fakeSyncReader{statuses: []remotesync.AreaStatus{{AreaID: "area-x", FilesFetched: 2}}}
	handler := NewServer(s, nil, sync)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/settings", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var got settingsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.SyncStatus) != 1 || got.SyncStatus[0].AreaID != "area-x" {
		t.Errorf("got.SyncStatus = %+v", got.SyncStatus)
	}
}

func TestHandleGetAppSettings_DefaultsWhenUnconfigured(t *testing.T) {
	_, handler := setupServer(t)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/settings/app", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var got store.AppSettings
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.RemoteSyncIntervalMinutes != store.DefaultRemoteSyncIntervalMinutes {
		t.Errorf("RemoteSyncIntervalMinutes = %d, want default %d", got.RemoteSyncIntervalMinutes, store.DefaultRemoteSyncIntervalMinutes)
	}
}

func TestHandleSaveAppSettings_PersistsAndUpdatesRunningPoller(t *testing.T) {
	s := &store.Store{Root: t.TempDir()}
	sync := &fakeSyncReader{}
	handler := NewServer(s, nil, sync)

	rr := httptest.NewRecorder()
	body := `{"remoteSyncIntervalMinutes":45}`
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPut, "/api/settings/app", strings.NewReader(body)))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if sync.interval != 45*time.Minute {
		t.Errorf("syncPoller.interval = %v, want 45m (SetInterval must be called on save)", sync.interval)
	}

	persisted, err := s.GetAppSettings()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.RemoteSyncIntervalMinutes != 45 {
		t.Errorf("persisted RemoteSyncIntervalMinutes = %d, want 45", persisted.RemoteSyncIntervalMinutes)
	}
}

func TestHandleSaveAppSettings_RejectsInvalidInterval(t *testing.T) {
	_, handler := setupServer(t)

	rr := httptest.NewRecorder()
	body := `{"remoteSyncIntervalMinutes":0}`
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPut, "/api/settings/app", strings.NewReader(body)))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

type fakeUsageReader []usage.Snapshot

func (f fakeUsageReader) Snapshots() []usage.Snapshot { return f }

type fakeUsageRefresher struct {
	snapshots []usage.Snapshot
	refreshed bool
}

func (f *fakeUsageRefresher) Snapshots() []usage.Snapshot { return f.snapshots }

func (f *fakeUsageRefresher) Refresh(context.Context) { f.refreshed = true }

func TestHandleUsage_NilPollerReturnsEmptyArray(t *testing.T) {
	_, handler := setupServer(t) // setupServer passes a nil usageReader

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/usage", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if body := rr.Body.String(); body != "[]\n" {
		t.Errorf("body = %q, want %q", body, "[]\n")
	}
}

func TestHandleUsage_ReturnsPollerSnapshots(t *testing.T) {
	dir := t.TempDir()
	s := &store.Store{Root: dir}
	fetchedAt := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	handler := NewServer(s, fakeUsageReader{
		{Provider: "claude", Available: true, FetchedAt: fetchedAt, Windows: []usage.Window{
			{Name: "session", UsedPercent: 49, RemainingPercent: 51, ResetLabel: "Sep 9, 1:39am"},
		}},
	}, nil)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/usage", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var got []usage.Snapshot
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 || got[0].Provider != "claude" || len(got[0].Windows) != 1 || got[0].Windows[0].RemainingPercent != 51 {
		t.Errorf("got %+v", got)
	}
}

func TestHandleUsageRefresh_RefreshesBeforeReturningSnapshots(t *testing.T) {
	reader := &fakeUsageRefresher{snapshots: []usage.Snapshot{{Provider: "claude", Available: true}}}
	s := &store.Store{Root: t.TempDir()}
	handler := NewServer(s, reader, nil)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/usage/refresh", nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rr.Code, rr.Body.String())
	}
	if !reader.refreshed {
		t.Fatal("Refresh was not called")
	}
	var got []usage.Snapshot
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 || got[0].Provider != "claude" {
		t.Errorf("got %+v", got)
	}
}

func TestStaticFrontendServed(t *testing.T) {
	_, handler := setupServer(t)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(rr.Body.String(), "maatplug") {
		t.Error("index.html body does not mention maatplug")
	}
}
