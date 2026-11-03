package collector

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranscriptCursorResumesAtNextChunk(t *testing.T) {
	root := t.TempDir()
	worker := New(root)
	path := filepath.Join(root, "transcript.jsonl")
	var source strings.Builder
	for index := 0; index < transcriptChunkMaxLines+1; index++ {
		fmt.Fprintf(&source, "{\"line\":%d}\n", index)
	}
	if err := os.WriteFile(path, []byte(source.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	first, err := worker.readTranscript("codex", path)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Lines) != transcriptChunkMaxLines || !first.HasMore || first.NextLine != transcriptChunkMaxLines {
		t.Fatalf("first chunk = lines:%d next:%d more:%t", len(first.Lines), first.NextLine, first.HasMore)
	}
	if err := worker.saveTranscriptCursor("codex", first, []string{"seen-first-chunk"}); err != nil {
		t.Fatal(err)
	}

	second, err := worker.readTranscript("codex", path)
	if err != nil {
		t.Fatal(err)
	}
	if second.StartByteOffset != first.NextByteOffset || second.StartLine != first.NextLine {
		t.Fatalf("second chunk starts at byte %d line %d, want byte %d line %d", second.StartByteOffset, second.StartLine, first.NextByteOffset, first.NextLine)
	}
	if len(second.Lines) != 1 || second.HasMore || second.NextLine != transcriptChunkMaxLines+1 {
		t.Fatalf("second chunk = lines:%d next:%d more:%t", len(second.Lines), second.NextLine, second.HasMore)
	}
	if !second.KnownIDs["seen-first-chunk"] {
		t.Fatal("saved normalized ID was not restored with the cursor")
	}
}
