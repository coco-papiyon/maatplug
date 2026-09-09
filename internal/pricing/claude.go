package pricing

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ClaudeSourceURL is Anthropic's public pricing page. It redirects to
// claude.com/pricing as of this writing; net/http's default client follows
// redirects automatically, so this constant is the stable, well-known URL to
// request rather than the redirect target itself.
const ClaudeSourceURL = "https://www.anthropic.com/pricing"

// claudeRateBlockRe matches one model's rate block, verified against the
// page's real (normalized) text, e.g.:
//
//	"Input $2 / MTok Output $10 / MTok Prompt caching Read $0.20 / MTok Write $2.50 / MTok"
var claudeRateBlockRe = regexp.MustCompile(`Input \$([0-9.]+) / MTok Output \$([0-9.]+) / MTok Prompt caching Read \$([0-9.]+) / MTok Write \$([0-9.]+) / MTok`)

// claudeHeadingRe matches a model heading like "Sonnet 5", "Haiku 4.5", or
// "Fable 5.1" — a capitalized name followed by a version number. Each
// heading is immediately followed by a one-line description before the rate
// block (e.g. "Sonnet 5 High-performance model for coding and agents Input
// $2 / MTok ..."), so extraction looks backward from each rate block match
// for the last heading-shaped text within a bounded window rather than
// matching the description text.
var claudeHeadingRe = regexp.MustCompile(`\b[A-Z][a-zA-Z]+(?: [A-Z][a-zA-Z]+)? [0-9]+(?:\.[0-9]+)?\b`)

// claudeHeadingLookback bounds how far back before a rate block claudeHeadingRe
// is allowed to search for that block's model heading.
const claudeHeadingLookback = 200

// FetchClaude scrapes Anthropic's public pricing page for every listed
// Claude model's per-million-token rates. The page is prose/marketing HTML,
// not a stable structured API, so this is inherently best-effort: a page
// redesign can silently return zero results, which the caller
// (cmd/maatplug-pricing-fetch) treats as a failed fetch and falls back to
// whatever pricing was already on record rather than overwriting it with
// nothing (see package doc).
func FetchClaude(ctx context.Context) ([]ModelPricing, error) {
	text, err := fetchNormalizedText(ctx, ClaudeSourceURL)
	if err != nil {
		return nil, fmt.Errorf("fetch claude pricing: %w", err)
	}
	return parseClaudePricing(text, time.Now().UTC())
}

// parseClaudePricing is FetchClaude's pure extraction step, split out so it
// can be unit-tested against fixture text without a live network call.
func parseClaudePricing(text string, now time.Time) ([]ModelPricing, error) {
	var out []ModelPricing
	for _, m := range claudeRateBlockRe.FindAllStringSubmatchIndex(text, -1) {
		windowStart := m[0] - claudeHeadingLookback
		if windowStart < 0 {
			windowStart = 0
		}
		heading := lastMatch(claudeHeadingRe, text[windowStart:m[0]])
		// The previous model's rate block ends in "... / MTok" right before
		// the next model's heading (e.g. "Write $6.25 / MTok Opus 5 High-
		// performance..."), and claudeHeadingRe's optional second word
		// greedily absorbs "MTok" as if it were part of the heading
		// ("MTok Opus 5"). Strip it back off rather than tighten the regex
		// with a lookbehind Go's RE2 engine doesn't support.
		heading = strings.TrimPrefix(heading, "MTok ")
		if heading == "" {
			continue // no recognizable model heading immediately before this rate block; skip rather than guess which model it belongs to
		}

		input, errInput := strconv.ParseFloat(text[m[2]:m[3]], 64)
		output, errOutput := strconv.ParseFloat(text[m[4]:m[5]], 64)
		cachedRead, errCachedRead := strconv.ParseFloat(text[m[6]:m[7]], 64)
		cacheWrite, errCacheWrite := strconv.ParseFloat(text[m[8]:m[9]], 64)
		if errInput != nil || errOutput != nil || errCachedRead != nil || errCacheWrite != nil {
			continue
		}

		out = append(out, ModelPricing{
			Provider:              "claude",
			Model:                 claudeModelID(heading),
			InputPerMillion:       input,
			CachedInputPerMillion: cachedRead,
			CacheWritePerMillion:  cacheWrite,
			OutputPerMillion:      output,
			SourceURL:             ClaudeSourceURL,
			RetrievedAt:           now,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no Claude model pricing blocks recognized on %s (page structure may have changed)", ClaudeSourceURL)
	}
	return out, nil
}

// claudeModelID converts a pricing page heading like "Sonnet 5" or
// "Haiku 4.5" into maatplug's best guess at the API model id it corresponds
// to ("claude-sonnet-5", "claude-haiku-4-5"), by lowercasing and replacing
// spaces/dots with hyphens.
//
// This is a heuristic, not a guarantee: Anthropic sometimes ships a specific
// model under a dated snapshot id (e.g. "claude-haiku-4-5-20251001") that
// isn't derivable from the pricing page's display name alone, so this won't
// always exactly match the "model" string internal/provider/claude records
// from a live transcript (see model.TurnEndEvent.Model). Acceptable for
// pricing reference data; a consumer that needs an exact join should treat
// this as a prefix/best match, not an exact key.
func claudeModelID(heading string) string {
	id := strings.ToLower(heading)
	id = strings.NewReplacer(" ", "-", ".", "-").Replace(id)
	return "claude-" + id
}
