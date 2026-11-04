// Package collector drains the atomic inbox into content objects, journals,
// and rebuildable catalogs.
package collector

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"maatplug/internal/catalog"
	"maatplug/internal/contentstore"
	"maatplug/internal/ingest"
	"maatplug/internal/journal"
	"maatplug/internal/model"
	provideradapter "maatplug/internal/provider/adapter"
	"maatplug/internal/provider/claude"
	"maatplug/internal/provider/codex"
	"maatplug/internal/store"
)

type Collector struct {
	Root        string
	Inbox       *ingest.Inbox
	Journal     *journal.Journal
	Content     *contentstore.Store
	Catalog     *catalog.Builder
	MaxAttempts int
	RetryDelay  time.Duration
	Jobs        []MaintenanceJob
	OnProgress  func(TranscriptProgress)
	OnError     func(error)
}

type MaintenanceJob interface {
	RunDue(context.Context) (attempted bool, err error)
}

func New(root string) *Collector {
	return &Collector{
		Root: root, Inbox: ingest.New(root), Journal: journal.New(root),
		Content: contentstore.New(root), Catalog: catalog.New(root),
		MaxAttempts: 3, RetryDelay: time.Second,
	}
}

type Stats struct {
	Recovered   int `json:"recovered"`
	Claimed     int `json:"claimed"`
	Completed   int `json:"completed"`
	Retried     int `json:"retried"`
	Quarantined int `json:"quarantined"`
	Duplicates  int `json:"duplicates"`
	Catalogs    int `json:"catalogs"`
	JobsRun     int `json:"jobsRun"`
	JobsFailed  int `json:"jobsFailed"`
}

type TranscriptProgress struct {
	Provider       string
	ProcessedBytes int64
	TotalBytes     int64
	ProcessedLines uint64
	HasMore        bool
}

// RunOnce recovers abandoned work, drains every ready record, and rebuilds
// catalogs for projects whose journals were touched.
func (worker *Collector) RunOnce(ctx context.Context) (Stats, error) {
	started := time.Now().UTC()
	if err := worker.ensureCutover(started); err != nil {
		return Stats{}, err
	}
	state := WorkerState{SchemaVersion: 1, LastAttempt: &started, Status: "running"}
	if previous, err := worker.LoadWorkerState(); err == nil {
		state.LastSuccess = previous.LastSuccess
	}
	if err := worker.saveWorkerState(state); err != nil {
		return Stats{}, err
	}
	stats, runErr := worker.runOnce(ctx)
	finished := time.Now().UTC()
	state.Status = "idle"
	state.DurationMillis = finished.Sub(started).Milliseconds()
	state.LastStats = stats
	if runErr == nil {
		state.LastSuccess = &finished
	} else {
		state.Status = "error"
		state.LastError = runErr.Error()
	}
	if err := worker.saveWorkerState(state); err != nil {
		return stats, errors.Join(runErr, err)
	}
	return stats, runErr
}

