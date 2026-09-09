package aggregate

import (
	"sync"

	"maatplug/internal/model"
	"maatplug/internal/pricing"
)

// pricingOnce loads internal/pricing's embedded pricing.json exactly once
// per process. A load failure (should only happen if pricing.json itself is
// malformed, which build.bat's fetch step would already have caught) leaves
// pricingDoc as an empty Document rather than panicking — cost simply comes
// back unavailable for every turn, the same graceful-degradation behavior
// the rest of this package already gives clocSkipped/tokensUnavailable data.
var (
	pricingOnce sync.Once
	pricingDoc  pricing.Document
)

func loadPricing() pricing.Document {
	pricingOnce.Do(func() {
		doc, err := pricing.Embedded()
		if err == nil {
			pricingDoc = doc
		}
	})
	return pricingDoc
}

// costFor prices one turn's token usage against doc (the pricing reference
// data for provider/modelName). ok is false when modelName is empty
// (adapter couldn't determine it) or has no matching entry in doc (e.g. an
// unrecognized/new model id) — callers surface that as "unpriced" rather
// than silently treating it as free.
//
// doc is a parameter rather than read from loadPricing() internally so this
// stays a pure function of its inputs, testable against a synthetic
// pricing.Document without depending on whatever internal/pricing/pricing.json
// happens to contain when the test runs (mirrors internal/pricing's own
// split between its live fetch and pure-parse test coverage).
func costFor(doc pricing.Document, provider, modelName string, tokens model.TokenUsage) (float64, bool) {
	if modelName == "" {
		return 0, false
	}
	rate, ok := doc.Lookup(provider, modelName)
	if !ok {
		return 0, false
	}
	return rate.Cost(tokens.Input, tokens.CacheRead, tokens.CacheCreation, tokens.Output), true
}
