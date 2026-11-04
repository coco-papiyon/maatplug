package catalog

import (
	"maatplug/internal/attribution"
	"maatplug/internal/billing"
	"maatplug/internal/cost"
	"maatplug/internal/model"
	"maatplug/internal/operation"
	"maatplug/internal/reconcile"
	"maatplug/internal/store"
	"sort"
	"time"
)

type Period struct {
	From *time.Time `json:"from,omitempty"`
	To   *time.Time `json:"to,omitempty"`
}

type Usage struct {
	KnownSubtotal      int64 `json:"knownSubtotal"`
	DirectTokens       int64 `json:"directTokens"`
	SharedUnallocated  int64 `json:"sharedUnallocated"`
	Undecomposed       int64 `json:"undecomposed"`
	UnattributedTokens int64 `json:"unattributedTokens"`
	AllocatedEstimate  int64 `json:"allocatedEstimate"`
	Unknown            bool  `json:"unknown"`
}

type ProjectSummary struct {
	ProjectID                string               `json:"projectId"`
	Name                     string               `json:"name"`
	Path                     string               `json:"path"`
	Sessions                 int                  `json:"sessions"`
	Instructions             int                  `json:"instructions"`
	TotalTokens              int64                `json:"totalTokens"`
	TokensInput              int64                `json:"tokensInput"`
	TokensOutput             int64                `json:"tokensOutput"`
	TokensCacheRead          int64                `json:"tokensCacheRead"`
	TokensCacheCreation      int64                `json:"tokensCacheCreation"`
	TokenBreakdownComplete   bool                 `json:"tokenBreakdownComplete"`
	Changes                  ChangeSummary        `json:"changes"`
	AbandonedInstructions    *int                 `json:"abandonedInstructions,omitempty"`
	CostUnpricedInstructions int                  `json:"costUnpricedInstructions"`
	Runs                     int                  `json:"runs"`
	Calls                    int                  `json:"calls"`
	Period                   Period               `json:"period"`
	Usage                    Usage                `json:"usage"`
	EstimatedCost            []cost.CurrencyTotal `json:"estimatedCost"`
	CostMissing              bool                 `json:"costMissing"`
	ActualSpend              billing.Summary      `json:"actualSpend"`
	ActualSpendScope         string               `json:"actualSpendScope"`
	Operations               int                  `json:"operations"`
	Artifacts                int                  `json:"artifacts"`
	Unresolved               int                  `json:"unresolved"`
	PricingVersion           string               `json:"pricingVersion,omitempty"`
	AttributionPolicy        string               `json:"attributionPolicy,omitempty"`
	Observation              Observation          `json:"observation"`
	Source                   string               `json:"source"`
}

type Observation struct {
	Events       int      `json:"events"`
	Streams      int      `json:"streams"`
	MissingPaths []string `json:"missingPaths"`
}

type SessionSummary struct {
	Provider                 string               `json:"provider"`
	SessionID                string               `json:"sessionId"`
	StartedAt                time.Time            `json:"startedAt,omitempty"`
	Instructions             int                  `json:"instructions"`
	TotalTokens              int64                `json:"totalTokens"`
	TokensInput              int64                `json:"tokensInput"`
	TokensOutput             int64                `json:"tokensOutput"`
	TokensCacheRead          int64                `json:"tokensCacheRead"`
	TokensCacheCreation      int64                `json:"tokensCacheCreation"`
	TokenBreakdownComplete   bool                 `json:"tokenBreakdownComplete"`
	Changes                  ChangeSummary        `json:"changes"`
	AbandonedInstructions    *int                 `json:"abandonedInstructions,omitempty"`
	CostUnpricedInstructions int                  `json:"costUnpricedInstructions"`
	Runs                     int                  `json:"runs"`
	EndedAt                  time.Time            `json:"endedAt,omitempty"`
	Calls                    int                  `json:"calls"`
	Usage                    Usage                `json:"usage"`
	EstimatedCost            []cost.CurrencyTotal `json:"estimatedCost"`
	CostMissing              bool                 `json:"costMissing"`
	Operations               int                  `json:"operations"`
	Artifacts                int                  `json:"artifacts"`
	Unresolved               int                  `json:"unresolved"`
	RunSummaries             []RunSummary         `json:"runSummaries"`
}

type ChangeSummary struct {
	NoEdits      bool                     `json:"noEdits,omitempty"`
	Added        *int                     `json:"added,omitempty"`
	Removed      *int                     `json:"removed,omitempty"`
	FilesChanged *int                     `json:"filesChanged,omitempty"`
	Availability model.AvailabilityStatus `json:"availability"`
	Reason       string                   `json:"reason,omitempty"`
}

type tokenBucketTotals struct{ input, output, cacheRead, cacheWrite int64 }

func (t *tokenBucketTotals) add(o tokenBucketTotals) {
	t.input += o.input
	t.output += o.output
	t.cacheRead += o.cacheRead
	t.cacheWrite += o.cacheWrite
}

