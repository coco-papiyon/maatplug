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

// codexModelBlockRe matches one priced model's card in a single pass: its
// exact API model id, then its Per-1M-token rates, anchored on "Model ID
// <id>" heading text immediately followed (within a bounded window, to
// avoid bridging into an unrelated later block if a future page omits
// pricing for some model) by that model's own "Input price $X / Input MTok
// [Cached input price $Y / Cached input MTok] Output price $Z / Output
// MTok" text (verified against the real, current page). Only OpenAI's
// priced chat/reasoning models carry a "Model ID" heading on this catalog
// page; the several dozen other href="/api/docs/models/<id>" links
// elsewhere on the page (image, realtime/voice, and transcription models,
// each priced in an entirely different unit) do not — so anchoring on this
// heading naturally excludes them, instead of scraping ids and rates as two
// independent lists and requiring their counts to match by position (this
// package's previous approach, which broke when OpenAI's page grew more
// non-chat model links than priced chat-model cards).
var codexModelBlockRe = regexp.MustCompile(`Model ID ([a-z0-9.\-]+).{0,200}?Input price \$([0-9.]+) / Input MTok(?: Cached input price \$([0-9.]+) / Cached input MTok)? Output price \$([0-9.]+) / Output MTok`)

// codexCachedInputRe matches the cache-read rate on a model's own detail
// page (developers.openai.com/api/docs/models/<id>), which — unlike the
// catalog page's per-model block above — still spells it out as "Input
// $X.XX Cached input $Y.YY Output $Z.ZZ" (verified against the real page).
var codexCachedInputRe = regexp.MustCompile(`Cached input(?: price)? \$([0-9.]+)`)

// FetchCodex scrapes OpenAI's public model catalog for every priced chat/
// reasoning model's per-million-token rates. Like FetchClaude, this is a
// prose/HTML doc page, not a stable API — see that function's doc comment
// for the general caveat.
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
			models[i].AcquisitionIssues = append(models[i].AcquisitionIssues, "cache_read_input: "+fetchErr.Error())
			continue
		}
		if cached, ok := parseCodexCachedInput(detailHTML); ok {
			models[i].CachedInputPerMillion = cached
		} else {
			models[i].AcquisitionIssues = append(models[i].AcquisitionIssues, "cache_read_input: rate not recognized on detail page")
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
	text := normalizeHTML(rawHTML)
	matches := codexModelBlockRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("found no \"Model ID <id> ... Input price ... Output price ...\" block on %s (page structure may have changed)", CodexSourceURL)
	}

	out := make([]ModelPricing, 0, len(matches))
	for _, match := range matches {
		id, inputText, cachedText, outputText := match[1], match[2], match[3], match[4]
		input, errInput := strconv.ParseFloat(inputText, 64)
		cached := 0.0
		var errCached error
		if cachedText != "" {
			cached, errCached = strconv.ParseFloat(cachedText, 64)
		}
		output, errOutput := strconv.ParseFloat(outputText, 64)
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
