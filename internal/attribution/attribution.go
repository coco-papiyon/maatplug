// Package attribution assigns reconciled usage to instructions without
// changing the source usage or using attributed display rows as totals.
package attribution

import (
	"errors"
	"fmt"
	"sort"

	"maatplug/internal/model"
	"maatplug/internal/reconcile"
)

const RuleVersion = "usage-attribution-v1"
const DefaultPolicyVersion = "automatic-v1"

type Allocation struct {
	Instruction model.EntityRef `json:"instruction"`
	Units       int64           `json:"units"`
	TotalUnits  int64           `json:"totalUnits"`
}

type Correction struct {
	Usage        model.EntityRef   `json:"usage"`
	Instructions []model.EntityRef `json:"instructions,omitempty"`
	Allocations  []Allocation      `json:"allocations,omitempty"`
	Reason       string            `json:"reason"`
}

type Policy struct {
	Version     string       `json:"version"`
	BasedOn     string       `json:"basedOn,omitempty"`
	Corrections []Correction `json:"corrections,omitempty"`
}

type Assigned struct {
	Instruction model.EntityRef `json:"instruction"`
	Tokens      int64           `json:"tokens"`
	Estimated   bool            `json:"estimated"`
}

type Record struct {
	Usage             model.EntityRef    `json:"usage"`
	EventID           model.NamespacedID `json:"eventId"`
	AppliesTo         model.EntityRef    `json:"appliesTo"`
	Tokens            *int64             `json:"tokens,omitempty"`
	Mode              string             `json:"mode"`
	Instructions      []model.EntityRef  `json:"instructions,omitempty"`
	Assigned          []Assigned         `json:"assigned,omitempty"`
	UnallocatedTokens int64              `json:"unallocatedTokens,omitempty"`
	Reason            string             `json:"reason"`
}

type Summary struct {
	KnownSubtotal       int64 `json:"knownSubtotal"`
	DirectTokens        int64 `json:"directTokens"`
	SharedUnallocated   int64 `json:"sharedUnallocated"`
	Undecomposed        int64 `json:"undecomposed"`
	UnattributedTokens  int64 `json:"unattributedTokens"`
	AllocatedEstimate   int64 `json:"allocatedEstimate"`
	CorrectionRemainder int64 `json:"correctionRemainder"`
	Missing             bool  `json:"missing"`
}

type EntitySummary struct {
	Entity  model.EntityRef `json:"entity"`
	Summary Summary         `json:"summary"`
}

type Result struct {
	RuleVersion         string               `json:"ruleVersion"`
	PolicyVersion       string               `json:"policyVersion"`
	InputEventIDs       []model.NamespacedID `json:"inputEventIds"`
	Records             []Record             `json:"records"`
	WorkTotalTokens     int64                `json:"workTotalTokens"`
	DirectTokens        int64                `json:"directTokens"`
	SharedUnallocated   int64                `json:"sharedUnallocated"`
	UnattributedTokens  int64                `json:"unattributedTokens"`
	AllocatedEstimate   int64                `json:"allocatedEstimate"`
	CorrectionRemainder int64                `json:"correctionRemainder"`
	MissingUsage        bool                 `json:"missingUsage"`
	Work                Summary              `json:"work"`
	Instructions        []EntitySummary      `json:"instructions"`
	Calls               []EntitySummary      `json:"calls"`
}

