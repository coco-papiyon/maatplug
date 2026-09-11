package pricing

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

func MissingRateFields(models []ModelPricing, provider string) []string {
	missing := []string{}
	for _, item := range models {
		if item.InputPerMillion <= 0 {
			missing = append(missing, fmt.Sprintf("%s:input", item.Model))
		}
		if item.OutputPerMillion <= 0 {
			missing = append(missing, fmt.Sprintf("%s:output", item.Model))
		}
		if provider == "claude" && item.CachedInputPerMillion <= 0 {
			missing = append(missing, fmt.Sprintf("%s:cache_read", item.Model))
		}
		if provider == "claude" && item.CacheWritePerMillion <= 0 {
			missing = append(missing, fmt.Sprintf("%s:cache_write", item.Model))
		}
		for _, issue := range item.AcquisitionIssues {
			missing = append(missing, fmt.Sprintf("%s:%s", item.Model, issue))
		}
	}
	return missing
}

// RatesFromModels adapts the existing provider page parsers to the versioned
// rate contract. The scraped values remain reference API rates; they do not
// claim to be an actual subscription charge.
func RatesFromModels(models []ModelPricing, sourceID string, effectiveFrom time.Time) []Rate {
	result := []Rate{}
	for _, item := range models {
		classes := []struct {
			name   string
			amount float64
		}{
			{"input_uncached", item.InputPerMillion},
			{"cache_read_input", item.CachedInputPerMillion},
			{"cache_write_input", item.CacheWritePerMillion},
			{"output", item.OutputPerMillion},
		}
		for _, class := range classes {
			// A zero scraped value means that the source did not publish this
			// class. Omitting it keeps the cost unknown rather than declaring it free.
			if class.amount <= 0 {
				continue
			}
			result = append(result, Rate{
				ID: rateID(item.Provider, item.Model, class.name, sourceID, effectiveFrom), Provider: item.Provider,
				Service: "api", ModelPattern: item.Model, UsageClass: class.name,
				Currency: "USD", Amount: strconv.FormatFloat(class.amount, 'f', -1, 64), UnitScale: 1_000_000,
				EffectiveFrom: effectiveFrom, SourceID: sourceID,
			})
		}
	}
	return result
}

func rateID(provider, model, class, source string, effectiveFrom time.Time) string {
	sum := sha256.Sum256([]byte(provider + "\x00" + model + "\x00" + class + "\x00" + source + "\x00" + effectiveFrom.Format(time.RFC3339Nano)))
	return "rate-" + hex.EncodeToString(sum[:])[:20]
}
