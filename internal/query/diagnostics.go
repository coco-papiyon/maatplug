package query

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"maatplug/internal/collector"
	"maatplug/internal/journal"
	"maatplug/internal/model"
	"maatplug/internal/pricing"
	"maatplug/internal/store"
)

type WorkerHealth struct {
	State  collector.WorkerState `json:"state"`
	Known  bool                  `json:"known"`
	Stale  bool                  `json:"stale"`
	Reason string                `json:"reason,omitempty"`
}

type PricingHealth struct {
	Sources       []pricing.SourceState `json:"sources"`
	ActiveVersion string                `json:"activeVersion,omitempty"`
	Missing       bool                  `json:"missing"`
	StaleSources  []string              `json:"staleSources"`
}

type Coverage struct {
	Events       int            `json:"events"`
	ByStatus     map[string]int `json:"byStatus"`
	MissingPaths map[string]int `json:"missingPaths"`
	ParseErrors  int            `json:"parseErrors"`
}

type ProviderReconciliation struct {
	Provider        string `json:"provider"`
	ReceivedEvents  int    `json:"receivedEvents"`
	UsageEvents     int    `json:"usageEvents"`
	OperationEvents int    `json:"operationEvents"`
}

type CutoverReport struct {
	State                 *collector.CutoverState  `json:"state,omitempty"`
	Providers             []ProviderReconciliation `json:"providers"`
	Missing               bool                     `json:"missing"`
	DuplicateInboxLastRun int                      `json:"duplicateInboxLastRun"`
	Limitations           []string                 `json:"limitations"`
}

type Performance struct {
	MeasuredAt          time.Time `json:"measuredAt"`
	FileCount           int       `json:"fileCount"`
	TotalBytes          int64     `json:"totalBytes"`
	ListMillis          int64     `json:"listMillis"`
	DetailMillis        int64     `json:"detailMillis"`
	PeriodMillis        int64     `json:"periodMillis"`
	WorkerCatchupMillis int64     `json:"workerCatchupMillis"`
	ProjectCount        int       `json:"projectCount"`
	BaselinePath        string    `json:"baselinePath,omitempty"`
}

type DoctorReport struct {
	GeneratedAt       time.Time              `json:"generatedAt"`
	FullScanAvailable bool                   `json:"fullScanAvailable"`
	Collection        collector.DoctorReport `json:"collection"`
	Worker            WorkerHealth           `json:"worker"`
	Pricing           PricingHealth          `json:"pricing"`
	Coverage          Coverage               `json:"coverage"`
	Cutover           CutoverReport          `json:"cutover"`
	Performance       Performance            `json:"performance"`
}

func (report DoctorReport) Healthy() bool {
	return report.FullScanAvailable && report.Collection.Healthy() && report.Collection.Inbox.Pending == 0 && report.Collection.Inbox.Processing == 0 && report.Collection.Inbox.Temporary == 0 && report.Coverage.ParseErrors == 0
}

func (service *Service) Doctor() (DoctorReport, error) {
	report, worker, state, now, err := service.quickDoctor()
	if err != nil {
		return DoctorReport{}, err
	}
	report.Collection, err = worker.Doctor()
	if err != nil {
		return DoctorReport{}, err
	}
	report.Coverage, report.Cutover, err = service.coverageAndCutover(worker, state)
	if err != nil {
		return DoctorReport{}, err
	}
	report.Performance, err = service.Measure()
	if err != nil {
		return DoctorReport{}, err
	}
	report.GeneratedAt = now.UTC()
	report.FullScanAvailable = true
	if err := service.saveDoctorSnapshot(report); err != nil {
		return DoctorReport{}, err
	}
	return report, nil
}

