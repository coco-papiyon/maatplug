// Package catalog builds disposable query projections from journal streams.
package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"maatplug/internal/attribution"
	"maatplug/internal/billing"
	"maatplug/internal/cost"
	"maatplug/internal/inputcomposition"
	"maatplug/internal/journal"
	"maatplug/internal/model"
	"maatplug/internal/operation"
	"maatplug/internal/pricing"
	"maatplug/internal/reconcile"
	"maatplug/internal/store"
)

const (
	SchemaVersion      = 3
	AggregationVersion = 2
)

type InputCursor struct {
	JournalPath    string             `json:"journalPath"`
	LastByteOffset int64              `json:"lastByteOffset"`
	EventCount     uint64             `json:"eventCount"`
	LastEventID    model.NamespacedID `json:"lastEventId,omitempty"`
}

type PeriodSummary struct {
	Period           string               `json:"period"`
	EventCount       uint64               `json:"eventCount"`
	KnownUsageTokens int64                `json:"knownUsageTokens"`
	UnknownUsage     bool                 `json:"unknownUsage"`
	UsageMissing     bool                 `json:"usageMissing,omitempty"`
	EstimatedCost    []cost.CurrencyTotal `json:"estimatedCost,omitempty"`
	CostMissing      bool                 `json:"costMissing,omitempty"`
}

type RunSummary struct {
	SchemaVersion      int                                 `json:"schemaVersion"`
	AggregationVersion int                                 `json:"aggregationVersion"`
	ProjectID          string                              `json:"projectId"`
	Provider           string                              `json:"provider"`
	SessionID          string                              `json:"sessionId"`
	RunID              string                              `json:"runId"`
	EventCount         uint64                              `json:"eventCount"`
	InstructionCount   uint64                              `json:"instructionCount"`
	ModelCallCount     uint64                              `json:"modelCallCount"`
	OperationCount     uint64                              `json:"operationCount"`
	ArtifactCount      uint64                              `json:"artifactCount"`
	KnownUsageTokens   int64                               `json:"knownUsageTokens"`
	UnknownUsage       bool                                `json:"unknownUsage"`
	SelectedEventIDs   []model.NamespacedID                `json:"selectedEventIds,omitempty"`
	ExcludedEventIDs   []model.NamespacedID                `json:"excludedEventIds,omitempty"`
	OperationState     string                              `json:"operationState,omitempty"`
	Availability       map[string]model.AvailabilityStatus `json:"availability,omitempty"`
	FirstReceivedAt    time.Time                           `json:"firstReceivedAt,omitempty"`
	LastReceivedAt     time.Time                           `json:"lastReceivedAt,omitempty"`
	Periods            []PeriodSummary                     `json:"periods,omitempty"`
	Input              InputCursor                         `json:"input"`
}

type Session struct {
	Provider    string         `json:"provider"`
	SessionID   string         `json:"sessionId"`
	Runs        []RunSummary   `json:"runs"`
	Derivations SessionDerived `json:"derivations"`
}

// SessionDerived is the same shape of aggregate a Web session-list view
// would otherwise have to recompute live from raw journal events on every
// request (see query.Service.derive). Unlike DerivedSummary (project-level),
// it only carries the active attribution policy and active pricing version
// — a session list never shows alternate versions, so there is no reason to
// pay for computing them here.
type SessionDerived struct {
	Usage            reconcile.Result          `json:"usage"`
	InputComposition inputcomposition.Result   `json:"inputComposition"`
	Attribution      attribution.Result        `json:"attribution"`
	Cost             cost.Result               `json:"cost"`
	Operations       operation.Result          `json:"operations"`
	ActualSpend      billing.Summary           `json:"actualSpend"`
	Missing          []model.FieldAvailability `json:"missing"`
}

