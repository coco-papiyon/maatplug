package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"maatplug/internal/aggregate"
	"maatplug/internal/store"
)

// runReport prints the same summaries as status, either as a table or as
// JSON for scripting (and, later, a starting point for whatever
// internal/aggregate's TypeScript port emits for the VSCode extension).
func runReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	project := fs.String("project", "", "limit to a single project id")
	format := fs.String("format", "table", "output format: table|json")
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

	switch *format {
	case "json":
		return printJSON(summaries)
	case "table":
		return printTable(summaries)
	default:
		return fmt.Errorf("unknown --format %q (want \"table\" or \"json\")", *format)
	}
}

func printJSON(summaries []aggregate.ProjectSummary) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(summaries)
}

func printTable(summaries []aggregate.ProjectSummary) error {
	if len(summaries) == 0 {
		fmt.Println("no recorded activity yet")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "PROJECT\tSESSIONS\tINSTRUCTIONS\tTOKENS\t+LINES\t-LINES\tABANDONED")
	for _, sum := range summaries {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\n",
			displayName(sum), sum.Sessions, sum.Instructions, sum.TotalTokens, sum.LinesAdded, sum.LinesRemoved,
			sum.AbandonedInstructions)
	}
	return w.Flush()
}
