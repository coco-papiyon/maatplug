package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"maatplug/internal/attribution"
	"maatplug/internal/catalog"
	"maatplug/internal/store"
)

func runAttribution(args []string) error {
	if len(args) == 0 {
		return errors.New("attribution requires apply, activate, or list")
	}
	switch args[0] {
	case "apply":
		return runAttributionApply(args[1:])
	case "activate":
		return runAttributionActivate(args[1:])
	case "list":
		return runAttributionList(args[1:])
	default:
		return fmt.Errorf("unknown attribution command %q", args[0])
	}
}

func runAttributionApply(args []string) error {
	fs := flag.NewFlagSet("attribution apply", flag.ContinueOnError)
	project := fs.String("project", "", "project ID")
	path := fs.String("file", "", "attribution policy JSON file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" || *path == "" {
		return errors.New("attribution apply requires --project and --file")
	}
	data, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	policy, err := decodeAttributionPolicy(data)
	if err != nil {
		return fmt.Errorf("decode attribution policy: %w", err)
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	policyStore := attribution.PolicyStore{Root: s.Root}
	if policy.BasedOn != "" && policy.BasedOn != attribution.DefaultPolicyVersion {
		if _, err := policyStore.Load(*project, policy.BasedOn); err != nil {
			return fmt.Errorf("load basedOn policy %s: %w", policy.BasedOn, err)
		}
	}
	if err := policyStore.Save(*project, policy); err != nil {
		return err
	}
	projection, err := activateAttributionAndRebuild(policyStore, catalog.New(s.Root), *project, policy.Version)
	if err != nil {
		return err
	}
	fmt.Printf("%s: active attribution policy %s, work total %d tokens\n", *project, projection.Derivations.ActivePolicyVersion, projection.Derivations.ActiveAttribution.WorkTotalTokens)
	return nil
}

func runAttributionActivate(args []string) error {
	fs := flag.NewFlagSet("attribution activate", flag.ContinueOnError)
	project := fs.String("project", "", "project ID")
	version := fs.String("version", "", "saved policy version, or automatic-v1")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" || *version == "" {
		return errors.New("attribution activate requires --project and --version")
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	_, err = activateAttributionAndRebuild(attribution.PolicyStore{Root: s.Root}, catalog.New(s.Root), *project, *version)
	return err
}

func activateAttributionAndRebuild(policyStore attribution.PolicyStore, builder *catalog.Builder, project, version string) (catalog.ProjectCatalog, error) {
	previous, err := policyStore.LoadActive(project)
	if err != nil {
		return catalog.ProjectCatalog{}, err
	}
	if err := policyStore.Activate(project, version); err != nil {
		return catalog.ProjectCatalog{}, err
	}
	projection, err := builder.RebuildProject(project)
	if err == nil {
		return projection, nil
	}
	if rollbackErr := policyStore.Activate(project, previous.Version); rollbackErr != nil {
		return catalog.ProjectCatalog{}, errors.Join(err, fmt.Errorf("restore attribution policy %s: %w", previous.Version, rollbackErr))
	}
	return catalog.ProjectCatalog{}, err
}

func runAttributionList(args []string) error {
	fs := flag.NewFlagSet("attribution list", flag.ContinueOnError)
	project := fs.String("project", "", "project ID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" {
		return errors.New("attribution list requires --project")
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	projection, err := catalog.New(s.Root).RebuildProject(*project)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(struct {
		Active   string                       `json:"active"`
		Versions []catalog.AttributionVersion `json:"versions"`
	}{projection.Derivations.ActivePolicyVersion, projection.Derivations.AttributionVersions})
}

func decodeAttributionPolicy(data []byte) (attribution.Policy, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var policy attribution.Policy
	if err := decoder.Decode(&policy); err != nil {
		return policy, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return policy, errors.New("multiple JSON values are not allowed")
		}
		return policy, err
	}
	return policy, policy.Validate()
}
