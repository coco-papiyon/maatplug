// Package ingest provides the short-lived, atomic write path into maatplug's
// flat-file inbox.
package ingest

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"maatplug/internal/model"
	"maatplug/internal/store"
)

const RecordVersion = 1

type RecordType string

const (
	RecordEvent        RecordType = "event"
	RecordProviderHook RecordType = "provider_hook"
)

type Route struct {
	ProjectID string `json:"projectId,omitempty"`
	Provider  string `json:"provider"`
	SessionID string `json:"sessionId,omitempty"`
	RunID     string `json:"runId,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
}

type ProviderHook struct {
	Event         string `json:"event"`
	PayloadBase64 string `json:"payloadBase64"`
}

func (hook ProviderHook) Payload() ([]byte, error) {
	return base64.StdEncoding.DecodeString(hook.PayloadBase64)
}

type ContentItem struct {
	Reference  model.ContentRef `json:"reference"`
	Policy     string           `json:"policy"`
	DataBase64 string           `json:"dataBase64"`
}

func (item ContentItem) Data() ([]byte, error) {
	return base64.StdEncoding.DecodeString(item.DataBase64)
}

type Record struct {
	Version     int                  `json:"version"`
	ReceiptID   string               `json:"receiptId"`
	ReceivedAt  time.Time            `json:"receivedAt"`
	Type        RecordType           `json:"type"`
	Route       Route                `json:"route"`
	Event       *model.EventEnvelope `json:"event,omitempty"`
	Hook        *ProviderHook        `json:"hook,omitempty"`
	Contents    []ContentItem        `json:"contents,omitempty"`
	Attempts    int                  `json:"attempts,omitempty"`
	AvailableAt *time.Time           `json:"availableAt,omitempty"`
	LastError   string               `json:"lastError,omitempty"`
}

type Limits struct {
	MaxActiveRecords int
	MaxRecordBytes   int64
}

func DefaultLimits() Limits {
	return Limits{MaxActiveRecords: 10_000, MaxRecordBytes: 64 << 20}
}

type Inbox struct {
	Root       string
	Limits     Limits
	Now        func() time.Time
	StaleAfter time.Duration
}

func New(root string) *Inbox {
	return &Inbox{
		Root:       root,
		Limits:     DefaultLimits(),
		Now:        time.Now,
		StaleAfter: 5 * time.Minute,
	}
}

func (inbox *Inbox) EnqueueHook(provider, event string, payload []byte) (string, error) {
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(event) == "" {
		return "", errors.New("provider and hook event are required")
	}
	record := Record{
		Type:  RecordProviderHook,
		Route: Route{Provider: provider},
		Hook: &ProviderHook{
			Event:         event,
			PayloadBase64: base64.StdEncoding.EncodeToString(payload),
		},
	}
	return inbox.Enqueue(record)
}

func (inbox *Inbox) EnqueueEvent(route Route, event model.EventEnvelope, contents []ContentItem) (string, error) {
	if err := event.Validate(); err != nil {
		return "", fmt.Errorf("validate event: %w", err)
	}
	return inbox.Enqueue(Record{Type: RecordEvent, Route: route, Event: &event, Contents: contents})
}

// Enqueue assigns a unique receipt ID and commits one complete JSON file into
// pending. The capacity check and commit share a cross-process lock.
func (inbox *Inbox) Enqueue(record Record) (string, error) {
	now := time.Now
	if inbox.Now != nil {
		now = inbox.Now
	}
	receivedAt := now().UTC()
	if record.ReceiptID == "" {
		var err error
		record.ReceiptID, err = newReceiptID(receivedAt)
		if err != nil {
			return "", fmt.Errorf("generate receipt ID: %w", err)
		}
	}
	record.Version = RecordVersion
	record.ReceivedAt = receivedAt
	if err := validateRecord(record); err != nil {
		inbox.writeDiagnostic(record.ReceiptID, "invalid_record", err.Error(), 0)
		return "", err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	encoded = append(encoded, '\n')
	if inbox.Limits.MaxRecordBytes > 0 && int64(len(encoded)) > inbox.Limits.MaxRecordBytes {
		err := fmt.Errorf("inbox record is %d bytes; limit is %d", len(encoded), inbox.Limits.MaxRecordBytes)
		inbox.writeDiagnostic(record.ReceiptID, "record_too_large", err.Error(), int64(len(encoded)))
		return "", err
	}

	layout := &store.Store{Root: inbox.Root}
	if err := layout.EnsureEventLayout(); err != nil {
		inbox.writeDiagnostic(record.ReceiptID, "layout_unavailable", err.Error(), int64(len(encoded)))
		return "", err
	}
	lockTarget := filepath.Join(inbox.Root, "inbox", "capacity")
	err = store.WithFileLock(lockTarget, func() error {
		if inbox.Limits.MaxActiveRecords > 0 {
			count, err := inbox.activeCount()
			if err != nil {
				return err
			}
			if count >= inbox.Limits.MaxActiveRecords {
				return fmt.Errorf("inbox capacity reached: %d active records", count)
			}
		}
		path := filepath.Join(layout.InboxDir(store.InboxPending), record.ReceiptID+".json")
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("receipt ID collision: %s", record.ReceiptID)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return store.AtomicWriteFile(path, encoded, 0o600)
	})
	if err != nil {
		inbox.writeDiagnostic(record.ReceiptID, "enqueue_failed", err.Error(), int64(len(encoded)))
		return "", err
	}
	return record.ReceiptID, nil
}

type Claim struct {
	Record Record
	Path   string
	Name   string
}

// ClaimNext atomically moves one ready record from pending to processing.
// Multiple collectors may race; only one rename succeeds for each record.
func (inbox *Inbox) ClaimNext() (*Claim, error) {
	layout := &store.Store{Root: inbox.Root}
	entries, err := os.ReadDir(layout.InboxDir(store.InboxPending))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	now := time.Now()
	if inbox.Now != nil {
		now = inbox.Now()
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		pendingPath := filepath.Join(layout.InboxDir(store.InboxPending), entry.Name())
		processingPath := filepath.Join(layout.InboxDir(store.InboxProcessing), entry.Name())
		if err := os.Rename(pendingPath, processingPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		data, err := os.ReadFile(processingPath)
		if err != nil {
			return &Claim{Path: processingPath, Name: entry.Name()}, err
		}
		var record Record
		if err := json.Unmarshal(data, &record); err != nil {
			return &Claim{Path: processingPath, Name: entry.Name()}, fmt.Errorf("decode inbox record: %w", err)
		}
		if err := validateRecord(record); err != nil {
			return &Claim{Record: record, Path: processingPath, Name: entry.Name()}, err
		}
		if record.AvailableAt != nil && record.AvailableAt.After(now) {
			if err := os.Rename(processingPath, pendingPath); err != nil {
				return nil, err
			}
			continue
		}
		return &Claim{Record: record, Path: processingPath, Name: entry.Name()}, nil
	}
	return nil, nil
}

func (inbox *Inbox) Complete(claim *Claim) error {
	if claim == nil || claim.Path == "" {
		return errors.New("claim is required")
	}
	return os.Remove(claim.Path)
}

func (inbox *Inbox) Retry(claim *Claim, cause error, delay time.Duration) error {
	if claim == nil || claim.Path == "" {
		return errors.New("claim is required")
	}
	claim.Record.Attempts++
	claim.Record.LastError = cause.Error()
	available := time.Now().Add(delay)
	if inbox.Now != nil {
		available = inbox.Now().Add(delay)
	}
	available = available.UTC()
	claim.Record.AvailableAt = &available
	encoded, err := json.Marshal(claim.Record)
	if err != nil {
		return err
	}
	if err := store.AtomicWriteFile(claim.Path, append(encoded, '\n'), 0o600); err != nil {
		return err
	}
	pendingPath := filepath.Join((&store.Store{Root: inbox.Root}).InboxDir(store.InboxPending), claim.Name)
	return os.Rename(claim.Path, pendingPath)
}

func (inbox *Inbox) Fail(claim *Claim, cause error) error {
	if claim == nil || claim.Path == "" {
		return errors.New("claim is required")
	}
	layout := &store.Store{Root: inbox.Root}
	base := strings.TrimSuffix(claim.Name, ".json")
	failedPath := filepath.Join(layout.InboxDir(store.InboxFailed), base+".record.json")
	if err := os.Rename(claim.Path, failedPath); err != nil {
		return err
	}
	diagnostic := struct {
		ReceiptID string    `json:"receiptId,omitempty"`
		FailedAt  time.Time `json:"failedAt"`
		Error     string    `json:"error"`
	}{ReceiptID: claim.Record.ReceiptID, FailedAt: time.Now().UTC(), Error: cause.Error()}
	data, _ := json.Marshal(diagnostic)
	return store.AtomicWriteFile(filepath.Join(layout.InboxDir(store.InboxFailed), base+".error.json"), append(data, '\n'), 0o600)
}

// RecoverStale returns abandoned processing records to pending. Fresh files
// are left alone so an active collector is not disrupted.
func (inbox *Inbox) RecoverStale() (int, error) {
	layout := &store.Store{Root: inbox.Root}
	recovered, err := inbox.recoverTemporaryFiles()
	if err != nil {
		return recovered, err
	}
	entries, err := os.ReadDir(layout.InboxDir(store.InboxProcessing))
	if errors.Is(err, os.ErrNotExist) {
		return recovered, nil
	}
	if err != nil {
		return recovered, err
	}
	now := time.Now()
	if inbox.Now != nil {
		now = inbox.Now()
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return recovered, err
		}
		if inbox.StaleAfter > 0 && now.Sub(info.ModTime()) < inbox.StaleAfter {
			continue
		}
		from := filepath.Join(layout.InboxDir(store.InboxProcessing), entry.Name())
		to := filepath.Join(layout.InboxDir(store.InboxPending), entry.Name())
		if err := os.Rename(from, to); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return recovered, err
		}
		recovered++
	}
	return recovered, nil
}

func (inbox *Inbox) recoverTemporaryFiles() (int, error) {
	layout := &store.Store{Root: inbox.Root}
	now := time.Now()
	if inbox.Now != nil {
		now = inbox.Now()
	}
	recovered := 0
	for _, state := range []string{store.InboxPending, store.InboxProcessing} {
		dir := layout.InboxDir(state)
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return recovered, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.Contains(entry.Name(), ".tmp-") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return recovered, err
			}
			if inbox.StaleAfter > 0 && now.Sub(info.ModTime()) < inbox.StaleAfter {
				continue
			}
			from := filepath.Join(dir, entry.Name())
			to := filepath.Join(layout.InboxDir(store.InboxFailed), "interrupted-"+entry.Name()+".partial")
			if err := os.Rename(from, to); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return recovered, err
			}
			inbox.writeDiagnostic("", "interrupted_write", "stale atomic-write temporary file preserved in failed inbox", info.Size())
			recovered++
		}
	}
	return recovered, nil
}

type Diagnostics struct {
	Pending        int   `json:"pending"`
	Processing     int   `json:"processing"`
	Failed         int   `json:"failed"`
	Temporary      int   `json:"temporary"`
	IngestErrors   int   `json:"ingestErrors"`
	ActiveBytes    int64 `json:"activeBytes"`
	Capacity       int   `json:"capacity"`
	MaxRecordBytes int64 `json:"maxRecordBytes"`
}

func (inbox *Inbox) Diagnostics() (Diagnostics, error) {
	layout := &store.Store{Root: inbox.Root}
	result := Diagnostics{Capacity: inbox.Limits.MaxActiveRecords, MaxRecordBytes: inbox.Limits.MaxRecordBytes}
	for _, item := range []struct {
		dir    string
		count  *int
		failed bool
	}{
		{layout.InboxDir(store.InboxPending), &result.Pending, false},
		{layout.InboxDir(store.InboxProcessing), &result.Processing, false},
		{layout.InboxDir(store.InboxFailed), &result.Failed, true},
	} {
		entries, err := os.ReadDir(item.dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Diagnostics{}, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if item.failed {
				if strings.HasSuffix(entry.Name(), ".record.json") {
					*item.count++
				}
				continue
			}
			if strings.Contains(entry.Name(), ".tmp-") {
				result.Temporary++
				continue
			}
			if strings.HasSuffix(entry.Name(), ".json") {
				*item.count++
				if info, err := entry.Info(); err == nil {
					result.ActiveBytes += info.Size()
				}
			}
		}
	}
	if entries, err := os.ReadDir(layout.StateDir("ingest-errors")); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
				result.IngestErrors++
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Diagnostics{}, err
	}
	return result, nil
}

func validateRecord(record Record) error {
	if record.Version != RecordVersion {
		return fmt.Errorf("unsupported inbox record version %d", record.Version)
	}
	if strings.TrimSpace(record.ReceiptID) == "" || record.ReceivedAt.IsZero() {
		return errors.New("receiptId and receivedAt are required")
	}
	if strings.TrimSpace(record.Route.Provider) == "" {
		return errors.New("route.provider is required")
	}
	switch record.Type {
	case RecordEvent:
		if record.Event == nil || record.Hook != nil {
			return errors.New("event record requires event and forbids hook")
		}
		if record.Route.ProjectID == "" || record.Route.SessionID == "" || record.Route.RunID == "" {
			return errors.New("event route requires projectId, sessionId, and runId")
		}
		if err := record.Event.Validate(); err != nil {
			return fmt.Errorf("validate event: %w", err)
		}
	case RecordProviderHook:
		if record.Hook == nil || record.Event != nil || record.Hook.Event == "" || record.Hook.PayloadBase64 == "" {
			return errors.New("provider hook record requires hook event and payload")
		}
		if _, err := record.Hook.Payload(); err != nil {
			return fmt.Errorf("decode hook payload: %w", err)
		}
	default:
		return fmt.Errorf("unsupported inbox record type %q", record.Type)
	}
	return nil
}

func (inbox *Inbox) activeCount() (int, error) {
	layout := &store.Store{Root: inbox.Root}
	count := 0
	for _, dir := range []string{layout.InboxDir(store.InboxPending), layout.InboxDir(store.InboxProcessing)} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return 0, err
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") && !strings.Contains(entry.Name(), ".tmp-") {
				count++
			}
		}
	}
	return count, nil
}

func (inbox *Inbox) writeDiagnostic(receiptID, kind, message string, size int64) {
	if receiptID == "" {
		generated, err := newReceiptID(time.Now())
		if err != nil {
			return
		}
		receiptID = generated
	}
	diagnostic := struct {
		ReceiptID string    `json:"receiptId"`
		At        time.Time `json:"at"`
		Kind      string    `json:"kind"`
		Message   string    `json:"message"`
		Bytes     int64     `json:"bytes,omitempty"`
	}{receiptID, time.Now().UTC(), kind, message, size}
	data, err := json.Marshal(diagnostic)
	if err != nil {
		return
	}
	path := (&store.Store{Root: inbox.Root}).StateDir("ingest-errors", receiptID+".json")
	_ = store.AtomicWriteFile(path, append(data, '\n'), 0o600)
}

func newReceiptID(now time.Time) (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return fmt.Sprintf("%020d-%s", now.UTC().UnixNano(), hex.EncodeToString(random)), nil
}