type ProjectCatalog struct {
	SchemaVersion      int             `json:"schemaVersion"`
	AggregationVersion int             `json:"aggregationVersion"`
	ProjectID          string          `json:"projectId"`
	BuiltFromAt        time.Time       `json:"builtFromAt,omitempty"`
	KnownUsageTokens   int64           `json:"knownUsageTokens"`
	UnknownUsage       bool            `json:"unknownUsage"`
	UndecomposedTokens int64           `json:"undecomposedTokens"`
	Inputs             []InputCursor   `json:"inputs"`
	Sessions           []Session       `json:"sessions"`
	Periods            []PeriodSummary `json:"periods,omitempty"`
	Derivations        DerivedSummary  `json:"derivations"`
}

type AttributionVersion struct {
	PolicyVersion  string                `json:"policyVersion"`
	BasedOn        string                `json:"basedOn,omitempty"`
	Result         attribution.Result    `json:"result"`
	ChangeFromBase attribution.ChangeSet `json:"changeFromBase"`
}

type DerivedSummary struct {
	Usage               reconcile.Result        `json:"usage"`
	InputComposition    inputcomposition.Result `json:"inputComposition"`
	ActivePolicyVersion string                  `json:"activePolicyVersion"`
	ActiveAttribution   attribution.Result      `json:"activeAttribution"`
	AttributionVersions []AttributionVersion    `json:"attributionVersions"`
	ActiveCost          cost.Result             `json:"activeCost"`
	CostVersions        []CostVersion           `json:"costVersions"`
	Operations          operation.Result        `json:"operations"`
}

type CostVersion struct {
	PricingVersion string         `json:"pricingVersion"`
	BasedOn        string         `json:"basedOn,omitempty"`
	Result         cost.Result    `json:"result"`
	ChangeFromBase cost.ChangeSet `json:"changeFromBase"`
}

type Builder struct {
	Root    string
	Journal *journal.Journal
}

func New(root string) *Builder {
	return &Builder{Root: root, Journal: journal.New(root)}
}

func (builder *Builder) RunSummaryPath(projectID, provider, sessionID, runID string) string {
	layout := &store.Store{Root: builder.Root}
	return filepath.Join(layout.ProjectSummariesDir(projectID), "runs", summaryName(provider, sessionID, runID))
}

// RebuildProject derives every catalog artifact for a project from journals.
// The result contains no wall-clock generation time, so identical journals
// produce identical JSON after a catalog is deleted and rebuilt.
func (builder *Builder) RebuildProject(projectID string) (ProjectCatalog, error) {
	layout := &store.Store{Root: builder.Root}
	var result ProjectCatalog
	err := store.WithFileLock(layout.ProjectCatalogPath(projectID), func() error {
		var err error
		result, err = builder.rebuildProject(projectID)
		return err
	})
	return result, err
}