func (worker *Collector) runOnce(ctx context.Context) (Stats, error) {
	var stats Stats
	recovered, err := worker.Inbox.RecoverStale()
	if err != nil {
		return stats, err
	}
	stats.Recovered = recovered
	touched := map[string]bool{}
	var processingErrors []error
	for {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		claim, err := worker.Inbox.ClaimNext()
		if err != nil {
			if claim != nil {
				if failErr := worker.Inbox.Fail(claim, err); failErr != nil {
					return stats, errors.Join(err, failErr)
				}
				stats.Quarantined++
				processingErrors = append(processingErrors, err)
				continue
			}
			return stats, err
		}
		if claim == nil {
			break
		}
		stats.Claimed++
		projectID, duplicate, err := worker.process(claim.Record)
		if err != nil {
			if claim.Record.Attempts+1 >= worker.MaxAttempts {
				if failErr := worker.Inbox.Fail(claim, err); failErr != nil {
					return stats, errors.Join(err, failErr)
				}
				stats.Quarantined++
				processingErrors = append(processingErrors, err)
			} else {
				if retryErr := worker.Inbox.Retry(claim, err, worker.RetryDelay); retryErr != nil {
					return stats, errors.Join(err, retryErr)
				}
				stats.Retried++
			}
			continue
		}
		if duplicate {
			stats.Duplicates++
		}
		if err := worker.Inbox.Complete(claim); err != nil {
			return stats, err
		}
		stats.Completed++
		touched[projectID] = true
	}
	maintenanceAttempted := false
	for _, job := range worker.Jobs {
		attempted, err := job.RunDue(ctx)
		if attempted {
			stats.JobsRun++
			maintenanceAttempted = true
		}
		if err != nil {
			stats.JobsFailed++
			processingErrors = append(processingErrors, err)
		}
	}
	if maintenanceAttempted {
		layout := &store.Store{Root: worker.Root}
		projectIDs, err := layout.ListProjectIDs()
		if err != nil {
			return stats, err
		}
		for _, projectID := range projectIDs {
			touched[projectID] = true
		}
	}
	// Bootstrap or repair list projections in the collector even when no new
	// inbox records arrived. HTTP requests do not perform this migration.
	ids, err := (&store.Store{Root: worker.Root}).ListProjectIDs()
	if err != nil {
		return stats, err
	}
	for _, id := range ids {
		if !worker.Catalog.ListSummariesReady(id) {
			touched[id] = true
		}
	}
	projects := make([]string, 0, len(touched))
	for projectID := range touched {
		projects = append(projects, projectID)
	}
	sort.Strings(projects)
	for _, projectID := range projects {
		if _, err := worker.Catalog.RebuildProject(projectID); err != nil {
			return stats, fmt.Errorf("rebuild catalog %s: %w", projectID, err)
		}
		stats.Catalogs++
	}
	if err := worker.Catalog.RefreshProjectList(); err != nil {
		return stats, err
	}
	return stats, errors.Join(processingErrors...)
}

func (worker *Collector) process(record ingest.Record) (string, bool, error) {
	switch record.Type {
	case ingest.RecordEvent:
		for _, item := range record.Contents {
			data, err := item.Data()
			if err != nil {
				return "", false, err
			}
			if err := worker.Content.PutExpected(item.Reference, data, item.Policy); err != nil {
				return "", false, err
			}
		}
		refs, err := model.ContentReferences(*record.Event)
		if err != nil {
			return "", false, err
		}
		for _, ref := range refs {
			if err := worker.Content.ValidateRef(ref); err != nil {
				return "", false, fmt.Errorf("validate content %s:%s: %w", ref.ID.Namespace, ref.ID.Value, err)
			}
		}
		result, err := worker.Journal.Append(journal.Route{ProjectID: record.Route.ProjectID, Provider: record.Route.Provider, SessionID: record.Route.SessionID, RunID: record.Route.RunID}, *record.Event)
		return record.Route.ProjectID, result.Duplicate, err
	case ingest.RecordProviderHook:
		return worker.processProviderHook(record)
	default:
		return "", false, fmt.Errorf("unsupported record type %q", record.Type)
	}
}

