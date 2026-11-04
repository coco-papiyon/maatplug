// Package query is the single read boundary for CLI and Web consumers.
// Journals are authoritative; catalogs and summaries are disposable indexes.
package query

import (
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"maatplug/internal/attribution"
	"maatplug/internal/billing"
	"maatplug/internal/catalog"
	"maatplug/internal/cost"
	"maatplug/internal/inputcomposition"
	"maatplug/internal/journal"
	"maatplug/internal/model"
	"maatplug/internal/operation"
	"maatplug/internal/pricing"
	"maatplug/internal/reconcile"
	"maatplug/internal/store"
)

var ErrNotFound = errors.New("query target not found")

type Filter struct {
	Provider string    `json:"provider,omitempty"`
	Since    time.Time `json:"since,omitempty"`
	Until    time.Time `json:"until,omitempty"`
}

type Period = catalog.Period

type Usage = catalog.Usage

// ProjectSummary keeps the original dashboard fields while exposing the
// accounting boundaries that prevent unknown amounts from looking like zero.
type ProjectSummary = catalog.ProjectSummary

type Observation = catalog.Observation

type SessionSummary = catalog.SessionSummary

type ChangeSummary = catalog.ChangeSummary

type Detail struct {
	ProjectID          string                    `json:"projectId"`
	Provider           string                    `json:"provider,omitempty"`
	SessionID          string                    `json:"sessionId,omitempty"`
	RunID              string                    `json:"runId,omitempty"`
	Period             Period                    `json:"period"`
	Usage              reconcile.Result          `json:"usage"`
	InputComposition   inputcomposition.Result   `json:"inputComposition"`
	Attribution        attribution.Result        `json:"attribution"`
	EstimatedCost      cost.Result               `json:"estimatedCost"`
	ActualSpend        billing.Summary           `json:"actualSpend"`
	ActualSpendRecords []billing.Record          `json:"actualSpendRecords"`
	Operations         operation.Result          `json:"operations"`
	Instructions       []InstructionView         `json:"instructions"`
	Missing            []model.FieldAvailability `json:"missing"`
	PricingEvidence    []PricingEvidence         `json:"pricingEvidence"`
	Events             []model.EventEnvelope     `json:"events,omitempty"`
	Source             string                    `json:"source"`
	// TokensInput/Output/CacheRead/CacheCreation and TokenBreakdownComplete
	// are Usage.KnownSubtotal's IN/OUT/cache-read/cache-write split — the
	// same classifyNormalized/tokenBreakdown basis ProjectSummary/
	// SessionSummary already show for the project/session list rows — so
	// the session detail screen's "Known tokens" stat can show the same
	// breakdown instead of just the bare total.
	TokensInput            int64 `json:"tokensInput"`
	TokensOutput           int64 `json:"tokensOutput"`
	TokensCacheRead        int64 `json:"tokensCacheRead"`
	TokensCacheCreation    int64 `json:"tokensCacheCreation"`
	TokenBreakdownComplete bool  `json:"tokenBreakdownComplete"`
}

type PricingEvidence struct {
	PricingVersion string   `json:"pricingVersion,omitempty"`
	RateIDs        []string `json:"rateIds"`
	Missing        bool     `json:"missing"`
}

type PeriodSummary struct {
	Period      string `json:"period"`
	Events      uint64 `json:"events"`
	KnownTokens int64  `json:"knownTokens"`
	Unknown     bool   `json:"unknown"`
	Source      string `json:"source"`
}

type EntityDetail struct {
	Entity          model.EntityRef           `json:"entity"`
	Period          Period                    `json:"period"`
	EventIDs        []model.NamespacedID      `json:"eventIds"`
	Summary         attribution.Summary       `json:"summary"`
	Costs           []cost.Record             `json:"costs"`
	EstimatedCost   []cost.CurrencyTotal      `json:"estimatedCost"`
	CostMissing     bool                      `json:"costMissing"`
	Spend           []billing.Record          `json:"spend"`
	ActualSpend     billing.Summary           `json:"actualSpend"`
	Operations      []operation.Operation     `json:"operations"`
	Missing         []model.FieldAvailability `json:"missing"`
	PricingEvidence []PricingEvidence         `json:"pricingEvidence"`
}

type Service struct {
	Root string
	Now  func() time.Time
}

func New(root string) *Service { return &Service{Root: root, Now: time.Now} }