func (builder *Builder) rebuildProject(projectID string) (ProjectCatalog, error) {
	paths, err := builder.Journal.ListProjectStreams(projectID)
	if err != nil {
		return ProjectCatalog{}, err
	}
	catalog := ProjectCatalog{
		SchemaVersion: SchemaVersion, AggregationVersion: AggregationVersion,
		ProjectID: projectID, Inputs: []InputCursor{}, Sessions: []Session{},
	}
	runs := make([]RunSummary, 0, len(paths))
	allEvents := []model.EventEnvelope{}
	sessionEvents := map[string][]model.EventEnvelope{}
	for _, path := range paths {
		route, events, err := builder.Journal.ReadPath(path)
		if err != nil {
			return ProjectCatalog{}, fmt.Errorf("read %s: %w", path, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return ProjectCatalog{}, err
		}
		rel, err := filepath.Rel(builder.Root, path)
		if err != nil {
			return ProjectCatalog{}, err
		}
		input := InputCursor{JournalPath: filepath.ToSlash(rel), LastByteOffset: info.Size(), EventCount: uint64(len(events))}
		if len(events) > 0 {
			input.LastEventID = events[len(events)-1].EventID
		}
		allEvents = append(allEvents, events...)
		sessionKey := route.Provider + "\x00" + route.SessionID
		sessionEvents[sessionKey] = append(sessionEvents[sessionKey], events...)
		run, err := summarizeRun(route, events, input)
		if err != nil {
			return ProjectCatalog{}, fmt.Errorf("summarize %s: %w", path, err)
		}
		runs = append(runs, run)
		catalog.Inputs = append(catalog.Inputs, input)
		if run.LastReceivedAt.After(catalog.BuiltFromAt) {
			catalog.BuiltFromAt = run.LastReceivedAt
		}
	}

	sort.Slice(runs, func(i, k int) bool {
		if runs[i].Provider != runs[k].Provider {
			return runs[i].Provider < runs[k].Provider
		}
		if runs[i].SessionID != runs[k].SessionID {
			return runs[i].SessionID < runs[k].SessionID
		}
		return runs[i].RunID < runs[k].RunID
	})
	for _, run := range runs {
		index := -1
		for i := range catalog.Sessions {
			if catalog.Sessions[i].Provider == run.Provider && catalog.Sessions[i].SessionID == run.SessionID {
				index = i
				break
			}
		}
		if index < 0 {
			catalog.Sessions = append(catalog.Sessions, Session{Provider: run.Provider, SessionID: run.SessionID, Runs: []RunSummary{run}})
		} else {
			catalog.Sessions[index].Runs = append(catalog.Sessions[index].Runs, run)
		}
	}
	catalog.Periods = mergePeriods(runs)
	usageProjection, err := reconcile.Build(allEvents)
	if err != nil {
		return ProjectCatalog{}, fmt.Errorf("reconcile project usage: %w", err)
	}
	catalog.KnownUsageTokens = usageProjection.KnownSubtotal
	catalog.UnknownUsage = usageProjection.Unknown
	catalog.UndecomposedTokens = usageProjection.Undecomposed
	applyProjectUsagePeriods(catalog.Periods, allEvents, usageProjection)
	inputProjection, err := inputcomposition.Build(allEvents, usageProjection)
	if err != nil {
		return ProjectCatalog{}, fmt.Errorf("build input composition: %w", err)
	}
	policyStore := attribution.PolicyStore{Root: builder.Root}
	activePolicy, err := policyStore.LoadActive(projectID)
	if err != nil {
		return ProjectCatalog{}, fmt.Errorf("load active attribution policy: %w", err)
	}
	policies, err := policyStore.List(projectID)
	if err != nil {
		return ProjectCatalog{}, fmt.Errorf("list attribution policies: %w", err)
	}
	defaultPolicy := attribution.Policy{Version: attribution.DefaultPolicyVersion}
	allPolicies := []attribution.Policy{defaultPolicy}
	for _, policy := range policies {
		if policy.Version != attribution.DefaultPolicyVersion {
			allPolicies = append(allPolicies, policy)
		}
	}
	projectedByPolicy := make(map[string]attribution.Result, len(allPolicies))
	for _, policy := range allPolicies {
		projected, err := attribution.Build(allEvents, usageProjection, policy)
		if err != nil {
			return ProjectCatalog{}, fmt.Errorf("build attribution policy %s: %w", policy.Version, err)
		}
		projectedByPolicy[policy.Version] = projected
	}
	activeAttribution := projectedByPolicy[attribution.DefaultPolicyVersion]
	versions := make([]AttributionVersion, 0, len(allPolicies))
	for _, policy := range allPolicies {
		projected := projectedByPolicy[policy.Version]
		baseVersion := policy.BasedOn
		if baseVersion == "" && policy.Version != attribution.DefaultPolicyVersion {
			baseVersion = attribution.DefaultPolicyVersion
		}
		base := projected
		if baseVersion != "" {
			var ok bool
			base, ok = projectedByPolicy[baseVersion]
			if !ok {
				return ProjectCatalog{}, fmt.Errorf("attribution policy %s is based on missing policy %s", policy.Version, baseVersion)
			}
		}
		versions = append(versions, AttributionVersion{PolicyVersion: policy.Version, BasedOn: baseVersion, Result: projected, ChangeFromBase: attribution.Compare(base, projected)})
		if policy.Version == activePolicy.Version {
			activeAttribution = projected
		}
	}
	activeCost, costVersions, err := buildCosts(builder.Root, allEvents, usageProjection)
	if err != nil {
		return ProjectCatalog{}, err
	}
	applyProjectCostPeriods(catalog.Periods, allEvents, usageProjection, activeCost)
	operationProjection, err := operation.Build(builder.Root, allEvents)
	if err != nil {
		return ProjectCatalog{}, fmt.Errorf("build operation audit: %w", err)
	}
	catalog.Derivations = DerivedSummary{Usage: usageProjection, InputComposition: inputProjection, ActivePolicyVersion: activePolicy.Version, ActiveAttribution: activeAttribution, AttributionVersions: versions, ActiveCost: activeCost, CostVersions: costVersions, Operations: operationProjection}
	billingRecords, err := (billing.Store{Root: builder.Root}).List()
	if err != nil {
		return ProjectCatalog{}, fmt.Errorf("list billing records: %w", err)
	}
	activePricing, hasActivePricing, err := (pricing.VersionStore{Root: builder.Root}).LoadActive()
	if err != nil {
		return ProjectCatalog{}, fmt.Errorf("load active pricing for sessions: %w", err)
	}
	var activePricingVersion *pricing.Version
	if hasActivePricing {
		activePricingVersion = &activePricing
	}
	for i := range catalog.Sessions {
		session := &catalog.Sessions[i]
		events := sessionEvents[session.Provider+"\x00"+session.SessionID]
		derived, err := buildSessionDerived(builder.Root, projectID, events, activePolicy, activePricingVersion, billingRecords)
		if err != nil {
			return ProjectCatalog{}, fmt.Errorf("summarize session %s/%s: %w", session.Provider, session.SessionID, err)
		}
		session.Derivations = derived
	}
	if err := builder.writeProjection(catalog, runs); err != nil {
		return ProjectCatalog{}, err
	}
	if err := builder.writeListSummaries(catalog, allEvents, activePolicy, activePricingVersion, billingRecords); err != nil {
		return ProjectCatalog{}, err
	}
	return catalog, nil
}

func buildCosts(root string, events []model.EventEnvelope, usage reconcile.Result) (cost.Result, []CostVersion, error) {
	versionStore := pricing.VersionStore{Root: root}
	active, hasActive, err := versionStore.LoadActive()
	if err != nil {
		return cost.Result{}, nil, fmt.Errorf("load active pricing: %w", err)
	}
	pricingVersions, err := versionStore.List()
	if err != nil {
		return cost.Result{}, nil, fmt.Errorf("list pricing versions: %w", err)
	}
	if !hasActive {
		missing, err := cost.Build(events, usage, nil)
		return missing, []CostVersion{}, err
	}
	results := make(map[string]cost.Result, len(pricingVersions))
	byVersion := make(map[string]pricing.Version, len(pricingVersions))
	for index := range pricingVersions {
		version := pricingVersions[index]
		projected, err := cost.Build(events, usage, &version)
		if err != nil {
			return cost.Result{}, nil, fmt.Errorf("calculate pricing version %s: %w", version.Version, err)
		}
		results[version.Version], byVersion[version.Version] = projected, version
	}
	activeResult, ok := results[active.Version]
	if !ok {
		return cost.Result{}, nil, fmt.Errorf("active pricing version %s is not listed", active.Version)
	}
	versions := make([]CostVersion, 0, len(pricingVersions))
	for _, version := range pricingVersions {
		projected := results[version.Version]
		base := projected
		if version.BasedOn != "" {
			var ok bool
			base, ok = results[version.BasedOn]
			if !ok {
				return cost.Result{}, nil, fmt.Errorf("pricing version %s is based on missing version %s", version.Version, version.BasedOn)
			}
		}
		versions = append(versions, CostVersion{PricingVersion: version.Version, BasedOn: version.BasedOn, Result: projected, ChangeFromBase: cost.Compare(base, projected)})
	}
	return activeResult, versions, nil
}

func summarizeRun(route journal.Route, events []model.EventEnvelope, input InputCursor) (RunSummary, error) {
	usageProjection, err := reconcile.Build(events)
	if err != nil {
		return RunSummary{}, err
	}
	run := RunSummary{
		SchemaVersion: SchemaVersion, AggregationVersion: AggregationVersion,
		ProjectID: route.ProjectID, Provider: route.Provider, SessionID: route.SessionID, RunID: route.RunID,
		EventCount: uint64(len(events)), Availability: map[string]model.AvailabilityStatus{}, Input: input,
	}
	periods := map[string]*PeriodSummary{}
	instructions := map[string]bool{}
	calls := map[string]bool{}
	for _, event := range events {
		if run.FirstReceivedAt.IsZero() || event.Time.ReceivedAt.Before(run.FirstReceivedAt) {
			run.FirstReceivedAt = event.Time.ReceivedAt
		}
		if event.Time.ReceivedAt.After(run.LastReceivedAt) {
			run.LastReceivedAt = event.Time.ReceivedAt
		}
		day := event.Time.ReceivedAt.UTC().Format("2006-01-02")
		period := periods[day]
		if period == nil {
			period = &PeriodSummary{Period: day}
			periods[day] = period
		}
		period.EventCount++
		for _, availability := range event.Availability {
			run.Availability[availability.Path] = availability.Status
			if uncertainUsagePath(availability.Path) && uncertain(availability.Status) {
				run.UnknownUsage = true
				period.UnknownUsage = true
			}
		}
		payload, err := event.DecodePayload()
		if err != nil {
			return RunSummary{}, err
		}
		switch value := payload.(type) {
		case *model.InstructionPayload:
			instructions[eventEntityKey(value.Instruction)] = true
		case *model.UsagePayload:
			// Reconciliation below selects the non-overlapping source set.
		case *model.OperationPayload:
			run.OperationCount++
		case *model.ArtifactPayload:
			run.ArtifactCount++
		case *model.OperationResultPayload:
			if value.Status != "" {
				run.OperationState = value.Status
			}
		case *model.StateTransitionPayload:
			if value.Subject.Kind == model.EntityOperation {
				run.OperationState = value.To
			}
		case *model.ModelCallPayload:
			calls[eventEntityKey(value.Call)] = true
			if value.State == "in_progress" {
				run.OperationState = "pending"
				run.UnknownUsage = true
				period.UnknownUsage = true
			}
		}
	}
	run.InstructionCount = uint64(len(instructions))
	run.ModelCallCount = uint64(len(calls))
	run.KnownUsageTokens = usageProjection.KnownSubtotal
	run.UnknownUsage = run.UnknownUsage || usageProjection.Unknown
	run.SelectedEventIDs = append([]model.NamespacedID(nil), usageProjection.SelectedEventIDs...)
	run.ExcludedEventIDs = append([]model.NamespacedID(nil), usageProjection.ExcludedEventIDs...)
	// Period attribution requires event timestamps; distribute only counted
	// decisions rather than summing instruction display rows.
	for _, period := range periods {
		period.KnownUsageTokens = 0
	}
	for _, decision := range usageProjection.Decisions {
		if decision.Role != reconcile.RoleCounted || decision.Tokens == nil {
			continue
		}
		day := ""
		for _, event := range events {
			if event.EventID == decision.EventID {
				day = event.Time.ReceivedAt.UTC().Format("2006-01-02")
				break
			}
		}
		if periods[day] != nil {
			periods[day].KnownUsageTokens += *decision.Tokens
		}
	}
	if len(run.Availability) == 0 {
		run.Availability = nil
	}
	keys := make([]string, 0, len(periods))
	for key := range periods {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		run.Periods = append(run.Periods, *periods[key])
	}
	return run, nil
}

func usageTokens(normalized *model.NormalizedUsage) (int64, bool) {
	if normalized == nil {
		return 0, false
	}
	for _, total := range normalized.Totals {
		if total.Name == "all" || total.Name == "all_tokens" || total.Name == "total" {
			if total.Tokens.Value == nil {
				return 0, false
			}
			return *total.Tokens.Value, true
		}
	}
	var total int64
	for _, bucket := range normalized.ExclusiveBuckets {
		if bucket.Tokens.Value == nil {
			return 0, false
		}
		total += *bucket.Tokens.Value
	}
	return total, len(normalized.ExclusiveBuckets) > 0
}

func uncertain(status model.AvailabilityStatus) bool {
	switch status {
	case model.AvailabilityUnknown, model.AvailabilityConflict, model.AvailabilityUnavailable, model.AvailabilityNotCollected:
		return true
	default:
		return false
	}
}

func uncertainUsagePath(path string) bool {
	path = strings.ToLower(path)
	return strings.Contains(path, "usage") || strings.Contains(path, "token") || strings.Contains(path, "normalized") || strings.Contains(path, "attribution")
}

func mergePeriods(runs []RunSummary) []PeriodSummary {
	byDay := map[string]*PeriodSummary{}
	for _, run := range runs {
		for _, source := range run.Periods {
			target := byDay[source.Period]
			if target == nil {
				target = &PeriodSummary{Period: source.Period}
				byDay[source.Period] = target
			}
			target.EventCount += source.EventCount
			target.KnownUsageTokens += source.KnownUsageTokens
			target.UnknownUsage = target.UnknownUsage || source.UnknownUsage
		}
	}
	keys := make([]string, 0, len(byDay))
	for key := range byDay {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]PeriodSummary, 0, len(keys))
	for _, key := range keys {
		result = append(result, *byDay[key])
	}
	return result
}

