package collector

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	provideradapter "maatplug/internal/provider/adapter"
	"maatplug/internal/store"
)

const transcriptCursorVersion = 1

type transcriptCursor struct {
	Version    int      `json:"version"`
	Provider   string   `json:"provider"`
	Path       string   `json:"path"`
	Generation string   `json:"generation"`
	ByteOffset int64    `json:"byteOffset"`
	Line       uint64   `json:"line"`
	SeenIDs    []string `json:"seenIds,omitempty"`
}

func (worker *Collector) readTranscript(provider, path string) (provideradapter.Transcript, error) {
	result := provideradapter.Transcript{Path: path}
	if path == "" {
		result.Unavailable = "hook payload did not provide transcript_path"
		return result, nil
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		result.Unavailable = "transcript file is not available yet"
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return result, err
	}
	firstLine, err := firstCompleteLine(file)
	if err != nil {
		return result, err
	}
	result.Generation = transcriptGeneration(path, firstLine)
	cursor, err := worker.loadTranscriptCursor(provider, path)
	if err != nil {
		return result, err
	}
	if cursor.Generation != result.Generation || info.Size() < cursor.ByteOffset {
		result.Rotated = cursor.Generation != ""
		cursor.ByteOffset = 0
		cursor.Line = 0
	}
	result.StartByteOffset = cursor.ByteOffset
	result.StartLine = cursor.Line
	result.KnownIDs = make(map[string]bool, len(cursor.SeenIDs))
	for _, id := range cursor.SeenIDs {
		result.KnownIDs[id] = true
	}
	if _, err := file.Seek(cursor.ByteOffset, io.SeekStart); err != nil {
		return result, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return result, err
	}
	completeLength := 0
	if index := bytes.LastIndexByte(data, '\n'); index >= 0 {
		completeLength = index + 1
	}
	result.IncompleteTail = completeLength < len(data)
	complete := data[:completeLength]
	offset := cursor.ByteOffset
	lineNo := cursor.Line
	for len(complete) > 0 {
		index := bytes.IndexByte(complete, '\n')
		line := bytes.TrimSuffix(complete[:index], []byte{'\r'})
		consumed := index + 1
		if len(bytes.TrimSpace(line)) > 0 {
			result.Lines = append(result.Lines, provideradapter.Line{Number: lineNo, ByteOffset: offset, Data: append([]byte(nil), line...)})
		}
		offset += int64(consumed)
		lineNo++
		complete = complete[consumed:]
	}
	result.NextByteOffset = cursor.ByteOffset + int64(completeLength)
	result.NextLine = lineNo
	return result, nil
}

func firstCompleteLine(file *os.File) ([]byte, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(file, 64*1024)
	line, err := reader.ReadBytes('\n')
	if errors.Is(err, io.EOF) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return bytes.TrimSpace(line), nil
}

func transcriptGeneration(path string, firstLine []byte) string {
	hash := sha256.Sum256(append(append([]byte(filepath.Clean(path)), 0), firstLine...))
	return hex.EncodeToString(hash[:])
}

func (worker *Collector) transcriptCursorPath(provider, path string) string {
	hash := sha256.Sum256([]byte(provider + "\x00" + filepath.Clean(path)))
	return (&store.Store{Root: worker.Root}).StateDir("provider-cursors", provider, hex.EncodeToString(hash[:])+".json")
}

func (worker *Collector) loadTranscriptCursor(provider, path string) (transcriptCursor, error) {
	cursor := transcriptCursor{Version: transcriptCursorVersion, Provider: provider, Path: path}
	data, err := os.ReadFile(worker.transcriptCursorPath(provider, path))
	if errors.Is(err, os.ErrNotExist) {
		return cursor, nil
	}
	if err != nil {
		return cursor, err
	}
	if err := json.Unmarshal(data, &cursor); err != nil {
		return cursor, fmt.Errorf("decode transcript cursor: %w", err)
	}
	if cursor.Version != transcriptCursorVersion || cursor.Provider != provider || filepath.Clean(cursor.Path) != filepath.Clean(path) {
		return transcriptCursor{}, errors.New("transcript cursor identity mismatch")
	}
	return cursor, nil
}

func (worker *Collector) saveTranscriptCursor(provider string, transcript provideradapter.Transcript, seenIDs []string) error {
	if transcript.Path == "" || transcript.Unavailable != "" {
		return nil
	}
	if transcript.IncompleteTail && transcript.StartByteOffset == 0 && transcript.NextByteOffset == 0 {
		return nil
	}
	seen := make(map[string]bool, len(transcript.KnownIDs)+len(seenIDs))
	for id := range transcript.KnownIDs {
		seen[id] = true
	}
	for _, id := range seenIDs {
		seen[id] = true
	}
	allSeen := make([]string, 0, len(seen))
	for id := range seen {
		allSeen = append(allSeen, id)
	}
	sort.Strings(allSeen)
	cursor := transcriptCursor{Version: transcriptCursorVersion, Provider: provider, Path: transcript.Path, Generation: transcript.Generation, ByteOffset: transcript.NextByteOffset, Line: transcript.NextLine, SeenIDs: allSeen}
	data, err := json.Marshal(cursor)
	if err != nil {
		return err
	}
	return store.AtomicWriteFile(worker.transcriptCursorPath(provider, transcript.Path), append(data, '\n'), 0o600)
}