func (service *Service) ListProjects(filter Filter) ([]ProjectSummary, error) {
	if filter.Since.IsZero() && filter.Until.IsZero() {
		if list, err := catalog.ReadProjectList(service.Root); err == nil {
			result := make([]ProjectSummary, 0, len(list.Projects))
			for _, entry := range list.Projects {
				row := entry.Project
				if filter.Provider != "" {
					provider, ok := entry.Providers[filter.Provider]
					if !ok {
						continue
					}
					row = provider.Project
				}
				if !pathWithin(service.Root, row.Path) {
					result = append(result, row)
				}
			}
			return result, nil
		}
	}
	layout := &store.Store{Root: service.Root}
	ids, err := layout.ListProjectIDs()
	if err != nil {
		return nil, err
	}
	sort.Strings(ids)
	result := make([]ProjectSummary, 0, len(ids))
	for _, id := range ids {
		_, projectPath, metaErr := layout.ProjectMeta(id)
		if metaErr == nil && pathWithin(service.Root, projectPath) {
			continue
		}
		paths, err := journal.New(service.Root).ListProjectStreams(id)
		if err != nil {
			return nil, err
		}
		if len(paths) == 0 {
			continue
		}
		var summary ProjectSummary
		if filter.Provider == "" && filter.Since.IsZero() && filter.Until.IsZero() {
			projection, err := service.catalog(id)
			if err != nil {
				return nil, err
			}
			summary, err = service.summaryFromCatalog(projection)
			if err != nil {
				return nil, err
			}
		} else {
			detail, err := service.projectDetail(id, filter, false)
			if err != nil {
				return nil, err
			}
			summary, err = service.summaryFromDetail(id, detail)
			if err != nil {
				return nil, err
			}
		}
		result = append(result, summary)
	}
	return result, nil
}

func (service *Service) Project(projectID string, filter Filter) (ProjectSummary, error) {
	paths, err := journal.New(service.Root).ListProjectStreams(projectID)
	if err != nil {
		return ProjectSummary{}, err
	}
	if len(paths) == 0 {
		return ProjectSummary{}, ErrNotFound
	}
	if filter.Provider == "" && filter.Since.IsZero() && filter.Until.IsZero() {
		projection, err := service.catalog(projectID)
		if err != nil {
			return ProjectSummary{}, err
		}
		return service.summaryFromCatalog(projection)
	}
	detail, err := service.projectDetail(projectID, filter, false)
	if err != nil {
		return ProjectSummary{}, err
	}
	return service.summaryFromDetail(projectID, detail)
}

func (service *Service) Periods(projectID string) ([]PeriodSummary, error) {
	paths, err := journal.New(service.Root).ListProjectStreams(projectID)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, ErrNotFound
	}
	projection, err := service.catalog(projectID)
	if err != nil {
		return nil, err
	}
	result := make([]PeriodSummary, 0, len(projection.Periods))
	for _, p := range projection.Periods {
		result = append(result, PeriodSummary{Period: p.Period, Events: p.EventCount, KnownTokens: p.KnownUsageTokens, Unknown: p.UnknownUsage, Source: "period-summary"})
	}
	return result, nil
}

func (service *Service) Sessions(projectID string, filter Filter) ([]SessionSummary, error) {
	if filter.Since.IsZero() && filter.Until.IsZero() {
		if list, err := catalog.ReadSessionList(service.Root, projectID); err == nil {
			result := []SessionSummary{}
			for _, row := range list.Sessions {
				if filter.Provider == "" || row.Provider == filter.Provider {
					result = append(result, row)
				}
			}
			sort.Slice(result, func(i, j int) bool { return result[i].StartedAt.After(result[j].StartedAt) })
			return result, nil
		}
	}
	paths, err := journal.New(service.Root).ListProjectStreams(projectID)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return []SessionSummary{}, nil
	}
	projection, err := service.catalog(projectID)
	if err != nil {
		return nil, err
	}
	result := []SessionSummary{}
	for _, session := range projection.Sessions {
		if filter.Provider != "" && session.Provider != filter.Provider {
			continue
		}
		if filter.Since.IsZero() && filter.Until.IsZero() {
			// No time window: the catalog's cached per-session aggregate
			// (computed once at collect time, see catalog.buildSessionDerived)
			// already answers this exactly, without re-reading raw journal
			// events or re-running reconcile/attribution/cost/operation.
			result = append(result, sessionSummaryFromCatalog(session))
			continue
		}
		events, runs, err := service.sessionEvents(projectID, session.Provider, session.SessionID, filter)
		if err != nil {
			return nil, err
		}
		if len(events) == 0 {
			continue
		}
		detail, err := service.derive(projectID, session.Provider, session.SessionID, "", events, "journal")
		if err != nil {
			return nil, err
		}
		result = append(result, sessionSummary(detail, runs))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StartedAt.After(result[j].StartedAt) })
	return result, nil
}

