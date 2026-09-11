package remotesync

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"maatplug/internal/store"
)

// AreaStatus is the last known sync outcome for one SSH data area, kept in
// memory only (never persisted — a fresh maatplug-web process just re-syncs
// on its own first cycle, see Poller.Start).
type AreaStatus struct {
	AreaID       string    `json:"areaId"`
	LastSyncAt   time.Time `json:"lastSyncAt"`
	LastError    string    `json:"lastError,omitempty"`
	FilesFetched int       `json:"filesFetched"`
}

// Poller periodically syncs every configured SSH data area (see
// store.DataArea.IsSSH) into its local cache directory. Areas are synced
// one at a time, in store.ListDataAreas order — like internal/usage.Poller,
// this keeps a background sync from piling up concurrent ssh connections a
// human might also be actively using bandwidth/CPU alongside.
type Poller struct {
	store *store.Store

	mu        sync.RWMutex
	interval  time.Duration
	status    map[string]AreaStatus
	syncLocks map[string]*sync.Mutex
	syncFunc  func(context.Context, string, string, string) (Result, error)

	// resetSignal wakes Start's loop to re-read interval and Reset the
	// ticker after SetInterval changes it — buffered 1 so SetInterval never
	// blocks, and a redundant signal (the loop hasn't consumed the
	// previous one yet) is simply dropped: one pending reset already
	// covers it, since the loop always re-reads the latest interval value
	// once it wakes, not whatever was current when the signal was sent.
	resetSignal chan struct{}
}

// NewPoller builds a Poller that re-syncs every configured SSH area every
// interval, reading the current area list from s (so areas added or
// removed via the settings UI take effect on the next cycle without a
// restart). interval can be changed later via SetInterval — e.g. from the
// settings screen — without restarting the process.
func NewPoller(s *store.Store, interval time.Duration) *Poller {
	return &Poller{store: s, interval: interval, status: map[string]AreaStatus{}, syncLocks: map[string]*sync.Mutex{}, syncFunc: Sync, resetSignal: make(chan struct{}, 1)}
}

// Start runs an immediate sync pass and then loops on the current interval
// (see SetInterval) until ctx is done. Returns immediately; syncing happens
// on a background goroutine.
func (p *Poller) Start(ctx context.Context) {
	go func() {
		p.syncAll(ctx)
		ticker := time.NewTicker(p.Interval())
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.syncAll(ctx)
			case <-p.resetSignal:
				ticker.Reset(p.Interval())
			}
		}
	}()
}

// Interval returns the poller's current sync interval.
func (p *Poller) Interval() time.Duration {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.interval
}

// SetInterval changes how often Start's loop re-syncs, taking effect on the
// running poller immediately (the next tick is rescheduled from now, not
// left at whatever was already in flight under the old interval) — no
// restart needed. Safe to call before or after Start.
func (p *Poller) SetInterval(d time.Duration) {
	p.mu.Lock()
	p.interval = d
	p.mu.Unlock()
	select {
	case p.resetSignal <- struct{}{}:
	default:
	}
}

func (p *Poller) syncAll(ctx context.Context) {
	areas, err := p.store.ListDataAreas()
	if err != nil {
		log.Printf("maatplug-web: remotesync: list data areas: %v", err)
		return
	}
	for _, area := range areas {
		if !area.IsSSH() {
			continue
		}
		p.syncOne(ctx, area)
	}
}

func (p *Poller) syncOne(ctx context.Context, area store.DataArea) AreaStatus {
	lock := p.areaLock(area.ID)
	lock.Lock()
	defer lock.Unlock()
	result, err := p.syncFunc(ctx, area.SSHHost, area.RemotePath, area.Path)
	status := AreaStatus{AreaID: area.ID, LastSyncAt: time.Now()}
	if err != nil {
		status.LastError = err.Error()
		log.Printf("maatplug-web: remotesync: sync %s (%s): %v", area.Name, area.ID, err)
	} else {
		status.FilesFetched = result.FilesFetched
	}
	p.mu.Lock()
	p.status[area.ID] = status
	p.mu.Unlock()
	return status
}

func (p *Poller) areaLock(areaID string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	lock := p.syncLocks[areaID]
	if lock == nil {
		lock = &sync.Mutex{}
		p.syncLocks[areaID] = lock
	}
	return lock
}

// SyncNow synchronously syncs one area immediately (the settings page's
// "sync now" action, and the first sync right after an area is added — see
// internal/web's handleAddDataArea) rather than waiting for the next
// scheduled cycle. areaID must name a configured SSH area.
func (p *Poller) SyncNow(ctx context.Context, areaID string) (AreaStatus, error) {
	areas, err := p.store.ListDataAreas()
	if err != nil {
		return AreaStatus{}, err
	}
	for _, area := range areas {
		if area.ID == areaID && area.IsSSH() {
			return p.syncOne(ctx, area), nil
		}
	}
	return AreaStatus{}, fmt.Errorf("no configured SSH data area with id %q", areaID)
}

// Statuses returns every SSH area's last known sync outcome, for the
// settings page.
func (p *Poller) Statuses() []AreaStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]AreaStatus, 0, len(p.status))
	for _, status := range p.status {
		out = append(out, status)
	}
	return out
}