// applyProjectUsagePeriods replaces run-local usage sums with the project-wide
// non-overlapping selection. A parent total can contain usage from another run,
// so adding the per-run display rows can otherwise double count the work total.
func applyProjectUsagePeriods(periods []PeriodSummary, events []model.EventEnvelope, usage reconcile.Result) {
	byEvent := make(map[string]model.EventEnvelope, len(events))
	byDay := make(map[string]*PeriodSummary, len(periods))
	for _, event := range events {
		byEvent[eventKey(event.EventID)] = event
	}
	for index := range periods {
		periods[index].KnownUsageTokens = 0
		periods[index].UsageMissing = false
		byDay[periods[index].Period] = &periods[index]
	}
	for _, decision := range usage.Decisions {
		if decision.Role != reconcile.RoleCounted {
			continue
		}
		event, ok := byEvent[eventKey(decision.EventID)]
		if !ok {
			continue
		}
		day := event.Time.ReceivedAt.UTC().Format("2006-01-02")
		period := byDay[day]
		if period == nil {
			continue
		}
		if decision.Tokens == nil {
			// Mirrors query.Trend's usageRows: a counted decision with no
			// known token amount marks that day's usage as incomplete,
			// distinct from UnknownUsage (a broader "some field on this day
			// had uncertain availability" signal used by the Periods API).
			period.UsageMissing = true
			continue
		}
		period.KnownUsageTokens += *decision.Tokens
	}
}

