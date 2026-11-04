package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"

	"maatplug/internal/attribution"
	"maatplug/internal/billing"
	"maatplug/internal/model"
	"maatplug/internal/pricing"
	"maatplug/internal/store"
)

const ListVersion = 1

var listProjectID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type ProjectListEntry struct {
	CatalogModifiedNS  int64                        `json:"catalogModifiedNs"`
	Version            int                          `json:"version"`
	AggregationVersion int                          `json:"aggregationVersion"`
	Project            ProjectSummary               `json:"project"`
	Periods            []PeriodSummary              `json:"periods,omitempty"`
	Providers          map[string]ProviderListEntry `json:"providers,omitempty"`
}

type ProviderListEntry struct {
	Project ProjectSummary  `json:"project"`
	Periods []PeriodSummary `json:"periods,omitempty"`
}

type ProjectList struct {
	Version            int                `json:"version"`
	AggregationVersion int                `json:"aggregationVersion"`
	Projects           []ProjectListEntry `json:"projects"`
}

type SessionList struct {
	CatalogModifiedNS  int64            `json:"catalogModifiedNs"`
	Version            int              `json:"version"`
	AggregationVersion int              `json:"aggregationVersion"`
	ProjectID          string           `json:"projectId"`
	Sessions           []SessionSummary `json:"sessions"`
}

func ProjectListPath(root string) string { return filepath.Join(root, "project-list.json") }
func SessionListPath(root, id string) string {
	return filepath.Join(root, "projects", id, "session-list.json")
}
func projectListEntryPath(root, id string) string {
	return filepath.Join(root, "projects", id, "list-summary.json")
}

