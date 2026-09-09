package aggregate

import (
	"path/filepath"
	"time"

	"maatplug/internal/model"
	"maatplug/internal/store"
)

// trendDefaultWindowDays is how far back Trend looks when filter.Since is
// unset ("all time") — an unbounded trend isn't a meaningful chart, so
// "all time" still gets a bounded, recent window rather than every day a
// project has ever existed.
const trendDefaultWindowDays = 30

// DailyPoint is one day's totals, for the web dashboard's cost/token trend
// chart (Date is "YYYY-MM-DD", in the server's local time zone).
type DailyPoint struct {
	Date        string  `json:"date"`
	CostUSD     float64 `json:"costUsd"`
	TotalTokens int     `json:"totalTokens"`
}

// Trend buckets recorded activity by day for the web dashboard's cost/token
// trend chart, restricted to activity matching filter (see Filter's doc
// comment). projectID scopes to one project; empty means every project the
// store knows about (mirroring All's exclusion of the usage-check probe
// directory). The returned series is one point per calendar day from the
// window's start through today, in order, with zero-filled days for dates
// with no matching activity — a chart can plot it directly without having
// to reconstruct the date axis itself. Unlike Project/Sessions (which
// decide per-SESSION using its start time), Trend buckets per-EVENT using
// each turn_end's own timestamp, since a day's totals should reflect
// activity that actually happened that day even inside a longer-running
// session.
func Trend(s *store.Store, projectID string, filter Filter) ([]DailyPoint, error) {
	since, _, dates := trendWindow(filter)

	projectIDs, err := trendProjectIDs(s, projectID)
	if err != nil {
		return nil, err
	}

	pricingDoc := loadPricing()
	byDay := map[string]*DailyPoint{}
	for _, id := range projectIDs {
		files, err := s.ListSessionFiles(id)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			provider, _, ok := store.ParseSessionFileName(filepath.Base(f))
			if !ok {
				continue // not a session file this store wrote; ignore rather than fail the whole series
			}
			if !filter.matchesProvider(provider) {
				continue
			}

			events, err := store.ReadEvents(f)
			if err != nil {
				return nil, err
			}
			for _, ev := range events {
				if ev.Type != model.EventTurnEnd || ev.Abandoned || ev.MergedInto != "" || ev.TokensUnavailable {
					continue
				}
				if ev.Timestamp.Before(since) {
					continue
				}
				// Local() matters here: ev.Timestamp round-trips through
				// JSON with whatever zone offset it was written with, which
				// bucketing by calendar day must not use directly — since/
				// today (below) are always Local, so an event recorded a
				// few hours either side of midnight in a different offset
				// would otherwise land one day off from where a viewer
				// reading the chart on their own local clock expects it.
				day := ev.Timestamp.Local().Format("2006-01-02")
				point := byDay[day]
				if point == nil {
					point = &DailyPoint{Date: day}
					byDay[day] = point
				}
				point.TotalTokens += ev.Tokens.Total
				if cost, ok := costFor(pricingDoc, provider, ev.Model, ev.Tokens); ok {
					point.CostUSD += cost
				}
			}
		}
	}

	points := make([]DailyPoint, 0, len(dates))
	for _, day := range dates {
		if point, ok := byDay[day]; ok {
			points = append(points, *point)
		} else {
			points = append(points, DailyPoint{Date: day})
		}
	}
	return points, nil
}

// trendWindow resolves filter.Since into the bounded [since, today] range
// Trend and Breakdown both plot (see trendDefaultWindowDays), and the list
// of "YYYY-MM-DD" dates it spans in order — the shared date axis both
// functions zero-fill against.
func trendWindow(filter Filter) (since, today time.Time, dates []string) {
	since = filter.Since
	if since.IsZero() {
		since = time.Now().AddDate(0, 0, -trendDefaultWindowDays)
	}
	// Local() matters here: ev.Timestamp (compared against since below)
	// round-trips through JSON with whatever zone offset it was written
	// with. Bucketing by calendar day must not use that directly — since/
	// today must agree with the zone events get formatted in (also
	// .Local(), in Trend/Breakdown), or an event recorded a few hours
	// either side of midnight in a different offset would land one day off
	// from where a viewer reading the chart on their own local clock
	// expects it.
	since = truncateToDate(since.Local())
	today = truncateToDate(time.Now())

	dates = make([]string, 0, int(today.Sub(since).Hours()/24)+1)
	for d := since; !d.After(today); d = d.AddDate(0, 0, 1) {
		dates = append(dates, d.Format("2006-01-02"))
	}
	return since, today, dates
}

// trendProjectIDs resolves Trend's projectID argument: either the one given
// project, or (when empty) every real project in the store, excluding the
// usage-check probe the same way All does.
func trendProjectIDs(s *store.Store, projectID string) ([]string, error) {
	if projectID != "" {
		return []string{projectID}, nil
	}
	ids, err := s.ListProjectIDs()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		_, path, err := s.ProjectMeta(id)
		if err != nil {
			return nil, err
		}
		if isUnderStoreRoot(s.Root, path) {
			continue
		}
		out = append(out, id)
	}
	return out, nil
}

func truncateToDate(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
