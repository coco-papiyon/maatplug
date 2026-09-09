// Package pricing scrapes each provider's public pricing/docs page for
// current per-model USD-per-million-token rates, and reads/writes the flat
// JSON config file cmd/maatplug-pricing-fetch produces from that data.
//
// There is no stable structured pricing API for either provider (see
// FetchClaude and FetchCodex's doc comments), so every fetch is inherently
// best-effort: a page redesign can silently return zero results. The
// fetch-and-merge policy (cmd/maatplug-pricing-fetch/main.go) is: try a live
// fetch; on failure, keep whatever was already on record for that provider;
// if nothing was ever recorded either, leave it empty. This package itself
// only fetches/parses one provider's page at a time and does not implement
// that merge policy — see the cmd for that.
package pricing

import (
	"encoding/json"
	"os"
	"time"
)

// ModelPricing is one model's USD-per-million-token rates, as last
// successfully scraped from its provider's public pricing page.
type ModelPricing struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`

	InputPerMillion       float64 `json:"inputPerMillion"`
	CachedInputPerMillion float64 `json:"cachedInputPerMillion"`
	// CacheWritePerMillion is 0 for providers with no separate cache-write
	// rate (Codex/OpenAI has none as of this writing; see FetchCodex).
	CacheWritePerMillion float64 `json:"cacheWritePerMillion"`
	OutputPerMillion     float64 `json:"outputPerMillion"`

	SourceURL   string    `json:"sourceUrl"`
	RetrievedAt time.Time `json:"retrievedAt"`
}

// Document is the on-disk config file schema cmd/maatplug-pricing-fetch
// reads and writes.
type Document struct {
	GeneratedAt time.Time      `json:"generatedAt"`
	Models      []ModelPricing `json:"models"`
}

// ForProvider returns the subset of d.Models with the given Provider, in
// their original order.
func (d Document) ForProvider(provider string) []ModelPricing {
	var out []ModelPricing
	for _, m := range d.Models {
		if m.Provider == provider {
			out = append(out, m)
		}
	}
	return out
}

// Lookup returns the ModelPricing on record for an exact provider/model
// pair. Model ids are provider-specific to begin with, so an exact match is
// the right join key — there's no cross-provider ambiguity to resolve.
func (d Document) Lookup(provider, model string) (ModelPricing, bool) {
	for _, m := range d.Models {
		if m.Provider == provider && m.Model == model {
			return m, true
		}
	}
	return ModelPricing{}, false
}

// Cost returns the USD cost of a turn's token usage at this model's rates.
// Callers pass the same input/cachedInput/cacheWrite/output buckets
// model.TokenUsage carries (see internal/aggregate, the only caller).
func (m ModelPricing) Cost(input, cachedInput, cacheWrite, output int) float64 {
	const perMillion = 1_000_000
	return float64(input)*m.InputPerMillion/perMillion +
		float64(cachedInput)*m.CachedInputPerMillion/perMillion +
		float64(cacheWrite)*m.CacheWritePerMillion/perMillion +
		float64(output)*m.OutputPerMillion/perMillion
}

// Load reads a pricing Document from path. A missing file is not an error —
// it returns a zero Document — since the first-ever run of
// cmd/maatplug-pricing-fetch has nothing on disk to fall back to yet.
func Load(path string) (Document, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Document{}, nil
		}
		return Document{}, err
	}
	var doc Document
	if err := json.Unmarshal(b, &doc); err != nil {
		return Document{}, err
	}
	return doc, nil
}

// Save writes doc to path as indented JSON, creating parent directories as
// needed.
func Save(path string, doc Document) error {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