func (worker *Collector) processProviderHook(record ingest.Record) (string, bool, error) {
	payload, err := record.Hook.Payload()
	if err != nil {
		return "", false, err
	}
	var hook provideradapter.Hook
	switch record.Route.Provider {
	case claude.ProviderName:
		hook, err = claude.DecodeHook(record.Hook.Event, payload)
	case codex.ProviderName:
		hook, err = codex.DecodeHook(record.Hook.Event, payload)
	default:
		err = fmt.Errorf("unsupported provider %q", record.Route.Provider)
	}
	if err != nil {
		return "", false, err
	}
	projectID := store.ProjectID(hook.Cwd)
	route := journal.Route{ProjectID: projectID, Provider: record.Route.Provider, SessionID: hook.SessionID, RunID: hook.RunID}
	if err := route.Validate(); err != nil {
		return "", false, err
	}
	layout := &store.Store{Root: worker.Root}
	if err := layout.EnsureProject(projectID, filepath.Base(filepath.Clean(hook.Cwd)), hook.Cwd); err != nil {
		return "", false, err
	}

	batch, err := worker.normalizeHook(record.Route.Provider, projectID, record.ReceiptID, record.ReceivedAt, hook)
	if err != nil {
		return "", false, err
	}
	ref := batch.AddContent(payload, model.ContentRaw, "application/json", "provider-hook-raw-v1")
	receiptID := model.NamespacedID{Namespace: "maatplug/receipt", Value: record.ReceiptID}
	streamID := model.NamespacedID{Namespace: "maatplug/inbox", Value: record.Route.Provider}
	payloadObject := model.CollectionStatePayload{
		Stream: model.EntityRef{Kind: model.EntityStream, ID: streamID}, State: "received",
		Cursor: record.ReceiptID, Generation: "provider-hook-v1", Evidence: []model.ContentRef{ref},
	}
	payloadJSON, err := json.Marshal(payloadObject)
	if err != nil {
		return "", false, err
	}
	rawEvent := model.EventEnvelope{
		SchemaVersion: model.SchemaVersion, EventID: receiptID, EventType: model.EventCollectionState,
		Source: model.EventSource{Provider: record.Route.Provider, Surface: record.Hook.Event, Kind: model.SourceHook, RecordID: &receiptID},
		Time:   model.EventTime{ReceivedAt: record.ReceivedAt},
		Scope: []model.EntityRef{
			{Kind: model.EntityProject, ID: model.NamespacedID{Namespace: "maatplug/project", Value: projectID}},
			{Kind: model.EntitySession, ID: model.NamespacedID{Namespace: record.Route.Provider, Value: hook.SessionID}},
			{Kind: model.EntityRun, ID: model.NamespacedID{Namespace: record.Route.Provider, Value: hook.RunID}},
		}, Payload: payloadJSON,
	}
	if err := rawEvent.Validate(); err != nil {
		return "", false, err
	}
	batch.Events = append([]model.EventEnvelope{rawEvent}, batch.Events...)

	process := func() (bool, error) {
		if record.Hook.Event == "Stop" {
			allDuplicates := true
			firstChunk := true
			for {
				chunk, err := worker.readTranscript(record.Route.Provider, hook.TranscriptPath)
				if err != nil {
					return false, err
				}
				transcriptBatch, err := worker.normalizeTranscript(record.Route.Provider, projectID, record.ReceiptID, record.ReceivedAt, hook, chunk)
				if err != nil {
					return false, err
				}
				if firstChunk {
					transcriptBatch.Events = append(batch.Events, transcriptBatch.Events...)
					transcriptBatch.Contents = append(batch.Contents, transcriptBatch.Contents...)
				}
				duplicate, err := worker.persistBatch(route, transcriptBatch)
				if err != nil {
					return false, err
				}
				allDuplicates = allDuplicates && duplicate
				if err := worker.saveTranscriptCursor(record.Route.Provider, chunk, transcriptBatch.SeenIDs); err != nil {
					return false, err
				}
				if worker.OnProgress != nil {
					worker.OnProgress(TranscriptProgress{Provider: record.Route.Provider, ProcessedBytes: chunk.NextByteOffset, TotalBytes: chunk.TotalBytes, ProcessedLines: chunk.NextLine, HasMore: chunk.HasMore})
				}
				if !chunk.HasMore {
					return allDuplicates, nil
				}
				firstChunk = false
			}
		}
		return worker.persistBatch(route, batch)
	}
	var duplicate bool
	if record.Hook.Event == "Stop" && hook.TranscriptPath != "" {
		err = store.WithFileLock(worker.transcriptCursorPath(record.Route.Provider, hook.TranscriptPath), func() error {
			var inner error
			duplicate, inner = process()
			return inner
		})
	} else {
		duplicate, err = process()
	}
	return projectID, duplicate, err
}