func (policy Policy) Validate() error {
	if policy.Version == "" {
		return errors.New("policy version is required")
	}
	if policy.BasedOn != "" && (!safeName.MatchString(policy.BasedOn) || policy.BasedOn == policy.Version) {
		return errors.New("basedOn must name a different valid policy version")
	}
	seen := map[string]bool{}
	for index, correction := range policy.Corrections {
		key := entityKey(correction.Usage)
		if correction.Usage.Kind != model.EntityUsage || key == "" {
			return fmt.Errorf("corrections[%d].usage must be a usage entity", index)
		}
		if seen[key] {
			return fmt.Errorf("corrections[%d] duplicates a usage", index)
		}
		seen[key] = true
		if correction.Reason == "" {
			return fmt.Errorf("corrections[%d].reason is required", index)
		}
		for _, instruction := range correction.Instructions {
			if instruction.Kind != model.EntityInstruction || entityKey(instruction) == "" {
				return fmt.Errorf("corrections[%d] contains a non-instruction target", index)
			}
		}
		for _, allocation := range correction.Allocations {
			if allocation.Instruction.Kind != model.EntityInstruction || entityKey(allocation.Instruction) == "" || allocation.Units < 0 || allocation.TotalUnits <= 0 || allocation.Units > allocation.TotalUnits {
				return fmt.Errorf("corrections[%d] has an invalid allocation", index)
			}
		}
	}
	return nil
}

func Build(events []model.EventEnvelope, usage reconcile.Result, policy Policy) (Result, error) {
	if policy.Version == "" {
		policy.Version = DefaultPolicyVersion
	}
	if err := policy.Validate(); err != nil {
		return Result{}, err
	}
	result := Result{RuleVersion: RuleVersion, PolicyVersion: policy.Version, InputEventIDs: append([]model.NamespacedID(nil), usage.InputEventIDs...), Records: []Record{}, WorkTotalTokens: usage.KnownSubtotal, MissingUsage: usage.Unknown}
	calls := map[string]model.EntityRef{}
	relations := map[string][]model.EntityRef{}
	for _, event := range events {
		if event.EventType == model.EventModelCall {
			decoded, err := event.DecodePayload()
			if err != nil {
				return result, err
			}
			call := decoded.(*model.ModelCallPayload)
			calls[entityKey(call.Call)] = call.Instruction
		}
		for _, relation := range event.Relations {
			if relation.Kind == model.RelationAttributedTo && relation.To.Kind == model.EntityInstruction {
				relations[entityKey(relation.From)] = appendUnique(relations[entityKey(relation.From)], relation.To)
			}
		}
	}
	corrections := map[string]Correction{}
	for _, correction := range policy.Corrections {
		corrections[entityKey(correction.Usage)] = correction
	}
	for _, decision := range usage.Decisions {
		if decision.Role != reconcile.RoleCounted {
			continue
		}
		record := Record{Usage: decision.Usage, EventID: decision.EventID, AppliesTo: decision.AppliesTo, Tokens: decision.Tokens}
		if correction, ok := corrections[entityKey(decision.Usage)]; ok {
			record.Instructions = sortedRefs(correction.Instructions)
			record.Reason = "user correction in policy " + policy.Version + ": " + correction.Reason
			if len(correction.Allocations) > 0 {
				if err := applyAllocations(&record, correction.Allocations); err != nil {
					return result, fmt.Errorf("usage %s: %w", decision.Usage.ID.Value, err)
				}
				record.Mode = "corrected_allocated"
			} else if len(record.Instructions) > 1 {
				record.Mode = "corrected_shared"
			} else if len(record.Instructions) == 1 {
				record.Mode = "corrected_direct"
			} else {
				record.Mode = "corrected_unattributed"
			}
		} else {
			instructions := append([]model.EntityRef(nil), relations[entityKey(decision.Usage)]...)
			instructions = append(instructions, relations[entityKey(decision.AppliesTo)]...)
			if instruction, ok := calls[entityKey(decision.AppliesTo)]; ok {
				instructions = appendUnique(instructions, instruction)
			}
			record.Instructions = sortedRefs(instructions)
			switch len(record.Instructions) {
			case 0:
				record.Mode, record.Reason = "unattributed", "no explicit instruction relation is available"
			case 1:
				record.Mode, record.Reason = "direct", "model call or explicit relation identifies one instruction"
			default:
				record.Mode, record.Reason = "shared", "multiple explicit instruction relations share one non-duplicated usage"
			}
		}
		categorize(&result, &record)
		result.Records = append(result.Records, record)
	}
	sort.Slice(result.Records, func(i, j int) bool { return entityKey(result.Records[i].Usage) < entityKey(result.Records[j].Usage) })
	buildSummaries(&result, usage)
	return result, nil
}

