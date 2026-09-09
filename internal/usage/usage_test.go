package usage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeFetcher returns windows/err on every call and records how many times
// (and in what order relative to other fakeFetchers) it was invoked.
func fakeFetcher(name string, calls *[]string, mu *sync.Mutex, windows []Window, err error) fetcher {
	return fetcher{
		provider: name,
		fetch: func(context.Context, string) ([]Window, error) {
			mu.Lock()
			*calls = append(*calls, name)
			mu.Unlock()
			return windows, err
		},
	}
}

func newTestPoller(providers []fetcher) *Poller {
	return &Poller{
		dir:       ".",
		interval:  time.Hour, // long enough that the ticker never fires during a test
		providers: providers,
		snapshots: map[string]Snapshot{},
		enabled:   map[string]bool{},
	}
}

func TestPoller_StartupCheck_AvailableProviderIsReported(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	windows := []Window{{Name: "session", UsedPercent: 10, RemainingPercent: 90}}
	p := newTestPoller([]fetcher{fakeFetcher("claude", &calls, &mu, windows, nil)})

	p.checkAll(context.Background(), true)

	snaps := p.Snapshots()
	if len(snaps) != 1 || !snaps[0].Available || len(snaps[0].Windows) != 1 {
		t.Fatalf("Snapshots() = %+v", snaps)
	}
	if snaps[0].Windows[0].RemainingPercent != 90 {
		t.Errorf("RemainingPercent = %d, want 90", snaps[0].Windows[0].RemainingPercent)
	}
}

func TestPoller_StartupCheck_FailedProviderIsExcludedEntirely(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	p := newTestPoller([]fetcher{fakeFetcher("codex", &calls, &mu, nil, errors.New("no plan"))})

	p.checkAll(context.Background(), true)

	if snaps := p.Snapshots(); len(snaps) != 0 {
		t.Errorf("Snapshots() = %+v, want none (provider excluded at startup)", snaps)
	}
}

func TestPoller_PeriodicCheck_SkipsProvidersExcludedAtStartup(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	p := newTestPoller([]fetcher{fakeFetcher("codex", &calls, &mu, nil, errors.New("no plan"))})

	p.checkAll(context.Background(), true) // excludes codex
	calls = nil
	p.checkAll(context.Background(), false) // periodic cycle

	if len(calls) != 0 {
		t.Errorf("periodic cycle re-fetched %v, want no calls (codex was excluded at startup)", calls)
	}
}

func TestPoller_ChecksProvidersStrictlyInOrder(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	p := newTestPoller([]fetcher{
		fakeFetcher("claude", &calls, &mu, []Window{{Name: "session"}}, nil),
		fakeFetcher("codex", &calls, &mu, []Window{{Name: "primary"}}, nil),
	})

	p.checkAll(context.Background(), true)

	if len(calls) != 2 || calls[0] != "claude" || calls[1] != "codex" {
		t.Errorf("call order = %v, want [claude codex]", calls)
	}
}

func TestPoller_PeriodicFailureUpdatesErrorButStaysEnabled(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	callCount := 0
	p := newTestPoller([]fetcher{{
		provider: "claude",
		fetch: func(context.Context, string) ([]Window, error) {
			mu.Lock()
			calls = append(calls, "claude")
			mu.Unlock()
			callCount++
			if callCount == 1 {
				return []Window{{Name: "session", RemainingPercent: 80}}, nil
			}
			return nil, errors.New("timed out")
		},
	}})

	p.checkAll(context.Background(), true)  // succeeds, enables claude
	p.checkAll(context.Background(), false) // transient failure

	snaps := p.Snapshots()
	if len(snaps) != 1 {
		t.Fatalf("Snapshots() = %+v, want claude to still be reported", snaps)
	}
	if snaps[0].Error == "" {
		t.Error("Error is empty, want the transient failure recorded")
	}
	// The last-good windows must survive a transient failure rather than
	// being wiped out, since Available stays true.
	if len(snaps[0].Windows) != 1 || snaps[0].Windows[0].RemainingPercent != 80 {
		t.Errorf("Windows = %+v, want the last successful fetch preserved", snaps[0].Windows)
	}
}
