// Package inputcomposition builds a per-call explanation of model input.
// It keeps explicit input observations separate from inferred instruction
// links and never replaces provider usage with token estimates.
package inputcomposition

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	"maatplug/internal/model"
	"maatplug/internal/reconcile"
)

const RuleVersion = "input-composition-v1"

type Element struct {
	EventID         model.NamespacedID       `json:"eventId"`
	Element         model.EntityRef          `json:"element"`
	Call            model.EntityRef          `json:"call"`
	Kind            string                   `json:"kind"`
	Content         *model.ContentRef        `json:"content,omitempty"`
	OriginalContent *model.NamespacedID      `json:"originalContent,omitempty"`
	Tokens          *model.Quantity          `json:"tokens,omitempty"`
	Transformation  string                   `json:"transformation,omitempty"`
	Sources         []model.EntityRef        `json:"sources,omitempty"`
	Availability    model.AvailabilityStatus `json:"availability"`
	Reason          string                   `json:"reason,omitempty"`
}

type Gap struct {
	Value        *int64                   `json:"value,omitempty"`
	Availability model.AvailabilityStatus `json:"availability"`
	Kind         string                   `json:"kind"`
	Reason       string                   `json:"reason"`
}

type Call struct {
	Call                 model.EntityRef `json:"call"`
	Elements             []Element       `json:"elements"`
	ProviderInputTokens  *int64          `json:"providerInputTokens,omitempty"`
	AccountedTokens      int64           `json:"accountedTokens"`
	EstimatedElements    bool            `json:"estimatedElements"`
	MissingElementTokens bool            `json:"missingElementTokens"`
	Gap                  Gap             `json:"gap"`
}

type Result struct {
	RuleVersion   string               `json:"ruleVersion"`
	InputEventIDs []model.NamespacedID `json:"inputEventIds"`
	Calls         []Call               `json:"calls"`
}

func Build(events []model.EventEnvelope, usage reconcile.Result) (Result, error) {
	result := Result{RuleVersion: RuleVersion, InputEventIDs: append([]model.NamespacedID(nil), usage.InputEventIDs...), Calls: []Call{}}
	calls := map[string]*Call{}
	callInstruction := map[string]model.EntityRef{}
	instructions := map[string]model.InstructionPayload{}
	for _, event := range events {
		decoded, err := event.DecodePayload()
		if err != nil {
			return result, err
		}
		switch payload := decoded.(type) {
		case *model.ModelCallPayload:
			key := entityKey(payload.Call)
			ensureCall(calls, payload.Call)
			callInstruction[key] = payload.Instruction
		case *model.InstructionPayload:
			instructions[entityKey(payload.Instruction)] = *payload
		case *model.InputElementPayload:
			call := ensureCall(calls, payload.Call)
			element := Element{EventID: event.EventID, Element: payload.Element, Call: payload.Call, Kind: payload.Kind, Content: payload.Content, Tokens: payload.Tokens, Transformation: payload.Transformation, Availability: model.AvailabilityObserved}
			if payload.Content != nil && payload.Content.DerivedFrom != nil {
				original := *payload.Content.DerivedFrom
				element.OriginalContent = &original
			}
			for _, relation := range event.Relations {
				if relation.From.Kind == model.EntityInputElement && entityKey(relation.From) == entityKey(payload.Element) && (relation.Kind == model.RelationDerivedFrom || relation.Kind == model.RelationInputTo) {
					element.Sources = append(element.Sources, relation.To)
				}
			}
			call.Elements = append(call.Elements, element)
		}
	}
	for key, instructionRef := range callInstruction {
		call := calls[key]
		instruction, ok := instructions[entityKey(instructionRef)]
		if !ok {
			continue
		}
		hasInstruction := false
		for _, element := range call.Elements {
			if element.Kind == "instruction" {
				hasInstruction = true
				break
			}
		}
		if hasInstruction {
			continue
		}
		for index, content := range instruction.Content {
			elementRef := derivedElement(call.Call, instructionRef, index)
			copyContent := content
			call.Elements = append(call.Elements, Element{Element: elementRef, Call: call.Call, Kind: "instruction", Content: &copyContent, Sources: []model.EntityRef{instructionRef}, Availability: model.AvailabilityInferred, Reason: "model call explicitly references the instruction; exact serialized input was not observed"})
		}
	}
	for _, decision := range usage.Decisions {
		if decision.Role != reconcile.RoleCounted || decision.AppliesTo.Kind != model.EntityModelCall {
			continue
		}
		call := ensureCall(calls, decision.AppliesTo)
		input, known := providerInput(decision.Normalized)
		if known {
			value := input
			call.ProviderInputTokens = &value
		}
	}
	keys := make([]string, 0, len(calls))
	for key := range calls {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		call := calls[key]
		sort.Slice(call.Elements, func(i, j int) bool { return entityKey(call.Elements[i].Element) < entityKey(call.Elements[j].Element) })
		finalize(call)
		result.Calls = append(result.Calls, *call)
	}
	return result, nil
}

