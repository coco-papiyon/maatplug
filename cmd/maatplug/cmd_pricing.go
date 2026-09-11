package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"maatplug/internal/catalog"
	"maatplug/internal/cost"
	"maatplug/internal/journal"
	"maatplug/internal/model"
	"maatplug/internal/pricing"
	"maatplug/internal/reconcile"
	"maatplug/internal/store"
)

func runPricing(args []string) error {
	if len(args) == 0 {
		return errors.New("pricing requires import, activate, list, calculate, or conflicts")
	}
	switch args[0] {
	case "import", "add":
		return runPricingImport(args[1:])
	case "activate":
		return runPricingActivate(args[1:])
	case "list":
		return runPricingList(args[1:])
	case "calculate":
		return runPricingCalculate(args[1:], false)
	case "conflicts":
		return runPricingCalculate(args[1:], true)
	default:
		return fmt.Errorf("unknown pricing command %q", args[0])
	}
}

func runPricingImport(args []string) error {
	fs := flag.NewFlagSet("pricing import", flag.ContinueOnError)
	path := fs.String("file", "", "pricing version JSON")
	activate := fs.Bool("activate", false, "activate this version and rebuild project catalogs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("pricing import requires --file")
	}
	data, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	version, err := decodePricingVersion(data)
	if err != nil {
		return err
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	versionStore := pricing.VersionStore{Root: s.Root}
	if err := versionStore.Save(version); err != nil {
		return err
	}
	if *activate {
		if err := activatePricingAndRebuild(versionStore, s, version.Version); err != nil {
			return err
		}
	}
	fmt.Printf("pricing version %s imported\n", version.Version)
	return nil
}

func runPricingActivate(args []string) error {
	fs := flag.NewFlagSet("pricing activate", flag.ContinueOnError)
	version := fs.String("version", "", "pricing version")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version == "" {
		return errors.New("pricing activate requires --version")
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	return activatePricingAndRebuild(pricing.VersionStore{Root: s.Root}, s, *version)
}

func runPricingList(args []string) error {
	fs := flag.NewFlagSet("pricing list", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	versionStore := pricing.VersionStore{Root: s.Root}
	versions, err := versionStore.List()
	if err != nil {
		return err
	}
	active, ok, err := versionStore.LoadActive()
	if err != nil {
		return err
	}
	activeID := ""
	if ok {
		activeID = active.Version
	}
	states, err := pricing.ListSourceStates(s.Root)
	if err != nil {
		return err
	}
	return writeJSON(struct {
		Active      string                `json:"active,omitempty"`
		Versions    []pricing.Version     `json:"versions"`
		FetchStates []pricing.SourceState `json:"fetchStates"`
	}{activeID, versions, states})
}

func runPricingCalculate(args []string, conflictsOnly bool) error {
	name := "pricing calculate"
	if conflictsOnly {
		name = "pricing conflicts"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	project := fs.String("project", "", "project ID")
	versionID := fs.String("version", "", "pricing version (active version when omitted)")
	service := fs.String("service", "", "service condition, such as api")
	contract := fs.String("contract", "", "contract condition")
	pricingAtText := fs.String("at", "", "evaluate rates at RFC3339 time instead of each usage time")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" {
		return fmt.Errorf("%s requires --project", name)
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	versionStore := pricing.VersionStore{Root: s.Root}
	var selectedVersion pricing.Version
	if *versionID == "" {
		var ok bool
		selectedVersion, ok, err = versionStore.LoadActive()
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("no active pricing version")
		}
	} else {
		selectedVersion, err = versionStore.Load(*versionID)
		if err != nil {
			return err
		}
	}
	events, err := projectEvents(s.Root, *project)
	if err != nil {
		return err
	}
	usage, err := reconcile.Build(events)
	if err != nil {
		return err
	}
	var pricingAt *time.Time
	if *pricingAtText != "" {
		parsed, err := time.Parse(time.RFC3339, *pricingAtText)
		if err != nil {
			return fmt.Errorf("parse --at: %w", err)
		}
		pricingAt = &parsed
	}
	selected, err := cost.BuildWithOptions(events, usage, &selectedVersion, cost.Options{Service: *service, Contract: *contract, PricingAt: pricingAt})
	if err != nil {
		return err
	}
	if conflictsOnly {
		problems := []cost.Record{}
		for _, record := range selected.Records {
			if record.Status != "calculated" && record.Status != "zero_usage" {
				problems = append(problems, record)
			}
		}
		return writeJSON(struct {
			PricingVersion string        `json:"pricingVersion"`
			MissingCost    bool          `json:"missingCost"`
			Problems       []cost.Record `json:"problems"`
		}{selected.PricingVersion, selected.MissingCost, problems})
	}
	data, err := json.MarshalIndent(selected, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.Root, "projects", *project, "derivations", "cost", selected.CalculationID+".json")
	data = append(data, '\n')
	if err := store.WithFileLock(path, func() error {
		existing, err := os.ReadFile(path)
		if err == nil {
			if bytes.Equal(existing, data) {
				return nil
			}
			return fmt.Errorf("cost calculation %q is immutable", selected.CalculationID)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return store.AtomicWriteFile(path, data, 0o600)
	}); err != nil {
		return err
	}
	return writeJSON(selected)
}

func projectEvents(root, project string) ([]model.EventEnvelope, error) {
	j := journal.New(root)
	paths, err := j.ListProjectStreams(project)
	if err != nil {
		return nil, err
	}
	events := []model.EventEnvelope{}
	for _, path := range paths {
		_, stream, err := j.ReadPath(path)
		if err != nil {
			return nil, err
		}
		events = append(events, stream...)
	}
	return events, nil
}

func decodePricingVersion(data []byte) (pricing.Version, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var version pricing.Version
	if err := decoder.Decode(&version); err != nil {
		return version, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return version, errors.New("pricing file must contain one JSON value")
	}
	return version, version.Validate()
}

func rebuildAllProjectCatalogs(s *store.Store) error {
	projects, err := s.ListProjectIDs()
	if err != nil {
		return err
	}
	builder := catalog.New(s.Root)
	for _, project := range projects {
		if _, err := builder.RebuildProject(project); err != nil {
			return fmt.Errorf("rebuild catalog %s: %w", project, err)
		}
	}
	return nil
}

func activatePricingAndRebuild(versionStore pricing.VersionStore, s *store.Store, version string) error {
	previous, hadPrevious, err := versionStore.LoadActive()
	if err != nil {
		return err
	}
	if err := versionStore.Activate(version); err != nil {
		return err
	}
	if err := rebuildAllProjectCatalogs(s); err != nil {
		var rollbackErr error
		if hadPrevious {
			rollbackErr = versionStore.Activate(previous.Version)
		} else {
			rollbackErr = versionStore.Deactivate()
		}
		if rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("restore active pricing: %w", rollbackErr))
		}
		return err
	}
	return nil
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