func ProjectSummaryFromCatalog(root string, projection ProjectCatalog) (ProjectSummary, error) {
	name, path, _ := (&store.Store{Root: root}).ProjectMeta(projection.ProjectID)
	instructions, calls, artifacts := catalogCounts(projection)
	attr := projection.Derivations.ActiveAttribution
	buckets, complete := tokenBreakdown(projection.Derivations.Usage)
	result := ProjectSummary{ProjectID: projection.ProjectID, Name: name, Path: path, Sessions: len(projection.Sessions), Instructions: instructions, TotalTokens: projection.KnownUsageTokens, TokensInput: buckets.input, TokensOutput: buckets.output, TokensCacheRead: buckets.cacheRead, TokensCacheCreation: buckets.cacheWrite, TokenBreakdownComplete: complete, Changes: changeSummary(projection.Derivations.Operations), CostUnpricedInstructions: incompleteCosts(projection.Derivations.ActiveCost), Runs: runCount(projection), Calls: calls, Usage: usageView(projection.Derivations.Usage, attr), EstimatedCost: projection.Derivations.ActiveCost.KnownTotals, CostMissing: projection.Derivations.ActiveCost.MissingCost, Operations: len(projection.Derivations.Operations.Operations), Artifacts: artifacts, PricingVersion: projection.Derivations.ActiveCost.PricingVersion, AttributionPolicy: projection.Derivations.ActivePolicyVersion, Source: "catalog"}
	result.Period = periodFromCatalog(projection)
	result.Observation = observationFromCatalog(projection)
	result.Unresolved = unresolved(projection.Derivations.Operations, projection.Derivations.ActiveCost, attr, result.Observation.MissingPaths)
	records, err := (billing.Store{Root: root}).List()
	if err != nil {
		return ProjectSummary{}, err
	}
	entityKeys := map[string]bool{}
	for _, record := range projection.Derivations.ActiveAttribution.Records {
		entityKeys[eventEntityKey(record.Usage)] = true
		entityKeys[eventEntityKey(record.AppliesTo)] = true
		for _, ref := range record.Instructions {
			entityKeys[eventEntityKey(ref)] = true
		}
		for _, assigned := range record.Assigned {
			entityKeys[eventEntityKey(assigned.Instruction)] = true
		}
	}
	for _, record := range projection.Derivations.ActiveCost.Records {
		entityKeys[eventEntityKey(record.Usage)] = true
		entityKeys[eventEntityKey(record.Call)] = true
	}
	records = filterBillingKeys(records, projection.ProjectID, entityKeys)
	result.ActualSpend, err = billing.Summarize(records)
	result.ActualSpendScope = "project-or-entity-linked"
	return result, err
}

func SessionSummaryFromCatalog(session Session) SessionSummary {
	var instructions, calls, artifacts uint64
	var start, end time.Time
	for _, run := range session.Runs {
		instructions += run.InstructionCount
		calls += run.ModelCallCount
		artifacts += run.ArtifactCount
		if !run.FirstReceivedAt.IsZero() && (start.IsZero() || run.FirstReceivedAt.Before(start)) {
			start = run.FirstReceivedAt
		}
		if run.LastReceivedAt.After(end) {
			end = run.LastReceivedAt
		}
	}
	derived := session.Derivations
	buckets, complete := tokenBreakdown(derived.Usage)
	return SessionSummary{
		Provider: session.Provider, SessionID: session.SessionID, StartedAt: start,
		Instructions: int(instructions), TotalTokens: derived.Usage.KnownSubtotal,
		TokensInput: buckets.input, TokensOutput: buckets.output, TokensCacheRead: buckets.cacheRead, TokensCacheCreation: buckets.cacheWrite,
		TokenBreakdownComplete: complete, Changes: changeSummary(derived.Operations),
		CostUnpricedInstructions: incompleteCosts(derived.Cost), Runs: len(session.Runs), EndedAt: end,
		Calls: int(calls), Usage: usageView(derived.Usage, derived.Attribution),
		EstimatedCost: derived.Cost.KnownTotals, CostMissing: derived.Cost.MissingCost,
		Operations: len(derived.Operations.Operations), Artifacts: int(artifacts),
		Unresolved: len(derived.Missing), RunSummaries: session.Runs,
	}
}

func catalogCounts(c ProjectCatalog) (int, int, int) {
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

func runCount(c ProjectCatalog) int {
	n := 0
	for _, s := range c.Sessions {
		n += len(s.Runs)
	}
	return n
}

func periodFromCatalog(c ProjectCatalog) Period {
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

func observationFromCatalog(c ProjectCatalog) Observation {
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

func usageView(u reconcile.Result, a attribution.Result) Usage {
	return Usage{KnownSubtotal: u.KnownSubtotal, DirectTokens: a.DirectTokens, SharedUnallocated: a.SharedUnallocated, Undecomposed: u.Undecomposed, UnattributedTokens: a.UnattributedTokens, AllocatedEstimate: a.AllocatedEstimate, Unknown: u.Unknown || a.MissingUsage}
}
