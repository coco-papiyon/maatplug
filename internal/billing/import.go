package billing

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"maatplug/internal/model"
)

func ParseJSON(data []byte) ([]Record, error) {
	var document struct {
		SchemaVersion int      `json:"schemaVersion"`
		Records       []Record `json:"records"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	if document.SchemaVersion != SchemaVersion {
		return nil, errors.New("unsupported billing schemaVersion")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("billing JSON must contain one document")
	}
	for _, record := range document.Records {
		if err := record.Validate(); err != nil {
			return nil, err
		}
	}
	return document.Records, nil
}

func ParseCSV(data []byte) ([]Record, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("billing CSV is empty")
	}
	index := map[string]int{}
	for column, name := range rows[0] {
		index[strings.TrimSpace(name)] = column
	}
	for _, required := range []string{"id", "kind", "amount", "currency", "occurred_at", "source_id"} {
		if _, ok := index[required]; !ok {
			return nil, fmt.Errorf("billing CSV missing column %s", required)
		}
	}
	result := []Record{}
	for rowIndex, row := range rows[1:] {
		value := func(name string) string {
			column, ok := index[name]
			if !ok || column >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[column])
		}
		occurredAt, err := time.Parse(time.RFC3339, value("occurred_at"))
		if err != nil {
			return nil, fmt.Errorf("row %d occurred_at: %w", rowIndex+2, err)
		}
		record := Record{SchemaVersion: SchemaVersion, ID: value("id"), Kind: Kind(value("kind")), Amount: value("amount"), Currency: value("currency"), OccurredAt: occurredAt, SourceID: value("source_id"), ExternalID: value("external_id"), Note: value("note")}
		record.Usage, err = parseRefs(value("usage"))
		if err != nil {
			return nil, fmt.Errorf("row %d usage: %w", rowIndex+2, err)
		}
		record.Coverage, err = parseRefs(value("coverage"))
		if err != nil {
			return nil, fmt.Errorf("row %d coverage: %w", rowIndex+2, err)
		}
		if err := record.Validate(); err != nil {
			return nil, fmt.Errorf("row %d: %w", rowIndex+2, err)
		}
		result = append(result, record)
	}
	return result, nil
}

// Entity lists use kind|namespace|value entries separated by semicolons.
func parseRefs(value string) ([]model.EntityRef, error) {
	if value == "" {
		return nil, nil
	}
	result := []model.EntityRef{}
	for _, item := range strings.Split(value, ";") {
		parts := strings.Split(item, "|")
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return nil, errors.New("entity must use kind|namespace|value")
		}
		result = append(result, model.EntityRef{Kind: model.EntityKind(parts[0]), ID: model.NamespacedID{Namespace: parts[1], Value: parts[2]}})
	}
	return result, nil
}
