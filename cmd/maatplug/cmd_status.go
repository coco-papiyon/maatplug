package main

import (
	"flag"
	"fmt"

	"maatplug/internal/aggregate"
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

	summaries, err := summariesFor(s, *project)
	if err != nil {
		return err
	}
	if len(summaries) == 0 {
		fmt.Println("no recorded activity yet")
		return nil
	}

	for _, sum := range summaries {
		fmt.Printf("%s  sessions=%d  instructions=%d  tokens=%d  +%d/-%d lines%s\n",
			displayName(sum), sum.Sessions, sum.Instructions, sum.TotalTokens, sum.LinesAdded, sum.LinesRemoved,
			abandonedSuffix(sum))
	}
	return nil
}

// abandonedSuffix flags projects that have unmeasured (abandoned) turns, so
// the totals above are never silently mistaken for "fully measured, and it
// came out to this" — see docs/design.md section 6.1.
func abandonedSuffix(sum aggregate.ProjectSummary) string {
	if sum.AbandonedInstructions == 0 {
		return ""
	}
	return fmt.Sprintf("  (%d unmeasured/abandoned)", sum.AbandonedInstructions)
}

func summariesFor(s *store.Store, projectID string) ([]aggregate.ProjectSummary, error) {
	if projectID != "" {
		sum, err := aggregate.Project(s, projectID, aggregate.Filter{})
		if err != nil {
			return nil, err
		}
		return []aggregate.ProjectSummary{sum}, nil
	}
	return aggregate.All(s, aggregate.Filter{})
}

func displayName(sum aggregate.ProjectSummary) string {
	if sum.Name != "" {
		return sum.Name
	}
	return sum.ProjectID
}
