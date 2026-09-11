package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"maatplug/internal/ingest"
	legacyclaude "maatplug/internal/provider/claude"
	"maatplug/internal/recorder"
	"maatplug/internal/store"
)

// BenchmarkHookBoundary compares the Phase 3 synchronous hook boundary with
// the previous direct recorder path. The collector is deliberately excluded:
// it runs after the provider process has been released.
func BenchmarkHookBoundary(b *testing.B) {
	payloadFor := func(sessionID, cwd string) []byte {
		data, err := json.Marshal(map[string]string{
			"session_id": sessionID, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "benchmark prompt",
		})
		if err != nil {
			b.Fatal(err)
		}
		return data
	}
	b.Run("phase3-inbox", func(b *testing.B) {
		root := b.TempDir()
		inbox := ingest.New(root)
		cwd := b.TempDir()
		payload := payloadFor("session", cwd)
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			if _, err := inbox.EnqueueHook("claude", "UserPromptSubmit", payload); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(0, "failures/op")
	})
	b.Run("legacy-direct-recorder", func(b *testing.B) {
		root := b.TempDir()
		cwd := b.TempDir()
		handler := legacyclaude.NewHandler(recorder.New(&store.Store{Root: root}))
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			payload := payloadFor(fmt.Sprintf("session-%d", index), cwd)
			if err := handler.Handle("UserPromptSubmit", payload); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(0, "failures/op")
	})
}