func readListFile(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func ReadProjectList(root string) (ProjectList, error) {
	var result ProjectList
	err := readListFile(ProjectListPath(root), &result)
	if err == nil && (result.Version != ListVersion || result.AggregationVersion != AggregationVersion) {
		err = errors.New("outdated project list")
	}
	return result, err
}

func ReadSessionList(root, id string) (SessionList, error) {
	var result SessionList
	if !listProjectID.MatchString(id) {
		return result, errors.New("invalid project ID")
	}
	err := readListFile(SessionListPath(root, id), &result)
	if err == nil && (result.Version != ListVersion || result.AggregationVersion != AggregationVersion || result.ProjectID != id) {
		err = errors.New("outdated session list")
	}
	return result, err
}

func writeListFile(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if previous, err := os.ReadFile(path); err == nil && bytes.Equal(previous, raw) {
		return nil
	}
	return store.AtomicWriteFile(path, raw, 0o600)
}

func (builder *Builder) writeListSummaries(projection ProjectCatalog, events []model.EventEnvelope, policy attribution.Policy, version *pricing.Version, bills []billing.Record) error {
	info, err := os.Stat((&store.Store{Root: builder.Root}).ProjectCatalogPath(projection.ProjectID))
	if err != nil {
		return err
	}
	project, err := ProjectSummaryFromCatalog(builder.Root, projection)
	if err != nil {
		return err
	}
	list := SessionList{Version: ListVersion, AggregationVersion: AggregationVersion, ProjectID: projection.ProjectID, Sessions: []SessionSummary{}}
	list.CatalogModifiedNS = info.ModTime().UnixNano()
	for _, session := range projection.Sessions {
		row := SessionSummaryFromCatalog(session)
		row.RunSummaries = append([]RunSummary(nil), row.RunSummaries...)
		// Keep run identifiers for API compatibility, but omit event IDs, cursors
		// and all per-run accounting evidence from the list projection.
		for i, run := range row.RunSummaries {
			row.RunSummaries[i] = RunSummary{ProjectID: run.ProjectID, Provider: run.Provider, SessionID: run.SessionID, RunID: run.RunID, InstructionCount: run.InstructionCount, ModelCallCount: run.ModelCallCount, FirstReceivedAt: run.FirstReceivedAt, LastReceivedAt: run.LastReceivedAt}
		}
		list.Sessions = append(list.Sessions, row)
	}
	if err := writeListFile(SessionListPath(builder.Root, projection.ProjectID), list); err != nil {
		return err
	}
	entry := ProjectListEntry{Version: ListVersion, AggregationVersion: AggregationVersion, Project: project, Periods: projection.Periods}
	entry.CatalogModifiedNS = list.CatalogModifiedNS
	entry.Providers = map[string]ProviderListEntry{}
	for _, session := range projection.Sessions {
		provider := session.Provider
		if _, exists := entry.Providers[provider]; exists {
			continue
		}
		onlyProvider := true
		for _, s := range projection.Sessions {
			if s.Provider != provider {
				onlyProvider = false
				break
			}
		}
		if onlyProvider {
			entry.Providers[provider] = ProviderListEntry{Project: project, Periods: projection.Periods}
			continue
		}
		subset := projection
		subset.Sessions, subset.Inputs = nil, nil
		runs := []RunSummary{}
		for _, s := range projection.Sessions {
			if s.Provider == provider {
				subset.Sessions = append(subset.Sessions, s)
				for _, run := range s.Runs {
					runs = append(runs, run)
					subset.Inputs = append(subset.Inputs, run.Input)
				}
			}
		}
		selected := []model.EventEnvelope{}
		for _, event := range events {
			if event.Source.Provider == provider {
				selected = append(selected, event)
			}
		}
		derived, err := buildSessionDerived(builder.Root, projection.ProjectID, selected, policy, version, bills)
		if err != nil {
			return err
		}
		subset.KnownUsageTokens, subset.UnknownUsage = derived.Usage.KnownSubtotal, derived.Usage.Unknown
		subset.Derivations = DerivedSummary{Usage: derived.Usage, ActiveAttribution: derived.Attribution, ActiveCost: derived.Cost, Operations: derived.Operations, ActivePolicyVersion: policy.Version}
		subset.Periods = mergePeriods(runs)
		applyProjectUsagePeriods(subset.Periods, selected, derived.Usage)
		applyProjectCostPeriods(subset.Periods, selected, derived.Usage, derived.Cost)
		row, err := ProjectSummaryFromCatalog(builder.Root, subset)
		if err != nil {
			return err
		}
		entry.Providers[provider] = ProviderListEntry{Project: row, Periods: subset.Periods}
	}
	return writeListFile(projectListEntryPath(builder.Root, projection.ProjectID), entry)
}

// ListSummariesReady is called by the collector, never by an HTTP list request.
func (builder *Builder) ListSummariesReady(id string) bool {
	full, err := os.Stat((&store.Store{Root: builder.Root}).ProjectCatalogPath(id))
	if err != nil {
		return false
	}
	sessions, err := ReadSessionList(builder.Root, id)
	if err != nil || sessions.CatalogModifiedNS != full.ModTime().UnixNano() {
		return false
	}
	var entry ProjectListEntry
	return readListFile(projectListEntryPath(builder.Root, id), &entry) == nil && entry.Version == ListVersion && entry.AggregationVersion == AggregationVersion && entry.Project.ProjectID == id && entry.CatalogModifiedNS == sessions.CatalogModifiedNS
}

// RefreshProjectList assembles one small, atomic area-wide index. It reads only
// per-project list summaries, not journals or full catalogs.
func (builder *Builder) RefreshProjectList() error {
	ids, err := (&store.Store{Root: builder.Root}).ListProjectIDs()
	if err != nil {
		return err
	}
	list := ProjectList{Version: ListVersion, AggregationVersion: AggregationVersion, Projects: []ProjectListEntry{}}
	for _, id := range ids {
		var entry ProjectListEntry
		if err := readListFile(projectListEntryPath(builder.Root, id), &entry); err != nil {
			return err
		}
		if entry.Project.Instructions == 0 && entry.Project.Sessions == 0 {
			continue
		}
		list.Projects = append(list.Projects, entry)
	}
	return writeListFile(ProjectListPath(builder.Root), list)
}
