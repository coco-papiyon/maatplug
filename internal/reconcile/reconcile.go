// Package reconcile selects a non-overlapping usage set without changing
// provider observations. Its output is a disposable, versioned projection.
package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"

	"maatplug/internal/model"
)

const RuleVersion = "usage-reconcile-v1"

type Role string

const (
	RoleCounted     Role = "counted"
	RoleExplanatory Role = "explanatory"
	RoleExcluded    Role = "excluded"
	RoleConflict    Role = "conflict"
)

type Decision struct {
	DecisionID model.NamespacedID     `json:"decisionId"`
	EventID    model.NamespacedID     `json:"eventId"`
	Usage      model.EntityRef        `json:"usage"`
	AppliesTo  model.EntityRef        `json:"appliesTo"`
	Scope      []model.EntityRef      `json:"scope"`
	Counter    model.CounterSemantics `json:"counter"`
	State      model.MeasurementState `json:"state"`
	Normalized *model.NormalizedUsage `json:"normalized,omitempty"`
	Tokens     *int64                 `json:"tokens,omitempty"`
	Role       Role                   `json:"role"`
	Reason     string                 `json:"reason"`
}

type Residual struct {
	ParentUsage model.EntityRef   `json:"parentUsage"`
	ChildUsage  []model.EntityRef `json:"childUsage"`
	Tokens      int64             `json:"tokens"`
	Reason      string            `json:"reason"`
}

type Conflict struct {
	Usage  []model.EntityRef `json:"usage"`
	Reason string            `json:"reason"`
}

type Result struct {
	RuleVersion      string               `json:"ruleVersion"`
	InputEventIDs    []model.NamespacedID `json:"inputEventIds"`
	Decisions        []Decision           `json:"decisions"`
	SelectedEventIDs []model.NamespacedID `json:"selectedEventIds"`
	ExcludedEventIDs []model.NamespacedID `json:"excludedEventIds"`
	KnownSubtotal    int64                `json:"knownSubtotal"`
	Undecomposed     int64                `json:"undecomposed"`
	Unknown          bool                 `json:"unknown"`
	Residuals        []Residual           `json:"residuals,omitempty"`
	Conflicts        []Conflict           `json:"conflicts,omitempty"`
}

type observation struct {
	event    model.EventEnvelope
	payload  model.UsagePayload
	tokens   *int64
	decision int
}