// applyProjectCostPeriods attributes each active-pricing cost line to the day
// of the usage event it prices, the same way applyProjectUsagePeriods
// attributes tokens — so Trend() can read day/cost totals straight out of
// the catalog instead of re-running reconcile.Build/cost.Build over every
// journal event on every request (see internal/query/trend.go usageRows).
func applyProjectCostPeriods(periods []PeriodSummary, events []model.EventEnvelope, usage reconcile.Result, activeCost cost.Result) {
	byEvent := make(map[string]model.EventEnvelope, len(events))
	byDay := make(map[string]*PeriodSummary, len(periods))
	for _, event := range events {
		byEvent[eventKey(event.EventID)] = event
	}
	for index := range periods {
		periods[index].EstimatedCost = nil
		periods[index].CostMissing = false
		byDay[periods[index].Period] = &periods[index]
	}
	priceByUsage := make(map[string]*cost.Record, len(activeCost.Records))
	for i := range activeCost.Records {
		priceByUsage[eventEntityKey(activeCost.Records[i].Usage)] = &activeCost.Records[i]
	}
	totals := map[string]map[string]*big.Rat{}
	missingDays := map[string]bool{}
	for _, decision := range usage.Decisions {
		if decision.Role != reconcile.RoleCounted {
			continue
		}
		event, ok := byEvent[eventKey(decision.EventID)]
		if !ok {
			continue
		}
		day := event.Time.ReceivedAt.UTC().Format("2006-01-02")
		record := priceByUsage[eventEntityKey(decision.Usage)]
		if record == nil {
			missingDays[day] = true
			continue
		}
		if record.Status != "calculated" && record.Status != "zero_usage" {
			missingDays[day] = true
		}
		for _, line := range record.Lines {
			if line.Amount == nil {
				if line.Quantity != 0 {
					missingDays[day] = true
				}
				continue
			}
			value, ok := new(big.Rat).SetString(*line.Amount)
			if !ok || line.Currency == "" {
				missingDays[day] = true
				continue
			}
			if totals[day] == nil {
				totals[day] = map[string]*big.Rat{}
			}
			if totals[day][line.Currency] == nil {
				totals[day][line.Currency] = new(big.Rat)
			}
			totals[day][line.Currency].Add(totals[day][line.Currency], value)
		}
	}
	for day, byCurrency := range totals {
		period := byDay[day]
		if period == nil {
			continue
		}
		currencies := make([]string, 0, len(byCurrency))
		for currency := range byCurrency {
			currencies = append(currencies, currency)
		}
		sort.Strings(currencies)
		for _, currency := range currencies {
			period.EstimatedCost = append(period.EstimatedCost, cost.CurrencyTotal{Currency: currency, Amount: formatCostRat(byCurrency[currency])})
		}
	}
	for day := range missingDays {
		if period := byDay[day]; period != nil {
			period.CostMissing = true
		}
	}
}