func sessionSummaryFromCatalog(session catalog.Session) SessionSummary {
	return catalog.SessionSummaryFromCatalog(session)
}

func (service *Service) ProjectDetail(projectID string, filter Filter) (Detail, error) {
	return service.projectDetail(projectID, filter, true)
}

func (service *Service) projectDetail(projectID string, filter Filter, includeEvents bool) (Detail, error) {
	paths, err := journal.New(service.Root).ListProjectStreams(projectID)
	if err != nil {
		return Detail{}, err
	}
	if len(paths) == 0 {
		return Detail{}, ErrNotFound
	}
	events := []model.EventEnvelope{}
	for _, path := range paths {
		route, items, err := journal.New(service.Root).ReadPath(path)
		if err != nil {
			return Detail{}, err
		}
		if filter.Provider != "" && route.Provider != filter.Provider {
			continue
		}
		events = append(events, filterEvents(items, filter)...)
	}
	detail, err := service.derive(projectID, "", "", "", events, "journal")
	if err == nil && !includeEvents {
		detail.Events = nil
	}
	return detail, err
}

func (service *Service) SessionDetail(projectID, provider, sessionID string, filter Filter) (Detail, error) {
	events, _, err := service.sessionEvents(projectID, provider, sessionID, filter)
	if err != nil {
		return Detail{}, err
	}
	if len(events) == 0 {
		return Detail{}, ErrNotFound
	}
	return service.derive(projectID, provider, sessionID, "", events, "journal")
}

func (service *Service) RunDetail(projectID, provider, sessionID, runID string) (Detail, error) {
	route := journal.Route{ProjectID: projectID, Provider: provider, SessionID: sessionID, RunID: runID}
	events, err := journal.New(service.Root).Read(route)
	if err != nil {
		return Detail{}, err
	}
	if len(events) == 0 {
		return Detail{}, ErrNotFound
	}
	return service.derive(projectID, provider, sessionID, runID, events, "journal")
}

func (service *Service) Entity(projectID string, kind model.EntityKind, namespace, id string, filter Filter) (EntityDetail, error) {
	detail, err := service.ProjectDetail(projectID, filter)
	if err != nil {
		return EntityDetail{}, err
	}
	ref := model.EntityRef{Kind: kind, ID: model.NamespacedID{Namespace: namespace, Value: id}}
	result := EntityDetail{Entity: ref, EventIDs: []model.NamespacedID{}, Costs: []cost.Record{}, Spend: []billing.Record{}, Operations: []operation.Operation{}, Missing: []model.FieldAvailability{}, PricingEvidence: detail.PricingEvidence}
	found := false
	relatedUsage := map[string]costShare{}
	if kind == model.EntityInstruction {
		for _, record := range detail.Attribution.Records {
			var assigned int64
			for _, item := range record.Assigned {
				if sameEntity(item.Instruction, ref) {
					assigned += item.Tokens
				}
			}
			if assigned == 0 && refsContain(record.Instructions, ref) && len(record.Instructions) == 1 && record.Tokens != nil {
				assigned = *record.Tokens
			}
			if assigned > 0 && record.Tokens != nil && *record.Tokens > 0 {
				relatedUsage[entityKey(record.Usage)] = costShare{part: assigned, total: *record.Tokens}
			}
		}
	}
	relatedEvents := []model.EventEnvelope{}
	for _, event := range detail.Events {
		if eventTouches(event, ref) {
			result.EventIDs = append(result.EventIDs, event.EventID)
			relatedEvents = append(relatedEvents, event)
			found = true
		}
	}
	result.Period = periodFor(relatedEvents)
	entities := detail.Attribution.Instructions
	if kind == model.EntityModelCall {
		entities = detail.Attribution.Calls
	}
	for _, item := range entities {
		if sameEntity(item.Entity, ref) {
			result.Summary = item.Summary
			found = true
		}
	}
	costTotals := map[string]*big.Rat{}
	for _, item := range detail.EstimatedCost.Records {
		share, shared := relatedUsage[entityKey(item.Usage)]
		if sameEntity(item.Call, ref) {
			relatedUsage[entityKey(item.Usage)] = costShare{part: 1, total: 1}
		}
		if sameEntity(item.Call, ref) || sameEntity(item.Usage, ref) || shared {
			result.Costs = append(result.Costs, item)
			if item.Amount == nil || item.Status == "incomplete" {
				result.CostMissing = true
			} else if amount, ok := new(big.Rat).SetString(*item.Amount); ok {
				if shared {
					amount.Mul(amount, big.NewRat(share.part, share.total))
				}
				if costTotals[item.Currency] == nil {
					costTotals[item.Currency] = new(big.Rat)
				}
				costTotals[item.Currency].Add(costTotals[item.Currency], amount)
			} else {
				result.CostMissing = true
			}
			found = true
		}
	}
	for currency, total := range costTotals {
		result.EstimatedCost = append(result.EstimatedCost, cost.CurrencyTotal{Currency: currency, Amount: formatRat(total)})
	}
	sort.Slice(result.EstimatedCost, func(i, j int) bool { return result.EstimatedCost[i].Currency < result.EstimatedCost[j].Currency })
	for _, item := range detail.ActualSpendRecords {
		matched := refsContain(item.Usage, ref) || refsContain(item.Coverage, ref)
		for _, usageRef := range append(append([]model.EntityRef{}, item.Usage...), item.Coverage...) {
			if _, ok := relatedUsage[entityKey(usageRef)]; ok {
				matched = true
			}
		}
		if matched {
			result.Spend = append(result.Spend, item)
			found = true
		}
	}
	result.ActualSpend, err = billing.Summarize(result.Spend)
	if err != nil {
		return EntityDetail{}, err
	}
	for _, item := range detail.Operations.Operations {
		matched := sameEntity(item.Operation, ref)
		for _, related := range item.Instructions {
			matched = matched || sameEntity(related.Entity, ref)
		}
		if matched {
			result.Operations = append(result.Operations, item)
			found = true
		}
	}
	for _, item := range detail.Missing {
		if strings.Contains(item.Path, id) {
			result.Missing = append(result.Missing, item)
		}
	}
	if !found {
		return EntityDetail{}, ErrNotFound
	}
	return result, nil
}

