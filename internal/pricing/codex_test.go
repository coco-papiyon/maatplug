package pricing

import (
	"testing"
	"time"
)

// codexPricingFixture mirrors the real raw HTML shape of
// developers.openai.com/api/docs/models, verified against the live page:
// each priced chat/reasoning model has a "Model ID <id>" heading directly
// followed by its own "Input price $X / Input MTok Output price $Y / Output
// MTok" text (no cached-input rate on this page — that's only on each
// model's own detail page, see codexLunaPricingFixture below and
// TestParseCodexCachedInput_DetailPage). The page also links to several
// dozen unrelated non-chat models (image, realtime, transcription) via the
// same href="/api/docs/models/<id>" pattern without a "Model ID" heading —
// modelIDNoise below stands in for those, to confirm they're ignored
// instead of breaking a positional id/rate pairing.
const codexPricingFixture = `<div class="cards">
<a href="/api/docs/models/gpt-6-astra" aria-label="View GPT-6 Astra">GPT-6 Astra</a>
<a href="/api/docs/models/gpt-5.6-sol" aria-label="View GPT-5.6 Sol">GPT-5.6 Sol</a>
<a href="/api/docs/models/gpt-5.6-terra" aria-label="View GPT-5.6 Terra">GPT-5.6 Terra</a>
` + modelIDNoise + `
</div>
<section>
<h2>GPT-6 Astra</h2><p>Our most capable model, built for the hardest end-to-end work</p>
Model ID gpt-6-astra Reasoning low medium high xhigh max
Input price $10 / Input MTok Output price $50 / Output MTok
</section>
<section>
<h2>GPT-5.6 Sol</h2><p>Flagship model for complex professional work</p>
Model ID gpt-5.6-sol Alias gpt-5.6 Reasoning none low medium high xhigh max
Input price $4 / Input MTok Output price $20 / Output MTok
</section>
<section>
<h2>GPT-5.6 Terra</h2><p>GPT-5.6 model that balances intelligence and cost</p>
Model ID gpt-5.6-terra Reasoning none low medium high xhigh max
Input price $2 / Input MTok Output price $12 / Output MTok
</section>`

// modelIDNoise stands in for the several dozen non-chat model links
// (image/realtime/transcription) that share the catalog page's
// href="/api/docs/models/<id>" pattern but never get a "Model ID" heading
// or a price block — real examples verified on the live page include
// gpt-image-2.5-sunburst, gpt-realtime-2.1, and gpt-4o-transcribe.
const modelIDNoise = `<a href="/api/docs/models/gpt-image-2.5-sunburst">GPT Image</a>
<a href="/api/docs/models/gpt-realtime-2.1">Realtime</a>`

const codexLunaPricingFixture = `<div class="cards">
<a href="/api/docs/models/gpt-5.6-luna">GPT-5.6 Luna</a>
</div>
<section><h2>GPT-5.6 Luna</h2>Model ID gpt-5.6-luna Reasoning none low medium high xhigh max Input price $0.20 / Input MTok Output price $1.20 / Output MTok</section>`

func TestParseCodexPricing_PairsIDsWithRatesInOrder(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	got, err := parseCodexPricing(codexPricingFixture, now)
	if err != nil {
		t.Fatal(err)
	}

	// CachedInputPerMillion is 0 here — the catalog page itself carries no
	// cached-input rate (see FetchCodex, which enriches it from each
	// model's own detail page afterward; TestParseCodexPricing_
	// AllowsModelsWithoutCachedInputRate and TestParseCodexCachedInput_
	// DetailPage cover that path).
	want := []ModelPricing{
		{Provider: "codex", Model: "gpt-6-astra", InputPerMillion: 10, CachedInputPerMillion: 0, OutputPerMillion: 50},
		{Provider: "codex", Model: "gpt-5.6-sol", InputPerMillion: 4, CachedInputPerMillion: 0, OutputPerMillion: 20},
		{Provider: "codex", Model: "gpt-5.6-terra", InputPerMillion: 2, CachedInputPerMillion: 0, OutputPerMillion: 12},
	}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Provider != w.Provider || g.Model != w.Model || g.InputPerMillion != w.InputPerMillion ||
			g.CachedInputPerMillion != w.CachedInputPerMillion || g.OutputPerMillion != w.OutputPerMillion {
			t.Errorf("got[%d] = %+v, want %+v", i, g, w)
		}
		if g.CacheWritePerMillion != 0 {
			t.Errorf("got[%d].CacheWritePerMillion = %v, want 0 (OpenAI has no separate cache-write rate)", i, g.CacheWritePerMillion)
		}
		if !g.RetrievedAt.Equal(now) {
			t.Errorf("got[%d].RetrievedAt = %v, want %v", i, g.RetrievedAt, now)
		}
	}
}

func TestParseCodexPricing_AllowsModelsWithoutCachedInputRate(t *testing.T) {
	got, err := parseCodexPricing(codexLunaPricingFixture, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Model != "gpt-5.6-luna" || got[0].InputPerMillion != 0.20 || got[0].CachedInputPerMillion != 0 || got[0].OutputPerMillion != 1.20 {
		t.Fatalf("got %+v, want Luna with input 0.20, cached input 0, output 1.20", got)
	}
}

func TestParseCodexCachedInput_DetailPage(t *testing.T) {
	rawHTML := `<section><h2>Pricing</h2><div>Input</div><div>$0.20</div><div>Cached input</div><div>$0.02</div><div>Output</div><div>$1.20</div></section>`
	got, ok := parseCodexCachedInput(rawHTML)
	if !ok || got != 0.02 {
		t.Fatalf("parseCodexCachedInput() = %v, %v; want 0.02, true", got, ok)
	}
}

func TestParseCodexPricing_IgnoresNonChatModelLinks(t *testing.T) {
	// modelIDNoise's links share the catalog page's href="/api/docs/models/
	// <id>" pattern but have no "Model ID" heading or price block of their
	// own (real non-chat models like image/realtime/transcription) — they
	// must not produce a spurious ModelPricing entry, and must not be
	// mistaken for extra rate blocks belonging to a real model.
	got, err := parseCodexPricing(modelIDNoise, time.Now())
	if err == nil {
		t.Fatalf("parseCodexPricing(modelIDNoise) = %+v, want an error (no priced model block present)", got)
	}
}

func TestParseCodexPricing_NoRecognizedBlocksIsAnError(t *testing.T) {
	_, err := parseCodexPricing("<html>no pricing here</html>", time.Now())
	if err == nil {
		t.Fatal("parseCodexPricing() error = nil, want an error when the page structure doesn't match")
	}
}
