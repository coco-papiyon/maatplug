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
	"strings"
	"time"

	"maatplug/internal/billing"
	"maatplug/internal/store"
)

func runBilling(args []string) error {
	if len(args) == 0 {
		return errors.New("billing requires import, list, convert, or allocate")
	}
	switch args[0] {
	case "import":
		return runBillingImport(args[1:])
	case "list":
		return runBillingList(args[1:])
	case "convert":
		return runBillingConvert(args[1:])
	case "allocate":
		return runBillingAllocate(args[1:])
	default:
		return fmt.Errorf("unknown billing command %q", args[0])
	}
}

func runBillingConvert(args []string) error {
	fs := flag.NewFlagSet("billing convert", flag.ContinueOnError)
	path := fs.String("file", "", "conversion request JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("billing convert requires --file")
	}
	var request struct {
		Version        string        `json:"version"`
		Original       billing.Money `json:"original"`
		TargetCurrency string        `json:"targetCurrency"`
		Rate           string        `json:"rate"`
		RateAt         time.Time     `json:"rateAt"`
		Method         string        `json:"method"`
		SourceID       string        `json:"sourceId"`
		Scale          int           `json:"scale"`
	}
	if err := decodeJSONFile(*path, &request); err != nil {
		return err
	}
	result, err := billing.Convert(request.Version, request.Original, request.TargetCurrency, request.Rate, request.RateAt, request.Method, request.SourceID, request.Scale)
	if err != nil {
		return err
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	if err := (billing.DerivationStore{Root: s.Root}).SaveConversion(result); err != nil {
		return err
	}
	return writeJSON(result)
}

func runBillingAllocate(args []string) error {
	fs := flag.NewFlagSet("billing allocate", flag.ContinueOnError)
	path := fs.String("file", "", "allocation request JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("billing allocate requires --file")
	}
	var request struct {
		Version  string                    `json:"version"`
		SourceID string                    `json:"sourceId"`
		Original billing.Money             `json:"original"`
		Method   string                    `json:"method"`
		Scale    int                       `json:"scale"`
		Shares   []billing.AllocationShare `json:"shares"`
	}
	if err := decodeJSONFile(*path, &request); err != nil {
		return err
	}
	result, err := billing.Allocate(request.Version, request.SourceID, request.Original, request.Method, request.Scale, request.Shares)
	if err != nil {
		return err
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	if err := (billing.DerivationStore{Root: s.Root}).SaveAllocation(result); err != nil {
		return err
	}
	return writeJSON(result)
}

func decodeJSONFile(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("file must contain one JSON value")
	}
	return nil
}

func runBillingImport(args []string) error {
	fs := flag.NewFlagSet("billing import", flag.ContinueOnError)
	path := fs.String("file", "", "billing JSON or CSV file")
	format := fs.String("format", "", "json or csv (inferred from extension when omitted)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("billing import requires --file")
	}
	data, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	selectedFormat := strings.ToLower(*format)
	if selectedFormat == "" {
		selectedFormat = strings.TrimPrefix(strings.ToLower(filepath.Ext(*path)), ".")
	}
	var records []billing.Record
	switch selectedFormat {
	case "json":
		records, err = billing.ParseJSON(data)
	case "csv":
		records, err = billing.ParseCSV(data)
	default:
		return fmt.Errorf("unknown billing format %q", selectedFormat)
	}
	if err != nil {
		return err
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	if err := (billing.Store{Root: s.Root}).SaveAll(records); err != nil {
		return err
	}
	fmt.Printf("imported %d billing records\n", len(records))
	return nil
}

func runBillingList(args []string) error {
	fs := flag.NewFlagSet("billing list", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	records, err := (billing.Store{Root: s.Root}).List()
	if err != nil {
		return err
	}
	summary, err := billing.Summarize(records)
	if err != nil {
		return err
	}
	return writeJSON(struct {
		Records []billing.Record `json:"records"`
		Summary billing.Summary  `json:"summary"`
	}{records, summary})
}