func formatCostRat(value *big.Rat) string {
	s := value.FloatString(12)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}

// buildSessionDerived computes the same aggregate a live query.Service.derive
// call would produce for one session's events, so Sessions() can read it
// back from the catalog instead of recomputing it on every request. policy
// and pricingVersion are always the active ones — a session-list row never
// shows alternate attribution/pricing versions.
func buildSessionDerived(root, projectID string, events []model.EventEnvelope, policy attribution.Policy, pricingVersion *pricing.Version, billingRecords []billing.Record) (SessionDerived, error) {
	usage, err := reconcile.Build(events)
	if err != nil {
		return SessionDerived{}, err
	}
	inputComposition, err := inputcomposition.Build(events, usage)
	if err != nil {
		return SessionDerived{}, err
	}
	attr, err := attribution.Build(events, usage, policy)
	if err != nil {
		return SessionDerived{}, err
	}
	costResult, err := cost.Build(events, usage, pricingVersion)
	if err != nil {
		return SessionDerived{}, err
	}
	ops, err := operation.Build(root, events)
	if err != nil {
		return SessionDerived{}, err
	}
	spend, err := billing.Summarize(filterBilling(billingRecords, projectID, events))
	if err != nil {
		return SessionDerived{}, err
	}
	return SessionDerived{
		Usage: usage, InputComposition: inputComposition, Attribution: attr, Cost: costResult,
		Operations: ops, ActualSpend: spend, Missing: collectMissing(events, ops, costResult, attr),
	}, nil
}

