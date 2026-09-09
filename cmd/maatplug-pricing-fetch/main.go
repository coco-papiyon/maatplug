// Command maatplug-pricing-fetch is a standalone, build-time-only tool: it
// scrapes each provider's public pricing page for current per-model rates
// and writes the result to internal/pricing/pricing.json, the flat config
// file maatplug ships pricing reference data in. It is not part of the
// maatplug/maatplug-web runtime and is not distributed to end users — see
// build.bat, which runs it before packaging so the shipped pricing.json is
// current as of each release.
//
// Fetch failures are not fatal: for each provider, a failed or unrecognized
// fetch leaves that provider's entries exactly as they already were in the
// existing output file (see internal/pricing's package doc for why every
// scrape is inherently best-effort). If a provider has never been
// successfully fetched before, it stays empty rather than blocking the
// whole run. Only a local I/O failure (can't read or write the output file)
// is treated as fatal, so a transient network problem during a build never
// needs to fail the build — the release simply ships with whatever pricing
// data was already on record.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"maatplug/internal/pricing"
)

// provider pairs a name with its fetch function.
type provider struct {
	name  string
	fetch func(context.Context) ([]pricing.ModelPricing, error)
}

// defaultProviders lists every provider this tool knows how to fetch, in a
// fixed order so pricing.json's model list is deterministic across runs.
// Adding a provider means adding one entry here once internal/pricing has a
// corresponding Fetch function (e.g. once a Copilot adapter exists).
var defaultProviders = []provider{
	{"claude", pricing.FetchClaude},
	{"codex", pricing.FetchCodex},
}

const pricingRefreshInterval = 24 * time.Hour

func main() {
	out := flag.String("out", "internal/pricing/pricing.json", "path to the pricing config file to update")
	timeout := flag.Duration("timeout", 20*time.Second, "per-provider fetch timeout")
	flag.Parse()

	if err := run(*out, *timeout, defaultProviders); err != nil {
		fmt.Fprintf(os.Stderr, "maatplug-pricing-fetch: %v\n", err)
		os.Exit(1)
	}
}

// run fetches every provider in providers and writes the merged result to
// out. It takes providers as a parameter (rather than reading
// defaultProviders directly) so tests can exercise the merge/fallback policy
// with fake fetchers instead of hitting the real network.
func run(out string, timeout time.Duration, providers []provider) error {
	existing, err := pricing.Load(out)
	if err != nil {
		return fmt.Errorf("reading existing %s: %w", out, err)
	}
	now := time.Now().UTC()
	if !existing.GeneratedAt.IsZero() && now.Sub(existing.GeneratedAt) < pricingRefreshInterval {
		fmt.Printf("maatplug-pricing-fetch: pricing is fresh (generated %s), skipping fetch\n", existing.GeneratedAt.Format(time.RFC3339))
		return nil
	}

	doc := pricing.Document{GeneratedAt: existing.GeneratedAt}
	fetched := false
	for _, p := range providers {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		models, fetchErr := p.fetch(ctx)
		cancel()

		if fetchErr != nil {
			previous := existing.ForProvider(p.name)
			if len(previous) == 0 {
				fmt.Fprintf(os.Stderr, "maatplug-pricing-fetch: %s: fetch failed and no previous data on file (%v) — leaving empty\n", p.name, fetchErr)
			} else {
				fmt.Fprintf(os.Stderr, "maatplug-pricing-fetch: %s: fetch failed (%v) — keeping %d previously recorded model(s)\n", p.name, fetchErr, len(previous))
			}
			doc.Models = append(doc.Models, previous...)
			continue
		}

		fmt.Printf("maatplug-pricing-fetch: %s: fetched %d model(s)\n", p.name, len(models))
		fetched = true
		doc.Models = append(doc.Models, models...)
	}
	if fetched {
		doc.GeneratedAt = now
	}

	if err := pricing.Save(out, doc); err != nil {
		return fmt.Errorf("writing %s: %w", out, err)
	}
	return nil
}
