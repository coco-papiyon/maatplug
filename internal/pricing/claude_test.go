package pricing

import (
	"testing"
	"time"
)

// claudePricingFixture mirrors the real (already tag-stripped, whitespace-
// collapsed) text shape of claude.com/pricing, verified against the live
// page: each model is "<Heading> <one-line description> Input $X / MTok
// Output $Y / MTok Prompt caching Read $Z / MTok Write $W / MTok", back to
// back with no separator beyond that trailing "/ MTok".
const claudePricingFixture = `Claude Pricing Latest models Fable 5.1 Our most intelligent model Input $10 / MTok Output $50 / MTok Prompt caching Read $0.25 / MTok Write $12.50 / MTok Opus 5 Exceptional model for specialized tasks Input $5 / MTok Output $25 / MTok Prompt caching Read $0.50 / MTok Write $6.25 / MTok Sonnet 5 High-performance model for coding and agents Input $2 / MTok Output $10 / MTok Prompt caching Read $0.20 / MTok Write $2.50 / MTok Haiku 4.5 Fastest, most cost-efficient model Input $1 / MTok Output $5 / MTok Prompt caching Read $0.10 / MTok Write $1.25 / MTok Legacy models`

func TestParseClaudePricing_ExtractsEveryModel(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	got, err := parseClaudePricing(claudePricingFixture, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("len(got) = %d, want 4: %+v", len(got), got)
	}

	want := []ModelPricing{
		{Provider: "claude", Model: "claude-fable-5-1", InputPerMillion: 10, OutputPerMillion: 50, CachedInputPerMillion: 0.25, CacheWritePerMillion: 12.50},
		{Provider: "claude", Model: "claude-opus-5", InputPerMillion: 5, OutputPerMillion: 25, CachedInputPerMillion: 0.50, CacheWritePerMillion: 6.25},
		{Provider: "claude", Model: "claude-sonnet-5", InputPerMillion: 2, OutputPerMillion: 10, CachedInputPerMillion: 0.20, CacheWritePerMillion: 2.50},
		{Provider: "claude", Model: "claude-haiku-4-5", InputPerMillion: 1, OutputPerMillion: 5, CachedInputPerMillion: 0.10, CacheWritePerMillion: 1.25},
	}
	for i, w := range want {
		g := got[i]
		if g.Provider != w.Provider || g.Model != w.Model || g.InputPerMillion != w.InputPerMillion ||
			g.OutputPerMillion != w.OutputPerMillion || g.CachedInputPerMillion != w.CachedInputPerMillion ||
			g.CacheWritePerMillion != w.CacheWritePerMillion {
			t.Errorf("got[%d] = %+v, want %+v", i, g, w)
		}
		if !g.RetrievedAt.Equal(now) {
			t.Errorf("got[%d].RetrievedAt = %v, want %v", i, g.RetrievedAt, now)
		}
		if g.SourceURL != ClaudeSourceURL {
			t.Errorf("got[%d].SourceURL = %q, want %q", i, g.SourceURL, ClaudeSourceURL)
		}
	}
}

func TestParseClaudePricing_FirstModelHasNoLeadingMTokArtifact(t *testing.T) {
	// The very first model on the page has no preceding "... / MTok" text to
	// accidentally absorb into its heading — this guards the opposite edge
	// from TestParseClaudePricing_StripsMTokArtifactFromHeading.
	got, err := parseClaudePricing(claudePricingFixture, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Model != "claude-fable-5-1" {
		t.Errorf("got[0].Model = %q, want claude-fable-5-1", got[0].Model)
	}
}

func TestParseClaudePricing_StripsMTokArtifactFromHeading(t *testing.T) {
	// Every model after the first is immediately preceded by the previous
	// model's trailing "Write $W / MTok" — claudeHeadingRe's lookback would
	// otherwise capture "MTok Opus 5" as the heading instead of "Opus 5".
	got, err := parseClaudePricing(claudePricingFixture, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range got[1:] {
		if got := m.Model; got == "claude-mtok-opus-5" || got == "claude-mtok-sonnet-5" || got == "claude-mtok-haiku-4-5" {
			t.Errorf("Model = %q still carries the MTok artifact", got)
		}
	}
}

func TestParseClaudePricing_NoRecognizedBlocksIsAnError(t *testing.T) {
	_, err := parseClaudePricing("this page no longer mentions any pricing at all", time.Now())
	if err == nil {
		t.Fatal("parseClaudePricing() error = nil, want an error when the page structure doesn't match (caller falls back to previous data on error)")
	}
}
