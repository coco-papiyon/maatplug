// Package journal owns the validated, append-only EventEnvelope streams.
package journal

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"maatplug/internal/model"
	"maatplug/internal/store"
)

const checkpointVersion = 1

var safeSegment = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")

type Route struct {
	ProjectID string `json:"projectId"`
	Provider  string `json:"provider"`
	SessionID string `json:"sessionId"`
	RunID     string `json:"runId"`
}

func (route Route) Validate() error {
	if !safeSegment.MatchString(route.ProjectID) || route.ProjectID == "." || route.ProjectID == ".." {
		return errors.New("projectId must be a safe filesystem identifier")
	}
	if !safeSegment.MatchString(route.Provider) || route.Provider == "." || route.Provider == ".." {
		return errors.New("provider must be a safe filesystem identifier")
	}
	if route.SessionID == "" || route.RunID == "" {
		return errors.New("sessionId and runId are required")
	}
	return nil
}

type Journal struct {
	Root string
}

func New(root string) *Journal {
	return &Journal{Root: root}
}

func (journal *Journal) StreamPath(route Route) (string, error) {
	if err := route.Validate(); err != nil {
		return "", err
	}
	layout := &store.Store{Root: journal.Root}
	return filepath.Join(
		layout.ProjectStreamsDir(route.ProjectID),
		route.Provider,
		encodeSegment(route.SessionID),
		encodeSegment(route.RunID)+".jsonl",
	), nil
}

func (journal *Journal) RouteFromPath(path string) (Route, error) {
	rel, err := filepath.Rel(journal.Root, path)
	if err != nil {
		return Route{}, err
	}
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	if len(parts) != 6 || parts[0] != "projects" || parts[2] != "streams" || filepath.Ext(parts[5]) != ".jsonl" {
		return Route{}, fmt.Errorf("path is not a journal stream: %s", path)
	}
	sessionID, err := decodeSegment(parts[4])
	if err != nil {
		return Route{}, err
	}
	runID, err := decodeSegment(strings.TrimSuffix(parts[5], ".jsonl"))
	if err != nil {
		return Route{}, err
	}
	route := Route{ProjectID: parts[1], Provider: parts[3], SessionID: sessionID, RunID: runID}
	return route, route.Validate()
}

type AppendResult struct {
	Path      string
	Position  uint64
	Duplicate bool
}

type checkpoint struct {
	Version        int                             `json:"version"`
	Route          Route                           `json:"route"`
	JournalPath    string                          `json:"journalPath"`
	LastByteOffset int64                           `json:"lastByteOffset"`
	LastPosition   uint64                          `json:"lastPosition"`
	LastEventID    model.NamespacedID              `json:"lastEventId"`
	SeenEventIDs   []model.NamespacedID            `json:"seenEventIds"`
	Adoption       map[string]model.AdoptionStatus `json:"adoption,omitempty"`
	SupersededBy   map[string]model.NamespacedID   `json:"supersededBy,omitempty"`
	CorrectedBy    map[string]model.NamespacedID   `json:"correctedBy,omitempty"`
	UpdatedAt      time.Time                       `json:"updatedAt"`
}

type scanResult struct {
	Events       []model.EventEnvelope
	Canonical    map[string][]byte
	ByteOffset   int64
	Adoption     map[string]model.AdoptionStatus
	SupersededBy map[string]model.NamespacedID
	CorrectedBy  map[string]model.NamespacedID
}

// Append validates and syncs an event before advancing the checkpoint. If a
// crash occurs after the append but before the checkpoint, the next attempt
// rescans the journal and returns Duplicate without adding a second line.
func (journal *Journal) Append(route Route, event model.EventEnvelope) (AppendResult, error) {
	results, err := journal.AppendBatch(route, []model.EventEnvelope{event})
	if err != nil {
		return AppendResult{}, err
	}
	return results[0], nil
}

