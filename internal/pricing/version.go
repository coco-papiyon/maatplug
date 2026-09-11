package pricing

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

const VersionSchema = 1

var decimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

type SourceDocument struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	URL         string    `json:"url,omitempty"`
	RetrievedAt time.Time `json:"retrievedAt"`
	Digest      string    `json:"digest,omitempty"`
}

type Rate struct {
	ID            string            `json:"id"`
	Provider      string            `json:"provider"`
	Service       string            `json:"service,omitempty"`
	Contract      string            `json:"contract,omitempty"`
	ModelPattern  string            `json:"modelPattern"`
	UsageClass    string            `json:"usageClass"`
	Currency      string            `json:"currency"`
	Amount        string            `json:"amount"`
	UnitScale     int64             `json:"unitScale"`
	EffectiveFrom time.Time         `json:"effectiveFrom"`
	EffectiveTo   *time.Time        `json:"effectiveTo,omitempty"`
	MinQuantity   *int64            `json:"minQuantity,omitempty"`
	MaxQuantity   *int64            `json:"maxQuantity,omitempty"`
	Attributes    map[string]string `json:"attributes,omitempty"`
	Priority      int               `json:"priority,omitempty"`
	SourceID      string            `json:"sourceId"`
}

type Version struct {
	SchemaVersion int              `json:"schemaVersion"`
	Version       string           `json:"version"`
	BasedOn       string           `json:"basedOn,omitempty"`
	CreatedAt     time.Time        `json:"createdAt"`
	Reason        string           `json:"reason"`
	Sources       []SourceDocument `json:"sources"`
	Rates         []Rate           `json:"rates"`
}

func (version Version) Validate() error {
	if version.SchemaVersion != VersionSchema || !safeIdentifier(version.Version) || version.BasedOn != "" && !safeIdentifier(version.BasedOn) || version.CreatedAt.IsZero() || strings.TrimSpace(version.Reason) == "" || len(version.Sources) == 0 || len(version.Rates) == 0 {
		return errors.New("schemaVersion, safe version, createdAt, and reason are required")
	}
	seen := map[string]bool{}
	sources := map[string]bool{}
	for index, source := range version.Sources {
		if !safeIdentifier(source.ID) || source.Kind == "" || source.RetrievedAt.IsZero() || sources[source.ID] {
			return fmt.Errorf("sources[%d] is invalid or duplicated", index)
		}
		sources[source.ID] = true
	}
	for index, rate := range version.Rates {
		if !safeIdentifier(rate.ID) || seen[rate.ID] || rate.Provider == "" || rate.ModelPattern == "" || rate.UsageClass == "" || rate.Currency == "" || !decimalPattern.MatchString(rate.Amount) || strings.HasPrefix(rate.Amount, "-") || rate.UnitScale <= 0 || rate.EffectiveFrom.IsZero() || rate.SourceID == "" {
			return fmt.Errorf("rates[%d] has invalid required fields", index)
		}
		if _, err := path.Match(rate.ModelPattern, rate.ModelPattern); err != nil {
			return fmt.Errorf("rates[%d].modelPattern: %w", index, err)
		}
		if rate.EffectiveTo != nil && !rate.EffectiveTo.After(rate.EffectiveFrom) {
			return fmt.Errorf("rates[%d] has an invalid effective interval", index)
		}
		if rate.MinQuantity != nil && *rate.MinQuantity < 0 || rate.MaxQuantity != nil && *rate.MaxQuantity < 0 || rate.MinQuantity != nil && rate.MaxQuantity != nil && *rate.MinQuantity > *rate.MaxQuantity {
			return fmt.Errorf("rates[%d] has invalid quantity thresholds", index)
		}
		if !sources[rate.SourceID] {
			return fmt.Errorf("rates[%d] references unknown source %q", index, rate.SourceID)
		}
		seen[rate.ID] = true
	}
	return nil
}

func (version *Version) Sort() {
	sort.Slice(version.Sources, func(i, j int) bool { return version.Sources[i].ID < version.Sources[j].ID })
	sort.Slice(version.Rates, func(i, j int) bool { return version.Rates[i].ID < version.Rates[j].ID })
}

func safeIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 160 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}
