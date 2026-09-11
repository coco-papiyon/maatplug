// Package web is maatplug-web's HTTP handler: a JSON API over the shared
// internal/query service plus the embedded static dashboard. It is
// self-contained per docs/design.md section 9 — every request re-reads the
// flat-file store directly (no dependency on any other maatplug process,
// no background indexer/cache to keep in sync).
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"maatplug/internal/cost"
	"maatplug/internal/model"
	"maatplug/internal/query"
	"maatplug/internal/remotesync"
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

// remoteSyncReader is the subset of *remotesync.Poller the web server
// needs. A narrow interface, like usageReader, so tests can fake it — and
// so a caller that doesn't want SSH data areas synced at all can simply
// pass nil (GET /api/settings then reports no sync status, and adding an
// SSH area skips the immediate connectivity/first-sync check).
type remoteSyncReader interface {
	Statuses() []remotesync.AreaStatus
	SyncNow(ctx context.Context, areaID string) (remotesync.AreaStatus, error)
	Interval() time.Duration
	SetInterval(d time.Duration)
}

// NewServer builds the full maatplug-web handler bound to s, usagePoller,
// and syncPoller. usagePoller may be nil (GET /api/usage then always
// reports no providers); syncPoller may be nil (SSH data areas can still be
// added, but nothing syncs them — see remoteSyncReader).
func NewServer(s *store.Store, usagePoller usageReader, syncPoller remoteSyncReader) http.Handler {
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
	mux.HandleFunc("GET /api/collection-status", handleCollectionStatus(s))
	mux.HandleFunc("GET /api/v1/projects", handleProjects(s))
	mux.HandleFunc("GET /api/v1/projects/{id}", handleProjectDetail(s))
	mux.HandleFunc("GET /api/v1/projects/{id}/work", handleProjectDetail(s))
	mux.HandleFunc("GET /api/v1/projects/{id}/periods", handlePeriods(s))
	mux.HandleFunc("GET /api/v1/projects/{id}/sessions", handleSessions(s))
	mux.HandleFunc("GET /api/v1/projects/{id}/sessions/{provider}/{sessionId}", handleCanonicalSessionDetail(s))
	mux.HandleFunc("GET /api/v1/projects/{id}/sessions/{provider}/{sessionId}/runs/{runId}", handleRunDetail(s))
	mux.HandleFunc("GET /api/v1/projects/{id}/entities/{kind}/{namespace}/{entityId}", handleEntityDetail(s))
	mux.HandleFunc("GET /api/v1/projects/{id}/entities/{kind}", handleEntityDetail(s))
	mux.HandleFunc("GET /api/usage", handleUsage(usagePoller))
	mux.HandleFunc("POST /api/usage/refresh", handleUsageRefresh(usagePoller))
	mux.HandleFunc("GET /api/settings", handleSettings(s, syncPoller))
	mux.HandleFunc("POST /api/settings/areas", handleAddDataArea(s, syncPoller))
	mux.HandleFunc("DELETE /api/settings/areas/{id}", handleRemoveDataArea(s, syncPoller))
	mux.HandleFunc("POST /api/settings/areas/{id}/sync", handleSyncDataArea(syncPoller))
	mux.HandleFunc("GET /api/settings/app", handleGetAppSettings(s))
	mux.HandleFunc("PUT /api/settings/app", handleSaveAppSettings(s, syncPoller))
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
			items, err := query.New(area.store.Root).ListProjects(filterFromQuery(r))
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
			items, err := query.New(area.store.Root).Sessions(r.PathValue("id"), filterFromQuery(r))
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
func filterFromQuery(r *http.Request) query.Filter {
	filter := query.Filter{Provider: r.URL.Query().Get("provider")}
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
		series := make([][]query.DailyPoint, 0, len(areas))
		for _, area := range areas {
			points, err := query.New(area.store.Root).Trend("", filterFromQuery(r))
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
		series := make([][]query.DailyPoint, 0, len(areas))
		for _, area := range areas {
			points, err := query.New(area.store.Root).Trend(r.PathValue("id"), filterFromQuery(r))
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
func mergeDailyPoints(series [][]query.DailyPoint) []query.DailyPoint {
	byDay := map[string]*query.DailyPoint{}
	order := make([]string, 0, len(series))
	for _, points := range series {
		for _, p := range points {
			existing := byDay[p.Date]
			if existing == nil {
				existing = &query.DailyPoint{Date: p.Date, EstimatedCost: []cost.CurrencyTotal{}}
				byDay[p.Date] = existing
				order = append(order, p.Date)
			}
			existing.TotalTokens += p.TotalTokens
			existing.EstimatedCost = mergeCurrencyTotals(existing.EstimatedCost, p.EstimatedCost)
			existing.CostMissing = existing.CostMissing || p.CostMissing
			existing.UsageMissing = existing.UsageMissing || p.UsageMissing
		}
	}
	sort.Strings(order) // "YYYY-MM-DD" sorts lexicographically == chronologically
	merged := make([]query.DailyPoint, 0, len(order))
	for _, day := range order {
		merged = append(merged, *byDay[day])
	}
	return merged
}

// breakdownDimensionFromQuery reads ?dimension=provider|model|tokenType,
// defaulting to provider (Breakdown's own zero-value fallback too) for any
// missing or unrecognized value rather than erroring — a bad or stale value
// degrades to the default view instead of breaking the page.
func breakdownDimensionFromQuery(r *http.Request) query.BreakdownDimension {
	switch query.BreakdownDimension(r.URL.Query().Get("dimension")) {
	case query.BreakdownModel:
		return query.BreakdownModel
	case query.BreakdownTokenType:
		return query.BreakdownTokenType
	default:
		return query.BreakdownProvider
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
		results := make([]query.TrendBreakdown, 0, len(areas))
		for _, area := range areas {
			tb, err := query.New(area.store.Root).Breakdown("", filterFromQuery(r), dimension)
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
		results := make([]query.TrendBreakdown, 0, len(areas))
		for _, area := range areas {
			tb, err := query.New(area.store.Root).Breakdown(r.PathValue("id"), filterFromQuery(r), dimension)
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
func mergeBreakdowns(results []query.TrendBreakdown) query.TrendBreakdown {
	if len(results) == 0 {
		return query.TrendBreakdown{Dates: []string{}, Series: []query.CategorySeries{}}
	}

	byCategory := map[string]map[string]*query.DailyPoint{}
	order := make([]string, 0, len(results[0].Series))
	for _, tb := range results {
		for _, series := range tb.Series {
			days, ok := byCategory[series.Key]
			if !ok {
				days = map[string]*query.DailyPoint{}
				byCategory[series.Key] = days
				order = append(order, series.Key)
			}
			for _, p := range series.Points {
				existing, ok := days[p.Date]
				if !ok {
					existing = &query.DailyPoint{Date: p.Date, EstimatedCost: []cost.CurrencyTotal{}}
					days[p.Date] = existing
				}
				existing.TotalTokens += p.TotalTokens
				existing.EstimatedCost = mergeCurrencyTotals(existing.EstimatedCost, p.EstimatedCost)
				existing.CostMissing = existing.CostMissing || p.CostMissing
				existing.UsageMissing = existing.UsageMissing || p.UsageMissing
			}
		}
	}

	dates := results[0].Dates
	merged := make([]query.CategorySeries, 0, len(order))
	for _, key := range order {
		days := byCategory[key]
		points := make([]query.DailyPoint, 0, len(dates))
		for _, day := range dates {
			if p, ok := days[day]; ok {
				points = append(points, *p)
			} else {
				points = append(points, query.DailyPoint{Date: day, EstimatedCost: []cost.CurrencyTotal{}})
			}
		}
		merged = append(merged, query.CategorySeries{Key: key, Points: points})
	}
	if merged == nil {
		merged = []query.CategorySeries{}
	}
	return query.TrendBreakdown{Dates: dates, Series: merged}
}

func mergeCurrencyTotals(left, right []cost.CurrencyTotal) []cost.CurrencyTotal {
	values := map[string]*big.Rat{}
	for _, item := range append(append([]cost.CurrencyTotal{}, left...), right...) {
		value, ok := new(big.Rat).SetString(item.Amount)
		if !ok {
			continue
		}
		if values[item.Currency] == nil {
			values[item.Currency] = new(big.Rat)
		}
		values[item.Currency].Add(values[item.Currency], value)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]cost.CurrencyTotal, 0, len(keys))
	for _, key := range keys {
		s := values[key].FloatString(12)
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
		if s == "" {
			s = "0"
		}
		result = append(result, cost.CurrencyTotal{Currency: key, Amount: s})
	}
	return result
}

func handleSessionDetail(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		areas, err := selectedDashboardAreas(s, r.URL.Query().Get("area"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		for _, area := range areas {
			detail, err := query.New(area.store.Root).SessionView(r.PathValue("id"), r.PathValue("provider"), r.PathValue("sessionId"))
			if err == nil {
				writeJSON(w, http.StatusOK, detail)
				return
			}
			if !errors.Is(err, query.ErrNotFound) {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		}
		writeError(w, http.StatusNotFound, query.ErrNotFound)
	}
}

func handleProjectDetail(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		areas, err := selectedDashboardAreas(s, r.URL.Query().Get("area"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		for _, area := range areas {
			detail, err := query.New(area.store.Root).ProjectDetail(r.PathValue("id"), filterFromQuery(r))
			if err == nil {
				writeJSON(w, http.StatusOK, detail)
				return
			}
			if !errors.Is(err, query.ErrNotFound) {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		}
		writeError(w, http.StatusNotFound, query.ErrNotFound)
	}
}

func handlePeriods(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		areas, err := selectedDashboardAreas(s, r.URL.Query().Get("area"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		for _, area := range areas {
			periods, err := query.New(area.store.Root).Periods(r.PathValue("id"))
			if err == nil {
				writeJSON(w, http.StatusOK, periods)
				return
			}
			if !errors.Is(err, query.ErrNotFound) {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		}
		writeError(w, http.StatusNotFound, query.ErrNotFound)
	}
}

func handleCanonicalSessionDetail(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		areas, err := selectedDashboardAreas(s, r.URL.Query().Get("area"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		for _, area := range areas {
			detail, err := query.New(area.store.Root).SessionDetail(r.PathValue("id"), r.PathValue("provider"), r.PathValue("sessionId"), filterFromQuery(r))
			if err == nil {
				writeJSON(w, http.StatusOK, detail)
				return
			}
			if !errors.Is(err, query.ErrNotFound) {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		}
		writeError(w, http.StatusNotFound, query.ErrNotFound)
	}
}

func handleRunDetail(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		areas, err := selectedDashboardAreas(s, r.URL.Query().Get("area"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		for _, area := range areas {
			detail, err := query.New(area.store.Root).RunDetail(r.PathValue("id"), r.PathValue("provider"), r.PathValue("sessionId"), r.PathValue("runId"))
			if err == nil {
				writeJSON(w, http.StatusOK, detail)
				return
			}
			if !errors.Is(err, query.ErrNotFound) {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		}
		writeError(w, http.StatusNotFound, query.ErrNotFound)
	}
}

func handleEntityDetail(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind := model.EntityKind(r.PathValue("kind"))
		if kind == "call" {
			kind = model.EntityModelCall
		}
		if kind == "instruction" {
			kind = model.EntityInstruction
		}
		if kind != model.EntityInstruction && kind != model.EntityModelCall {
			writeError(w, http.StatusBadRequest, errors.New("kind must be instruction or model_call"))
			return
		}
		areas, err := selectedDashboardAreas(s, r.URL.Query().Get("area"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		for _, area := range areas {
			namespace, entityID := r.PathValue("namespace"), r.PathValue("entityId")
			if namespace == "" {
				namespace = r.URL.Query().Get("namespace")
			}
			if entityID == "" {
				entityID = r.URL.Query().Get("entityId")
			}
			if namespace == "" || entityID == "" {
				writeError(w, http.StatusBadRequest, errors.New("namespace and entityId are required"))
				return
			}
			detail, err := query.New(area.store.Root).Entity(r.PathValue("id"), kind, namespace, entityID, filterFromQuery(r))
			if err == nil {
				writeJSON(w, http.StatusOK, detail)
				return
			}
			if !errors.Is(err, query.ErrNotFound) {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		}
		writeError(w, http.StatusNotFound, query.ErrNotFound)
	}
}

type collectionStatus struct {
	DataAreaID   string             `json:"dataAreaId"`
	DataAreaName string             `json:"dataAreaName"`
	Report       query.DoctorReport `json:"report"`
}

func handleCollectionStatus(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		areas, err := selectedDashboardAreas(s, r.URL.Query().Get("area"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		result := make([]collectionStatus, 0, len(areas))
		for _, area := range areas {
			report, err := query.New(area.store.Root).CachedDoctor()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			result = append(result, collectionStatus{DataAreaID: area.ID, DataAreaName: area.Name, Report: report})
		}
		writeJSON(w, http.StatusOK, result)
	}
}

type dashboardArea struct {
	store.DataArea
	store *store.Store
}

type projectSummary struct {
	query.ProjectSummary
	DataAreaID   string `json:"dataAreaId"`
	DataAreaName string `json:"dataAreaName"`
}

type sessionSummary struct {
	query.SessionSummary
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

// settingsResponse's SyncStatus is only ever populated for SSH areas (see
// store.DataArea.IsSSH) and only once syncPoller has actually run a cycle
// for that area — a brand-new area with no status yet, or any request
// served with a nil syncPoller, simply has no entry for that area's id,
// which the frontend reads as "not synced yet" rather than an error.
type settingsResponse struct {
	DataAreas  []store.DataArea        `json:"dataAreas"`
	SyncStatus []remotesync.AreaStatus `json:"syncStatus,omitempty"`
}

func settingsResponseFor(s *store.Store, syncPoller remoteSyncReader) (settingsResponse, error) {
	areas, err := s.ListDataAreas()
	if err != nil {
		return settingsResponse{}, err
	}
	resp := settingsResponse{DataAreas: areas}
	if syncPoller != nil {
		resp.SyncStatus = syncPoller.Statuses()
	}
	return resp, nil
}

func handleSettings(s *store.Store, syncPoller remoteSyncReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp, err := settingsResponseFor(s, syncPoller)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// handleAddDataArea adds either a local (existing behavior: name+path) or
// an SSH (kind:"ssh", name+sshHost+remotePath — see store.DataArea's
// SSHHost/RemotePath doc comments) data area. An SSH area additionally gets
// an immediate synchronous first sync (via syncPoller, when non-nil) right
// here, before responding: a bad host/path is far more useful to learn
// right now, in direct response to the user's own action, than silently
// discovered on the next background cycle up to syncInterval later — a
// failed first sync rolls the just-added area back out rather than leaving
// a broken entry in the list.
func handleAddDataArea(s *store.Store, syncPoller remoteSyncReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Name       string `json:"name"`
			Kind       string `json:"kind"`
			Path       string `json:"path"`
			SSHHost    string `json:"sshHost"`
			RemotePath string `json:"remotePath"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		if input.Kind != store.DataAreaKindSSH {
			area, err := s.AddDataArea(strings.TrimSpace(input.Name), strings.TrimSpace(input.Path))
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusCreated, area)
			return
		}

		area, err := s.AddSSHDataArea(strings.TrimSpace(input.Name), strings.TrimSpace(input.SSHHost), strings.TrimSpace(input.RemotePath))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if syncPoller != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
			status, err := syncPoller.SyncNow(ctx, area.ID)
			cancel()
			if err != nil || status.LastError != "" {
				_ = s.RemoveDataArea(area.ID) // don't leave a known-broken area behind
				msg := err
				if msg == nil {
					msg = errors.New(status.LastError)
				}
				writeError(w, http.StatusBadRequest, fmt.Errorf("added, but the first sync failed (removed again): %w", msg))
				return
			}
		}
		writeJSON(w, http.StatusCreated, area)
	}
}

// handleSyncDataArea triggers an immediate sync of one SSH area (the
// settings page's "sync now" button), outside the regular interval.
func handleSyncDataArea(syncPoller remoteSyncReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if syncPoller == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("remote sync is not enabled"))
			return
		}
		status, err := syncPoller.SyncNow(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

// handleGetAppSettings serves maatplug-web's own settings (as opposed to
// data areas — see the "データ"/"設定" nav split): today just the remote
// sync interval, read from persisted storage (store.GetAppSettings fills in
// the default when unconfigured, so this never reports a zero interval).
func handleGetAppSettings(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		settings, err := s.GetAppSettings()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, settings)
	}
}

// handleSaveAppSettings validates and persists new app settings, and — when
// syncPoller is non-nil — applies the new remote-sync interval to the
// already-running poller immediately (see remotesync.Poller.SetInterval),
// so a change here takes effect without restarting maatplug-web.
func handleSaveAppSettings(s *store.Store, syncPoller remoteSyncReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input store.AppSettings
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := s.SaveAppSettings(input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if syncPoller != nil {
			syncPoller.SetInterval(time.Duration(input.RemoteSyncIntervalMinutes) * time.Minute)
		}
		writeJSON(w, http.StatusOK, input)
	}
}

func handleRemoveDataArea(s *store.Store, syncPoller remoteSyncReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.RemoveDataArea(r.PathValue("id")); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeError(w, http.StatusNotFound, err)
				return
			}
			writeError(w, http.StatusBadRequest, err)
			return
		}
		resp, err := settingsResponseFor(s, syncPoller)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
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
