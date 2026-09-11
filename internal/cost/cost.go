// Package cost calculates reproducible usage estimates from a selected,
// immutable pricing version. Missing rates and conditions never become zero.
package cost

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"maatplug/internal/model"
	"maatplug/internal/pricing"
	"maatplug/internal/reconcile"
)

const CalculationVersion = "usage-cost-v1"

type Rounding struct {
	Mode  string `json:"mode"`
	Scale int    `json:"scale"`
}

var DefaultRounding = Rounding{Mode: "half_up", Scale: 12}

type Line struct {
	UsageClass string                   `json:"usageClass"`
	Quantity   int64                    `json:"quantity"`
	RateID     string                   `json:"rateId,omitempty"`
	Currency   string                   `json:"currency,omitempty"`
	Amount     *string                  `json:"amount,omitempty"`
	Resolution pricing.ResolutionStatus `json:"resolution"`
	Reason     string                   `json:"reason"`
}

type Record struct {
	Usage     model.EntityRef `json:"usage"`
	Call      model.EntityRef `json:"call,omitempty"`
	Provider  string          `json:"provider,omitempty"`
	Model     string          `json:"model,omitempty"`
	At        time.Time       `json:"at"`
	PricingAt time.Time       `json:"pricingAt"`
	Lines     []Line          `json:"lines"`
	Currency  string          `json:"currency,omitempty"`
	Amount    *string         `json:"amount,omitempty"`
	Status    string          `json:"status"`
	Reason    string          `json:"reason,omitempty"`
}

type CurrencyTotal struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

type Result struct {
	CalculationVersion string               `json:"calculationVersion"`
	CalculationID      string               `json:"calculationId"`
	PricingVersion     string               `json:"pricingVersion,omitempty"`
	Rounding           Rounding             `json:"rounding"`
	Records            []Record             `json:"records"`
	KnownTotals        []CurrencyTotal      `json:"knownTotals"`
	MissingCost        bool                 `json:"missingCost"`
	InputEventIDs      []model.NamespacedID `json:"inputEventIds"`
	Conditions         Options              `json:"conditions"`
}

