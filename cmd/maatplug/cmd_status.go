package main

import (
	"flag"
	"fmt"

	"maatplug/internal/query"
	"maatplug/internal/store"
)

// runStatus prints a one-line-per-project summary, reading the flat-file
// store directly (no dependency on any other maatplug process — see
// docs/design.md section 8).
func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	project := fs.String("project", "", "limit to a single project id")
	if err := fs.Parse(args); err != nil {
		return err
	}

	s, err := store.New()
	if err != nil {
		return err
	}

	summaries, err := summariesFor(query.New(s.Root), *project)
	if err != nil {
		return err
	}
	if len(summaries) == 0 {
		fmt.Println("no recorded activity yet")
		return nil
	}

	for _, sum := range summaries {
		fmt.Printf("%s  sessions=%d  runs=%d  instructions=%d  known=%d  shared=%d  unattributed=%d  uncomputed=%t  operations=%d%s\n",
			displayName(sum), sum.Sessions, sum.Runs, sum.Instructions, sum.Usage.KnownSubtotal, sum.Usage.SharedUnallocated, sum.Usage.UnattributedTokens, sum.Usage.Unknown || sum.CostMissing, sum.Operations,
			abandonedSuffix(sum))
	}
	return nil
}

// abandonedSuffix flags projects that have unmeasured (abandoned) turns, so
// the totals above are never silently mistaken for "fully measured, and it
// came out to this" — see docs/design.md section 6.1.
func abandonedSuffix(sum query.ProjectSummary) string {
	if sum.AbandonedInstructions == nil || *sum.AbandonedInstructions == 0 {
		return ""
	}
	return fmt.Sprintf("  (%d unmeasured/abandoned)", *sum.AbandonedInstructions)
}

func summariesFor(service *query.Service, projectID string) ([]query.ProjectSummary, error) {
	if projectID != "" {
		sum, err := service.Project(projectID, query.Filter{})
		if err != nil {
			return nil, err
		}
		return []query.ProjectSummary{sum}, nil
	}
	return service.ListProjects(query.Filter{})
}

func displayName(sum query.ProjectSummary) string {
	if sum.Name != "" {
		return sum.Name
	}
	return sum.ProjectID
}
