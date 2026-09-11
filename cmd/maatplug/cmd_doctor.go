package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"

	"maatplug/internal/query"
	"maatplug/internal/store"
)

func runDoctor(args []string) error {
	if len(args) == 0 || args[0] != "collection" {
		return errors.New("usage: maatplug doctor collection [--json] [--record-baseline]")
	}
	fs := flag.NewFlagSet("doctor collection", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print machine-readable diagnostics")
	recordBaseline := fs.Bool("record-baseline", false, "save a flat-file performance baseline")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	service := query.New(s.Root)
	report, err := service.Doctor()
	if err != nil {
		return err
	}
	if *recordBaseline {
		report.Performance, err = service.RecordBaseline()
		if err != nil {
			return err
		}
	}
	if *asJSON {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
	} else {
		collection := report.Collection
		fmt.Printf("collection: pending=%d processing=%d failed=%d ingest-errors=%d streams=%d events=%d\n", collection.Inbox.Pending, collection.Inbox.Processing, collection.Inbox.Failed, collection.Inbox.IngestErrors, collection.Streams, collection.Events)
		fmt.Printf("worker: known=%t stale=%t status=%s last-success=%v reason=%s\n", report.Worker.Known, report.Worker.Stale, report.Worker.State.Status, report.Worker.State.LastSuccess, report.Worker.Reason)
		fmt.Printf("pricing: active=%s missing=%t stale-sources=%v\n", report.Pricing.ActiveVersion, report.Pricing.Missing, report.Pricing.StaleSources)
		fmt.Printf("coverage: events=%d parse-errors=%d missing-paths=%d\n", report.Coverage.Events, report.Coverage.ParseErrors, len(report.Coverage.MissingPaths))
		fmt.Printf("performance: files=%d bytes=%d list=%dms detail=%dms period=%dms catch-up=%dms baseline=%s\n", report.Performance.FileCount, report.Performance.TotalBytes, report.Performance.ListMillis, report.Performance.DetailMillis, report.Performance.PeriodMillis, report.Performance.WorkerCatchupMillis, report.Performance.BaselinePath)
		for _, issue := range collection.JournalErrors {
			fmt.Println("journal:", issue)
		}
		for _, issue := range collection.ContentErrors {
			fmt.Println("content:", issue)
		}
		for _, issue := range collection.CatalogIssues {
			fmt.Println("catalog:", issue)
		}
	}
	if !report.Healthy() {
		return errors.New("collection diagnostics found problems")
	}
	return nil
}
