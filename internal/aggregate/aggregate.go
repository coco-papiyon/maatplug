// Package aggregate computes summaries from the flat-file JSONL store. It is
// meant to be the one place this logic lives in Go, shared by the CLI's
// report/status commands and (later) maatplug-web — see docs/design.md
// section 8. A future TypeScript port for the VSCode extension follows the
// same shape but is a separate implementation (design.md section 12).
package aggregate

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	"maatplug/internal/model"
	"maatplug/internal/store"
)

// Filter narrows which recorded activity Project/All/Sessions summarize.
// The zero value matches everything.
type Filter struct {
	// Provider, when non-empty ("claude" or "codex"), restricts the summary
	// to that provider's session files only.
	Provider string
	// Since, when non-zero, excludes sessions that started before it —
	// see sessionStartedAt for how "started" is determined.
	Since time.Time
}

// matchesProvider reports whether a session file's provider passes f.
func (f Filter) matchesProvider(provider string) bool {
	return f.Provider == "" || f.Provider == provider
}

// sessionStartedAt returns the timestamp of a session's session_start
// event (always the first event a session file is written with — see
// internal/recorder.Recorder.SessionStart). Returns the zero Time if the
// file has no session_start event (e.g. truncated/malformed), in which
// case callers should treat the session as passing any Since filter rather
// than silently hiding data of unknown age.
func sessionStartedAt(events []model.RawEvent) time.Time {
	for _, ev := range events {
		if ev.Type == model.EventSessionStart {
			return ev.Timestamp
		}
	}
	return time.Time{}
}

// matchesSince reports whether a session (whose events start with
// startedAt) passes f's Since filter.
func (f Filter) matchesSince(startedAt time.Time) bool {
	return f.Since.IsZero() || startedAt.IsZero() || !startedAt.Before(f.Since)
}

// ProjectSummary totals one project's recorded activity across every
// provider session on disk.
type ProjectSummary struct {
	ProjectID    string `json:"projectId"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	Sessions     int    `json:"sessions"`
	Instructions int    `json:"instructions"`
	TotalTokens  int    `json:"totalTokens"`
	// TokensInput/Output/CacheRead/CacheCreation break TotalTokens down by
	// bucket (see model.TokenUsage) for the web dashboard's IN/Cache/OUT
	// display. Like TotalTokens, these exclude abandoned turns.
	TokensInput         int `json:"tokensInput"`
	TokensOutput        int `json:"tokensOutput"`
	TokensCacheRead     int `json:"tokensCacheRead"`
	TokensCacheCreation int `json:"tokensCacheCreation"`
	LinesAdded          int `json:"linesAdded"`
	LinesRemoved        int `json:"linesRemoved"`
	// AbandonedInstructions counts instructions whose turn_end was written
	// with abandoned:true (docs/design.md section 6.1: an unresolvable
	// concurrent-session-resume state, forced closed with no real diff/
	// tokens). These are counted in Instructions but deliberately excluded
	// from TotalTokens/LinesAdded/LinesRemoved, so a project with abandoned
	// turns doesn't read as "measured, and the measurement was zero".
	AbandonedInstructions int `json:"abandonedInstructions"`
	// CostUSD sums each priced instruction's token usage against
	// internal/pricing's reference rates for the model that answered it. Like
	// TotalTokens, it excludes abandoned/merged turns.
	CostUSD float64 `json:"costUsd"`
	// CostUnpricedInstructions counts instructions with real token usage
	// whose model had no matching entry in the pricing reference data (e.g.
	// an unrecognized/new model id), so CostUSD is missing their
	// contribution. Mirrors AbandonedInstructions: surfaced so the total
	// doesn't silently read as "fully priced" when it isn't.
	CostUnpricedInstructions int `json:"costUnpricedInstructions"`
}

// Project summarizes a single project by ID, restricted to activity
// matching filter (see Filter's doc comment; the zero Filter matches
// everything).
func Project(s *store.Store, projectID string, filter Filter) (ProjectSummary, error) {
	name, path, err := s.ProjectMeta(projectID)
	if err != nil {
		return ProjectSummary{}, err
	}

	summary := ProjectSummary{ProjectID: projectID, Name: name, Path: path}

	files, err := s.ListSessionFiles(projectID)
	if err != nil {
		return ProjectSummary{}, err
	}

	pricingDoc := loadPricing()
	for _, f := range files {
		provider, _, ok := store.ParseSessionFileName(filepath.Base(f))
		if !ok {
			continue // not a session file this store wrote; ignore rather than fail the whole summary
		}
		if !filter.matchesProvider(provider) {
			continue
		}

		events, err := store.ReadEvents(f)
		if err != nil {
			return ProjectSummary{}, err
		}
		if !filter.matchesSince(sessionStartedAt(events)) {
			continue
		}
		summary.Sessions++

		for _, ev := range events {
			switch ev.Type {
			case model.EventInstruction:
				summary.Instructions++
			case model.EventTurnEnd:
				if ev.Abandoned {
					summary.AbandonedInstructions++
					continue
				}
				if ev.MergedInto != "" {
					// Its diff/tokens live on the instruction named by
					// MergedInto instead (see model.TurnEndEvent.MergedInto);
					// counting this one too would double-count them.
					continue
				}
				summary.TotalTokens += ev.Tokens.Total
				summary.TokensInput += ev.Tokens.Input
				summary.TokensOutput += ev.Tokens.Output
				summary.TokensCacheRead += ev.Tokens.CacheRead
				summary.TokensCacheCreation += ev.Tokens.CacheCreation
				summary.LinesAdded += ev.Diff.Added
				summary.LinesRemoved += ev.Diff.Removed
				if !ev.TokensUnavailable {
					if cost, ok := costFor(pricingDoc, provider, ev.Model, ev.Tokens); ok {
						summary.CostUSD += cost
					} else {
						summary.CostUnpricedInstructions++
					}
				}
			}
		}
	}
	return summary, nil
}

// All summarizes every project the store knows about, sorted by project ID
// for stable output. Projects rooted inside the store's own data directory
// are excluded — no real project lives there; it's maatplug-web's own
// usage-check probe (see store.UsageCheckDir) polling plan-usage windows,
// possibly under a stale name from an earlier version of that probe
// directory, not real project activity. filter is passed through to
// Project — see its doc comment.
func All(s *store.Store, filter Filter) ([]ProjectSummary, error) {
	ids, err := s.ListProjectIDs()
	if err != nil {
		return nil, err
	}
	sort.Strings(ids)

	summaries := make([]ProjectSummary, 0, len(ids))
	for _, id := range ids {
		_, path, err := s.ProjectMeta(id)
		if err != nil {
			return nil, err
		}
		if isUnderStoreRoot(s.Root, path) {
			continue
		}

		summary, err := Project(s, id, filter)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

// isUnderStoreRoot reports whether path lies inside root (maatplug's own
// data directory) — the case for the usage-check probe directory, under
// whatever name it was recorded with, and never the case for a real project.
func isUnderStoreRoot(root, path string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