func Build(events []model.EventEnvelope) (Result, error) {
	result := Result{RuleVersion: RuleVersion, Decisions: []Decision{}, SelectedEventIDs: []model.NamespacedID{}, ExcludedEventIDs: []model.NamespacedID{}}
	ordered := append([]model.EventEnvelope(nil), events...)
	sort.Slice(ordered, func(i, j int) bool { return eventKey(ordered[i].EventID) < eventKey(ordered[j].EventID) })
	for _, event := range ordered {
		result.InputEventIDs = append(result.InputEventIDs, event.EventID)
	}

	groups := map[string][]observation{}
	for _, event := range ordered {
		if event.EventType != model.EventUsageReported {
			continue
		}
		decoded, err := event.DecodePayload()
		if err != nil {
			return result, err
		}
		payload := *(decoded.(*model.UsagePayload))
		groups[entityKey(payload.Usage)] = append(groups[entityKey(payload.Usage)], observation{event: event, payload: payload, tokens: totalTokens(payload.Normalized)})
	}
	usageKeys := sortedKeys(groups)
	active := map[string]*observation{}
	byUsage := map[string]*observation{}
	for _, key := range usageKeys {
		items := groups[key]
		sort.SliceStable(items, func(i, j int) bool { return better(items[i], items[j]) })
		for index := range items {
			item := &items[index]
			role, reason := RoleExcluded, "superseded by a stronger observation of the same Provider usage ID"
			if index == 0 {
				role, reason = initialRole(*item)
				active[key] = item
			}
			result.Decisions = append(result.Decisions, makeDecision(*item, role, reason))
			item.decision = len(result.Decisions) - 1
			if index == 0 {
				byUsage[key] = item
			}
		}
	}

	// A delta is preferred to run/thread cumulative counters in the same
	// scope. If no delta exists, only the latest comparable cumulative value
	// is used as a fallback total.
	deltaScopes := map[string]bool{}
	for key, item := range active {
		if result.Decisions[item.decision].Role == RoleCounted && item.payload.Counter.Kind == model.CounterDelta {
			deltaScopes[scopeGroup(item.event.Scope)] = true
		}
		_ = key
	}
	cumulative := map[string][]*observation{}
	for _, item := range active {
		decision := &result.Decisions[item.decision]
		if item.payload.Counter.Kind == model.CounterDelta {
			continue
		}
		if deltaScopes[scopeGroup(item.event.Scope)] {
			decision.Role, decision.Reason = RoleExcluded, "a non-cumulative detail is available in the same run scope"
			continue
		}
		key := scopeGroup(item.event.Scope) + "\x00" + entityKey(item.payload.AppliesTo) + "\x00" + item.payload.Counter.Generation
		cumulative[key] = append(cumulative[key], item)
	}
	for _, key := range sortedKeys(cumulative) {
		items := cumulative[key]
		sort.SliceStable(items, func(i, j int) bool { return eventNewer(items[i].event, items[j].event) })
		for index, item := range items {
			decision := &result.Decisions[item.decision]
			if index == 0 && decision.Role != RoleConflict && item.tokens != nil {
				decision.Role, decision.Reason = RoleCounted, "latest comparable cumulative value used because no delta detail is available"
			} else if index == 0 && item.tokens == nil {
				decision.Role, decision.Reason = RoleExcluded, "cumulative value has no normalized total and cannot establish a baseline"
			} else {
				decision.Role, decision.Reason = RoleExcluded, "older cumulative value in the same target and generation"
			}
		}
	}

	contains := containment(events)
	for parentKey, childKeys := range contains {
		parent := byUsage[parentKey]
		if parent == nil || result.Decisions[parent.decision].Role != RoleCounted {
			continue
		}
		parentTokens := parent.tokens
		children := []*observation{}
		var childTotal int64
		known := parentTokens != nil
		for _, childKey := range childKeys {
			child := byUsage[childKey]
			if child == nil || result.Decisions[child.decision].Role != RoleCounted {
				continue
			}
			children = append(children, child)
			if child.tokens == nil {
				known = false
			} else {
				childTotal += *child.tokens
			}
		}
		if len(children) == 0 {
			continue
		}
		refs := make([]model.EntityRef, 0, len(children)+1)
		refs = append(refs, parent.payload.Usage)
		if !known {
			setRole(&result, parent, RoleConflict, "explicit parent/child overlap has a missing normalized total")
			for _, child := range children {
				setRole(&result, child, RoleConflict, "explicit parent/child overlap has a missing normalized total")
				refs = append(refs, child.payload.Usage)
			}
			result.Unknown = true
			result.Conflicts = append(result.Conflicts, Conflict{Usage: refs, Reason: "parent/child inclusion is explicit but one normalized total is missing"})
			continue
		}
		switch {
		case childTotal == *parentTokens:
			setRole(&result, parent, RoleExcluded, "complete child details equal the containing total")
		case childTotal < *parentTokens:
			childRefs := []model.EntityRef{}
			for _, child := range children {
				setRole(&result, child, RoleExplanatory, "included in a counted parent total")
				childRefs = append(childRefs, child.payload.Usage)
			}
			residual := *parentTokens - childTotal
			result.Undecomposed += residual
			result.Residuals = append(result.Residuals, Residual{ParentUsage: parent.payload.Usage, ChildUsage: childRefs, Tokens: residual, Reason: "proven residual of an explicit containing total"})
		case childTotal > *parentTokens:
			setRole(&result, parent, RoleConflict, "child details exceed the explicit containing total")
			for _, child := range children {
				setRole(&result, child, RoleConflict, "child details exceed the explicit containing total")
				refs = append(refs, child.payload.Usage)
			}
			result.Unknown = true
			result.Conflicts = append(result.Conflicts, Conflict{Usage: refs, Reason: "overlapping measurements cannot produce a single total"})
		}
	}
	for _, item := range active {
		if item.tokens == nil && (item.event.Adoption == nil || (item.event.Adoption.Status != model.AdoptionRejected && item.event.Adoption.Status != model.AdoptionSuperseded)) {
			result.Unknown = true
		}
	}

	for index := range result.Decisions {
		decision := &result.Decisions[index]
		switch decision.Role {
		case RoleCounted:
			result.SelectedEventIDs = append(result.SelectedEventIDs, decision.EventID)
			if decision.Tokens == nil {
				result.Unknown = true
			} else {
				result.KnownSubtotal += *decision.Tokens
			}
		default:
			result.ExcludedEventIDs = append(result.ExcludedEventIDs, decision.EventID)
		}
	}
	sort.Slice(result.SelectedEventIDs, func(i, j int) bool {
		return eventKey(result.SelectedEventIDs[i]) < eventKey(result.SelectedEventIDs[j])
	})
	sort.Slice(result.ExcludedEventIDs, func(i, j int) bool {
		return eventKey(result.ExcludedEventIDs[i]) < eventKey(result.ExcludedEventIDs[j])
	})
	return result, nil
}

