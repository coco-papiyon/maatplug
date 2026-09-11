// Package billing imports provider-independent spend facts. It keeps bills,
// estimates, payments, credits, refunds, and allocations as distinct records.
package billing

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"maatplug/internal/model"
)

const SchemaVersion = 1

type Kind string

const (
	KindProviderEstimate   Kind = "provider_estimate"
	KindCalculatedEstimate Kind = "calculated_estimate"
	KindBilled             Kind = "billed"
	KindSubscription       Kind = "subscription"
	KindPayment            Kind = "payment"
	KindCredit             Kind = "credit"
	KindRefund             Kind = "refund"
	KindAdjustment         Kind = "adjustment"
)

type Record struct {
	SchemaVersion int               `json:"schemaVersion"`
	ID            string            `json:"id"`
	Kind          Kind              `json:"kind"`
	Amount        string            `json:"amount"`
	Currency      string            `json:"currency"`
	OccurredAt    time.Time         `json:"occurredAt"`
	Usage         []model.EntityRef `json:"usage,omitempty"`
	Coverage      []model.EntityRef `json:"coverage,omitempty"`
	SourceID      string            `json:"sourceId"`
	ExternalID    string            `json:"externalId,omitempty"`
	Note          string            `json:"note,omitempty"`
}

func (record Record) Validate() error {
	if record.SchemaVersion != SchemaVersion || !safeIdentifier(record.ID) || !validKind(record.Kind) || !validDecimal(record.Amount) || record.Currency == "" || record.OccurredAt.IsZero() || record.SourceID == "" {
		return errors.New("invalid billing record required fields")
	}
	for index, ref := range append(append([]model.EntityRef(nil), record.Usage...), record.Coverage...) {
		if ref.Kind == "" || ref.ID.Namespace == "" || ref.ID.Value == "" {
			return fmt.Errorf("invalid entity reference %d", index)
		}
	}
	return nil
}

func validKind(kind Kind) bool {
	switch kind {
	case KindProviderEstimate, KindCalculatedEstimate, KindBilled, KindSubscription, KindPayment, KindCredit, KindRefund, KindAdjustment:
		return true
	default:
		return false
	}
}

type CategoryTotal struct {
	Kind     Kind   `json:"kind"`
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

type Summary struct {
	Totals []CategoryTotal `json:"totals"`
}

func Summarize(records []Record) (Summary, error) {
	values := map[string][]string{}
	for _, record := range records {
		if err := record.Validate(); err != nil {
			return Summary{}, err
		}
		key := string(record.Kind) + "\x00" + record.Currency
		values[key] = append(values[key], record.Amount)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := Summary{Totals: []CategoryTotal{}}
	for _, key := range keys {
		parts := strings.SplitN(key, "\x00", 2)
		amount, err := addDecimals(values[key]...)
		if err != nil {
			return result, err
		}
		result.Totals = append(result.Totals, CategoryTotal{Kind: Kind(parts[0]), Currency: parts[1], Amount: amount})
	}
	return result, nil
}

func MarshalDocument(records []Record) ([]byte, error) {
	return json.MarshalIndent(struct {
		SchemaVersion int      `json:"schemaVersion"`
		Records       []Record `json:"records"`
	}{SchemaVersion, records}, "", "  ")
}
