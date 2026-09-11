package aggregate

import (
	"path/filepath"
	"sort"

	"maatplug/internal/model"
	"maatplug/internal/store"
)

// BreakdownDimension selects which axis Breakdown splits a trend into.
type BreakdownDimension string

const (
	BreakdownProvider  BreakdownDimension = "provider"
	BreakdownModel     BreakdownDimension = "model"
	BreakdownTokenType BreakdownDimension = "tokenType"
)

// breakdownMaxCategories caps how many distinct categories Breakdown ever
// returns for BreakdownModel (the only dimension with an unbounded category
// count — Provider and TokenType are small fixed sets). Past this many, the
// smallest are folded into a single "other" category rather than seating an
// ever-growing legend — see dataviz's series-count ladder: past ~7-8,
// generating more categorical hues stops being distinguishable anyway.
const breakdownMaxCategories = 7

// breakdownOtherKey is the folded-tail category's key for BreakdownModel.
const breakdownOtherKey = "other"

// CategorySeries is one category's daily totals within a Breakdown
// response, aligned index-for-index with TrendBreakdown.Dates.
type CategorySeries struct {
	Key    string       `json:"key"`
	Points []DailyPoint `json:"points"`
}

// TrendBreakdown is Trend's data split into stacked categories along one
// dimension, for the web dashboard's graph detail page.
type TrendBreakdown struct {
	Dates  []string         `json:"dates"`
	Series []CategorySeries `json:"series"`
}

// Breakdown buckets recorded activity by day AND by category along
// dimension, restricted to activity matching filter (see Filter's doc
// comment) — the same window and event-level day-bucketing as Trend, split
// further into named categories a stacked chart can plot directly.
//
//   - BreakdownProvider: "claude" / "codex", in that fixed order (matching
//     the rest of the dashboard); a provider absent from every matching
//     event (e.g. filter.Provider already narrowed to the other one) is
//     omitted rather than included as an all-zero series.
//   - BreakdownTokenType: "in" / "cache" / "out", always all three in that
//     fixed order (cache = cacheRead+cacheCreation combined, matching
//     tokenBreakdown's IN/Cache/OUT display elsewhere) — each turn_end
//     contributes to all three simultaneously, unlike Provider/Model where
//     it belongs to exactly one category. Per-bucket cost is computed
//     against each bucket's own rate, not divided out of the turn's total.
//   - BreakdownModel: the model id turn_end recorded ("unknown" if empty),
//     ranked by total tokens across the window and capped at
//     breakdownMaxCategories — the rest fold into a shared "other" series
//     (see breakdownMaxCategories's doc comment).
func Breakdown(s *store.Store, projectID string, filter Filter, dimension BreakdownDimension) (TrendBreakdown, error) {
	since, _, dates := trendWindow(filter)

	projectIDs, err := trendProjectIDs(s, projectID)
	if err != nil {
		return TrendBreakdown{}, err
	}

	pricingDoc := loadPricing()
	byCategory := map[string]map[string]*DailyPoint{} // category -> day -> point
	add := func(category, day string, tokens int, cost float64, costMissing bool) {
		days, ok := byCategory[category]
		if !ok {
			days = map[string]*DailyPoint{}
			byCategory[category] = days
		}
		point, ok := days[day]
		if !ok {
			point = &DailyPoint{Date: day}
			days[day] = point
		}
		point.TotalTokens += tokens
		point.CostUSD += cost
		point.CostMissing = point.CostMissing || costMissing
	}

	for _, id := range projectIDs {
		files, err := s.ListSessionFiles(id)
		if err != nil {
			return TrendBreakdown{}, err
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
				return TrendBreakdown{}, err
			}
			for _, ev := range events {
				if ev.Type != model.EventTurnEnd || ev.Abandoned || ev.MergedInto != "" || ev.TokensUnavailable {
					continue
				}
				if ev.Timestamp.Before(since) {
					continue
				}
				day := ev.Timestamp.Local().Format("2006-01-02") // see trendWindow's .Local() note

				switch dimension {
				case BreakdownTokenType:
					var inCost, cacheCost, outCost float64
					missing := false
					if rate, ok := pricingDoc.Lookup(provider, ev.Model); ok {
						inCost = rate.Cost(ev.Tokens.Input, 0, 0, 0)
						cacheCost = rate.Cost(0, ev.Tokens.CacheRead, ev.Tokens.CacheCreation, 0)
						outCost = rate.Cost(0, 0, 0, ev.Tokens.Output)
					} else {
						missing = true
					}
					add("in", day, ev.Tokens.Input, inCost, missing && ev.Tokens.Input != 0)
					add("cache", day, ev.Tokens.CacheRead+ev.Tokens.CacheCreation, cacheCost, missing && ev.Tokens.CacheRead+ev.Tokens.CacheCreation != 0)
					add("out", day, ev.Tokens.Output, outCost, missing && ev.Tokens.Output != 0)
				case BreakdownModel:
					key := ev.Model
					if key == "" {
						key = "unknown"
					}
					cost, priced := costFor(pricingDoc, provider, ev.Model, ev.Tokens)
					add(key, day, ev.Tokens.Total, cost, !priced && ev.Tokens.Total != 0)
				default: // BreakdownProvider
					cost, priced := costFor(pricingDoc, provider, ev.Model, ev.Tokens)
					add(provider, day, ev.Tokens.Total, cost, !priced && ev.Tokens.Total != 0)
				}
			}
		}
	}

	categories := orderBreakdownCategories(dimension, byCategory)
	series := make([]CategorySeries, 0, len(categories))
	for _, category := range categories {
		days := byCategory[category]
		points := make([]DailyPoint, 0, len(dates))
		for _, day := range dates {
			if point, ok := days[day]; ok {
				points = append(points, *point)
			} else {
				points = append(points, DailyPoint{Date: day})
			}
		}
		series = append(series, CategorySeries{Key: category, Points: points})
	}

	return TrendBreakdown{Dates: dates, Series: series}, nil
}