type costShare struct{ part, total int64 }

func formatRat(value *big.Rat) string {
	s := value.FloatString(12)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}

func (service *Service) catalog(projectID string) (catalog.ProjectCatalog, error) {
	path := (&store.Store{Root: service.Root}).ProjectCatalogPath(projectID)
	data, err := os.ReadFile(path)
	if err == nil {
		var projection catalog.ProjectCatalog
		if json.Unmarshal(data, &projection) == nil && projection.SchemaVersion == catalog.SchemaVersion && projection.ProjectID == projectID && service.catalogFresh(projection) {
			return projection, nil
		}
	}
	return catalog.New(service.Root).RebuildProject(projectID)
}

func (service *Service) catalogFresh(projection catalog.ProjectCatalog) bool {
	if projection.AggregationVersion != catalog.AggregationVersion {
		return false
	}
	paths, err := journal.New(service.Root).ListProjectStreams(projection.ProjectID)
	if err != nil || len(paths) != len(projection.Inputs) {
		return false
	}
	actual := map[string]int64{}
	for _, path := range paths {
		info, e := os.Stat(path)
		rel, re := filepath.Rel(service.Root, path)
		if e != nil || re != nil {
			return false
		}
		actual[filepath.ToSlash(rel)] = info.Size()
	}
	for _, input := range projection.Inputs {
		if actual[input.JournalPath] != input.LastByteOffset {
			return false
		}
	}
	// Run summaries are already embedded verbatim in projection.Sessions
	// (writeProjection wrote both from the same in-memory value), so there
	// is nothing to gain from reading each one back and comparing fields —
	// only an existence check is needed, to catch a file deleted or moved
	// out from under the catalog (see TestCatalogRepairAndJournalDetails).
	builder := catalog.New(service.Root)
	for _, session := range projection.Sessions {
		for _, run := range session.Runs {
			if _, err := os.Stat(builder.RunSummaryPath(projection.ProjectID, session.Provider, session.SessionID, run.RunID)); err != nil {
				return false
			}
		}
	}
	return true
}

