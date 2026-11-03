package query

import (
	"testing"

	"maatplug/internal/attribution"
	"maatplug/internal/model"
	"maatplug/internal/operation"
	"maatplug/internal/reconcile"
)

func TestInstructionTokensRecognizeLegacyCodexNamespace(t *testing.T) {
	ref := func(namespace string) model.EntityRef {
		return model.EntityRef{Kind: model.EntityInstruction, ID: model.NamespacedID{Namespace: namespace, Value: "turn-1"}}
	}
	current, legacy := ref("codex/instruction"), ref("codex/turn")
	for _, tt := range []struct {
		name      string
		refs      []model.EntityRef
		wantKnown bool
	}{
		{"legacy", []model.EntityRef{legacy}, true},
		{"current", []model.EntityRef{current}, true},
		{"both aliases counted once", []model.EntityRef{legacy, current}, true},
		{"other provider stays unknown", []model.EntityRef{ref("claude/instruction")}, false},
		{"unrelated namespace stays unknown", []model.EntityRef{ref("codex/unrelated")}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			usageRef := model.EntityRef{Kind: model.EntityUsage, ID: model.NamespacedID{Namespace: "codex/usage", Value: "response-1"}}
			input, output := int64(100), int64(20)
			u := reconcile.Result{KnownSubtotal: 120, Decisions: []reconcile.Decision{{Usage: usageRef, Role: reconcile.RoleCounted, Normalized: &model.NormalizedUsage{ExclusiveBuckets: []model.UsageBucket{
				{Name: "input_uncached", Tokens: model.Quantity{Value: &input}},
				{Name: "output", Tokens: model.Quantity{Value: &output}},
			}}}}}
			attr := attribution.Result{Records: []attribution.Record{{Usage: usageRef, Mode: "direct", Instructions: tt.refs}}}
			got := attachInstructionStats([]InstructionView{{Instruction: current}, {Instruction: ref("codex/other")}}, u, attr, operation.Result{})
			if (got[0].Tokens != nil) != tt.wantKnown {
				t.Fatalf("tokens = %+v; want known %t", got[0].Tokens, tt.wantKnown)
			}
			if tt.wantKnown && (got[0].Tokens.Total != 120 || got[0].Tokens.Input != 100 || got[0].Tokens.Output != 20) {
				t.Fatalf("wrong breakdown: %+v", got[0].Tokens)
			}
			if got[1].Tokens != nil || u.KnownSubtotal != 120 || attr.Records[0].Instructions[0] != tt.refs[0] {
				t.Fatal("unrelated instruction, totals, or evidence changed")
			}
		})
	}
}
