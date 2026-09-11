// Package pricing acquires Provider pricing, stores immutable versions and
// source snapshots, and resolves rates by model, service, contract, usage
// class, effective interval, quantity, currency, and additional conditions.
// The Document and ModelPricing types remain the acquisition format used by
// the existing public-page parsers and build-time pricing fetch command.
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

	SourceURL         string    `json:"sourceUrl"`
	RetrievedAt       time.Time `json:"retrievedAt"`
	AcquisitionIssues []string  `json:"acquisitionIssues,omitempty"`
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

// Lookup supports the legacy embedded reference-price view. Phase 5 event
// costing uses Version.Resolve instead so all rate conditions are evaluated.
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