func (worker *Collector) normalizeHook(provider, projectID, receiptID string, receivedAt time.Time, hook provideradapter.Hook) (provideradapter.Batch, error) {
	switch provider {
	case claude.ProviderName:
		return claude.NormalizeHook(projectID, receiptID, receivedAt, hook)
	case codex.ProviderName:
		return codex.NormalizeHook(projectID, receiptID, receivedAt, hook)
	default:
		return provideradapter.Batch{}, errors.New("provider hook has no supported provider identity")
	}
}

func (worker *Collector) normalizeTranscript(provider, projectID, receiptID string, receivedAt time.Time, hook provideradapter.Hook, chunk provideradapter.Transcript) (provideradapter.Batch, error) {
	switch provider {
	case claude.ProviderName:
		return claude.NormalizeTranscript(projectID, receiptID, receivedAt, hook, chunk)
	case codex.ProviderName:
		return codex.NormalizeTranscript(projectID, receiptID, receivedAt, hook, chunk)
	default:
		return provideradapter.Batch{}, errors.New("transcript has no supported provider identity")
	}
}

func (worker *Collector) persistBatch(fallback journal.Route, batch provideradapter.Batch) (bool, error) {
	for _, content := range batch.Contents {
		if err := worker.Content.PutExpected(content.Reference, content.Data, content.Policy); err != nil {
			return false, err
		}
	}
	allDuplicates := len(batch.Events) > 0
	type streamBatch struct {
		route  journal.Route
		events []model.EventEnvelope
	}
	streams := make([]streamBatch, 0)
	streamIndex := map[journal.Route]int{}
	for _, event := range batch.Events {
		refs, err := model.ContentReferences(event)
		if err != nil {
			return false, err
		}
		for _, ref := range refs {
			if err := worker.Content.ValidateRef(ref); err != nil {
				return false, fmt.Errorf("validate content %s:%s: %w", ref.ID.Namespace, ref.ID.Value, err)
			}
		}
		route := routeForEvent(fallback, event)
		index, found := streamIndex[route]
		if !found {
			index = len(streams)
			streamIndex[route] = index
			streams = append(streams, streamBatch{route: route})
		}
		streams[index].events = append(streams[index].events, event)
	}
	for _, stream := range streams {
		results, err := worker.Journal.AppendBatch(stream.route, stream.events)
		if err != nil {
			return false, err
		}
		for _, result := range results {
			allDuplicates = allDuplicates && result.Duplicate
		}
	}
	return allDuplicates, nil
}

