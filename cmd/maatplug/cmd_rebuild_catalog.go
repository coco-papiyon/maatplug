package main

import (
	"flag"
	"fmt"
	"sort"

	"maatplug/internal/catalog"
	"maatplug/internal/store"
)

func runRebuildCatalog(args []string) error {
	fs := flag.NewFlagSet("rebuild-catalog", flag.ContinueOnError)
	project := fs.String("project", "", "project ID (all projects when omitted)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	projects := []string{*project}
	if *project == "" {
		projects, err = s.ListProjectIDs()
		if err != nil {
			return err
		}
	}
	sort.Strings(projects)
	builder := catalog.New(s.Root)
	for _, projectID := range projects {
		projection, err := builder.RebuildProject(projectID)
		if err != nil {
			return err
		}
		fmt.Printf("%s: %d sessions, %d streams\n", projectID, len(projection.Sessions), len(projection.Inputs))
	}
	return nil
}
