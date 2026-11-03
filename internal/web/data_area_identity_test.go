package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"maatplug/internal/store"
)

func TestDataAreaIdentityUpdateAndSessionFilters(t *testing.T) {
	s, handler := setupServer(t)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPut, "/api/settings/areas/default", strings.NewReader(`{"name":"Desktop","userName":"Alice","path":"/do-not-change"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rr.Code, rr.Body.String())
	}
	var defaultArea store.DataArea
	if err := json.Unmarshal(rr.Body.Bytes(), &defaultArea); err != nil {
		t.Fatal(err)
	}
	if defaultArea.UserName != "Alice" || defaultArea.Path != s.Root {
		t.Fatalf("area = %+v", defaultArea)
	}
	otherRoot := t.TempDir()
	_, err := s.AddDataArea("Build Server", otherRoot, "Bob")
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range []*store.Store{s, {Root: otherRoot}} {
		if err := data.EnsureProject("shared-project", "Shared project", "/repo"); err != nil {
			t.Fatal(err)
		}
		appendNewInstruction(t, data, "shared-project", "claude", "same-session", "run", "input", time.Now().UTC())
	}
	for _, endpoint := range []string{"/api/projects", "/api/projects/shared-project/sessions"} {
		for _, tt := range []struct {
			query string
			want  int
			user  string
		}{
			{"", 2, ""},
			{"?areaName=server&userName=BO", 1, "Bob"},
			{"?userName=alice", 1, "Alice"},
			{"?areaName=Desktop&userName=Bob", 0, ""},
			{"?userName=missing", 0, ""},
		} {
			t.Run(endpoint+tt.query, func(t *testing.T) {
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, endpoint+tt.query, nil))
				if rr.Code != http.StatusOK {
					t.Fatalf("GET: %d %s", rr.Code, rr.Body.String())
				}
				var rows []struct {
					AreaID   string `json:"dataAreaId"`
					UserName string `json:"dataAreaUserName"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
					t.Fatal(err)
				}
				if len(rows) != tt.want {
					t.Fatalf("rows=%+v; want %d", rows, tt.want)
				}
				if tt.user != "" && rows[0].UserName != tt.user {
					t.Fatalf("wrong identity: %+v", rows)
				}
			})
		}
	}
	for _, body := range []string{`{"name":""}`, `{invalid}`} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPut, "/api/settings/areas/default", strings.NewReader(body)))
		if rr.Code != http.StatusBadRequest {
			t.Fatal("invalid update accepted")
		}
	}
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPut, "/api/settings/areas/missing", strings.NewReader(`{"name":"X"}`)))
	if rr.Code != http.StatusNotFound {
		t.Fatal("missing area was not 404")
	}
	// Local creation accepts userName in the same request, without an extra update.
	body := fmt.Sprintf(`{"name":"New area","path":%q,"userName":"Carol"}`, t.TempDir())
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/settings/areas", strings.NewReader(body)))
	if rr.Code != http.StatusCreated || !strings.Contains(rr.Body.String(), `"userName":"Carol"`) {
		t.Fatalf("POST: %d %s", rr.Code, rr.Body.String())
	}
}
