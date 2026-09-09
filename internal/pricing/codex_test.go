package pricing

import (
	"testing"
	"time"
)

// codexPricingFixture mirrors the real raw HTML shape of
// developers.openai.com/api/docs/models, verified against the live
// page: each model's card link carries its id early
// (href="/api/docs/models/<id>"), and later there is one "Pricing Per 1M
// tokens" detail block per model, in the same order.
const codexPricingFixture = `<div class="cards">
<a href="/api/docs/models/gpt-6-astra" aria-label="View GPT-6 Astra">GPT-6 Astra</a>
<a href="/api/docs/models/gpt-5.6-sol" aria-label="View GPT-5.6 Sol">GPT-5.6 Sol</a>
<a href="/api/docs/models/gpt-5.6-terra" aria-label="View GPT-5.6 Terra">GPT-5.6 Terra</a>
</div>
<section>
<h2>GPT-6 Astra</h2><p>Our most capable model, built for the hardest end-to-end work</p>
<h3>Pricing</h3><p>Per 1M tokens</p>
<div>Input</div><div>$10.00</div>
<div>Cached input</div><div>$1.00</div>
<div>Output</div><div>$50.00</div>
</section>
<section>
<h2>GPT-5.6 Sol</h2><p>Flagship model for complex professional work</p>
<h3>Pricing</h3><p>Per 1M tokens</p>
<div>Input</div><div>$4.00</div>
<div>Cached input</div><div>$0.40</div>
<div>Output</div><div>$20.00</div>
</section>
<section>
<h2>GPT-5.6 Terra</h2><p>GPT-5.6 model that balances intelligence and cost</p>
<h3>Pricing</h3><p>Per 1M tokens</p>
<div>Input</div><div>$2.00</div>
<div>Cached input</div><div>$0.20</div>
<div>Output</div><div>$12.00</div>
</section>`

const codexLunaPricingFixture = `<div class="cards">
<a href="/api/docs/models/gpt-5.6-luna">GPT-5.6 Luna</a>
</div>
<section><h2>GPT-5.6 Luna</h2><div>Input $0.20</div><div>Output $1.20</div></section>`

func TestParseCodexPricing_PairsIDsWithRatesInOrder(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	got, err := parseCodexPricing(codexPricingFixture, now)
	if err != nil {
		t.Fatal(err)
	}

	want := []ModelPricing{
		{Provider: "codex", Model: "gpt-6-astra", InputPerMillion: 10, CachedInputPerMillion: 1, OutputPerMillion: 50},
		{Provider: "codex", Model: "gpt-5.6-sol", InputPerMillion: 4, CachedInputPerMillion: 0.40, OutputPerMillion: 20},
		{Provider: "codex", Model: "gpt-5.6-terra", InputPerMillion: 2, CachedInputPerMillion: 0.20, OutputPerMillion: 12},
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

func TestParseCodexPricing_MismatchedCountsFailsRatherThanGuess(t *testing.T) {
	// One more card link than there are rate blocks: pairing by position
	// could silently attribute the wrong price to the wrong model, so this
	// must return an error instead of a best guess.
	html := codexPricingFixture + `<a href="/api/docs/models/gpt-5.6-luna">GPT-5.6 Luna</a>`
	_, err := parseCodexPricing(html, time.Now())
	if err == nil {
		t.Fatal("parseCodexPricing() error = nil, want an error on a model/rate count mismatch")
	}
}

func TestParseCodexPricing_NoRecognizedBlocksIsAnError(t *testing.T) {
	_, err := parseCodexPricing("<html>no pricing here</html>", time.Now())
	if err == nil {
		t.Fatal("parseCodexPricing() error = nil, want an error when the page structure doesn't match")
	}
}
