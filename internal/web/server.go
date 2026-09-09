// Package web is maatplug-web's HTTP handler: a JSON API over
// internal/aggregate plus the embedded static dashboard. It is
// self-contained per docs/design.md section 9 — every request re-reads the
// flat-file store directly (no dependency on any other maatplug process,
// no background indexer/cache to keep in sync).
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"maatplug/internal/aggregate"
	"maatplug/internal/store"
	"maatplug/internal/usage"
)

//go:embed frontend
var frontendFS embed.FS

// usageReader is the subset of *usage.Poller the web server needs: whatever
// remaining-usage data has been fetched so far. A narrow interface (rather
// than taking *usage.Poller directly) keeps this package's tests free to
// fake it.
type usageReader interface {
	Snapshots() []usage.Snapshot
}

// NewServer builds the full maatplug-web handler bound to s and usagePoller.
// usagePoller may be nil (GET /api/usage then always reports no providers)
// so callers that don't care about the usage badge don't need one.
func NewServer(s *store.Store, usagePoller usageReader) http.Handler {
	static, err := fs.Sub(frontendFS, "frontend")
	if err != nil {
		panic(err) // frontend is embedded at build time; this cannot fail at runtime
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(static)))
	mux.HandleFunc("GET /api/projects", handleProjects(s))
	mux.HandleFunc("GET /api/projects/trend", handleProjectsTrend(s))
	mux.HandleFunc("GET /api/projects/breakdown", handleProjectsBreakdown(s))
	mux.HandleFunc("GET /api/projects/{id}/sessions", handleSessions(s))
	mux.HandleFunc("GET /api/projects/{id}/trend", handleProjectTrend(s))
	mux.HandleFunc("GET /api/projects/{id}/breakdown", handleProjectBreakdown(s))
	mux.HandleFunc("GET /api/projects/{id}/sessions/{provider}/{sessionId}", handleSessionDetail(s))
	mux.HandleFunc("GET /api/usage", handleUsage(usagePoller))
	mux.HandleFunc("POST /api/usage/refresh", handleUsageRefresh(usagePoller))
	mux.HandleFunc("GET /api/settings", handleSettings(s))
	mux.HandleFunc("POST /api/settings/areas", handleAddDataArea(s))
	mux.HandleFunc("DELETE /api/settings/areas/{id}", handleRemoveDataArea(s))
	return mux
}

func handleProjects(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		areas, err := dashboardAreas(s)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		var summaries []projectSummary
		for _, area := range areas {
			items, err := aggregate.All(area.store, filterFromQuery(r))
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			for _, item := range items {
				summaries = append(summaries, projectSummary{ProjectSummary: item, DataAreaID: area.ID, DataAreaName: area.Name})
			}
		}
		if summaries == nil {
			summaries = []projectSummary{}
		}
		writeJSON(w, http.StatusOK, summaries)
	}
}

func handleSessions(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		areas, err := selectedDashboardAreas(s, r.URL.Query().Get("area"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		var summaries []sessionSummary
		for _, area := range areas {
			items, err := aggregate.Sessions(area.store, r.PathValue("id"), filterFromQuery(r))
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			for _, item := range items {
				summaries = append(summaries, sessionSummary{SessionSummary: item, DataAreaID: area.ID, DataAreaName: area.Name})
			}
		}
		if summaries == nil {
			summaries = []sessionSummary{}
		}
		writeJSON(w, http.StatusOK, summaries)
	}
}

// filterFromQuery reads the dashboard's two list-view filters off the
// request's query string: ?provider=claude|codex and ?days=7|30|90 (a
// rolling window ending now; absent, empty, zero, or unparseable means no
// lower bound — "all time" is simply not sending the param, not a special
// value).
func filterFromQuery(r *http.Request) aggregate.Filter {
	filter := aggregate.Filter{Provider: r.URL.Query().Get("provider")}
	if days, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && days > 0 {
		filter.Since = time.Now().AddDate(0, 0, -days)
	}
	return filter
}

// handleProjectsTrend serves the cost/token trend chart across every
// project (see handleProjects — same "every configured area" scope), for
// the projects-list screen's stat cards.
func handleProjectsTrend(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		areas, err := dashboardAreas(s)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		series := make([][]aggregate.DailyPoint, 0, len(areas))
		for _, area := range areas {
			points, err := aggregate.Trend(area.store, "", filterFromQuery(r))
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			series = append(series, points)
		}
		writeJSON(w, http.StatusOK, mergeDailyPoints(series))
	}
}

