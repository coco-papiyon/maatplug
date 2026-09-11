package main

import (
	"errors"
	"flag"
	"time"

	"maatplug/internal/model"
	"maatplug/internal/query"
	"maatplug/internal/store"
)

func runQuery(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: maatplug query projects|project|work|periods|sessions|session|run|instruction|call [flags]")
	}
	fs := flag.NewFlagSet("query "+args[0], flag.ContinueOnError)
	project := fs.String("project", "", "project identifier")
	provider := fs.String("provider", "", "provider filter or identifier")
	session := fs.String("session", "", "session identifier")
	run := fs.String("run", "", "run identifier")
	namespace := fs.String("namespace", "", "entity ID namespace")
	id := fs.String("id", "", "entity identifier")
	sinceText := fs.String("since", "", "inclusive RFC3339 lower bound")
	untilText := fs.String("until", "", "exclusive RFC3339 upper bound")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	filter := query.Filter{Provider: *provider}
	var err error
	if *sinceText != "" {
		filter.Since, err = time.Parse(time.RFC3339, *sinceText)
		if err != nil {
			return err
		}
	}
	if *untilText != "" {
		filter.Until, err = time.Parse(time.RFC3339, *untilText)
		if err != nil {
			return err
		}
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	service := query.New(s.Root)
	switch args[0] {
	case "projects":
		value, err := service.ListProjects(filter)
		if err != nil {
			return err
		}
		return writeJSON(value)
	case "project", "work":
		if *project == "" {
			return errors.New("--project is required")
		}
		value, err := service.ProjectDetail(*project, filter)
		if err != nil {
			return err
		}
		return writeJSON(value)
	case "periods":
		if *project == "" {
			return errors.New("--project is required")
		}
		value, err := service.Periods(*project)
		if err != nil {
			return err
		}
		return writeJSON(value)
	case "sessions":
		if *project == "" {
			return errors.New("--project is required")
		}
		value, err := service.Sessions(*project, filter)
		if err != nil {
			return err
		}
		return writeJSON(value)
	case "session":
		if *project == "" || *provider == "" || *session == "" {
			return errors.New("--project, --provider, and --session are required")
		}
		value, err := service.SessionDetail(*project, *provider, *session, filter)
		if err != nil {
			return err
		}
		return writeJSON(value)
	case "run":
		if *project == "" || *provider == "" || *session == "" || *run == "" {
			return errors.New("--project, --provider, --session, and --run are required")
		}
		value, err := service.RunDetail(*project, *provider, *session, *run)
		if err != nil {
			return err
		}
		return writeJSON(value)
	case "instruction", "call":
		if *project == "" || *namespace == "" || *id == "" {
			return errors.New("--project, --namespace, and --id are required")
		}
		kind := model.EntityInstruction
		if args[0] == "call" {
			kind = model.EntityModelCall
		}
		value, err := service.Entity(*project, kind, *namespace, *id, filter)
		if err != nil {
			return err
		}
		return writeJSON(value)
	default:
		return errors.New("usage: maatplug query projects|project|work|periods|sessions|session|run|instruction|call [flags]")
	}
}
