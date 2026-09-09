package aggregate

import (
	"testing"

	"maatplug/internal/model"
	"maatplug/internal/pricing"
)

func testDoc() pricing.Document {
	return pricing.Document{
		Models: []pricing.ModelPricing{
			{
				Provider: "claude", Model: "claude-sonnet-5",
				InputPerMillion: 2, CachedInputPerMillion: 0.2, CacheWritePerMillion: 2.5, OutputPerMillion: 10,
			},
		},
	}
}

func TestCostFor_PricesKnownModel(t *testing.T) {
	tokens := model.TokenUsage{Input: 1_000_000, Output: 1_000_000, CacheRead: 1_000_000, CacheCreation: 1_000_000}
	cost, ok := costFor(testDoc(), "claude", "claude-sonnet-5", tokens)
	if !ok {
		t.Fatal("costFor: ok = false, want true for a model on record")
	}
	const want = 2 + 0.2 + 2.5 + 10 // 1M tokens in each bucket, at testDoc's per-million rates
	if cost != want {
		t.Errorf("cost = %v, want %v", cost, want)
	}
}

func TestCostFor_UnrecognizedModelIsUnpriced(t *testing.T) {
	if _, ok := costFor(testDoc(), "claude", "claude-nonexistent", model.TokenUsage{Input: 100}); ok {
		t.Error("costFor: ok = true for a model absent from the pricing doc, want false")
	}
}

func TestCostFor_WrongProviderIsUnpriced(t *testing.T) {
	// Same model id, different provider than what's on record — must not
	// match across providers.
	if _, ok := costFor(testDoc(), "codex", "claude-sonnet-5", model.TokenUsage{Input: 100}); ok {
		t.Error("costFor: ok = true across providers, want false")
	}
}

func TestCostFor_EmptyModelIsUnpriced(t *testing.T) {
	if _, ok := costFor(testDoc(), "claude", "", model.TokenUsage{Input: 100}); ok {
		t.Error("costFor: ok = true for an empty model name, want false")
	}
}