func (service *Service) derive(projectID, provider, sessionID, runID string, events []model.EventEnvelope, source string) (Detail, error) {
	usageResult, err := reconcile.Build(events)
	if err != nil {
		return Detail{}, err
	}
	inputResult, err := inputcomposition.Build(events, usageResult)
	if err != nil {
		return Detail{}, err
	}
	policy, err := (attribution.PolicyStore{Root: service.Root}).LoadActive(projectID)
	if err != nil {
		return Detail{}, err
	}
	attr, err := attribution.Build(events, usageResult, policy)
	if err != nil {
		return Detail{}, err
	}
	version, ok, err := (pricing.VersionStore{Root: service.Root}).LoadActive()
	if err != nil {
		return Detail{}, err
	}
	var versionPtr *pricing.Version
	if ok {
		versionPtr = &version
	}
	costResult, err := cost.Build(events, usageResult, versionPtr)
	if err != nil {
		return Detail{}, err
	}
	ops, err := operation.Build(service.Root, events)
	if err != nil {
		return Detail{}, err
	}
	instructions, err := buildInstructions(service.Root, events)
	if err != nil {
		return Detail{}, err
	}
	instructions = attachInstructionStats(instructions, usageResult, attr, ops)
	instructions = attachInstructionCosts(instructions, attr, costResult)
	instructions = attachInstructionAnswers(service.Root, instructions, events)
	instructions = markInstructionsWithoutEdits(instructions, ops, events)
	records, err := (billing.Store{Root: service.Root}).List()
	if err != nil {
		return Detail{}, err
	}
	records = filterBilling(records, projectID, events)
	spend, err := billing.Summarize(records)
	if err != nil {
		return Detail{}, err
	}
	detail := Detail{ProjectID: projectID, Provider: provider, SessionID: sessionID, RunID: runID, Usage: usageResult, InputComposition: inputResult, Attribution: attr, EstimatedCost: costResult, ActualSpend: spend, ActualSpendRecords: records, Operations: ops, Instructions: instructions, Events: events, Source: source}
	detail.Period = periodFor(events)
	detail.Missing = collectMissing(events, ops, costResult, attr)
	detail.PricingEvidence = []PricingEvidence{{PricingVersion: costResult.PricingVersion, RateIDs: rateIDs(costResult), Missing: costResult.MissingCost}}
	buckets, complete := tokenBreakdown(usageResult)
	detail.TokensInput, detail.TokensOutput, detail.TokensCacheRead, detail.TokensCacheCreation, detail.TokenBreakdownComplete = buckets.input, buckets.output, buckets.cacheRead, buckets.cacheWrite, complete
	return detail, nil
}

func (service *Service) summaryFromCatalog(projection catalog.ProjectCatalog) (ProjectSummary, error) {
	return catalog.ProjectSummaryFromCatalog(service.Root, projection)
}

func (service *Service) summaryFromDetail(projectID string, detail Detail) (ProjectSummary, error) {
	name, path, _ := (&store.Store{Root: service.Root}).ProjectMeta(projectID)
	sessions := map[string]bool{}
	runs := map[string]bool{}
	instructions := map[string]bool{}
	calls := map[string]bool{}
	artifacts := map[string]bool{}
	for _, event := range detail.Events {
		sessionID := scopeValue(event.Scope, model.EntitySession)
		runID := scopeValue(event.Scope, model.EntityRun)
		if sessionID != "" {
			sessions[event.Source.Provider+"\x00"+sessionID] = true
		}
		if runID != "" {
			runs[event.Source.Provider+"\x00"+sessionID+"\x00"+runID] = true
		}
		switch event.EventType {
		case model.EventInstructionRecorded:
			if p, e := event.DecodePayload(); e == nil {
				v := p.(*model.InstructionPayload)
				instructions[entityKey(v.Instruction)] = true
			}
		case model.EventModelCall:
			if p, e := event.DecodePayload(); e == nil {
				v := p.(*model.ModelCallPayload)
				calls[entityKey(v.Call)] = true
			}
		case model.EventArtifactObserved:
			if p, e := event.DecodePayload(); e == nil {
				v := p.(*model.ArtifactPayload)
				artifacts[entityKey(v.Artifact)] = true
			}
		}
	}
	buckets, complete := tokenBreakdown(detail.Usage)
	result := ProjectSummary{ProjectID: projectID, Name: name, Path: path, Sessions: len(sessions), Instructions: len(instructions), TotalTokens: detail.Usage.KnownSubtotal, TokensInput: buckets.input, TokensOutput: buckets.output, TokensCacheRead: buckets.cacheRead, TokensCacheCreation: buckets.cacheWrite, TokenBreakdownComplete: complete, Changes: changeSummary(detail.Operations), CostUnpricedInstructions: incompleteCosts(detail.EstimatedCost), Runs: len(runs), Calls: len(calls), Period: detail.Period, Usage: usageView(detail.Usage, detail.Attribution), EstimatedCost: detail.EstimatedCost.KnownTotals, CostMissing: detail.EstimatedCost.MissingCost, ActualSpend: detail.ActualSpend, ActualSpendScope: "project-or-entity-linked", Operations: len(detail.Operations.Operations), Artifacts: len(artifacts), Unresolved: len(detail.Missing), PricingVersion: detail.EstimatedCost.PricingVersion, AttributionPolicy: detail.Attribution.PolicyVersion, Observation: Observation{Events: len(detail.Events), Streams: len(runs), MissingPaths: missingPaths(detail.Missing)}, Source: "journal"}
	return result, nil
}