func initialRole(item observation) (Role, string) {
	if item.event.Adoption != nil {
		switch item.event.Adoption.Status {
		case model.AdoptionRejected, model.AdoptionSuperseded:
			return RoleExcluded, "source adoption marks the measurement as " + string(item.event.Adoption.Status)
		case model.AdoptionAdopted:
			return RoleCounted, "source adapter selected a non-overlapping measurement"
		}
	}
	if item.payload.Counter.Kind == model.CounterDelta {
		return RoleCounted, "final delta is the most specific available measurement"
	}
	return RoleExcluded, "cumulative or snapshot measurement is a fallback candidate"
}

func better(a, b observation) bool {
	rank := func(item observation) int {
		value := map[model.MeasurementState]int{model.MeasurementProvisional: 1, model.MeasurementFinal: 2, model.MeasurementCorrected: 3}[item.payload.State]
		if item.event.Adoption != nil && item.event.Adoption.Status == model.AdoptionAdopted {
			value += 10
		}
		return value
	}
	if rank(a) != rank(b) {
		return rank(a) > rank(b)
	}
	if !eventTime(a.event).Equal(eventTime(b.event)) {
		return eventTime(a.event).After(eventTime(b.event))
	}
	return eventKey(a.event.EventID) < eventKey(b.event.EventID)
}

func makeDecision(item observation, role Role, reason string) Decision {
	return Decision{DecisionID: decisionID(item.event.EventID), EventID: item.event.EventID, Usage: item.payload.Usage, AppliesTo: item.payload.AppliesTo, Scope: append([]model.EntityRef(nil), item.event.Scope...), Counter: item.payload.Counter, State: item.payload.State, Normalized: item.payload.Normalized, Tokens: item.tokens, Role: role, Reason: reason}
}

func setRole(result *Result, item *observation, role Role, reason string) {
	result.Decisions[item.decision].Role, result.Decisions[item.decision].Reason = role, reason
}

func totalTokens(usage *model.NormalizedUsage) *int64 {
	if usage == nil {
		return nil
	}
	for _, bucket := range usage.Totals {
		if bucket.Name == "all" || bucket.Name == "total" || bucket.Name == "all_tokens" {
			if bucket.Tokens.Value == nil {
				return nil
			}
			value := *bucket.Tokens.Value
			return &value
		}
	}
	var value int64
	if len(usage.ExclusiveBuckets) == 0 {
		return nil
	}
	for _, bucket := range usage.ExclusiveBuckets {
		if bucket.Tokens.Value == nil {
			return nil
		}
		value += *bucket.Tokens.Value
	}
	return &value
}

func containment(events []model.EventEnvelope) map[string][]string {
	result := map[string][]string{}
	for _, event := range events {
		for _, relation := range event.Relations {
			if relation.Kind == model.RelationContains && relation.Includes != nil && *relation.Includes && relation.From.Kind == model.EntityUsage && relation.To.Kind == model.EntityUsage {
				result[entityKey(relation.From)] = append(result[entityKey(relation.From)], entityKey(relation.To))
			}
		}
	}
	for key := range result {
		sort.Strings(result[key])
	}
	return result
}