// handleProjectTrend serves the cost/token trend chart for one project (see
// handleSessions — same single-or-every-area scope via ?area=), for the
// session-list screen's stat cards.
func handleProjectTrend(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		areas, err := selectedDashboardAreas(s, r.URL.Query().Get("area"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		series := make([][]aggregate.DailyPoint, 0, len(areas))
		for _, area := range areas {
			points, err := aggregate.Trend(area.store, r.PathValue("id"), filterFromQuery(r))
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			series = append(series, points)
		}
		writeJSON(w, http.StatusOK, mergeDailyPoints(series))
	}
}

// mergeDailyPoints sums same-date points across multiple areas' trend
// series into one, keyed by date rather than position — every series here
// is built over the same requested window (filterFromQuery), so they line
// up, but matching by date rather than index is what actually guarantees
// that instead of assuming it.
func mergeDailyPoints(series [][]aggregate.DailyPoint) []aggregate.DailyPoint {
	byDay := map[string]*aggregate.DailyPoint{}
	order := make([]string, 0, len(series))
	for _, points := range series {
		for _, p := range points {
			existing := byDay[p.Date]
			if existing == nil {
				existing = &aggregate.DailyPoint{Date: p.Date}
				byDay[p.Date] = existing
				order = append(order, p.Date)
			}
			existing.CostUSD += p.CostUSD
			existing.TotalTokens += p.TotalTokens
		}
	}
	sort.Strings(order) // "YYYY-MM-DD" sorts lexicographically == chronologically
	merged := make([]aggregate.DailyPoint, 0, len(order))
	for _, day := range order {
		merged = append(merged, *byDay[day])
	}
	return merged
}

// breakdownDimensionFromQuery reads ?dimension=provider|model|tokenType,
// defaulting to provider (Breakdown's own zero-value fallback too) for any
// missing or unrecognized value rather than erroring — a bad or stale value
// degrades to the default view instead of breaking the page.
func breakdownDimensionFromQuery(r *http.Request) aggregate.BreakdownDimension {
	switch aggregate.BreakdownDimension(r.URL.Query().Get("dimension")) {
	case aggregate.BreakdownModel:
		return aggregate.BreakdownModel
	case aggregate.BreakdownTokenType:
		return aggregate.BreakdownTokenType
	default:
		return aggregate.BreakdownProvider
	}
}

// handleProjectsBreakdown serves the graph detail page's stacked chart
// across every project (see handleProjects — same "every configured area"
// scope).
func handleProjectsBreakdown(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		areas, err := dashboardAreas(s)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		dimension := breakdownDimensionFromQuery(r)
		results := make([]aggregate.TrendBreakdown, 0, len(areas))
		for _, area := range areas {
			tb, err := aggregate.Breakdown(area.store, "", filterFromQuery(r), dimension)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			results = append(results, tb)
		}
		writeJSON(w, http.StatusOK, mergeBreakdowns(results))
	}
}