// collectMissing and the billing filter helpers below intentionally
// duplicate internal/query's unexported equivalents (collectMissing,
// filterBilling, filterBillingKeys, indexEventEntities) rather than import
// internal/query from here — internal/query already imports
// internal/catalog, so the reverse import would cycle.
func collectMissing(events []model.EventEnvelope, ops operation.Result, c cost.Result, a attribution.Result) []model.FieldAvailability {
	out := []model.FieldAvailability{}
	for _, e := range events {
		for _, v := range e.Availability {
			if v.Status != model.AvailabilityObserved && v.Status != model.AvailabilityProviderReported && v.Status != model.AvailabilityInferred {
				out = append(out, v)
			}
		}
	}
	for _, op := range ops.Operations {
		out = append(out, op.Missing...)
	}
	if c.MissingCost {
		out = append(out, model.FieldAvailability{Path: "cost.estimated", Status: model.AvailabilityUnknown, Reason: "one or more usage records could not be priced"})
	}
	if a.MissingUsage {
		out = append(out, model.FieldAvailability{Path: "usage.attribution", Status: model.AvailabilityUnknown, Reason: "usage is incomplete"})
	}
	return out
}

func filterBilling(records []billing.Record, projectID string, events []model.EventEnvelope) []billing.Record {
	entities := map[string]bool{}
	for _, e := range events {
		indexEventEntities(entities, e)
		for _, r := range e.Relations {
			entities[eventEntityKey(r.From)] = true
			entities[eventEntityKey(r.To)] = true
		}
	}
	return filterBillingKeys(records, projectID, entities)
}