func routeForEvent(fallback journal.Route, event model.EventEnvelope) journal.Route {
	route := fallback
	for _, ref := range event.Scope {
		switch ref.Kind {
		case model.EntitySession:
			route.SessionID = ref.ID.Value
		case model.EntityRun:
			route.RunID = ref.ID.Value
		}
	}
	return route
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// Watch repeats collection until the caller cancels the context. A failed
// cycle is reported through OnError and retried after interval; watch mode is
// intended to survive transient lock contention and I/O failures.
func (worker *Collector) Watch(ctx context.Context, interval time.Duration, onCycle func(Stats)) error {
	if interval <= 0 {
		interval = time.Second
	}
	for {
		stats, err := worker.RunOnce(ctx)
		if onCycle != nil {
			onCycle(stats)
		}
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			if worker.OnError != nil {
				worker.OnError(err)
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type DoctorReport struct {
	Inbox         ingest.Diagnostics `json:"inbox"`
	Projects      int                `json:"projects"`
	Streams       int                `json:"streams"`
	Events        int                `json:"events"`
	JournalErrors []string           `json:"journalErrors,omitempty"`
	ContentErrors []string           `json:"contentErrors,omitempty"`
	CatalogIssues []string           `json:"catalogIssues,omitempty"`
}

func (report DoctorReport) Healthy() bool {
	return report.Inbox.Failed == 0 && report.Inbox.IngestErrors == 0 && report.Inbox.Temporary == 0 && len(report.JournalErrors) == 0 && len(report.ContentErrors) == 0 && len(report.CatalogIssues) == 0
}

// Doctor inspects durable collection state without changing journals or
// content. It reports backlog, quarantined records, damaged streams, missing
// content, and absent or stale catalog inputs.
func (worker *Collector) Doctor() (DoctorReport, error) {
	var report DoctorReport
	diagnostics, err := worker.Inbox.Diagnostics()
	if err != nil {
		return report, err
	}
	report.Inbox = diagnostics
	layout := &store.Store{Root: worker.Root}
	projects, err := layout.ListProjectIDs()
	if err != nil {
		return report, err
	}
	for _, projectID := range projects {
		paths, err := worker.Journal.ListProjectStreams(projectID)
		if err != nil {
			report.JournalErrors = append(report.JournalErrors, err.Error())
			continue
		}
		if len(paths) == 0 {
			continue
		}
		report.Projects++
		actual := map[string]InputCursorState{}
		for _, path := range paths {
			report.Streams++
			route, events, err := worker.Journal.ReadPath(path)
			if err != nil {
				report.JournalErrors = append(report.JournalErrors, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			_ = route
			report.Events += len(events)
			info, statErr := os.Stat(path)
			rel, relErr := filepath.Rel(worker.Root, path)
			if statErr == nil && relErr == nil {
				actual[filepath.ToSlash(rel)] = InputCursorState{Bytes: info.Size(), Events: uint64(len(events))}
			}
			for _, event := range events {
				refs, refErr := model.ContentReferences(event)
				if refErr != nil {
					report.ContentErrors = append(report.ContentErrors, fmt.Sprintf("%s: %v", event.EventID.Value, refErr))
					continue
				}
				for _, ref := range refs {
					if err := worker.Content.ValidateRef(ref); err != nil {
						report.ContentErrors = append(report.ContentErrors, fmt.Sprintf("%s %s:%s: %v", event.EventID.Value, ref.ID.Namespace, ref.ID.Value, err))
					}
				}
			}
		}
		catalogData, err := os.ReadFile(layout.ProjectCatalogPath(projectID))
		if err != nil {
			report.CatalogIssues = append(report.CatalogIssues, fmt.Sprintf("%s: catalog unavailable: %v", projectID, err))
			continue
		}
		var projection catalog.ProjectCatalog
		if err := json.Unmarshal(catalogData, &projection); err != nil {
			report.CatalogIssues = append(report.CatalogIssues, fmt.Sprintf("%s: invalid catalog: %v", projectID, err))
			continue
		}
		seen := map[string]bool{}
		for _, input := range projection.Inputs {
			state, ok := actual[input.JournalPath]
			seen[input.JournalPath] = true
			if !ok || state.Bytes != input.LastByteOffset || state.Events != input.EventCount {
				report.CatalogIssues = append(report.CatalogIssues, fmt.Sprintf("%s: stale catalog input %s", projectID, input.JournalPath))
			}
		}
		for path := range actual {
			if !seen[path] {
				report.CatalogIssues = append(report.CatalogIssues, fmt.Sprintf("%s: catalog missing input %s", projectID, path))
			}
		}
	}
	sort.Strings(report.JournalErrors)
	sort.Strings(report.ContentErrors)
	sort.Strings(report.CatalogIssues)
	return report, nil
}

type InputCursorState struct {
	Bytes  int64
	Events uint64
}

// DecodeContentItem is a convenience for callers that construct event inbox
// records outside Go.
func DecodeContentItem(ref model.ContentRef, policy string, data []byte) ingest.ContentItem {
	return ingest.ContentItem{Reference: ref, Policy: policy, DataBase64: base64.StdEncoding.EncodeToString(data)}
}