// handleProjectBreakdown serves the graph detail page's stacked chart for
// one project (see handleSessions — same single-or-every-area scope via
// ?area=).
func handleProjectBreakdown(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		areas, err := selectedDashboardAreas(s, r.URL.Query().Get("area"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		dimension := breakdownDimensionFromQuery(r)
		results := make([]aggregate.TrendBreakdown, 0, len(areas))
		for _, area := range areas {
			tb, err := aggregate.Breakdown(area.store, r.PathValue("id"), filterFromQuery(r), dimension)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			results = append(results, tb)
		}
		writeJSON(w, http.StatusOK, mergeBreakdowns(results))
	}
}

// mergeBreakdowns sums same-category, same-date points across multiple
// areas' breakdown results into one. Provider and TokenType dimensions
// merge exactly (every area returns the same small fixed category set);
// Model is a documented approximation when more than one area is
// configured — each area independently ranks and folds its own top
// breakdownMaxCategories models into its own "other" before this ever sees
// it, so two areas' "other" buckets can each represent different sets of
// tail models before landing in the same merged "other" series. Building a
// single cross-area top-N ranking instead would need Breakdown to operate
// across multiple stores at once; not worth that for what is, today, a
// rarely-multi-area feature — single-area setups (the common case) are
// unaffected, since there is nothing to merge.
func mergeBreakdowns(results []aggregate.TrendBreakdown) aggregate.TrendBreakdown {
	if len(results) == 0 {
		return aggregate.TrendBreakdown{Dates: []string{}, Series: []aggregate.CategorySeries{}}
	}

	byCategory := map[string]map[string]*aggregate.DailyPoint{}
	order := make([]string, 0, len(results[0].Series))
	for _, tb := range results {
		for _, series := range tb.Series {
			days, ok := byCategory[series.Key]
			if !ok {
				days = map[string]*aggregate.DailyPoint{}
				byCategory[series.Key] = days
				order = append(order, series.Key)
			}
			for _, p := range series.Points {
				existing, ok := days[p.Date]
				if !ok {
					existing = &aggregate.DailyPoint{Date: p.Date}
					days[p.Date] = existing
				}
				existing.CostUSD += p.CostUSD
				existing.TotalTokens += p.TotalTokens
			}
		}
	}

	dates := results[0].Dates
	merged := make([]aggregate.CategorySeries, 0, len(order))
	for _, key := range order {
		days := byCategory[key]
		points := make([]aggregate.DailyPoint, 0, len(dates))
		for _, day := range dates {
			if p, ok := days[day]; ok {
				points = append(points, *p)
			} else {
				points = append(points, aggregate.DailyPoint{Date: day})
			}
		}
		merged = append(merged, aggregate.CategorySeries{Key: key, Points: points})
	}
	if merged == nil {
		merged = []aggregate.CategorySeries{}
	}
	return aggregate.TrendBreakdown{Dates: dates, Series: merged}
}

func handleSessionDetail(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		areas, err := selectedDashboardAreas(s, r.URL.Query().Get("area"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		for _, area := range areas {
			detail, err := aggregate.SessionDetailFor(area.store, r.PathValue("id"), r.PathValue("provider"), r.PathValue("sessionId"))
			if err == nil {
				writeJSON(w, http.StatusOK, detail)
				return
			}
			if !errors.Is(err, aggregate.ErrSessionNotFound) {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		}
		writeError(w, http.StatusNotFound, aggregate.ErrSessionNotFound)
	}
}

type dashboardArea struct {
	store.DataArea
	store *store.Store
}

type projectSummary struct {
	aggregate.ProjectSummary
	DataAreaID   string `json:"dataAreaId"`
	DataAreaName string `json:"dataAreaName"`
}

type sessionSummary struct {
	aggregate.SessionSummary
	DataAreaID   string `json:"dataAreaId"`
	DataAreaName string `json:"dataAreaName"`
}

func dashboardAreas(s *store.Store) ([]dashboardArea, error) {
	areas, err := s.ListDataAreas()
	if err != nil {
		return nil, err
	}
	out := make([]dashboardArea, 0, len(areas))
	for _, area := range areas {
		out = append(out, dashboardArea{DataArea: area, store: &store.Store{Root: area.Path}})
	}
	return out, nil
}

func selectedDashboardAreas(s *store.Store, id string) ([]dashboardArea, error) {
	areas, err := dashboardAreas(s)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return areas, nil
	}
	for _, area := range areas {
		if area.ID == id {
			return []dashboardArea{area}, nil
		}
	}
	return nil, errors.New("data area not found")
}

type settingsResponse struct {
	DataAreas []store.DataArea `json:"dataAreas"`
}

func handleSettings(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		areas, err := s.ListDataAreas()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, settingsResponse{DataAreas: areas})
	}
}

func handleAddDataArea(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Name string `json:"name"`
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		area, err := s.AddDataArea(strings.TrimSpace(input.Name), strings.TrimSpace(input.Path))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, area)
	}
}

func handleRemoveDataArea(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.RemoveDataArea(r.PathValue("id")); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeError(w, http.StatusNotFound, err)
				return
			}
			writeError(w, http.StatusBadRequest, err)
			return
		}
		areas, err := s.ListDataAreas()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, settingsResponse{DataAreas: areas})
	}
}

func handleUsage(usagePoller usageReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if usagePoller == nil {
			writeJSON(w, http.StatusOK, []usage.Snapshot{})
			return
		}
		writeJSON(w, http.StatusOK, usagePoller.Snapshots())
	}
}

type usageRefresher interface {
	Refresh(context.Context)
}

type usageProviderRefresher interface {
	RefreshProvider(context.Context, string)
}

func handleUsageRefresh(usagePoller usageReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provider := r.URL.Query().Get("provider")
		if provider != "" {
			if refresher, ok := usagePoller.(usageProviderRefresher); ok {
				refresher.RefreshProvider(r.Context(), provider)
			} else if refresher, ok := usagePoller.(usageRefresher); ok {
				refresher.Refresh(r.Context())
			}
		} else if refresher, ok := usagePoller.(usageRefresher); ok {
			refresher.Refresh(r.Context())
		}
		if usagePoller == nil {
			writeJSON(w, http.StatusOK, []usage.Snapshot{})
			return
		}
		writeJSON(w, http.StatusOK, usagePoller.Snapshots())
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