func filterEvents(events []model.EventEnvelope, filter Filter) []model.EventEnvelope {
	out := []model.EventEnvelope{}
	for _, e := range events {
		at := e.Time.ReceivedAt
		if !filter.Since.IsZero() && at.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && !at.Before(filter.Until) {
			continue
		}
		out = append(out, e)
	}
	return out
}
func (service *Service) sessionEvents(projectID, provider, sessionID string, filter Filter) ([]model.EventEnvelope, []catalog.RunSummary, error) {
	p, err := service.catalog(projectID)
	if err != nil {
		return nil, nil, err
	}
	events := []model.EventEnvelope{}
	runs := []catalog.RunSummary{}
	for _, s := range p.Sessions {
		if s.Provider != provider || s.SessionID != sessionID {
			continue
		}
		for _, r := range s.Runs {
			items, e := journal.New(service.Root).Read(journal.Route{ProjectID: projectID, Provider: provider, SessionID: sessionID, RunID: r.RunID})
			if e != nil {
				return nil, nil, e
			}
			items = filterEvents(items, filter)
			if len(items) > 0 {
				events = append(events, items...)
				runs = append(runs, r)
			}
		}
	}
	return events, runs, nil
}

func sessionSummary(detail Detail, runs []catalog.RunSummary) SessionSummary {
	instructions, calls := 0, 0
	artifacts := 0
	seenI, seenC := map[string]bool{}, map[string]bool{}
	for _, e := range detail.Events {
		switch e.EventType {
		case model.EventInstructionRecorded:
			if p, x := e.DecodePayload(); x == nil {
				seenI[entityKey(p.(*model.InstructionPayload).Instruction)] = true
			}
		case model.EventModelCall:
			if p, x := e.DecodePayload(); x == nil {
				seenC[entityKey(p.(*model.ModelCallPayload).Call)] = true
			}
		case model.EventArtifactObserved:
			artifacts++
		}
	}
	instructions = len(seenI)
	calls = len(seenC)
	var start, end time.Time
	if detail.Period.From != nil {
		start = *detail.Period.From
	}
	if detail.Period.To != nil {
		end = *detail.Period.To
	}
	buckets, complete := tokenBreakdown(detail.Usage)
	return SessionSummary{Provider: detail.Provider, SessionID: detail.SessionID, StartedAt: start, Instructions: instructions, TotalTokens: detail.Usage.KnownSubtotal, TokensInput: buckets.input, TokensOutput: buckets.output, TokensCacheRead: buckets.cacheRead, TokensCacheCreation: buckets.cacheWrite, TokenBreakdownComplete: complete, Changes: changeSummary(detail.Operations), CostUnpricedInstructions: incompleteCosts(detail.EstimatedCost), Runs: len(runs), EndedAt: end, Calls: calls, Usage: usageView(detail.Usage, detail.Attribution), EstimatedCost: detail.EstimatedCost.KnownTotals, CostMissing: detail.EstimatedCost.MissingCost, Operations: len(detail.Operations.Operations), Artifacts: artifacts, Unresolved: len(detail.Missing), RunSummaries: runs}
}
func usageView(u reconcile.Result, a attribution.Result) Usage {
	return Usage{KnownSubtotal: u.KnownSubtotal, DirectTokens: a.DirectTokens, SharedUnallocated: a.SharedUnallocated, Undecomposed: u.Undecomposed, UnattributedTokens: a.UnattributedTokens, AllocatedEstimate: a.AllocatedEstimate, Unknown: u.Unknown || a.MissingUsage}
}
func periodFor(events []model.EventEnvelope) Period {
	var min, max time.Time
	for _, e := range events {
		at := e.Time.ReceivedAt
		if min.IsZero() || at.Before(min) {
			min = at
		}
		if max.IsZero() || at.After(max) {
			max = at
		}
	}
	p := Period{}
	if !min.IsZero() {
		p.From = &min
		p.To = &max
	}
	return p
}
func periodFromCatalog(c catalog.ProjectCatalog) Period {
	var min, max time.Time
	for _, s := range c.Sessions {
		for _, r := range s.Runs {
			if min.IsZero() || (!r.FirstReceivedAt.IsZero() && r.FirstReceivedAt.Before(min)) {
				min = r.FirstReceivedAt
			}
			if r.LastReceivedAt.After(max) {
				max = r.LastReceivedAt
			}
		}
	}
	p := Period{}
	if !min.IsZero() {
		p.From = &min
		p.To = &max
	}
	return p
}
func observationFromCatalog(c catalog.ProjectCatalog) Observation {
	o := Observation{Streams: len(c.Inputs), MissingPaths: []string{}}
	for _, i := range c.Inputs {
		o.Events += int(i.EventCount)
	}
	for _, session := range c.Sessions {
		for _, run := range session.Runs {
			for path, status := range run.Availability {
				if status != model.AvailabilityObserved && status != model.AvailabilityProviderReported && status != model.AvailabilityInferred {
					o.MissingPaths = append(o.MissingPaths, path)
				}
			}
		}
	}
	sort.Strings(o.MissingPaths)
	return o
}
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
func missingPaths(items []model.FieldAvailability) []string {
	set := map[string]bool{}
	for _, x := range items {
		set[x.Path] = true
	}
	out := []string{}
	for x := range set {
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}
func rateIDs(c cost.Result) []string {
	set := map[string]bool{}
	for _, r := range c.Records {
		for _, l := range r.Lines {
			if l.RateID != "" {
				set[l.RateID] = true
			}
		}
	}
	out := []string{}
	for x := range set {
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}
func unresolved(o operation.Result, c cost.Result, a attribution.Result, missing []string) int {
	n := len(o.OrphanApprovals) + len(o.OrphanArtifacts) + len(o.OrphanResults) + len(missing)
	if c.MissingCost {
		n++
	}
	if a.MissingUsage {
		n++
	}
	return n
}
func runCount(c catalog.ProjectCatalog) int {
	n := 0
	for _, s := range c.Sessions {
		n += len(s.Runs)
	}
	return n
}
func catalogCounts(c catalog.ProjectCatalog) (int, int, int) {
	instructions, calls, artifacts := 0, 0, 0
	for _, session := range c.Sessions {
		for _, run := range session.Runs {
			instructions += int(run.InstructionCount)
			calls += int(run.ModelCallCount)
			artifacts += int(run.ArtifactCount)
		}
	}
	return instructions, calls, artifacts
}

type tokenBucketTotals struct{ input, output, cacheRead, cacheWrite int64 }

func (t tokenBucketTotals) total() int64 { return t.input + t.output + t.cacheRead + t.cacheWrite }

func (t *tokenBucketTotals) add(other tokenBucketTotals) {
	t.input += other.input
	t.output += other.output
	t.cacheRead += other.cacheRead
	t.cacheWrite += other.cacheWrite
}

// scaled prorates every bucket by units/totalUnits, for the rare case where a
// single usage decision is split across several instructions (a correction
// policy's explicit allocation, or the concurrent-resume edge case
// docs/design.md section 12 point 9 describes). Integer division floors each
// bucket independently, so scaled buckets across all recipients of one
// decision can undercount the source decision by a few tokens — acceptable
// here since this only runs for already-approximate, explicitly-flagged
// splits, never for the common single-instruction case.
func (t tokenBucketTotals) scaled(units, totalUnits int64) tokenBucketTotals {
	if totalUnits <= 0 {
		return tokenBucketTotals{}
	}
	return tokenBucketTotals{
		input:      t.input * units / totalUnits,
		output:     t.output * units / totalUnits,
		cacheRead:  t.cacheRead * units / totalUnits,
		cacheWrite: t.cacheWrite * units / totalUnits,
	}
}

// classifyNormalized buckets one usage decision's normalized token counts
// into the input/output/cache-read/cache-write categories the dashboard
// shows, shared by both the session-wide breakdown (tokenBreakdown) and the
// per-instruction breakdown (instructionTokenBreakdowns).
func classifyNormalized(normalized *model.NormalizedUsage) (tokenBucketTotals, bool) {
	var out tokenBucketTotals
	if normalized == nil {
		return out, false
	}
	complete := true
	for _, bucket := range normalized.ExclusiveBuckets {
		if bucket.Tokens.Value == nil {
			complete = false
			continue
		}
		switch bucket.Name {
		case "input", "input_uncached":
			out.input += *bucket.Tokens.Value
		case "output":
			out.output += *bucket.Tokens.Value
		case "cache_read_input":
			out.cacheRead += *bucket.Tokens.Value
		case "cache_write_input":
			out.cacheWrite += *bucket.Tokens.Value
		default:
			complete = false
		}
	}
	return out, complete
}

func tokenBreakdown(result reconcile.Result) (tokenBucketTotals, bool) {
	var out tokenBucketTotals
	complete := !result.Unknown
	for _, decision := range result.Decisions {
		if decision.Role != reconcile.RoleCounted {
			continue
		}
		buckets, ok := classifyNormalized(decision.Normalized)
		if !ok {
			complete = false
		}
		out.add(buckets)
	}
	return out, complete
}

func incompleteCosts(result cost.Result) int {
	n := 0
	for _, record := range result.Records {
		if record.Status != "calculated" && record.Status != "zero_usage" {
			n++
		}
	}
	return n
}

func changeSummary(result operation.Result) ChangeSummary {
	added, removed, files := 0, 0, 0
	addedObserved, removedObserved, filesObserved := false, false, false
	for _, op := range result.Operations {
		for _, artifact := range op.Artifacts {
			m := artifact.Measurement
			if m.Added != nil {
				added += *m.Added
				addedObserved = true
			}
			if m.Removed != nil {
				removed += *m.Removed
				removedObserved = true
			}
			if m.FilesChanged != nil {
				files += *m.FilesChanged
				filesObserved = true
			}
		}
	}
	if !addedObserved && !removedObserved && !filesObserved {
		return ChangeSummary{Availability: model.AvailabilityUnknown, Reason: "no measured artifact diff was collected"}
	}
	resultSummary := ChangeSummary{Availability: model.AvailabilityObserved}
	resultSummary.NoEdits = addedObserved && removedObserved && filesObserved && added == 0 && removed == 0 && files == 0
	if addedObserved {
		resultSummary.Added = &added
	}
	if removedObserved {
		resultSummary.Removed = &removed
	}
	if filesObserved {
		resultSummary.FilesChanged = &files
	}
	return resultSummary
}
func filterBilling(records []billing.Record, projectID string, events []model.EventEnvelope) []billing.Record {
	entities := map[string]bool{}
	for _, e := range events {
		indexEventEntities(entities, e)
		for _, r := range e.Relations {
			entities[entityKey(r.From)] = true
			entities[entityKey(r.To)] = true
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
			if entities[entityKey(ref)] {
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
		set[entityKey(ref)] = true
	}
	payload, err := event.DecodePayload()
	if err != nil {
		return
	}
	switch value := payload.(type) {
	case *model.InstructionPayload:
		set[entityKey(value.Instruction)] = true
	case *model.ModelCallPayload:
		set[entityKey(value.Call)] = true
		set[entityKey(value.Instruction)] = true
	case *model.UsagePayload:
		set[entityKey(value.Usage)] = true
		set[entityKey(value.AppliesTo)] = true
	case *model.OperationPayload:
		set[entityKey(value.Operation)] = true
	case *model.OperationResultPayload:
		set[entityKey(value.Operation)] = true
	case *model.ArtifactPayload:
		set[entityKey(value.Artifact)] = true
	}
}
func refsContain(items []model.EntityRef, target model.EntityRef) bool {
	for _, x := range items {
		if sameEntity(x, target) {
			return true
		}
	}
	return false
}
func sameEntity(a, b model.EntityRef) bool { return a.Kind == b.Kind && a.ID == b.ID }
func entityKey(r model.EntityRef) string {
	return string(r.Kind) + "\x00" + r.ID.Namespace + "\x00" + r.ID.Value
}

func eventTouches(event model.EventEnvelope, target model.EntityRef) bool {
	if refsContain(event.Scope, target) {
		return true
	}
	for _, relation := range event.Relations {
		if sameEntity(relation.From, target) || sameEntity(relation.To, target) {
			return true
		}
	}
	payload, err := event.DecodePayload()
	if err != nil {
		return false
	}
	switch value := payload.(type) {
	case *model.InstructionPayload:
		return sameEntity(value.Instruction, target)
	case *model.ModelCallPayload:
		return sameEntity(value.Call, target) || sameEntity(value.Instruction, target)
	case *model.UsagePayload:
		return sameEntity(value.Usage, target) || sameEntity(value.AppliesTo, target)
	case *model.OperationPayload:
		return sameEntity(value.Operation, target)
	case *model.OperationResultPayload:
		return sameEntity(value.Operation, target)
	case *model.ArtifactPayload:
		return sameEntity(value.Artifact, target)
	}
	return false
}

func scopeValue(scope []model.EntityRef, kind model.EntityKind) string {
	for _, ref := range scope {
		if ref.Kind == kind {
			return ref.ID.Value
		}
	}
	return ""
}

func pathWithin(root, path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	absRoot, e1 := filepath.Abs(root)
	absPath, e2 := filepath.Abs(path)
	if e1 != nil || e2 != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
