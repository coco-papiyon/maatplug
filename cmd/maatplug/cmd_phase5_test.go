package main

import (
	"os"
	"path/filepath"
	"testing"

	"maatplug/internal/billing"
	"maatplug/internal/pricing"
)

func TestPricingImportActivateAndBillingImport(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MAATPLUG_HOME", root)
	files := t.TempDir()
	pricingPath := filepath.Join(files, "pricing.json")
	pricingJSON := `{"schemaVersion":1,"version":"manual-v1","createdAt":"2026-02-01T00:00:00Z","reason":"test fixture","sources":[{"id":"manual","kind":"manual","retrievedAt":"2026-02-01T00:00:00Z"}],"rates":[{"id":"rate-1","provider":"codex","modelPattern":"model","usageClass":"input_uncached","currency":"USD","amount":"2","unitScale":1000000,"effectiveFrom":"2026-01-01T00:00:00Z","sourceId":"manual"}]}`
	if err := os.WriteFile(pricingPath, []byte(pricingJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runPricing([]string{"import", "--file", pricingPath, "--activate"}); err != nil {
		t.Fatal(err)
	}
	active, ok, err := (pricing.VersionStore{Root: root}).LoadActive()
	if err != nil || !ok || active.Version != "manual-v1" {
		t.Fatalf("active = %+v, %v, %v", active, ok, err)
	}

	billingPath := filepath.Join(files, "billing.json")
	billingJSON := `{"schemaVersion":1,"records":[{"schemaVersion":1,"id":"invoice-1","kind":"billed","amount":"12.34","currency":"USD","occurredAt":"2026-02-02T00:00:00Z","sourceId":"manual-invoice"}]}`
	if err := os.WriteFile(billingPath, []byte(billingJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runBilling([]string{"import", "--file", billingPath}); err != nil {
		t.Fatal(err)
	}
	records, err := (billing.Store{Root: root}).List()
	if err != nil || len(records) != 1 || records[0].Kind != billing.KindBilled {
		t.Fatalf("records = %+v, %v", records, err)
	}
	conversionPath := filepath.Join(files, "conversion.json")
	conversionJSON := `{"version":"fx-v1","original":{"amount":"12.34","currency":"USD"},"targetCurrency":"JPY","rate":"150","rateAt":"2026-02-02T00:00:00Z","method":"daily-close","sourceId":"fx-source","scale":2}`
	if err := os.WriteFile(conversionPath, []byte(conversionJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runBilling([]string{"convert", "--file", conversionPath}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "billing", "derivations", "conversions", "fx-v1.json")); err != nil {
		t.Fatal(err)
	}
}

func TestDecodePricingVersionRejectsUnknownField(t *testing.T) {
	if _, err := decodePricingVersion([]byte(`{"schemaVersion":1,"version":"v1","unknown":true}`)); err == nil {
		t.Fatal("accepted unknown pricing field")
	}
}