func buildSummaries(result *Result, usage reconcile.Result) {
	result.Work = Summary{
		KnownSubtotal: result.WorkTotalTokens, DirectTokens: result.DirectTokens,
		SharedUnallocated: result.SharedUnallocated, Undecomposed: usage.Undecomposed,
		UnattributedTokens: result.UnattributedTokens, AllocatedEstimate: result.AllocatedEstimate,
		CorrectionRemainder: result.CorrectionRemainder, Missing: result.MissingUsage,
	}
	instructions := map[string]*EntitySummary{}
	calls := map[string]*EntitySummary{}
	records := map[string]*Record{}
	for index := range result.Records {
		record := &result.Records[index]
		records[entityKey(record.Usage)] = record
		if record.AppliesTo.Kind == model.EntityModelCall {
			summary := ensureEntitySummary(calls, record.AppliesTo)
			addRecord(&summary.Summary, record, true)
		}
		switch record.Mode {
		case "direct", "corrected_direct":
			if len(record.Instructions) == 1 {
				summary := ensureEntitySummary(instructions, record.Instructions[0])
				addRecord(&summary.Summary, record, true)
			}
		case "shared", "corrected_shared":
			for _, instruction := range record.Instructions {
				summary := ensureEntitySummary(instructions, instruction)
				summary.Summary.SharedUnallocated += tokenValue(record.Tokens)
				summary.Summary.Missing = summary.Summary.Missing || record.Tokens == nil
			}
		case "corrected_allocated":
			for _, assigned := range record.Assigned {
				summary := ensureEntitySummary(instructions, assigned.Instruction)
				summary.Summary.KnownSubtotal += assigned.Tokens
				summary.Summary.AllocatedEstimate += assigned.Tokens
			}
		}
	}
	for _, residual := range usage.Residuals {
		record := records[entityKey(residual.ParentUsage)]
		if record == nil {
			continue
		}
		if record.AppliesTo.Kind == model.EntityModelCall {
			ensureEntitySummary(calls, record.AppliesTo).Summary.Undecomposed += residual.Tokens
		}
		if len(record.Instructions) == 1 {
			ensureEntitySummary(instructions, record.Instructions[0]).Summary.Undecomposed += residual.Tokens
		}
	}
	result.Instructions = sortedEntitySummaries(instructions)
	result.Calls = sortedEntitySummaries(calls)
}

func addRecord(summary *Summary, record *Record, includeKnown bool) {
	if record.Tokens == nil {
		summary.Missing = true
		return
	}
	if includeKnown {
		summary.KnownSubtotal += *record.Tokens
	}
	switch record.Mode {
	case "direct", "corrected_direct":
		summary.DirectTokens += *record.Tokens
	case "shared", "corrected_shared":
		summary.SharedUnallocated += *record.Tokens
	case "unattributed", "corrected_unattributed":
		summary.UnattributedTokens += *record.Tokens
	case "corrected_allocated":
		for _, assigned := range record.Assigned {
			summary.AllocatedEstimate += assigned.Tokens
		}
		summary.CorrectionRemainder += record.UnallocatedTokens
	}
}

func ensureEntitySummary(values map[string]*EntitySummary, entity model.EntityRef) *EntitySummary {
	key := entityKey(entity)
	if values[key] == nil {
		values[key] = &EntitySummary{Entity: entity}
	}
	return values[key]
}

func sortedEntitySummaries(values map[string]*EntitySummary) []EntitySummary {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]EntitySummary, 0, len(keys))
	for _, key := range keys {
		result = append(result, *values[key])
	}
	return result
}

func tokenValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func applyAllocations(record *Record, allocations []Allocation) error {
	if record.Tokens == nil {
		return errors.New("cannot allocate usage with unknown tokens")
	}
	denominator := allocations[0].TotalUnits
	var units int64
	seen := map[string]bool{}
	for _, allocation := range allocations {
		if allocation.TotalUnits != denominator {
			return errors.New("allocation denominators differ")
		}
		key := entityKey(allocation.Instruction)
		if seen[key] {
			return errors.New("allocation repeats an instruction")
		}
		seen[key] = true
		units += allocation.Units
	}
	if units > denominator {
		return errors.New("allocation units exceed total units")
	}
	var assigned int64
	for index, allocation := range allocations {
		tokens := *record.Tokens * allocation.Units / denominator
		if units == denominator && index == len(allocations)-1 {
			tokens = *record.Tokens - assigned
		}
		assigned += tokens
		record.Assigned = append(record.Assigned, Assigned{Instruction: allocation.Instruction, Tokens: tokens, Estimated: true})
		record.Instructions = appendUnique(record.Instructions, allocation.Instruction)
	}
	record.UnallocatedTokens = *record.Tokens - assigned
	record.Instructions = sortedRefs(record.Instructions)
	return nil
}

func categorize(result *Result, record *Record) {
	if record.Tokens == nil {
		result.MissingUsage = true
		return
	}
	tokens := *record.Tokens
	switch record.Mode {
	case "direct", "corrected_direct":
		result.DirectTokens += tokens
	case "shared", "corrected_shared":
		result.SharedUnallocated += tokens
	case "unattributed", "corrected_unattributed":
		result.UnattributedTokens += tokens
	case "corrected_allocated":
		for _, assigned := range record.Assigned {
			result.AllocatedEstimate += assigned.Tokens
		}
		result.CorrectionRemainder += record.UnallocatedTokens
	}
}

func appendUnique(values []model.EntityRef, value model.EntityRef) []model.EntityRef {
	key := entityKey(value)
	for _, candidate := range values {
		if entityKey(candidate) == key {
			return values
		}
	}
	return append(values, value)
}
func sortedRefs(values []model.EntityRef) []model.EntityRef {
	result := []model.EntityRef{}
	for _, value := range values {
		result = appendUnique(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return entityKey(result[i]) < entityKey(result[j]) })
	return result
}
func entityKey(ref model.EntityRef) string {
	if ref.Kind == "" || ref.ID.Namespace == "" || ref.ID.Value == "" {
		return ""
	}
	return string(ref.Kind) + "\x00" + ref.ID.Namespace + "\x00" + ref.ID.Value
}

type ChangeSet struct {
	FromPolicy     string            `json:"fromPolicy"`
	ToPolicy       string            `json:"toPolicy"`
	ChangedUsage   []model.EntityRef `json:"changedUsage,omitempty"`
	WorkTotalDelta int64             `json:"workTotalDelta"`
}

func Compare(oldResult, newResult Result) ChangeSet {
	change := ChangeSet{FromPolicy: oldResult.PolicyVersion, ToPolicy: newResult.PolicyVersion, WorkTotalDelta: newResult.WorkTotalTokens - oldResult.WorkTotalTokens}
	old := map[string]Record{}
	for _, record := range oldResult.Records {
		old[entityKey(record.Usage)] = record
	}
	for _, record := range newResult.Records {
		previous, ok := old[entityKey(record.Usage)]
		if !ok || record.Mode != previous.Mode || refsKey(record.Instructions) != refsKey(previous.Instructions) || assignedKey(record.Assigned) != assignedKey(previous.Assigned) || record.UnallocatedTokens != previous.UnallocatedTokens {
			change.ChangedUsage = append(change.ChangedUsage, record.Usage)
		}
	}
	return change
}
func assignedKey(assigned []Assigned) string {
	copyAssigned := append([]Assigned(nil), assigned...)
	sort.Slice(copyAssigned, func(i, j int) bool {
		return entityKey(copyAssigned[i].Instruction) < entityKey(copyAssigned[j].Instruction)
	})
	result := ""
	for _, item := range copyAssigned {
		result += fmt.Sprintf("%s=%d:%t\n", entityKey(item.Instruction), item.Tokens, item.Estimated)
	}
	return result
}
func refsKey(refs []model.EntityRef) string {
	result := ""
	for _, ref := range sortedRefs(refs) {
		result += entityKey(ref) + "\n"
	}
	return result
}
