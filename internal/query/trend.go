package query

import (
	"math/big"
	"sort"
	"time"

	"maatplug/internal/cost"
	"maatplug/internal/journal"
	"maatplug/internal/model"
	"maatplug/internal/pricing"
	"maatplug/internal/reconcile"
	"maatplug/internal/store"
)

type DailyPoint struct {
	Date          string               `json:"date"`
	TotalTokens   int64                `json:"totalTokens"`
	EstimatedCost []cost.CurrencyTotal `json:"estimatedCost"`
	CostMissing   bool                 `json:"costMissing"`
	UsageMissing  bool                 `json:"usageMissing"`
}

type BreakdownDimension string

const (
	BreakdownProvider  BreakdownDimension = "provider"
	BreakdownModel     BreakdownDimension = "model"
	BreakdownTokenType BreakdownDimension = "tokenType"
)

type CategorySeries struct {
	Key    string       `json:"key"`
	Points []DailyPoint `json:"points"`
}
type TrendBreakdown struct {
	Dates  []string         `json:"dates"`
	Series []CategorySeries `json:"series"`
}

type usageRow struct {
	at              time.Time
	provider, model string
	tokens          *int64
	normalized      *model.NormalizedUsage
	price           *cost.Record
}
type pointAccumulator struct {
	point  DailyPoint
	totals map[string]*big.Rat
}

func (service *Service) Trend(projectID string, filter Filter) ([]DailyPoint, error) {
	filter, dates := service.trendWindow(filter)
	if filter.Provider == "" {
		// The common case (no provider filter) is answered entirely from
		// each project's catalog — precomputed once per collect cycle,
		// see catalog.applyProjectCostPeriods — instead of re-reading and
		// re-pricing every journal event on every dashboard load.
		return service.trendPointsFromCatalog(projectID, dates)
	}
	rows, unknown, err := service.usageRows(projectID, filter)
	if err != nil {
		return nil, err
	}
	byDay := map[string]*pointAccumulator{}
	for _, row := range rows {
		day := row.at.UTC().Format("2006-01-02")
		acc := ensurePoint(byDay, day)
		if row.tokens == nil {
			acc.point.UsageMissing = true
		} else {
			acc.point.TotalTokens += *row.tokens
		}
		addRecordCost(acc, row.price)
	}
	if unknown && len(dates) > 0 {
		ensurePoint(byDay, dates[len(dates)-1]).point.UsageMissing = true
	}
	result := make([]DailyPoint, 0, len(dates))
	for _, day := range dates {
		acc := ensurePoint(byDay, day)
		finishPoint(acc)
		result = append(result, acc.point)
	}
	return result, nil
}

// trendPointsFromCatalog answers Trend for the no-provider-filter case out
// of each project's catalog.Periods (tokens and active-pricing cost, both
// already attributed per day at collect time) rather than re-scanning raw
// journal events. projectID == "" merges every project in the store, the
// same scope usageRows uses.
func (service *Service) trendPointsFromCatalog(projectID string, dates []string) ([]DailyPoint, error) {
	ids := []string{projectID}
	if projectID == "" {
		var err error
		ids, err = (&store.Store{Root: service.Root}).ListProjectIDs()
		if err != nil {
			return nil, err
		}
	}
	byDay := map[string]*pointAccumulator{}
	unknownProject := false
	for _, id := range ids {
		paths, err := journal.New(service.Root).ListProjectStreams(id)
		if err != nil {
			return nil, err
		}
		if len(paths) == 0 {
			continue
		}
		projection, err := service.catalog(id)
		if err != nil {
			return nil, err
		}
		unknownProject = unknownProject || projection.UnknownUsage
		for _, period := range projection.Periods {
			acc := ensurePoint(byDay, period.Period)
			acc.point.TotalTokens += period.KnownUsageTokens
			if period.UsageMissing {
				acc.point.UsageMissing = true
			}
			if period.CostMissing {
				acc.point.CostMissing = true
			}
			for _, total := range period.EstimatedCost {
				addDecimal(acc, total.Currency, total.Amount)
			}
		}
	}
	if unknownProject && len(dates) > 0 {
		ensurePoint(byDay, dates[len(dates)-1]).point.UsageMissing = true
	}
	result := make([]DailyPoint, 0, len(dates))
	for _, day := range dates {
		acc := ensurePoint(byDay, day)
		finishPoint(acc)
		result = append(result, acc.point)
	}
	return result, nil
}

