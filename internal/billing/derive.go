package billing

import (
	"errors"
	"math/big"
	"time"

	"maatplug/internal/model"
)

type Money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type Conversion struct {
	Version   string    `json:"version"`
	Original  Money     `json:"original"`
	Converted Money     `json:"converted"`
	Rate      string    `json:"rate"`
	RateAt    time.Time `json:"rateAt"`
	Method    string    `json:"method"`
	SourceID  string    `json:"sourceId"`
	Scale     int       `json:"scale"`
}

func Convert(version string, original Money, targetCurrency, rate string, rateAt time.Time, method, sourceID string, scale int) (Conversion, error) {
	amount, amountOK := new(big.Rat).SetString(original.Amount)
	rateValue, rateOK := new(big.Rat).SetString(rate)
	if !validDecimal(original.Amount) || !validDecimal(rate) || !amountOK || !rateOK || rateValue.Sign() < 0 || !safeIdentifier(version) || original.Currency == "" || targetCurrency == "" || rateAt.IsZero() || method == "" || sourceID == "" || scale < 0 {
		return Conversion{}, errors.New("invalid currency conversion")
	}
	converted := new(big.Rat).Mul(amount, rateValue)
	return Conversion{Version: version, Original: original, Converted: Money{Amount: formatRat(converted, scale), Currency: targetCurrency}, Rate: rate, RateAt: rateAt, Method: method, SourceID: sourceID, Scale: scale}, nil
}

type AllocationShare struct {
	Target     model.EntityRef `json:"target"`
	Units      int64           `json:"units"`
	TotalUnits int64           `json:"totalUnits"`
}

type AllocatedAmount struct {
	Target model.EntityRef `json:"target"`
	Money  Money           `json:"money"`
}

type Allocation struct {
	Version     string            `json:"version"`
	SourceID    string            `json:"sourceId"`
	Original    Money             `json:"original"`
	Method      string            `json:"method"`
	Scale       int               `json:"scale"`
	Allocated   []AllocatedAmount `json:"allocated"`
	Unallocated Money             `json:"unallocated"`
}

func Allocate(version, sourceID string, original Money, method string, scale int, shares []AllocationShare) (Allocation, error) {
	amount, ok := new(big.Rat).SetString(original.Amount)
	if !validDecimal(original.Amount) || !ok || version == "" || sourceID == "" || original.Currency == "" || method == "" || scale < 0 {
		return Allocation{}, errors.New("invalid allocation")
	}
	result := Allocation{Version: version, SourceID: sourceID, Original: original, Method: method, Scale: scale, Allocated: []AllocatedAmount{}, Unallocated: Money{Currency: original.Currency}}
	allocated := new(big.Rat)
	var denominator int64
	var units int64
	seen := map[string]bool{}
	for _, share := range shares {
		key := string(share.Target.Kind) + "\x00" + share.Target.ID.Namespace + "\x00" + share.Target.ID.Value
		if share.Target.Kind == "" || share.Target.ID.Namespace == "" || share.Target.ID.Value == "" || seen[key] || share.Units < 0 || share.TotalUnits <= 0 || denominator != 0 && denominator != share.TotalUnits {
			return Allocation{}, errors.New("invalid allocation share")
		}
		seen[key] = true
		denominator = share.TotalUnits
		units += share.Units
		part := new(big.Rat).Mul(amount, new(big.Rat).SetFrac64(share.Units, share.TotalUnits))
		displayed := formatRat(part, scale)
		displayedValue, _ := new(big.Rat).SetString(displayed)
		allocated.Add(allocated, displayedValue)
		result.Allocated = append(result.Allocated, AllocatedAmount{Target: share.Target, Money: Money{Amount: displayed, Currency: original.Currency}})
	}
	if denominator == 0 || units > denominator {
		return Allocation{}, errors.New("allocation shares exceed or omit the denominator")
	}
	remainderScale := scale
	if originalScale := decimalPlaces(original.Amount); originalScale > remainderScale {
		remainderScale = originalScale
	}
	result.Unallocated.Amount = formatRat(new(big.Rat).Sub(amount, allocated), remainderScale)
	return result, nil
}
