package pricing

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLoad_MissingFileReturnsEmptyDocument(t *testing.T) {
	dir := t.TempDir()
	doc, err := Load(filepath.Join(dir, "does-not-exist.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Models) != 0 {
		t.Errorf("doc.Models = %+v, want empty", doc.Models)
	}
}

func TestSaveThenLoad_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pricing.json")

	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	want := Document{
		GeneratedAt: now,
		Models: []ModelPricing{
			{Provider: "claude", Model: "claude-sonnet-5", InputPerMillion: 2, OutputPerMillion: 10, CachedInputPerMillion: 0.2, CacheWritePerMillion: 2.5, SourceURL: ClaudeSourceURL, RetrievedAt: now},
			{Provider: "codex", Model: "gpt-5.6-sol", InputPerMillion: 4, OutputPerMillion: 20, CachedInputPerMillion: 0.4, SourceURL: CodexSourceURL, RetrievedAt: now},
		},
	}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.GeneratedAt.Equal(want.GeneratedAt) {
		t.Errorf("GeneratedAt = %v, want %v", got.GeneratedAt, want.GeneratedAt)
	}
	if len(got.Models) != len(want.Models) {
		t.Fatalf("len(Models) = %d, want %d", len(got.Models), len(want.Models))
	}
	for i := range want.Models {
		if got.Models[i] != want.Models[i] {
			t.Errorf("Models[%d] = %+v, want %+v", i, got.Models[i], want.Models[i])
		}
	}
}

func TestDocument_ForProvider(t *testing.T) {
	doc := Document{
		Models: []ModelPricing{
			{Provider: "claude", Model: "claude-sonnet-5"},
			{Provider: "codex", Model: "gpt-5.6-sol"},
			{Provider: "claude", Model: "claude-opus-5"},
		},
	}

	claude := doc.ForProvider("claude")
	if len(claude) != 2 || claude[0].Model != "claude-sonnet-5" || claude[1].Model != "claude-opus-5" {
		t.Errorf("ForProvider(claude) = %+v, want [claude-sonnet-5 claude-opus-5]", claude)
	}

	copilot := doc.ForProvider("copilot")
	if len(copilot) != 0 {
		t.Errorf("ForProvider(copilot) = %+v, want empty", copilot)
	}
}

func TestDocument_Lookup(t *testing.T) {
	doc := Document{
		Models: []ModelPricing{
			{Provider: "claude", Model: "claude-sonnet-5", InputPerMillion: 2},
			{Provider: "codex", Model: "gpt-5.6-sol", InputPerMillion: 4},
		},
	}

	if m, ok := doc.Lookup("claude", "claude-sonnet-5"); !ok || m.InputPerMillion != 2 {
		t.Errorf("Lookup(claude, claude-sonnet-5) = %+v, %v, want the claude-sonnet-5 entry", m, ok)
	}
	if _, ok := doc.Lookup("codex", "claude-sonnet-5"); ok {
		t.Error("Lookup(codex, claude-sonnet-5) = ok, want not found (model id belongs to a different provider)")
	}
	if _, ok := doc.Lookup("claude", "claude-nonexistent"); ok {
		t.Error("Lookup(claude, claude-nonexistent) = ok, want not found")
	}
}

func TestModelPricing_Cost(t *testing.T) {
	m := ModelPricing{InputPerMillion: 2, CachedInputPerMillion: 0.2, CacheWritePerMillion: 2.5, OutputPerMillion: 10}

	got := m.Cost(1_000_000, 1_000_000, 1_000_000, 1_000_000)
	want := 2 + 0.2 + 2.5 + 10.0
	if got != want {
		t.Errorf("Cost(1M each) = %v, want %v", got, want)
	}

	if got := m.Cost(0, 0, 0, 0); got != 0 {
		t.Errorf("Cost(0,0,0,0) = %v, want 0", got)
	}

	// 500k input tokens at $2/million = $1.
	if got := m.Cost(500_000, 0, 0, 0); got != 1 {
		t.Errorf("Cost(500k input) = %v, want 1", got)
	}
}