func (service *Service) Breakdown(projectID string, filter Filter, dimension BreakdownDimension) (TrendBreakdown, error) {
	filter, dates := service.trendWindow(filter)
	rows, _, err := service.usageRows(projectID, filter)
	if err != nil {
		return TrendBreakdown{}, err
	}
	byCategory := map[string]map[string]*pointAccumulator{}
	for _, row := range rows {
		day := row.at.UTC().Format("2006-01-02")
		switch dimension {
		case BreakdownTokenType:
			if row.normalized == nil {
				continue
			}
			for _, bucket := range row.normalized.ExclusiveBuckets {
				category := tokenCategory(bucket.Name)
				if category == "" {
					continue
				}
				acc := ensureCategoryPoint(byCategory, category, day)
				if bucket.Tokens.Value == nil {
					acc.point.UsageMissing = true
				} else {
					acc.point.TotalTokens += *bucket.Tokens.Value
				}
				addLineCost(acc, row.price, bucket.Name)
			}
		case BreakdownModel:
			category := row.model
			if category == "" {
				category = "unknown"
			}
			acc := ensureCategoryPoint(byCategory, category, day)
			addUsageRow(acc, row)
		default:
			category := row.provider
			if category == "" {
				category = "unknown"
			}
			acc := ensureCategoryPoint(byCategory, category, day)
			addUsageRow(acc, row)
		}
	}
	keys := orderedCategories(dimension, byCategory)
	series := make([]CategorySeries, 0, len(keys))
	for _, key := range keys {
		points := make([]DailyPoint, 0, len(dates))
		for _, day := range dates {
			acc := ensureCategoryPoint(byCategory, key, day)
			finishPoint(acc)
			points = append(points, acc.point)
		}
		series = append(series, CategorySeries{Key: key, Points: points})
	}
	return TrendBreakdown{Dates: dates, Series: series}, nil
}

func (service *Service) usageRows(projectID string, filter Filter) ([]usageRow, bool, error) {
	ids := []string{projectID}
	if projectID == "" {
		var err error
		ids, err = (&store.Store{Root: service.Root}).ListProjectIDs()
		if err != nil {
			return nil, false, err
		}
	}
	version, hasVersion, err := (pricing.VersionStore{Root: service.Root}).LoadActive()
	if err != nil {
		return nil, false, err
	}
	var selected *pricing.Version
	if hasVersion {
		selected = &version
	}
	rows := []usageRow{}
	unknown := false
	for _, id := range ids {
		paths, err := journal.New(service.Root).ListProjectStreams(id)
		if err != nil {
			return nil, false, err
		}
		events := []model.EventEnvelope{}
		for _, path := range paths {
			route, items, err := journal.New(service.Root).ReadPath(path)
			if err != nil {
				return nil, false, err
			}
			if filter.Provider != "" && route.Provider != filter.Provider {
				continue
			}
			events = append(events, filterEvents(items, filter)...)
		}
		if len(events) == 0 {
			continue
		}
		usage, err := reconcile.Build(events)
		if err != nil {
			return nil, false, err
		}
		unknown = unknown || usage.Unknown
		prices, err := cost.Build(events, usage, selected)
		if err != nil {
			return nil, false, err
		}
		eventByID := map[string]model.EventEnvelope{}
		for _, event := range events {
			eventByID[idKey(event.EventID)] = event
		}
		priceByUsage := map[string]*cost.Record{}
		for i := range prices.Records {
			priceByUsage[entityKey(prices.Records[i].Usage)] = &prices.Records[i]
		}
		for _, decision := range usage.Decisions {
			if decision.Role != reconcile.RoleCounted {
				continue
			}
			event := eventByID[idKey(decision.EventID)]
			record := priceByUsage[entityKey(decision.Usage)]
			provider, modelName := event.Source.Provider, ""
			if record != nil {
				if record.Provider != "" {
					provider = record.Provider
				}
				modelName = record.Model
			}
			rows = append(rows, usageRow{at: event.Time.ReceivedAt, provider: provider, model: modelName, tokens: decision.Tokens, normalized: decision.Normalized, price: record})
		}
	}
	return rows, unknown, nil
}

