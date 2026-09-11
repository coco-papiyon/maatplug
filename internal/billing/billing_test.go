package billing

import (
	"testing"
	"time"

	"maatplug/internal/model"
)

func TestJSONCSVImportAndStoreKeepKindsSeparate(t *testing.T) {
	now := "2026-02-03T04:05:06Z"
	jsonData := []byte(`{"schemaVersion":1,"records":[{"schemaVersion":1,"id":"bill-1","kind":"billed","amount":"10.25","currency":"USD","occurredAt":"` + now + `","sourceId":"invoice"},{"schemaVersion":1,"id":"refund-1","kind":"refund","amount":"2.00","currency":"USD","occurredAt":"` + now + `","sourceId":"invoice"}]}`)
	records, err := ParseJSON(jsonData)
	if err != nil {
		t.Fatal(err)
	}
	csvData := []byte("id,kind,amount,currency,occurred_at,source_id,usage\npayment-1,payment,10.25,USD," + now + ",bank,usage|fixture/usage|u1\n")
	csvRecords, err := ParseCSV(csvData)
	if err != nil {
		t.Fatal(err)
	}
	records = append(records, csvRecords...)
	store := Store{Root: t.TempDir()}
	if err := store.SaveAll(records); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.List()
	if err != nil || len(loaded) != 3 {
		t.Fatalf("loaded = %+v, %v", loaded, err)
	}
	summary, err := Summarize(loaded)
	if err != nil || len(summary.Totals) != 3 {
		t.Fatalf("summary = %+v, %v", summary, err)
	}
	records[0].Amount = "99"
	if err := store.Save(records[0]); err == nil {
		t.Fatal("overwrote immutable billing record")
	}
}

func TestConversionAndAllocationPreserveEvidenceAndRemainder(t *testing.T) {
	now := time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC)
	converted, err := Convert("fx-v1", Money{Amount: "10", Currency: "USD"}, "JPY", "150.5", now, "daily_close", "fx-source", 2)
	if err != nil || converted.Converted.Amount != "1505" || converted.Rate != "150.5" {
		t.Fatalf("conversion = %+v, %v", converted, err)
	}
	a := model.EntityRef{Kind: model.EntityInstruction, ID: model.NamespacedID{Namespace: "fixture/instruction", Value: "a"}}
	b := model.EntityRef{Kind: model.EntityInstruction, ID: model.NamespacedID{Namespace: "fixture/instruction", Value: "b"}}
	allocation, err := Allocate("allocation-v1", "bill-1", Money{Amount: "0.01", Currency: "USD"}, "equal", 2, []AllocationShare{{Target: a, Units: 1, TotalUnits: 3}, {Target: b, Units: 1, TotalUnits: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if allocation.Allocated[0].Money.Amount != "0" || allocation.Allocated[1].Money.Amount != "0" || allocation.Unallocated.Amount != "0.01" {
		t.Fatalf("allocation = %+v", allocation)
	}
	store := DerivationStore{Root: t.TempDir()}
	if err := store.SaveConversion(converted); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAllocation(allocation); err != nil {
		t.Fatal(err)
	}
	converted.Rate = "999"
	if err := store.SaveConversion(converted); err == nil {
		t.Fatal("overwrote immutable conversion")
	}
}

func TestConversionRejectsNonCanonicalDecimalRate(t *testing.T) {
	now := time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC)
	for _, rate := range []string{"1/2", "1.5e2", "+2", "01"} {
		if _, err := Convert("fx-v1", Money{Amount: "10", Currency: "USD"}, "JPY", rate, now, "daily_close", "fx-source", 2); err == nil {
			t.Errorf("rate %q was accepted", rate)
		}
	}
	invalid := Conversion{Version: "fx-v1", Original: Money{Amount: "10", Currency: "USD"}, Converted: Money{Amount: "5", Currency: "JPY"}, Rate: "1/2", RateAt: now, Method: "manual", SourceID: "test"}
	if err := (DerivationStore{Root: t.TempDir()}).SaveConversion(invalid); err == nil {
		t.Fatal("noncanonical persisted conversion rate was accepted")
	}
}
