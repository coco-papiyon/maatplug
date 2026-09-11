package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"maatplug/internal/collector"
	"maatplug/internal/ingest"
	"maatplug/internal/store"
)

func TestHookWritesOnlyInboxUntilCollectorRuns(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAATPLUG_HOME", root)
	payload := fmt.Sprintf(`{"session_id":"session-1","cwd":%q,"turn_id":"turn-1","prompt":"test prompt"}`, cwd)
	stdinPath := filepath.Join(root, "stdin.json")
	if err := os.WriteFile(stdinPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	stdin, err := os.Open(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = stdin
	t.Cleanup(func() { os.Stdin = oldStdin; _ = stdin.Close() })

	if err := runHook([]string{"codex", "--event", "UserPromptSubmit"}); err != nil {
		t.Fatal(err)
	}
	diagnostics, err := ingest.New(root).Diagnostics()
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics.Pending != 1 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if _, err := os.Stat(filepath.Join(root, "projects")); !os.IsNotExist(err) {
		t.Fatalf("hook wrote outside inbox before collect: %v", err)
	}

	stats, err := collector.New(root).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Completed != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	projectID := store.ProjectID(cwd)
	if _, err := os.Stat((&store.Store{Root: root}).ProjectCatalogPath(projectID)); err != nil {
		t.Fatal(err)
	}
}