// orderBreakdownCategories decides which categories Breakdown returns, and
// in what order, given what it actually found in byCategory — see
// Breakdown's doc comment for the per-dimension rule.
func orderBreakdownCategories(dimension BreakdownDimension, byCategory map[string]map[string]*DailyPoint) []string {
	switch dimension {
	case BreakdownProvider:
		fixed := []string{"claude", "codex"}
		out := make([]string, 0, len(fixed))
		for _, key := range fixed {
			if _, ok := byCategory[key]; ok {
				out = append(out, key)
			}
		}
		return out
	case BreakdownTokenType:
		return []string{"in", "cache", "out"}
	default: // BreakdownModel
		return rankAndFoldModelCategories(byCategory)
	}
}

// rankAndFoldModelCategories ranks BreakdownModel's categories by total
// tokens across the whole window, descending, and folds every category past
// breakdownMaxCategories into one "other" bucket (summed day-by-day, not
// just dropped) — see breakdownMaxCategories's doc comment.
func rankAndFoldModelCategories(byCategory map[string]map[string]*DailyPoint) []string {
	type ranked struct {
		key   string
		total int
	}
	all := make([]ranked, 0, len(byCategory))
	for key, days := range byCategory {
		total := 0
		for _, point := range days {
			total += point.TotalTokens
		}
		all = append(all, ranked{key: key, total: total})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].total != all[j].total {
			return all[i].total > all[j].total
		}
		return all[i].key < all[j].key // stable tie-break, not insertion order
	})

	if len(all) <= breakdownMaxCategories {
		out := make([]string, len(all))
		for i, r := range all {
			out[i] = r.key
		}
		return out
	}

	kept := all[:breakdownMaxCategories]
	folded := all[breakdownMaxCategories:]
	other := map[string]*DailyPoint{}
	for _, r := range folded {
		for day, point := range byCategory[r.key] {
			existing, ok := other[day]
			if !ok {
				existing = &DailyPoint{Date: day}
				other[day] = existing
			}
			existing.CostUSD += point.CostUSD
			existing.TotalTokens += point.TotalTokens
			existing.CostMissing = existing.CostMissing || point.CostMissing
		}
	}
	byCategory[breakdownOtherKey] = other

	out := make([]string, 0, len(kept)+1)
	for _, r := range kept {
		out = append(out, r.key)
	}
	out = append(out, breakdownOtherKey)
	return out
}