type Options struct {
	Service    string            `json:"service,omitempty"`
	Contract   string            `json:"contract,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
	PricingAt  *time.Time        `json:"pricingAt,omitempty"`
}

type callInfo struct {
	ref      model.EntityRef
	provider string
	model    string
	at       time.Time
}

func Build(events []model.EventEnvelope, usage reconcile.Result, version *pricing.Version) (Result, error) {
	return BuildWithOptions(events, usage, version, Options{})
}

func BuildWithOptions(events []model.EventEnvelope, usage reconcile.Result, version *pricing.Version, options Options) (Result, error) {
	result := Result{CalculationVersion: CalculationVersion, Rounding: DefaultRounding, Records: []Record{}, KnownTotals: []CurrencyTotal{}, MissingCost: usage.Unknown, InputEventIDs: append([]model.NamespacedID(nil), usage.InputEventIDs...), Conditions: options}
	if version == nil {
		result.MissingCost = true
	} else {
		if err := version.Validate(); err != nil {
			return result, err
		}
		result.PricingVersion = version.Version
	}
	result.CalculationID = calculationID(result.PricingVersion, options, result.InputEventIDs)
	calls := map[string]callInfo{}
	eventTimes := map[string]time.Time{}
	for _, event := range events {
		eventTimes[idKey(event.EventID)] = observedAt(event)
		if event.EventType != model.EventModelCall {
			continue
		}
		decoded, err := event.DecodePayload()
		if err != nil {
			return result, err
		}
		payload := decoded.(*model.ModelCallPayload)
		actual := payload.ActualModel
		if actual == "" {
			actual = payload.RequestedModel
		}
		calls[entityKey(payload.Call)] = callInfo{ref: payload.Call, provider: event.Source.Provider, model: actual, at: observedAt(event)}
	}
	totals := map[string]*big.Rat{}
	for _, decision := range usage.Decisions {
		if decision.Role != reconcile.RoleCounted {
			continue
		}
		record := Record{Usage: decision.Usage, Call: decision.AppliesTo, At: eventTimes[idKey(decision.EventID)], Lines: []Line{}}
		info, hasCall := calls[entityKey(decision.AppliesTo)]
		if hasCall {
			record.Provider, record.Model = info.provider, info.model
			if record.At.IsZero() {
				record.At = info.at
			}
		}
		record.PricingAt = record.At
		if options.PricingAt != nil {
			record.PricingAt = *options.PricingAt
		}
		if version == nil {
			record.Status, record.Reason = "missing_pricing_version", "no active pricing version is available"
			result.Records = append(result.Records, record)
			continue
		}
		if !hasCall || record.Provider == "" || record.Model == "" {
			record.Status, record.Reason = "missing_model", "usage is not linked to a model call with an observed provider and model"
			result.MissingCost = true
			result.Records = append(result.Records, record)
			continue
		}
		if decision.Normalized == nil || len(decision.Normalized.ExclusiveBuckets) == 0 {
			record.Status, record.Reason = "missing_usage", "normalized exclusive usage buckets are unavailable"
			result.MissingCost = true
			result.Records = append(result.Records, record)
			continue
		}
		complete := true
		nonzero := 0
		lineCurrencies := map[string]bool{}
		lineTotal := new(big.Rat)
		for _, bucket := range decision.Normalized.ExclusiveBuckets {
			line := Line{UsageClass: bucket.Name, Resolution: pricing.ResolutionUnregistered}
			if bucket.Tokens.Value == nil {
				line.Reason = "usage quantity is missing"
				complete = false
				record.Lines = append(record.Lines, line)
				continue
			}
			line.Quantity = *bucket.Tokens.Value
			if line.Quantity == 0 {
				zero := "0"
				line.Resolution, line.Reason, line.Amount = pricing.ResolutionNotApplicable, "zero quantity requires no rate", &zero
				record.Lines = append(record.Lines, line)
				continue
			}
			nonzero++
			resolved := version.Resolve(pricing.Query{Provider: record.Provider, Service: options.Service, Contract: options.Contract, Model: record.Model, UsageClass: bucket.Name, Quantity: line.Quantity, At: record.PricingAt, Attributes: options.Attributes})
			line.Resolution, line.Reason = resolved.Status, resolved.Reason
			if resolved.Status != pricing.ResolutionResolved || resolved.Rate == nil {
				complete = false
				record.Lines = append(record.Lines, line)
				continue
			}
			amount, rational, err := calculate(line.Quantity, resolved.Rate.Amount, resolved.Rate.UnitScale, DefaultRounding)
			if err != nil {
				return result, fmt.Errorf("rate %s: %w", resolved.Rate.ID, err)
			}
			line.RateID, line.Currency, line.Amount = resolved.Rate.ID, resolved.Rate.Currency, &amount
			lineCurrencies[line.Currency] = true
			lineTotal.Add(lineTotal, rational)
			if totals[line.Currency] == nil {
				totals[line.Currency] = new(big.Rat)
			}
			rounded, _ := new(big.Rat).SetString(amount)
			totals[line.Currency].Add(totals[line.Currency], rounded)
			record.Lines = append(record.Lines, line)
		}
		if complete && nonzero == 0 {
			zero := "0"
			record.Amount, record.Status = &zero, "zero_usage"
		} else if complete && len(lineCurrencies) == 1 {
			for currency := range lineCurrencies {
				record.Currency = currency
			}
			amount := formatRat(lineTotal, DefaultRounding)
			record.Amount, record.Status = &amount, "calculated"
		} else {
			record.Status = "incomplete"
			if len(lineCurrencies) > 1 {
				record.Reason = "one usage resolves to rates in multiple currencies"
			} else {
				record.Reason = "one or more usage buckets could not be priced"
			}
			result.MissingCost = true
		}
		result.Records = append(result.Records, record)
	}
	sort.Slice(result.Records, func(i, j int) bool { return entityKey(result.Records[i].Usage) < entityKey(result.Records[j].Usage) })
	currencies := make([]string, 0, len(totals))
	for currency := range totals {
		currencies = append(currencies, currency)
	}
	sort.Strings(currencies)
	for _, currency := range currencies {
		result.KnownTotals = append(result.KnownTotals, CurrencyTotal{Currency: currency, Amount: formatRat(totals[currency], DefaultRounding)})
	}
	return result, nil
}

func calculate(quantity int64, decimal string, unitScale int64, rounding Rounding) (string, *big.Rat, error) {
	if quantity < 0 || unitScale <= 0 || rounding.Mode != "half_up" || rounding.Scale < 0 {
		return "", nil, errors.New("invalid quantity, unit scale, or rounding")
	}
	rate, ok := new(big.Rat).SetString(decimal)
	if !ok || rate.Sign() < 0 {
		return "", nil, errors.New("invalid non-negative decimal rate")
	}
	value := new(big.Rat).Mul(rate, new(big.Rat).SetInt64(quantity))
	value.Quo(value, new(big.Rat).SetInt64(unitScale))
	return formatRat(value, rounding), value, nil
}

func formatRat(value *big.Rat, rounding Rounding) string {
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(rounding.Scale)), nil)
	scaled := new(big.Rat).Mul(value, new(big.Rat).SetInt(factor))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(scaled.Num(), scaled.Denom(), remainder)
	if new(big.Int).Abs(new(big.Int).Mul(remainder, big.NewInt(2))).Cmp(scaled.Denom()) >= 0 {
		if scaled.Sign() >= 0 {
			quotient.Add(quotient, big.NewInt(1))
		} else {
			quotient.Sub(quotient, big.NewInt(1))
		}
	}
	negative := quotient.Sign() < 0
	digits := new(big.Int).Abs(quotient).String()
	if rounding.Scale == 0 {
		if negative {
			return "-" + digits
		}
		return digits
	}
	for len(digits) <= rounding.Scale {
		digits = "0" + digits
	}
	whole, fraction := digits[:len(digits)-rounding.Scale], strings.TrimRight(digits[len(digits)-rounding.Scale:], "0")
	result := whole
	if fraction != "" {
		result += "." + fraction
	}
	if negative && result != "0" {
		result = "-" + result
	}
	return result
}

func observedAt(event model.EventEnvelope) time.Time {
	if event.Time.ProviderAt != nil {
		return *event.Time.ProviderAt
	}
	return event.Time.ReceivedAt
}
func entityKey(ref model.EntityRef) string { return string(ref.Kind) + "\x00" + idKey(ref.ID) }
func idKey(id model.NamespacedID) string   { return id.Namespace + "\x00" + id.Value }

type ChangeSet struct {
	FromPricingVersion string            `json:"fromPricingVersion"`
	ToPricingVersion   string            `json:"toPricingVersion"`
	ChangedUsage       []model.EntityRef `json:"changedUsage,omitempty"`
}

func Compare(oldResult, newResult Result) ChangeSet {
	result := ChangeSet{FromPricingVersion: oldResult.PricingVersion, ToPricingVersion: newResult.PricingVersion}
	old := map[string]Record{}
	for _, record := range oldResult.Records {
		old[entityKey(record.Usage)] = record
	}
	for _, record := range newResult.Records {
		previous, ok := old[entityKey(record.Usage)]
		if !ok || previous.Status != record.Status || stringValue(previous.Amount) != stringValue(record.Amount) || previous.Currency != record.Currency {
			result.ChangedUsage = append(result.ChangedUsage, record.Usage)
		}
	}
	return result
}
func stringValue(value *string) string {
	if value == nil {
		return "<missing>"
	}
	return *value
}

func calculationID(pricingVersion string, options Options, inputs []model.NamespacedID) string {
	data, _ := json.Marshal(struct {
		Rule    string               `json:"rule"`
		Pricing string               `json:"pricing"`
		Options Options              `json:"options"`
		Inputs  []model.NamespacedID `json:"inputs"`
	}{CalculationVersion, pricingVersion, options, inputs})
	sum := sha256.Sum256(data)
	return "cost-" + hex.EncodeToString(sum[:])[:24]
}
