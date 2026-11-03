package query

import (
	"math/big"
	"sort"

	"maatplug/internal/attribution"
	"maatplug/internal/cost"
)

// attachInstructionCosts reuses priced usage records, including partial known
// line amounts, rather than multiplying a turn's tokens by a single model rate.
// Shared usage remains shared; only explicit allocations prorate an amount.
func attachInstructionCosts(instructions []InstructionView, attr attribution.Result, priced cost.Result) []InstructionView {
	type aggregate struct {
		totals          map[string]*big.Rat
		missing, shared bool
	}
	byUsage := map[string]cost.Record{}
	for _, record := range priced.Records {
		byUsage[entityKey(record.Usage)] = record
	}
	byInstruction := map[string]*aggregate{}
	for _, record := range attr.Records {
		shares := map[string]*big.Rat{}
		if record.Mode == "corrected_allocated" {
			for _, assigned := range record.Assigned {
				key := instructionStatsKey(assigned.Instruction)
				if shares[key] == nil {
					shares[key] = new(big.Rat)
				}
				if record.Tokens != nil && *record.Tokens > 0 {
					shares[key].Add(shares[key], big.NewRat(assigned.Tokens, *record.Tokens))
				}
			}
		} else {
			for _, ref := range record.Instructions {
				shares[instructionStatsKey(ref)] = big.NewRat(1, 1)
			}
		}
		item, found := byUsage[entityKey(record.Usage)]
		for key, share := range shares {
			a := byInstruction[key]
			if a == nil {
				a = &aggregate{totals: map[string]*big.Rat{}}
				byInstruction[key] = a
			}
			a.shared = a.shared || record.Mode == "shared" || record.Mode == "corrected_shared"
			if !found || (record.Mode == "corrected_allocated" && (record.Tokens == nil || *record.Tokens <= 0)) {
				a.missing = true
				continue
			}
			a.missing = a.missing || (item.Status != "calculated" && item.Status != "zero_usage")
			add := func(currency string, amount *string) {
				if amount == nil {
					a.missing = true
					return
				}
				value, ok := new(big.Rat).SetString(*amount)
				if !ok {
					a.missing = true
					return
				}
				if value.Sign() == 0 && currency == "" {
					return
				}
				if currency == "" {
					a.missing = true
					return
				}
				value.Mul(value, share)
				if a.totals[currency] == nil {
					a.totals[currency] = new(big.Rat)
				}
				a.totals[currency].Add(a.totals[currency], value)
			}
			if len(item.Lines) > 0 {
				for _, line := range item.Lines {
					add(line.Currency, line.Amount)
				}
			} else {
				add(item.Currency, item.Amount)
			}
		}
	}
	for i := range instructions {
		view := &instructions[i]
		view.EstimatedCost = []cost.CurrencyTotal{}
		view.CostMissing, view.CostShared = true, false
		if a := byInstruction[instructionStatsKey(view.Instruction)]; a != nil {
			view.CostMissing, view.CostShared = a.missing, a.shared
			for currency, amount := range a.totals {
				view.EstimatedCost = append(view.EstimatedCost, cost.CurrencyTotal{Currency: currency, Amount: formatRat(amount)})
			}
			sort.Slice(view.EstimatedCost, func(i, j int) bool { return view.EstimatedCost[i].Currency < view.EstimatedCost[j].Currency })
		}
	}
	return instructions
}
