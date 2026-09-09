// Package usage polls Claude Code's and Codex CLI's own usage-limit
// reporting (not maatplug's recorded token counts) so maatplug-web can show
// "how much of your plan's quota is left" in its UI. This is a live,
// ephemeral concept — unlike internal/model's event schema, none of it is
// written to the flat-file store.
//
// Retrieval mirrors the reference implementation the project owner pointed
// at (github.com/coco-papiyon/maatgen, apps/agent-manager/internal/agent):
// Claude Code is asked via its own `/usage` slash command in headless print
// mode, and Codex is asked via its app-server JSON-RPC `account/rateLimits/
// read` method. See claude.go and codex.go for each provider's specifics.
package usage

import (
	"context"
	"log"
	"sync"
	"time"
)

// Window is one usage-limit window as last reported by a provider's own
// CLI — Claude Code's "session"/"week", or Codex's "primary"/"secondary"
// rate-limit windows.
type Window struct {
	Name             string `json:"name"`
	UsedPercent      int    `json:"usedPercent"`
	RemainingPercent int    `json:"remainingPercent"`
	// ResetLabel is a human-readable reset time. Claude Code prints it as
	// free text already (e.g. "Sep 9, 1:39am (Asia/Tokyo)"); for Codex it's
	// formatted here from the Unix timestamp the app-server returns.
	ResetLabel string `json:"resetLabel,omitempty"`
}

// Snapshot is one provider's most recently determined usage status.
type Snapshot struct {
	Provider string `json:"provider"`
	// Available is false when this provider has no contract to report usage
	// against — its CLI isn't installed, or the account isn't on a plan the
	// usage command reports windows for (e.g. API-key-only billing). Decided
	// once at the first fetch attempt (see Poller) and then left alone: a
	// provider that starts out available keeps being polled even if a later
	// poll fails transiently.
	Available bool      `json:"available"`
	Windows   []Window  `json:"windows,omitempty"`
	FetchedAt time.Time `json:"fetchedAt"`
	// Error carries the most recent fetch failure, if any, even while
	// Available stays true — so a transient failure doesn't just make the
	// window data silently stale with no explanation.
	Error string `json:"error,omitempty"`
}

// fetcher is one provider's usage-window lookup, run in dir (the CLI needs
// an existing working directory; usage itself is account-wide, not
// project-specific — see Poller's doc comment for what dir is in practice).
type fetcher struct {
	provider string
	fetch    func(ctx context.Context, dir string) ([]Window, error)
}

// defaultProviders lists every provider Poller checks by default, in the
// fixed order they are always fetched in (claude, then codex) — see
// Poller's doc comment for why the order is fixed rather than concurrent.
var defaultProviders = []fetcher{
	{provider: "claude", fetch: FetchClaude},
	{provider: "codex", fetch: FetchCodex},
}

// Poller periodically re-checks every provider's remaining usage. Claude and
// Codex are never checked concurrently with each other — each cycle (the
// first one included) fetches claude, waits for it to finish, then fetches
// codex, on a single goroutine — because both CLIs are heavyish, locally
// interactive tools (Codex briefly spins up its own app-server subprocess
// per check) that a human might also be actively using; serializing keeps
// maatplug-web from doubling up on that load.
type Poller struct {
	dir       string
	interval  time.Duration
	providers []fetcher

	mu        sync.RWMutex
	checkMu   sync.Mutex
	snapshots map[string]Snapshot
	enabled   map[string]bool // gates periodic re-fetch after the startup contract check; false once a provider is found unavailable
}

// NewPoller builds a Poller that runs each provider's CLI in dir (typically
// the operator's home directory — usage is per-account, not tied to any
// particular maatplug project) and re-checks every interval.
func NewPoller(dir string, interval time.Duration) *Poller {
	return &Poller{
		dir:       dir,
		interval:  interval,
		providers: defaultProviders,
		snapshots: map[string]Snapshot{},
		enabled:   map[string]bool{},
	}
}

// Start runs the startup contract check (one sequential pass over every
// provider) and then loops on interval until ctx is done, each cycle only
// re-fetching providers the startup check found Available. It returns
// immediately; polling happens on a background goroutine.
func (p *Poller) Start(ctx context.Context) {
	go func() {
		p.checkAll(ctx, true)
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.checkAll(ctx, false)
			}
		}
	}()
}

// checkAll fetches every provider in providers, strictly one at a time in
// list order. On initial, a provider whose fetch fails is marked permanently
// disabled for this process's remaining periodic cycles (the "not under
// contract, excluded from usage retrieval" rule); on later cycles, a fetch
// failure updates Snapshot.Error but leaves an already-enabled provider
// enabled.
func (p *Poller) checkAll(ctx context.Context, initial bool) {
	p.checkMu.Lock()
	defer p.checkMu.Unlock()

	for _, f := range p.providers {
		if !initial && !p.isEnabled(f.provider) {
			continue
		}
		p.check(ctx, f, initial)
	}
}

// Refresh immediately fetches the usage windows for providers that passed the
// startup contract check. It is used by the dashboard's manual refresh action
// and shares the same serial execution guard as periodic polling.
func (p *Poller) Refresh(ctx context.Context) {
	p.checkAll(ctx, false)
}

// RefreshProvider immediately fetches one provider's usage windows. It is
// used when the dashboard's provider badge is clicked individually.
func (p *Poller) RefreshProvider(ctx context.Context, provider string) {
	p.checkMu.Lock()
	defer p.checkMu.Unlock()

	for _, f := range p.providers {
		if f.provider == provider && p.isEnabled(f.provider) {
			p.check(ctx, f, false)
			return
		}
	}
}

func (p *Poller) check(ctx context.Context, f fetcher, initial bool) {
	windows, err := f.fetch(ctx, p.dir)
	now := time.Now().UTC()

	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		if initial {
			p.enabled[f.provider] = false
			log.Printf("maatplug-web: %s usage: no contract detected at startup, excluding from further checks: %v", f.provider, err)
			return
		}
		snap := p.snapshots[f.provider]
		snap.Error = err.Error()
		p.snapshots[f.provider] = snap
		return
	}
	p.enabled[f.provider] = true
	p.snapshots[f.provider] = Snapshot{Provider: f.provider, Available: true, Windows: windows, FetchedAt: now}
}

func (p *Poller) isEnabled(provider string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.enabled[provider]
}

// Snapshots returns the current cached usage for every provider that has
// ever been found Available (i.e. excludes anything the startup check
// disqualified, and excludes providers no fetch has completed for yet), in
// the fixed provider order.
func (p *Poller) Snapshots() []Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]Snapshot, 0, len(p.providers))
	for _, f := range p.providers {
		if snap, ok := p.snapshots[f.provider]; ok {
			out = append(out, snap)
		}
	}
	return out
}
