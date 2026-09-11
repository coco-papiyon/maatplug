package remotesync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseRemoteFileList(t *testing.T) {
	out := []byte("123\t./projects/abc/sessions/claude-1.jsonl\n" +
		"45\t./projects/abc/project.json\n" +
		"0\t./data-areas.json\n")

	got, err := parseRemoteFileList(out)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{
		"projects/abc/sessions/claude-1.jsonl": 123,
		"projects/abc/project.json":            45,
		"data-areas.json":                      0,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("got[%q] = %d, want %d", k, got[k], v)
		}
	}
}

func TestParseRemoteFileList_ToleratesMalformedLines(t *testing.T) {
	out := []byte("123\t./ok.jsonl\n" +
		"not a valid line at all\n" +
		"abc\t./bad-size.jsonl\n" +
		"\n" +
		"7\t./also-ok.jsonl\n")

	got, err := parseRemoteFileList(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["ok.jsonl"] != 123 || got["also-ok.jsonl"] != 7 {
		t.Errorf("got = %+v, want only the two well-formed lines", got)
	}
}

func TestParseRemoteFileList_Empty(t *testing.T) {
	got, err := parseRemoteFileList([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got = %+v, want empty", got)
	}
}

func TestNeedsFetch_MissingFileNeedsFetch(t *testing.T) {
	dir := t.TempDir()
	if !needsFetch(filepath.Join(dir, "does-not-exist.jsonl"), 100) {
		t.Error("needsFetch(missing file) = false, want true")
	}
}

func TestNeedsFetch_SameSizeSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if needsFetch(path, 5) {
		t.Error("needsFetch(same size) = true, want false (unchanged, should skip)")
	}
}

func TestNeedsFetch_GrownFileNeedsRefetch(t *testing.T) {
	// The core case this whole package exists for: an append-only session
	// log that grew since the last sync must be re-fetched, not skipped
	// just because a local copy already exists.
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte("short"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !needsFetch(path, 500) {
		t.Error("needsFetch(grown file) = false, want true")
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/plain/path":  `'/plain/path'`,
		"/it's/quoted": `'/it'\''s/quoted'`,
		"":             `''`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