// CachedDoctor serves the interactive status page without traversing project
// journals or measuring the whole data area. A full `maatplug doctor` run
// refreshes the persisted snapshot; lightweight worker, inbox, and pricing
// health are refreshed for every request.
func (service *Service) CachedDoctor() (DoctorReport, error) {
	live, _, _, now, err := service.quickDoctor()
	if err != nil {
		return DoctorReport{}, err
	}
	data, err := os.ReadFile(service.doctorSnapshotPath())
	if err == nil {
		var cached DoctorReport
		if json.Unmarshal(data, &cached) == nil {
			cached.Collection.Inbox = live.Collection.Inbox
			cached.Worker = live.Worker
			cached.Pricing = live.Pricing
			cached.Cutover.DuplicateInboxLastRun = live.Cutover.DuplicateInboxLastRun
			return cached, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return DoctorReport{}, err
	}
	live.GeneratedAt = now.UTC()
	return live, nil
}

func (service *Service) quickDoctor() (DoctorReport, *collector.Collector, collector.WorkerState, time.Time, error) {
	worker := collector.New(service.Root)
	inbox, err := worker.Inbox.Diagnostics()
	if err != nil {
		return DoctorReport{}, worker, collector.WorkerState{}, time.Time{}, err
	}
	report := DoctorReport{Collection: collector.DoctorReport{Inbox: inbox}, Coverage: Coverage{ByStatus: map[string]int{}, MissingPaths: map[string]int{}}, Cutover: CutoverReport{Providers: []ProviderReconciliation{}, Limitations: []string{"run maatplug doctor to refresh journal coverage and performance metrics"}}}
	now := time.Now()
	if service.Now != nil {
		now = service.Now()
	}
	state, err := worker.LoadWorkerState()
	if err == nil {
		report.Worker = WorkerHealth{State: state, Known: true}
		if state.Status == "running" && state.LastAttempt != nil && now.Sub(*state.LastAttempt) > 5*time.Minute {
			report.Worker.Stale = true
			report.Worker.Reason = "worker has remained running without a completed cycle for more than five minutes"
		}
		if state.LastSuccess == nil {
			report.Worker.Stale = true
			report.Worker.Reason = "worker has not completed a successful cycle"
		} else if now.Sub(*state.LastSuccess) > 24*time.Hour {
			report.Worker.Stale = true
			report.Worker.Reason = "last successful collection cycle is older than 24 hours"
		}
	} else if errors.Is(err, os.ErrNotExist) {
		report.Worker = WorkerHealth{Known: false, Stale: true, Reason: "collector worker has never recorded a cycle"}
	} else {
		return DoctorReport{}, worker, state, now, err
	}
	report.Pricing, err = service.pricingHealth(now)
	if err != nil {
		return DoctorReport{}, worker, state, now, err
	}
	report.Cutover.DuplicateInboxLastRun = state.LastStats.Duplicates
	cutState, cutErr := worker.LoadCutoverState()
	if cutErr == nil {
		report.Cutover.State = &cutState
	} else if errors.Is(cutErr, os.ErrNotExist) {
		report.Cutover.Missing = true
	} else {
		return DoctorReport{}, worker, state, now, cutErr
	}
	return report, worker, state, now, nil
}

func (service *Service) doctorSnapshotPath() string {
	return filepath.Join(service.Root, "state", "doctor-report.json")
}

func (service *Service) saveDoctorSnapshot(report DoctorReport) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := store.AtomicWriteFile(service.doctorSnapshotPath(), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write doctor snapshot: %w", err)
	}
	return nil
}

func (service *Service) pricingHealth(now time.Time) (PricingHealth, error) {
	states, err := pricing.ListSourceStates(service.Root)
	if err != nil {
		return PricingHealth{}, err
	}
	active, ok, err := (pricing.VersionStore{Root: service.Root}).LoadActive()
	if err != nil {
		return PricingHealth{}, err
	}
	result := PricingHealth{Sources: states, Missing: !ok, StaleSources: []string{}}
	if ok {
		result.ActiveVersion = active.Version
	}
	for _, s := range states {
		if !s.Complete || s.LastSuccess == nil || s.FreshUntil == nil || now.After(*s.FreshUntil) {
			result.StaleSources = append(result.StaleSources, s.SourceID)
		}
	}
	return result, nil
}