func finalize(call *Call) {
	for _, element := range call.Elements {
		if element.Tokens == nil || element.Tokens.Value == nil {
			call.MissingElementTokens = true
			continue
		}
		call.AccountedTokens += *element.Tokens.Value
		if element.Tokens.Availability == model.AvailabilityInferred {
			call.EstimatedElements = true
		}
	}
	if call.ProviderInputTokens == nil {
		call.Gap = Gap{Availability: model.AvailabilityUnavailable, Kind: "missing_provider_usage", Reason: "selected usage has no known provider input total"}
		return
	}
	difference := *call.ProviderInputTokens - call.AccountedTokens
	call.Gap.Value = &difference
	switch {
	case call.MissingElementTokens:
		call.Gap.Availability = model.AvailabilityUnknown
		call.Gap.Kind = "non_displayed_or_missing_elements"
		call.Gap.Reason = "one or more observed or inferred input elements has no token measurement"
	case call.EstimatedElements:
		call.Gap.Availability = model.AvailabilityInferred
		call.Gap.Kind = "estimation_error"
		call.Gap.Reason = "element token counts are estimates and do not replace provider usage"
	case difference == 0:
		call.Gap.Availability = model.AvailabilityObserved
		call.Gap.Kind = "matched"
		call.Gap.Reason = "measured input elements equal provider input usage"
	default:
		call.Gap.Availability = model.AvailabilityUnknown
		call.Gap.Kind = "non_displayed_elements"
		call.Gap.Reason = "provider input includes elements not represented by measured input records"
	}
}

func providerInput(usage *model.NormalizedUsage) (int64, bool) {
	if usage == nil {
		return 0, false
	}
	var total int64
	found := false
	for _, bucket := range usage.ExclusiveBuckets {
		name := strings.ToLower(bucket.Name)
		if !strings.Contains(name, "input") {
			continue
		}
		if bucket.Tokens.Value == nil {
			return 0, false
		}
		total += *bucket.Tokens.Value
		found = true
	}
	return total, found
}
func ensureCall(calls map[string]*Call, ref model.EntityRef) *Call {
	key := entityKey(ref)
	if calls[key] == nil {
		calls[key] = &Call{Call: ref, Elements: []Element{}}
	}
	return calls[key]
}
func entityKey(ref model.EntityRef) string {
	return string(ref.Kind) + "\x00" + ref.ID.Namespace + "\x00" + ref.ID.Value
}
func derivedElement(call, instruction model.EntityRef, index int) model.EntityRef {
	sum := sha256.Sum256([]byte(entityKey(call) + "\x00" + entityKey(instruction) + "\x00" + strconv.Itoa(index)))
	return model.EntityRef{Kind: model.EntityInputElement, ID: model.NamespacedID{Namespace: "maatplug/derived-input", Value: hex.EncodeToString(sum[:])}}
}
