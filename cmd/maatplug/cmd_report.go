package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"maatplug/internal/query"
	"maatplug/internal/store"
)

// runReport prints the same summaries as status, either as a table or as
// JSON for scripting. Both formats use the same query service as the Web API.
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

	summaries, err := summariesFor(query.New(s.Root), *project)
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

func printJSON(summaries []query.ProjectSummary) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(summaries)
}

func printTable(summaries []query.ProjectSummary) error {
	if len(summaries) == 0 {
		fmt.Println("no recorded activity yet")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "PROJECT\tSESSIONS\tRUNS\tINSTRUCTIONS\tKNOWN\tSHARED\tUNATTRIBUTED\tUNCALCULATED\tOPERATIONS")
	for _, sum := range summaries {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%t\t%d\n",
			displayName(sum), sum.Sessions, sum.Runs, sum.Instructions, sum.Usage.KnownSubtotal,
			sum.Usage.SharedUnallocated, sum.Usage.UnattributedTokens, sum.Usage.Unknown || sum.CostMissing, sum.Operations)
	}
	return w.Flush()
}
