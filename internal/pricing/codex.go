package pricing

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CodexSourceURL is OpenAI's public model catalog, which includes all
// selectable models and their current API prices.
const CodexSourceURL = "https://developers.openai.com/api/docs/models"

// codexModelIDRe matches each model's card link, which carries its exact API
// model id directly (verified against the real page, e.g.
// href="/api/docs/models/gpt-5.6-sol") — unlike the Claude page, no
// display-name-to-id guessing is needed here.
var codexModelIDRe = regexp.MustCompile(`href="/api/docs/models/([a-z0-9.\-]+)"`)

// codexRateRe matches one model's rate block, verified against the page's
// real (normalized) text, e.g.:
//
//	"Pricing Per 1M tokens Input $4.00 Cached input $0.40 Output $20.00"
//
// OpenAI's pricing has no separate cache-write rate the way Anthropic's
// does.
var codexRateRe = regexp.MustCompile(`Input(?: price)? \$([0-9.]+)(?: Cached input(?: price)? \$([0-9.]+))? Output(?: price)? \$([0-9.]+)`)
var codexCachedInputRe = regexp.MustCompile(`Cached input(?: price)? \$([0-9.]+)`)

// FetchCodex scrapes OpenAI's public model catalog for every listed
// model's per-million-token rates. Like FetchClaude, this is a prose/HTML
// doc page, not a stable API — see that function's doc comment for the
// general caveat.
//
// The page lists each model's id once, early, as a card link
// (href="/api/docs/models/<id>"), and later has one "Pricing Per 1M tokens"
// detail block per model, in the same order. Unlike the Claude page there is
// no clean, reliably-matchable heading text immediately before each rate
// block to anchor on, so this pairs the two lists positionally by index
// instead. If the counts don't match, something about the page shape
// changed enough that positional pairing could silently mismatch a model to
// the wrong price, so this bails out entirely rather than guess.
func FetchCodex(ctx context.Context) ([]ModelPricing, error) {
	rawHTML, err := fetchRaw(ctx, CodexSourceURL)
	if err != nil {
		return nil, fmt.Errorf("fetch codex pricing: %w", err)
	}
	now := time.Now().UTC()
	models, err := parseCodexPricing(rawHTML, now)
	if err != nil {
		return nil, err
	}

	// The catalog page lists input/output prices, but cached-input pricing is
	// available on each model's detail page. Enrich each entry from that page;
	// retain the catalog result if an individual detail request fails.
	for i := range models {
		detailURL := strings.TrimRight(CodexSourceURL, "/") + "/" + models[i].Model
		detailHTML, fetchErr := fetchRaw(ctx, detailURL)
		if fetchErr != nil {
			continue
		}
		if cached, ok := parseCodexCachedInput(detailHTML); ok {
			models[i].CachedInputPerMillion = cached
		}
	}
	return models, nil
}

func parseCodexCachedInput(rawHTML string) (float64, bool) {
	match := codexCachedInputRe.FindStringSubmatch(normalizeHTML(rawHTML))
	if len(match) != 2 {
		return 0, false
	}
	cached, err := strconv.ParseFloat(match[1], 64)
	return cached, err == nil
}

// parseCodexPricing is FetchCodex's pure extraction step, split out so it
// can be unit-tested against fixture HTML without a live network call.
func parseCodexPricing(rawHTML string, now time.Time) ([]ModelPricing, error) {
	ids := uniqueOrderedMatches(codexModelIDRe, rawHTML)
	text := normalizeHTML(rawHTML)
	rates := codexRateRe.FindAllStringSubmatch(text, -1)

	if len(ids) == 0 || len(rates) == 0 || len(ids) != len(rates) {
		return nil, fmt.Errorf("found %d model id(s) and %d rate block(s) on %s (want a matching nonzero count; page structure may have changed)", len(ids), len(rates), CodexSourceURL)
	}

	out := make([]ModelPricing, 0, len(ids))
	for i, id := range ids {
		input, errInput := strconv.ParseFloat(rates[i][1], 64)
		cached := 0.0
		var errCached error
		if rates[i][2] != "" {
			cached, errCached = strconv.ParseFloat(rates[i][2], 64)
		}
		output, errOutput := strconv.ParseFloat(rates[i][3], 64)
		if errInput != nil || errCached != nil || errOutput != nil {
			continue
		}
		out = append(out, ModelPricing{
			Provider:              "codex",
			Model:                 id,
			InputPerMillion:       input,
			CachedInputPerMillion: cached,
			OutputPerMillion:      output,
			SourceURL:             CodexSourceURL,
			RetrievedAt:           now,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no Codex model pricing parsed from %s", CodexSourceURL)
	}
	return out, nil
}
