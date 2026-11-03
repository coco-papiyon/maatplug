package query

import (
	"testing"

	"maatplug/internal/attribution"
	"maatplug/internal/cost"
	"maatplug/internal/model"
)

func TestInstructionCostAttribution(t *testing.T) {
	ref := func(namespace, value string) model.EntityRef {
		return model.EntityRef{Kind: model.EntityInstruction, ID: model.NamespacedID{Namespace: namespace, Value: value}}
	}
	current := ref("codex/instruction", "a")
	legacy := ref("codex/turn", "a")
	other := ref("codex/instruction", "b")
	u := model.EntityRef{Kind: model.EntityUsage, ID: model.NamespacedID{Namespace: "codex/usage", Value: "response"}}
	amount := "0.4"
	tokens := int64(100)
	for _, tt := range []struct {
		name         string
		record       attribution.Record
		wantA, wantB string
		shared       bool
	}{
		{"legacy direct", attribution.Record{Usage: u, Mode: "direct", Instructions: []model.EntityRef{legacy}}, "0.4", "", false},
		{"aliases counted once", attribution.Record{Usage: u, Mode: "direct", Instructions: []model.EntityRef{legacy, current}}, "0.4", "", false},
		{"shared", attribution.Record{Usage: u, Mode: "shared", Instructions: []model.EntityRef{legacy, other}}, "0.4", "0.4", true},
		{"allocated", attribution.Record{Usage: u, Mode: "corrected_allocated", Tokens: &tokens, Assigned: []attribution.Assigned{{Instruction: legacy, Tokens: 25}, {Instruction: other, Tokens: 75}}}, "0.1", "0.3", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			priced := cost.Result{Records: []cost.Record{{Usage: u, Status: "calculated", Currency: "USD", Amount: &amount}}}
			got := attachInstructionCosts([]InstructionView{{Instruction: current}, {Instruction: other}}, attribution.Result{Records: []attribution.Record{tt.record}}, priced)
			for i, want := range []string{tt.wantA, tt.wantB} {
				if want == "" {
					if !got[i].CostMissing || len(got[i].EstimatedCost) != 0 {
						t.Fatalf("missing usage became a price: %+v", got[i])
					}
					continue
				}
				if got[i].CostMissing || got[i].CostShared != tt.shared || len(got[i].EstimatedCost) != 1 || got[i].EstimatedCost[0].Amount != want || got[i].EstimatedCost[0].Currency != "USD" {
					t.Fatalf("row %d = %+v; want %s USD (shared=%t)", i, got[i], want, tt.shared)
				}
			}
		})
	}
}

func TestInstructionCostPartialZeroAndMissing(t *testing.T) {
	ref := model.EntityRef{Kind: model.EntityInstruction, ID: model.NamespacedID{Namespace: "fixture", Value: "a"}}
	u := model.EntityRef{Kind: model.EntityUsage, ID: model.NamespacedID{Namespace: "fixture", Value: "usage"}}
	a, b, zero := "0.1", "0.2", "0"
	attr := attribution.Result{Records: []attribution.Record{{Usage: u, Instructions: []model.EntityRef{ref}, Mode: "direct"}}}
	for _, tt := range []struct {
		name    string
		records []cost.Record
		want    string
		missing bool
	}{
		{"partial known lines", []cost.Record{{Usage: u, Status: "incomplete", Lines: []cost.Line{{Currency: "USD", Amount: &a}, {Currency: "USD", Amount: &b}, {Amount: nil}}}}, "0.3", true},
		{"zero usage", []cost.Record{{Usage: u, Status: "zero_usage", Amount: &zero}}, "", false},
		{"unpriced", []cost.Record{{Usage: u, Status: "missing_model"}}, "", true},
		{"missing record", nil, "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := attachInstructionCosts([]InstructionView{{Instruction: ref}}, attr, cost.Result{Records: tt.records})[0]
			if got.CostMissing != tt.missing {
				t.Fatalf("missing=%t; want %t", got.CostMissing, tt.missing)
			}
			if tt.want == "" {
				if len(got.EstimatedCost) != 0 {
					t.Fatalf("unexpected cost: %+v", got.EstimatedCost)
				}
			} else if len(got.EstimatedCost) != 1 || got.EstimatedCost[0].Amount != tt.want {
				t.Fatalf("totals=%+v; want %s", got.EstimatedCost, tt.want)
			}
		})
	}
}
