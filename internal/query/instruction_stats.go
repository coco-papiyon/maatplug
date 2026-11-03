package query

import (
	"maatplug/internal/attribution"
	"maatplug/internal/model"
	"maatplug/internal/operation"
	"maatplug/internal/provider/codex"
	"maatplug/internal/reconcile"
)

// InstructionTokens is one instruction's share of session token usage,
// broken down the same way the session-wide totals are (see
// classifyNormalized) so the dashboard can show IN/OUT/cache per turn
// instead of only a whole-session total.
type InstructionTokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Total      int64 `json:"total"`
	// Complete is false when some usage attributed to this instruction
	// couldn't be classified into the four buckets above, or was only
	// approximately split across several instructions that shared one
	// provider usage record (see tokenBucketTotals.scaled and
	// attribution.Record.Mode) — the totals above are still the best
	// available estimate, just not exact.
	Complete bool `json:"complete"`
}

// InstructionActivity summarizes what an instruction's linked operations did
// — counts, not raw commands/diffs — per docs/design.md section 9's
// instruction card design. The underlying commands and file diffs remain
// reachable by drilling into the instruction's operation list.
type InstructionActivity struct {
	Commands    int           `json:"commands"`
	FilesEdited int           `json:"filesEdited"`
	Changes     ChangeSummary `json:"changes"`
}

// attachInstructionStats fills in each InstructionView's Tokens and Activity
// fields from the same reconcile/attribution/operation projections the rest
// of Detail is built from, so the frontend doesn't need to re-derive
// per-instruction aggregates itself from raw decisions/operations.
func attachInstructionStats(instructions []InstructionView, usage reconcile.Result, attr attribution.Result, ops operation.Result) []InstructionView {
	tokens := instructionTokenBreakdowns(usage, attr)
	opsByInstruction := map[string][]operation.Operation{}
	for _, op := range ops.Operations {
		for _, related := range op.Instructions {
			key := entityKey(related.Entity)
			opsByInstruction[key] = append(opsByInstruction[key], op)
		}
	}
	for i := range instructions {
		key := entityKey(instructions[i].Instruction)
		if breakdown, ok := tokens[instructionStatsKey(instructions[i].Instruction)]; ok {
			breakdown := breakdown
			instructions[i].Tokens = &breakdown
		}
		instructions[i].Activity = instructionActivity(opsByInstruction[key])
	}
	return instructions
}

func instructionStatsKey(ref model.EntityRef) string {
	return entityKey(codex.CanonicalInstructionRef(ref))
}

// instructionActivity counts commands run (shell operations) and distinct
// files edited (file-kind requested targets) among ops, and reuses
// changeSummary — the same diff-line aggregation used for session/project
// rows — scoped to just this instruction's operations.
func instructionActivity(ops []operation.Operation) InstructionActivity {
	commands := 0
	files := map[string]bool{}
	for _, op := range ops {
		if op.Kind == "shell" {
			commands++
		}
		for _, target := range op.RequestedTargets {
			if target.Kind == "file" && target.Value != "" {
				files[target.Value] = true
			}
		}
	}
	return InstructionActivity{Commands: commands, FilesEdited: len(files), Changes: changeSummary(operation.Result{Operations: ops})}
}

type instructionTokenAgg struct {
	totals   tokenBucketTotals
	complete bool
	hasData  bool
}

// instructionTokenBreakdowns groups reconciled usage decisions by the
// instruction(s) attribution.Build already resolved for them, keyed by
// entityKey. The common case is one decision per instruction
// ("direct"/"corrected_direct" attribution mode, one bucket set added once);
// "shared"/"corrected_shared" records (attribution.Build's doc comments:
// several instructions sharing one non-duplicated usage record, the
// concurrent-resume edge case in docs/design.md section 12 point 9) add the
// full bucket set to every sharing instruction — same non-division as
// attribution's own SharedUnallocated accounting — and are marked
// incomplete; "corrected_allocated" records (an explicit correction policy)
// prorate buckets by each instruction's assigned token share.
func instructionTokenBreakdowns(usage reconcile.Result, attr attribution.Result) map[string]InstructionTokens {
	decisionBuckets := map[string]tokenBucketTotals{}
	decisionComplete := map[string]bool{}
	for _, decision := range usage.Decisions {
		if decision.Role != reconcile.RoleCounted {
			continue
		}
		buckets, ok := classifyNormalized(decision.Normalized)
		decisionBuckets[entityKey(decision.Usage)] = buckets
		decisionComplete[entityKey(decision.Usage)] = ok
	}

	agg := map[string]*instructionTokenAgg{}
	ensure := func(ref model.EntityRef) *instructionTokenAgg {
		key := instructionStatsKey(ref)
		a := agg[key]
		if a == nil {
			a = &instructionTokenAgg{complete: true}
			agg[key] = a
		}
		return a
	}

	for _, record := range attr.Records {
		buckets, bucketsKnown := decisionBuckets[entityKey(record.Usage)]
		bucketsComplete := decisionComplete[entityKey(record.Usage)]
		if record.Mode == "corrected_allocated" {
			total := buckets.total()
			for _, assigned := range record.Assigned {
				a := ensure(assigned.Instruction)
				a.hasData = true
				if !bucketsKnown || total == 0 {
					a.complete = false
					continue
				}
				a.totals.add(buckets.scaled(assigned.Tokens, total))
				if assigned.Estimated || !bucketsComplete {
					a.complete = false
				}
			}
			continue
		}
		shared := record.Mode == "shared" || record.Mode == "corrected_shared"
		seen := map[string]bool{}
		for _, ref := range record.Instructions {
			key := instructionStatsKey(ref)
			if seen[key] {
				continue
			}
			seen[key] = true
			a := ensure(ref)
			a.hasData = true
			if !bucketsKnown {
				a.complete = false
				continue
			}
			a.totals.add(buckets)
			if !bucketsComplete || shared {
				a.complete = false
			}
		}
	}

	result := map[string]InstructionTokens{}
	for key, a := range agg {
		if !a.hasData {
			continue
		}
		result[key] = InstructionTokens{Input: a.totals.input, Output: a.totals.output, CacheRead: a.totals.cacheRead, CacheWrite: a.totals.cacheWrite, Total: a.totals.total(), Complete: a.complete}
	}
	return result
}
