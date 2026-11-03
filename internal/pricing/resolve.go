package pricing

import (
	"path"
	"sort"
	"strings"
	"time"
)

type Query struct {
	Provider   string            `json:"provider"`
	Service    string            `json:"service,omitempty"`
	Contract   string            `json:"contract,omitempty"`
	Model      string            `json:"model"`
	UsageClass string            `json:"usageClass"`
	Quantity   int64             `json:"quantity"`
	At         time.Time         `json:"at"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type ResolutionStatus string

const (
	ResolutionResolved         ResolutionStatus = "resolved"
	ResolutionUnregistered     ResolutionStatus = "unregistered"
	ResolutionConditionUnknown ResolutionStatus = "condition_unknown"
	ResolutionConflict         ResolutionStatus = "conflict"
	ResolutionNotApplicable    ResolutionStatus = "not_applicable"
)

type Resolution struct {
	Status     ResolutionStatus `json:"status"`
	Rate       *Rate            `json:"rate,omitempty"`
	Candidates []string         `json:"candidates,omitempty"`
	Reason     string           `json:"reason"`
}

type rateCandidate struct {
	rate  Rate
	score int
}

func (version Version) Resolve(query Query) Resolution {
	applicable := []rateCandidate{}
	unknown := []string{}
	for _, rate := range version.Rates {
		matched, missing := matches(rate, query)
		if missing {
			unknown = append(unknown, rate.ID)
			continue
		}
		if matched {
			applicable = append(applicable, rateCandidate{rate: rate, score: rate.Priority*10000 + specificity(rate)})
		}
	}
	if len(applicable) == 0 {
		if len(unknown) == 0 {
			if fallback := earliestApplicable(version.Rates, query); fallback != nil {
				return Resolution{Status: ResolutionResolved, Rate: fallback, Candidates: []string{fallback.ID}, Reason: "usage predates the earliest recorded price for this model; using the first price on record instead of leaving it unpriced"}
			}
		}
		sort.Strings(unknown)
		if len(unknown) > 0 {
			return Resolution{Status: ResolutionConditionUnknown, Candidates: unknown, Reason: "a matching rate requires service, contract, or attribute values that were not observed"}
		}
		return Resolution{Status: ResolutionUnregistered, Reason: "no rate matches provider, model, usage class, date, and quantity"}
	}
	sort.Slice(applicable, func(i, j int) bool {
		if applicable[i].score != applicable[j].score {
			return applicable[i].score > applicable[j].score
		}
		return applicable[i].rate.ID < applicable[j].rate.ID
	})
	bestScore := applicable[0].score
	best := []string{}
	for _, candidate := range applicable {
		if candidate.score != bestScore {
			break
		}
		best = append(best, candidate.rate.ID)
	}
	if len(best) > 1 {
		return Resolution{Status: ResolutionConflict, Candidates: best, Reason: "multiple equally specific rates apply"}
	}
	rate := applicable[0].rate
	return Resolution{Status: ResolutionResolved, Rate: &rate, Candidates: []string{rate.ID}, Reason: "highest-priority applicable rate"}
}

func matches(rate Rate, query Query) (matched, missing bool) {
	if !query.At.IsZero() && (query.At.Before(rate.EffectiveFrom) || rate.EffectiveTo != nil && !query.At.Before(*rate.EffectiveTo)) {
		return false, false
	}
	matched, missing = coreMatches(rate, query)
	if matched && query.At.IsZero() {
		missing = true
	}
	return matched, missing
}

// coreMatches checks every condition a rate must satisfy except its
// effective-date window: provider, usage class, model pattern, quantity
// thresholds, service, contract, and attributes. matches uses it for normal
// resolution; earliestApplicable uses it on its own (skipping the date
// window entirely) to find rates that would otherwise apply to query.
func coreMatches(rate Rate, query Query) (matched, missing bool) {
	if rate.Provider != query.Provider || rate.UsageClass != query.UsageClass || query.Quantity < 0 {
		return false, false
	}
	modelMatch, err := path.Match(rate.ModelPattern, query.Model)
	if err != nil || !modelMatch {
		return false, false
	}
	if rate.MinQuantity != nil && query.Quantity < *rate.MinQuantity || rate.MaxQuantity != nil && query.Quantity > *rate.MaxQuantity {
		return false, false
	}
	conditionMissing := false
	for _, actual := range [][2]string{{rate.Service, query.Service}, {rate.Contract, query.Contract}} {
		if actual[0] != "" && actual[1] != "" && actual[0] != actual[1] {
			return false, false
		}
		conditionMissing = conditionMissing || actual[0] != "" && actual[1] == ""
	}
	for key, expected := range rate.Attributes {
		actual, ok := query.Attributes[key]
		if ok && actual != expected {
			return false, false
		}
		conditionMissing = conditionMissing || !ok
	}
	if conditionMissing {
		return false, true
	}
	return true, false
}

// earliestApplicable finds, among rates that would otherwise match query
// (same provider/model/usage-class/service/contract/attributes/quantity)
// but whose EffectiveFrom postdates query.At, the one with the earliest
// EffectiveFrom — the first price maatplug ever recorded for it. Resolve
// only calls this once normal matching finds nothing at all: a usage
// record from before the earliest known price for its model has no
// earlier price to apply, so the first price on record is the best
// available stand-in rather than leaving the usage permanently unpriced —
// per the user's call: when no pricing data exists yet for that time,
// use whatever was first acquired.
func earliestApplicable(rates []Rate, query Query) *Rate {
	if query.At.IsZero() {
		return nil
	}
	var best *Rate
	for index := range rates {
		rate := &rates[index]
		if !query.At.Before(rate.EffectiveFrom) {
			continue
		}
		if matched, missing := coreMatches(*rate, query); !matched || missing {
			continue
		}
		if best == nil || rate.EffectiveFrom.Before(best.EffectiveFrom) {
			best = rate
		}
	}
	return best
}

func specificity(rate Rate) int {
	score := len(strings.Map(func(r rune) rune {
		if r == '*' || r == '?' || r == '[' || r == ']' {
			return -1
		}
		return r
	}, rate.ModelPattern))
	if rate.Service != "" {
		score += 1000
	}
	if rate.Contract != "" {
		score += 1000
	}
	score += len(rate.Attributes) * 100
	if rate.MinQuantity != nil || rate.MaxQuantity != nil {
		score += 10
	}
	return score
}