func filterBillingKeys(records []billing.Record, projectID string, entities map[string]bool) []billing.Record {
	out := []billing.Record{}
	for _, r := range records {
		matched := false
		for _, ref := range append(append([]model.EntityRef{}, r.Usage...), r.Coverage...) {
			if ref.Kind == model.EntityProject && ref.ID.Value == projectID {
				matched = true
			}
			if entities[eventEntityKey(ref)] {
				matched = true
			}
		}
		if matched {
			out = append(out, r)
		}
	}
	return out
}

func indexEventEntities(set map[string]bool, event model.EventEnvelope) {
	for _, ref := range event.Scope {
		set[eventEntityKey(ref)] = true
	}
	payload, err := event.DecodePayload()
	if err != nil {
		return
	}
	switch value := payload.(type) {
	case *model.InstructionPayload:
		set[eventEntityKey(value.Instruction)] = true
	case *model.ModelCallPayload:
		set[eventEntityKey(value.Call)] = true
		set[eventEntityKey(value.Instruction)] = true
	case *model.UsagePayload:
		set[eventEntityKey(value.Usage)] = true
		set[eventEntityKey(value.AppliesTo)] = true
	case *model.OperationPayload:
		set[eventEntityKey(value.Operation)] = true
	case *model.OperationResultPayload:
		set[eventEntityKey(value.Operation)] = true
	case *model.ArtifactPayload:
		set[eventEntityKey(value.Artifact)] = true
	}
}

func eventKey(id model.NamespacedID) string     { return id.Namespace + "\x00" + id.Value }
func eventEntityKey(ref model.EntityRef) string { return string(ref.Kind) + "\x00" + eventKey(ref.ID) }

func (builder *Builder) writeProjection(catalog ProjectCatalog, runs []RunSummary) error {
	layout := &store.Store{Root: builder.Root}
	runsDir := filepath.Join(layout.ProjectSummariesDir(catalog.ProjectID), "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		return err
	}
	wanted := map[string]bool{}
	for _, run := range runs {
		name := summaryName(run.Provider, run.SessionID, run.RunID)
		wanted[name] = true
		data, err := json.MarshalIndent(run, "", "  ")
		if err != nil {
			return err
		}
		if err := store.AtomicWriteFile(filepath.Join(runsDir, name), append(data, '\n'), 0o600); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") && !wanted[entry.Name()] {
			if err := os.Remove(filepath.Join(runsDir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	data, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return err
	}
	return store.AtomicWriteFile(layout.ProjectCatalogPath(catalog.ProjectID), append(data, '\n'), 0o600)
}

func summaryName(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(hash[:]) + ".json"
}