func (service *Service) coverageAndCutover(worker *collector.Collector, state collector.WorkerState) (Coverage, CutoverReport, error) {
	coverage := Coverage{ByStatus: map[string]int{}, MissingPaths: map[string]int{}}
	cut := CutoverReport{Providers: []ProviderReconciliation{}, DuplicateInboxLastRun: state.LastStats.Duplicates, Limitations: []string{"external Provider counters are not exposed consistently; counts are reconciled against accepted provider observations in the journals"}}
	cutState, err := worker.LoadCutoverState()
	if err == nil {
		cut.State = &cutState
	} else if errors.Is(err, os.ErrNotExist) {
		cut.Missing = true
	} else {
		return coverage, cut, err
	}
	byProvider := map[string]*ProviderReconciliation{}
	ids, err := (&store.Store{Root: service.Root}).ListProjectIDs()
	if err != nil {
		return coverage, cut, err
	}
	for _, id := range ids {
		paths, e := journal.New(service.Root).ListProjectStreams(id)
		if e != nil {
			coverage.ParseErrors++
			continue
		}
		for _, path := range paths {
			_, events, e := journal.New(service.Root).ReadPath(path)
			if e != nil {
				coverage.ParseErrors++
				continue
			}
			for _, event := range events {
				coverage.Events++
				for _, a := range event.Availability {
					coverage.ByStatus[string(a.Status)]++
					if missingStatus(a.Status) {
						coverage.MissingPaths[a.Path]++
					}
				}
				if cut.State == nil || event.Time.ReceivedAt.Before(cut.State.StartedAt) {
					continue
				}
				p := byProvider[event.Source.Provider]
				if p == nil {
					p = &ProviderReconciliation{Provider: event.Source.Provider}
					byProvider[event.Source.Provider] = p
				}
				p.ReceivedEvents++
				switch event.EventType {
				case model.EventUsageReported:
					p.UsageEvents++
				case model.EventOperationRequested, model.EventOperationResult:
					p.OperationEvents++
				}
			}
		}
	}
	keys := make([]string, 0, len(byProvider))
	for k := range byProvider {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := *byProvider[k]
		cut.Providers = append(cut.Providers, p)
	}
	return coverage, cut, nil
}

func missingStatus(status model.AvailabilityStatus) bool {
	return status == model.AvailabilityNotCollected || status == model.AvailabilityUnavailable || status == model.AvailabilityUnknown || status == model.AvailabilityConflict
}

func (service *Service) Measure() (Performance, error) {
	now := time.Now().UTC()
	if service.Now != nil {
		now = service.Now().UTC()
	}
	result := Performance{MeasuredAt: now}
	err := filepath.WalkDir(service.Root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		result.FileCount++
		result.TotalBytes += info.Size()
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return result, err
	}
	started := time.Now()
	items, err := service.ListProjects(Filter{})
	result.ListMillis = time.Since(started).Milliseconds()
	if err != nil {
		return result, err
	}
	result.ProjectCount = len(items)
	if len(items) > 0 {
		started = time.Now()
		_, err = service.ProjectDetail(items[0].ProjectID, Filter{})
		result.DetailMillis = time.Since(started).Milliseconds()
		if err != nil && !errors.Is(err, ErrNotFound) {
			return result, err
		}
		started = time.Now()
		_, err = service.Periods(items[0].ProjectID)
		result.PeriodMillis = time.Since(started).Milliseconds()
		if err != nil && !errors.Is(err, ErrNotFound) {
			return result, err
		}
	}
	if state, e := collector.New(service.Root).LoadWorkerState(); e == nil {
		result.WorkerCatchupMillis = state.DurationMillis
	}
	return result, nil
}

func (service *Service) RecordBaseline() (Performance, error) {
	result, err := service.Measure()
	if err != nil {
		return result, err
	}
	rel := filepath.Join("state", "query-baselines", result.MeasuredAt.Format("20060102T150405.000000000Z")+".json")
	result.BaselinePath = filepath.ToSlash(rel)
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return result, err
	}
	if err := store.AtomicWriteFile(filepath.Join(service.Root, rel), append(data, '\n'), 0o600); err != nil {
		return result, fmt.Errorf("write query baseline: %w", err)
	}
	return result, nil
}
