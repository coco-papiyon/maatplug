package remotesync

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"maatplug/internal/store"
)

func TestPoller_SetIntervalReschedulesRunningLoop(t *testing.T) {
	s := &store.Store{Root: t.TempDir()}
	// A real (deliberately unreachable) SSH area: its sync fails fast (DNS
	// resolution error) but still records an AreaStatus with a fresh
	// LastSyncAt every time syncAll runs it — that timestamp is this
	// test's only window into "did the loop actually tick again", since a
	// no-op poller with zero areas would leave nothing to observe.
	area, err := s.AddSSHDataArea("unreachable", "this-host-does-not-exist-anywhere-xyz", "/remote")
	if err != nil {
		t.Fatal(err)
	}

	p := NewPoller(s, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx) // runs one immediate sync pass before the 1h ticker even starts

	firstSync := waitForStatus(t, p, area.ID, time.Time{})

	// Narrowing the interval to a few milliseconds must make the loop tick
	// again almost immediately — if SetInterval only updated the stored
	// value without waking/resetting the running ticker, this would hang
	// until the ORIGINAL 1h ticker fired instead, and the test below would
	// time out.
	p.SetInterval(20 * time.Millisecond)
	if got := p.Interval(); got != 20*time.Millisecond {
		t.Fatalf("Interval() after SetInterval = %v, want 20ms", got)
	}
	waitForStatus(t, p, area.ID, firstSync)
}

// waitForStatus polls p.Statuses() until area's LastSyncAt moves past
// after, or fails the test after 3s.
func waitForStatus(t *testing.T, p *Poller, areaID string, after time.Time) time.Time {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, status := range p.Statuses() {
			if status.AreaID == areaID && status.LastSyncAt.After(after) {
				return status.LastSyncAt
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no sync of area %q observed after %v within 3s", areaID, after)
	return time.Time{}
}

func TestPoller_SyncNow_UnknownAreaErrors(t *testing.T) {
	s := &store.Store{Root: t.TempDir()}
	p := NewPoller(s, time.Hour)
	if _, err := p.SyncNow(context.Background(), "no-such-area"); err == nil {
		t.Error("SyncNow(unknown area) succeeded, want an error")
	}
}

func TestPollerSerializesConcurrentSyncsForOneArea(t *testing.T) {
	p := NewPoller(&store.Store{Root: t.TempDir()}, time.Hour)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var active, maximum atomic.Int32
	p.syncFunc = func(context.Context, string, string, string) (Result, error) {
		current := active.Add(1)
		for {
			seen := maximum.Load()
			if current <= seen || maximum.CompareAndSwap(seen, current) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		active.Add(-1)
		return Result{}, nil
	}
	area := store.DataArea{ID: "area", Name: "area", SSHHost: "host", RemotePath: "/remote", Path: t.TempDir()}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); p.syncOne(context.Background(), area) }()
	<-entered
	go func() { defer wg.Done(); p.syncOne(context.Background(), area) }()
	select {
	case <-entered:
		t.Fatal("second sync entered before the first completed")
	case <-time.After(50 * time.Millisecond):
	}
	release <- struct{}{}
	<-entered
	release <- struct{}{}
	wg.Wait()
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent syncs = %d", maximum.Load())
	}
}