func scopeGroup(scope []model.EntityRef) string {
	for _, ref := range scope {
		if ref.Kind == model.EntityRun {
			return entityKey(ref)
		}
	}
	for _, ref := range scope {
		if ref.Kind == model.EntitySession {
			return entityKey(ref)
		}
	}
	return "unscoped"
}
func entityKey(ref model.EntityRef) string  { return string(ref.Kind) + "\x00" + eventKey(ref.ID) }
func eventKey(id model.NamespacedID) string { return id.Namespace + "\x00" + id.Value }
func eventTime(event model.EventEnvelope) time.Time {
	if event.Time.ProviderAt != nil {
		return *event.Time.ProviderAt
	}
	return event.Time.ReceivedAt
}
func eventNewer(a, b model.EventEnvelope) bool {
	aTime, bTime := eventTime(a), eventTime(b)
	if !aTime.Equal(bTime) {
		return aTime.After(bTime)
	}
	if a.Sequence != nil && b.Sequence != nil && a.Sequence.Stream == b.Sequence.Stream && a.Sequence.Generation == b.Sequence.Generation && a.Sequence.Position != nil && b.Sequence.Position != nil && *a.Sequence.Position != *b.Sequence.Position {
		return *a.Sequence.Position > *b.Sequence.Position
	}
	return eventKey(a.EventID) > eventKey(b.EventID)
}
func decisionID(event model.NamespacedID) model.NamespacedID {
	sum := sha256.Sum256([]byte(RuleVersion + "\x00" + eventKey(event)))
	return model.NamespacedID{Namespace: "maatplug/reconcile", Value: hex.EncodeToString(sum[:])}
}
func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type Difference struct {
	Usage     *model.NormalizedUsage `json:"usage,omitempty"`
	Available bool                   `json:"available"`
	Reason    string                 `json:"reason,omitempty"`
}

// CumulativeDifference only subtracts counters with identical targets,
// generations and normalized bucket definitions.
func CumulativeDifference(previous, current Decision) Difference {
	if previous.Counter.Kind != model.CounterCumulative || current.Counter.Kind != model.CounterCumulative {
		return Difference{Reason: "both measurements must be cumulative"}
	}
	if entityKey(previous.AppliesTo) != entityKey(current.AppliesTo) || previous.Counter.Generation == "" || previous.Counter.Generation != current.Counter.Generation {
		return Difference{Reason: "target or cumulative generation differs"}
	}
	if previous.Normalized == nil || current.Normalized == nil {
		return Difference{Reason: "normalized usage is missing"}
	}
	prev := bucketMap(previous.Normalized)
	curr := bucketMap(current.Normalized)
	if len(prev) != len(curr) {
		return Difference{Reason: "bucket definitions differ"}
	}
	result := &model.NormalizedUsage{}
	for _, bucket := range current.Normalized.ExclusiveBuckets {
		p, ok := prev[bucket.Name]
		if !ok || bucket.Tokens.Value == nil || p == nil || *bucket.Tokens.Value < *p {
			return Difference{Reason: "counter reset, missing bucket, or decreasing value"}
		}
		value := *bucket.Tokens.Value - *p
		result.ExclusiveBuckets = append(result.ExclusiveBuckets, model.UsageBucket{Name: bucket.Name, Tokens: observed(value)})
	}
	return Difference{Usage: result, Available: true}
}

func bucketMap(usage *model.NormalizedUsage) map[string]*int64 {
	result := map[string]*int64{}
	for _, bucket := range usage.ExclusiveBuckets {
		result[bucket.Name] = bucket.Tokens.Value
	}
	return result
}
func observed(value int64) model.Quantity {
	return model.Quantity{Value: &value, Availability: model.AvailabilityObserved}
}

type ChangeSet struct {
	FromRule   string               `json:"fromRule"`
	ToRule     string               `json:"toRule"`
	Added      []model.NamespacedID `json:"added,omitempty"`
	Removed    []model.NamespacedID `json:"removed,omitempty"`
	TokenDelta int64                `json:"tokenDelta"`
}

func Compare(oldResult, newResult Result) ChangeSet {
	change := ChangeSet{FromRule: oldResult.RuleVersion, ToRule: newResult.RuleVersion, TokenDelta: newResult.KnownSubtotal - oldResult.KnownSubtotal}
	old := map[string]bool{}
	for _, id := range oldResult.SelectedEventIDs {
		old[eventKey(id)] = true
	}
	next := map[string]bool{}
	for _, id := range newResult.SelectedEventIDs {
		next[eventKey(id)] = true
		if !old[eventKey(id)] {
			change.Added = append(change.Added, id)
		}
	}
	for _, id := range oldResult.SelectedEventIDs {
		if !next[eventKey(id)] {
			change.Removed = append(change.Removed, id)
		}
	}
	return change
}