// AppendBatch appends several events for one stream while scanning, syncing,
// and checkpointing the journal only once. Results correspond to events in
// input order. A retry after a partial write remains safe because the next
// scan recognizes every complete line by event ID.
func (journal *Journal) AppendBatch(route Route, events []model.EventEnvelope) ([]AppendResult, error) {
	path, err := journal.StreamPath(route)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return []AppendResult{}, nil
	}
	type preparedEvent struct {
		event     model.EventEnvelope
		canonical []byte
	}
	prepared := make([]preparedEvent, len(events))
	for index, event := range events {
		if err := event.Validate(); err != nil {
			return nil, fmt.Errorf("validate event %d: %w", index, err)
		}
		canonical, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		prepared[index] = preparedEvent{event: event, canonical: canonical}
	}
	results := make([]AppendResult, len(events))
	err = store.WithFileLock(path, func() error {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := journal.repairTail(path); err != nil {
			return err
		}
		scanned, err := scanFile(path)
		if err != nil {
			return err
		}
		positions := make(map[string]uint64, len(scanned.Events))
		for index, event := range scanned.Events {
			positions[idKey(event.EventID)] = uint64(index)
		}
		var pending bytes.Buffer
		for index, item := range prepared {
			result := AppendResult{Path: path}
			key := idKey(item.event.EventID)
			if existing, found := scanned.Canonical[key]; found {
				var existingEvent model.EventEnvelope
				if json.Unmarshal(existing, &existingEvent) != nil || !sameObservation(existingEvent, item.event) {
					return fmt.Errorf("event ID %s already exists with different content", key)
				}
				result.Duplicate = true
				result.Position = positions[key]
				results[index] = result
				continue
			}
			result.Position = uint64(len(scanned.Events))
			line := append(item.canonical, '\n')
			_, _ = pending.Write(line)
			scanned.Events = append(scanned.Events, item.event)
			scanned.Canonical[key] = item.canonical
			scanned.ByteOffset += int64(len(line))
			positions[key] = result.Position
			indexRelationships(scanned, item.event)
			results[index] = result
		}
		if pending.Len() > 0 {
			file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			written, writeErr := file.Write(pending.Bytes())
			if writeErr == nil && written != pending.Len() {
				writeErr = io.ErrShortWrite
			}
			if writeErr == nil {
				writeErr = file.Sync()
			}
			closeErr := file.Close()
			if writeErr != nil {
				return writeErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return journal.writeCheckpoint(route, path, scanned)
	})
	return results, err
}

// ReceivedAt is collector metadata and can legitimately change when the same
// provider observation is delivered again. ProviderAt and semantic content
// remain part of the identity and still detect a conflicting ID reuse.
func sameObservation(a, b model.EventEnvelope) bool {
	a.Time.ReceivedAt = time.Time{}
	b.Time.ReceivedAt = time.Time{}
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}

func (journal *Journal) Read(route Route) ([]model.EventEnvelope, error) {
	path, err := journal.StreamPath(route)
	if err != nil {
		return nil, err
	}
	var events []model.EventEnvelope
	err = store.WithFileLock(path, func() error {
		if err := journal.repairTail(path); err != nil {
			return err
		}
		scanned, err := scanFile(path)
		if err != nil {
			return err
		}
		events = scanned.Events
		return journal.writeCheckpoint(route, path, scanned)
	})
	return events, err
}

func (journal *Journal) ListProjectStreams(projectID string) ([]string, error) {
	if !safeSegment.MatchString(projectID) {
		return nil, errors.New("projectId must be a safe filesystem identifier")
	}
	root := (&store.Store{Root: journal.Root}).ProjectStreamsDir(projectID)
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			paths = append(paths, path)
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	sort.Strings(paths)
	return paths, err
}

func (journal *Journal) ReadPath(path string) (Route, []model.EventEnvelope, error) {
	route, err := journal.RouteFromPath(path)
	if err != nil {
		return Route{}, nil, err
	}
	events, err := journal.Read(route)
	return route, events, err
}

func scanFile(path string) (scanResult, error) {
	result := scanResult{
		Canonical:    map[string][]byte{},
		Adoption:     map[string]model.AdoptionStatus{},
		SupersededBy: map[string]model.NamespacedID{},
		CorrectedBy:  map[string]model.NamespacedID{},
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := append([]byte(nil), scanner.Bytes()...)
		result.ByteOffset += int64(len(line) + 1)
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if err := model.ValidateJSON(line); err != nil {
			return result, fmt.Errorf("%s:%d: %w", path, lineNumber, err)
		}
		var event model.EventEnvelope
		if err := json.Unmarshal(line, &event); err != nil {
			return result, fmt.Errorf("%s:%d: %w", path, lineNumber, err)
		}
		key := idKey(event.EventID)
		canonical, err := json.Marshal(event)
		if err != nil {
			return result, err
		}
		if previous, found := result.Canonical[key]; found && !bytes.Equal(previous, canonical) {
			return result, fmt.Errorf("%s:%d: conflicting duplicate event ID %s", path, lineNumber, key)
		}
		if _, found := result.Canonical[key]; !found {
			result.Events = append(result.Events, event)
			result.Canonical[key] = canonical
			indexRelationships(result, event)
		}
	}
	return result, scanner.Err()
}

func indexRelationships(result scanResult, event model.EventEnvelope) {
	key := idKey(event.EventID)
	if event.Adoption != nil {
		result.Adoption[key] = event.Adoption.Status
	}
	if event.Revision != nil {
		for _, target := range event.Revision.Supersedes {
			result.SupersededBy[idKey(target)] = event.EventID
		}
		for _, target := range event.Revision.Corrects {
			result.CorrectedBy[idKey(target)] = event.EventID
		}
	}
}

func (journal *Journal) repairTail(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || len(data) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	if data[len(data)-1] == '\n' {
		return nil
	}
	lastNewline := bytes.LastIndexByte(data, '\n')
	completeLength := lastNewline + 1
	partial := data[completeLength:]
	sum := sha256.Sum256(append([]byte(path+"\x00"), partial...))
	name := hex.EncodeToString(sum[:])
	recoveryPath := (&store.Store{Root: journal.Root}).StateDir("journal-recovery", name+".partial")
	if err := store.AtomicWriteFile(recoveryPath, partial, 0o600); err != nil {
		return fmt.Errorf("preserve incomplete journal tail: %w", err)
	}
	if err := os.Truncate(path, int64(completeLength)); err != nil {
		return fmt.Errorf("truncate incomplete journal tail: %w", err)
	}
	return nil
}

func (journal *Journal) writeCheckpoint(route Route, path string, scanned scanResult) error {
	rel, err := filepath.Rel(journal.Root, path)
	if err != nil {
		return err
	}
	seen := make([]model.NamespacedID, 0, len(scanned.Events))
	var last model.NamespacedID
	var updated time.Time
	for _, event := range scanned.Events {
		seen = append(seen, event.EventID)
		last = event.EventID
		if event.Time.ReceivedAt.After(updated) {
			updated = event.Time.ReceivedAt
		}
	}
	value := checkpoint{
		Version:        checkpointVersion,
		Route:          route,
		JournalPath:    filepath.ToSlash(rel),
		LastByteOffset: scanned.ByteOffset,
		LastPosition:   uint64(len(scanned.Events)),
		LastEventID:    last,
		SeenEventIDs:   seen,
		Adoption:       scanned.Adoption,
		SupersededBy:   scanned.SupersededBy,
		CorrectedBy:    scanned.CorrectedBy,
		UpdatedAt:      updated,
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(filepath.ToSlash(rel)))
	checkpointPath := (&store.Store{Root: journal.Root}).StateDir("checkpoints", hex.EncodeToString(sum[:])+".json")
	return store.AtomicWriteFile(checkpointPath, append(data, '\n'), 0o600)
}

func encodeSegment(value string) string {
	return "id-" + base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeSegment(value string) (string, error) {
	if !strings.HasPrefix(value, "id-") {
		return "", errors.New("encoded stream segment is missing id- prefix")
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "id-"))
	if err != nil || len(data) == 0 {
		return "", errors.New("invalid encoded stream segment")
	}
	return string(data), nil
}

func idKey(id model.NamespacedID) string {
	return id.Namespace + ":" + id.Value
}
