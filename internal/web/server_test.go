package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"maatplug/internal/aggregate"
	"maatplug/internal/model"
	"maatplug/internal/store"
	"maatplug/internal/usage"
)

func setupServer(t *testing.T) (*store.Store, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	s := &store.Store{Root: dir}
	return s, NewServer(s, nil)
}

func TestHandleProjects(t *testing.T) {
	s, handler := setupServer(t)
	if err := s.EnsureProject("proj1", "myproject", "/repo"); err != nil {
		t.Fatal(err)
	}
	path := s.SessionPath("proj1", "claude", "session1")
	if err := s.AppendEvent(path, model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i1"}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var got []aggregate.ProjectSummary
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
	// special-case `null` — aggregate.All must return [] (via a non-nil,
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
	path := s.SessionPath("proj1", "claude", "session1")
	if err := s.AppendEvent(path, model.SessionStartEvent{Type: model.EventSessionStart}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects/proj1/sessions", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var got []aggregate.SessionSummary
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
	path := s.SessionPath("proj1", "claude", "session1")
	if err := s.AppendEvent(path, model.SessionStartEvent{Type: model.EventSessionStart, Cwd: "/repo"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(path, model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i1", Text: "hello"}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects/proj1/sessions/claude/session1", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var got aggregate.SessionDetail
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Cwd != "/repo" || len(got.Turns) != 1 || got.Turns[0].Text != "hello" {
		t.Errorf("got %+v", got)
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
	path := s.SessionPath("proj1", "claude", "session1")
	now := time.Now().UTC()
	if err := s.AppendEvent(path, model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(path, model.TurnEndEvent{
		Type: model.EventTurnEnd, InstructionID: "i1", Timestamp: now,
		Tokens: &model.TokenUsage{Total: 500},
	}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects/trend?days=7", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var got []aggregate.DailyPoint
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
		path := s.SessionPath(projectID, "claude", "session1")
		if err := s.AppendEvent(path, model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i1"}); err != nil {
			t.Fatal(err)
		}
		if err := s.AppendEvent(path, model.TurnEndEvent{
			Type: model.EventTurnEnd, InstructionID: "i1", Timestamp: now,
			Tokens: &model.TokenUsage{Total: tokens},
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendTurn("proj1", 500)
	appendTurn("proj2", 999)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects/proj1/trend?days=1", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var got []aggregate.DailyPoint
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
		path := s.SessionPath("proj1", provider, "session-"+provider)
		if err := s.AppendEvent(path, model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i1"}); err != nil {
			t.Fatal(err)
		}
		if err := s.AppendEvent(path, model.TurnEndEvent{
			Type: model.EventTurnEnd, InstructionID: "i1", Timestamp: now,
			Tokens: &model.TokenUsage{Total: tokens},
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendTurn("claude", 500)
	appendTurn("codex", 300)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects/breakdown?days=1&dimension=provider", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var got aggregate.TrendBreakdown
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
	path := s.SessionPath("proj1", "claude", "session1")
	if err := s.AppendEvent(path, model.InstructionEvent{Type: model.EventInstruction, InstructionID: "i1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(path, model.TurnEndEvent{
		Type: model.EventTurnEnd, InstructionID: "i1", Timestamp: time.Now(),
		Tokens: &model.TokenUsage{Total: 42},
	}); err != nil {
		t.Fatal(err)
	}

	// No ?dimension= at all — must fall back to provider, not error.
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects/proj1/breakdown?days=1", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var got aggregate.TrendBreakdown
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Series) != 1 || got.Series[0].Key != "claude" {
		t.Errorf("Series = %+v, want exactly one series keyed \"claude\"", got.Series)
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
	})

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
	handler := NewServer(s, reader)

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
