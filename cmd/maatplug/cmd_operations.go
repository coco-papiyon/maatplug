package main

import (
	"flag"
	"fmt"

	"maatplug/internal/operation"
	"maatplug/internal/query"
	"maatplug/internal/store"
)

func runOperations(args []string) error {
	fs := flag.NewFlagSet("operations", flag.ContinueOnError)
	projectID := fs.String("project", "", "project identifier")
	kind := fs.String("kind", "", "operation kind filter")
	status := fs.String("status", "", "final status filter")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *projectID == "" {
		return fmt.Errorf("--project is required")
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	detail, err := query.New(s.Root).ProjectDetail(*projectID, query.Filter{})
	if err != nil {
		return err
	}
	result := detail.Operations
	if *kind != "" || *status != "" {
		filtered := make([]operation.Operation, 0, len(result.Operations))
		for _, item := range result.Operations {
			if *kind != "" && item.Kind != *kind {
				continue
			}
			if *status != "" && item.FinalStatus != *status {
				continue
			}
			filtered = append(filtered, item)
		}
		result.Operations = filtered
	}
	return writeJSON(result)
}