func (service *Service) trendWindow(filter Filter) (Filter, []string) {
	now := time.Now().UTC()
	if service.Now != nil {
		now = service.Now().UTC()
	}
	if filter.Since.IsZero() {
		filter.Since = now.AddDate(0, 0, -30)
	}
	if filter.Until.IsZero() {
		filter.Until = now.AddDate(0, 0, 1)
	}
	start := dayStart(filter.Since.UTC())
	end := dayStart(filter.Until.UTC())
	dates := []string{}
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		dates = append(dates, day.Format("2006-01-02"))
	}
	return filter, dates
}
func dayStart(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}
func idKey(id model.NamespacedID) string { return id.Namespace + "\x00" + id.Value }
func ensurePoint(values map[string]*pointAccumulator, day string) *pointAccumulator {
	if values[day] == nil {
		values[day] = &pointAccumulator{point: DailyPoint{Date: day, EstimatedCost: []cost.CurrencyTotal{}}, totals: map[string]*big.Rat{}}
	}
	return values[day]
}
func ensureCategoryPoint(values map[string]map[string]*pointAccumulator, category, day string) *pointAccumulator {
	if values[category] == nil {
		values[category] = map[string]*pointAccumulator{}
	}
	return ensurePoint(values[category], day)
}
func addUsageRow(acc *pointAccumulator, row usageRow) {
	if row.tokens == nil {
		acc.point.UsageMissing = true
	} else {
		acc.point.TotalTokens += *row.tokens
	}
	addRecordCost(acc, row.price)
}
func addRecordCost(acc *pointAccumulator, record *cost.Record) {
	if record == nil {
		acc.point.CostMissing = true
		return
	}
	for _, line := range record.Lines {
		if line.Amount == nil {
			if line.Quantity != 0 {
				acc.point.CostMissing = true
			}
			continue
		}
		addDecimal(acc, line.Currency, *line.Amount)
	}
	if record.Status != "calculated" && record.Status != "zero_usage" {
		acc.point.CostMissing = true
	}
}
func addLineCost(acc *pointAccumulator, record *cost.Record, usageClass string) {
	if record == nil {
		acc.point.CostMissing = true
		return
	}
	found := false
	for _, line := range record.Lines {
		if line.UsageClass != usageClass {
			continue
		}
		found = true
		if line.Amount == nil {
			if line.Quantity != 0 {
				acc.point.CostMissing = true
			}
		} else {
			addDecimal(acc, line.Currency, *line.Amount)
		}
	}
	if !found {
		acc.point.CostMissing = true
	}
}
func addDecimal(acc *pointAccumulator, currency, amount string) {
	value, ok := new(big.Rat).SetString(amount)
	if !ok || currency == "" {
		acc.point.CostMissing = true
		return
	}
	if acc.totals[currency] == nil {
		acc.totals[currency] = new(big.Rat)
	}
	acc.totals[currency].Add(acc.totals[currency], value)
}
func finishPoint(acc *pointAccumulator) {
	keys := make([]string, 0, len(acc.totals))
	for key := range acc.totals {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	acc.point.EstimatedCost = acc.point.EstimatedCost[:0]
	for _, key := range keys {
		acc.point.EstimatedCost = append(acc.point.EstimatedCost, cost.CurrencyTotal{Currency: key, Amount: formatRat(acc.totals[key])})
	}
}
func tokenCategory(name string) string {
	switch name {
	case "input", "input_uncached":
		return "in"
	case "cache_read_input", "cache_write_input":
		return "cache"
	case "output":
		return "out"
	}
	return ""
}
func orderedCategories(dimension BreakdownDimension, values map[string]map[string]*pointAccumulator) []string {
	if dimension == BreakdownTokenType {
		return []string{"in", "cache", "out"}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if dimension == BreakdownProvider {
			rank := func(v string) int {
				if v == "claude" {
					return 0
				}
				if v == "codex" {
					return 1
				}
				return 2
			}
			if rank(keys[i]) != rank(keys[j]) {
				return rank(keys[i]) < rank(keys[j])
			}
		}
		return keys[i] < keys[j]
	})
	return keys
}
