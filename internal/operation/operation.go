// Package operation rebuilds auditable operation lifecycles from immutable
// provider events. It deliberately keeps requested targets separate from
// effects that a provider actually observed.
package operation

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"maatplug/internal/contentstore"
	"maatplug/internal/model"
)

const RuleVersion = "operation-projection-v1"
const RiskRuleVersion = "operation-risk-v1"

type Result struct {
	RuleVersion     string               `json:"ruleVersion"`
	Operations      []Operation          `json:"operations"`
	Elapsed         []Elapsed            `json:"elapsed"`
	OrphanApprovals []model.NamespacedID `json:"orphanApprovals,omitempty"`
	OrphanArtifacts []model.NamespacedID `json:"orphanArtifacts,omitempty"`
	OrphanResults   []model.NamespacedID `json:"orphanResults,omitempty"`
}

type Operation struct {
	Operation        model.EntityRef           `json:"operation"`
	Provider         string                    `json:"provider"`
	Kind             string                    `json:"kind"`
	Name             string                    `json:"name"`
	Actor            model.EntityRef           `json:"actor"`
	Scope            []model.EntityRef         `json:"scope"`
	RequestEventID   model.NamespacedID        `json:"requestEventId"`
	RequestEvidence  []model.NamespacedID      `json:"requestEvidence"`
	RequestedAt      time.Time                 `json:"requestedAt"`
	FinalStatus      string                    `json:"finalStatus"`
	ExitCode         *int                      `json:"exitCode,omitempty"`
	Async            bool                      `json:"async,omitempty"`
	Environment      Environment               `json:"environment"`
	Risk             RiskAssessment            `json:"risk"`
	RequestedTargets []TargetEvidence          `json:"requestedTargets"`
	ObservedEffects  []Effect                  `json:"observedEffects"`
	Artifacts        []ArtifactEvidence        `json:"artifacts"`
	Instructions     []RelatedEntity           `json:"instructions"`
	States           []Transition              `json:"states"`
	Intervals        []Interval                `json:"intervals"`
	Input            []model.ContentRef        `json:"input,omitempty"`
	Output           []model.ContentRef        `json:"output,omitempty"`
	Validations      []Validation              `json:"validations,omitempty"`
	Missing          []model.FieldAvailability `json:"missing,omitempty"`
}

type EvidenceValue struct {
	Value        string                   `json:"value,omitempty"`
	Availability model.AvailabilityStatus `json:"availability"`
	Reason       string                   `json:"reason,omitempty"`
	Evidence     []model.NamespacedID     `json:"evidence,omitempty"`
}

type Environment struct {
	WorkingDirectory EvidenceValue `json:"workingDirectory"`
	OS               EvidenceValue `json:"os"`
	Shell            EvidenceValue `json:"shell"`
	ExecutionSubject EvidenceValue `json:"executionSubject"`
	PermissionMode   EvidenceValue `json:"permissionMode"`
	Permissions      EvidenceValue `json:"permissions"`
	Sandbox          EvidenceValue `json:"sandbox"`
	NetworkAccess    EvidenceValue `json:"networkAccess"`
}

type TargetEvidence struct {
	Kind         string                   `json:"kind"`
	Value        string                   `json:"value"`
	Protocol     string                   `json:"protocol,omitempty"`
	Availability model.AvailabilityStatus `json:"availability"`
	Method       string                   `json:"method"`
	EventID      model.NamespacedID       `json:"eventId"`
}

type RelatedEntity struct {
	Entity       model.EntityRef          `json:"entity"`
	Availability model.AvailabilityStatus `json:"availability"`
	Method       string                   `json:"method"`
	EventID      model.NamespacedID       `json:"eventId"`
}

type RiskAssessment struct {
	Level        string                   `json:"level,omitempty"`
	RuleVersion  string                   `json:"ruleVersion"`
	Reasons      []string                 `json:"reasons,omitempty"`
	Availability model.AvailabilityStatus `json:"availability"`
	Evidence     []model.NamespacedID     `json:"evidence,omitempty"`
}

type Validation struct {
	Name         string                   `json:"name"`
	Status       string                   `json:"status"`
	Availability model.AvailabilityStatus `json:"availability"`
	RequestEvent model.NamespacedID       `json:"requestEvent"`
	ResultEvent  *model.NamespacedID      `json:"resultEvent,omitempty"`
}

type Effect struct {
	Kind             string                   `json:"kind"`
	Value            string                   `json:"value"`
	Change           string                   `json:"change"`
	Availability     model.AvailabilityStatus `json:"availability"`
	ObservationScope string                   `json:"observationScope"`
	Method           string                   `json:"method"`
	EventID          model.NamespacedID       `json:"eventId"`
}

type ArtifactEvidence struct {
	Artifact    model.EntityRef    `json:"artifact"`
	Kind        string             `json:"kind"`
	Change      string             `json:"change"`
	Locator     *model.ContentRef  `json:"locator,omitempty"`
	Content     *model.ContentRef  `json:"content,omitempty"`
	EventID     model.NamespacedID `json:"eventId"`
	Measurement Measurement        `json:"measurement"`
}

type Measurement struct {
	Method       string         `json:"method"`
	Scope        string         `json:"scope"`
	FilesChanged *int           `json:"filesChanged,omitempty"`
	Added        *int           `json:"added,omitempty"`
	Removed      *int           `json:"removed,omitempty"`
	ByLanguage   map[string]int `json:"byLanguage,omitempty"`
}

type Transition struct {
	State        string                   `json:"state"`
	At           time.Time                `json:"at"`
	Availability model.AvailabilityStatus `json:"availability"`
	EventID      model.NamespacedID       `json:"eventId"`
}

type Interval struct {
	Kind           string                   `json:"kind"`
	Start          time.Time                `json:"start"`
	End            time.Time                `json:"end"`
	DurationMillis int64                    `json:"durationMillis"`
	Availability   model.AvailabilityStatus `json:"availability"`
	Evidence       []model.NamespacedID     `json:"evidence"`
}

type Elapsed struct {
	Kind          string `json:"kind"`
	WallMillis    int64  `json:"wallMillis"`
	IntervalCount int    `json:"intervalCount"`
}

type builder struct {
	contents *contentstore.Store
	byID     map[string]int
	byRun    map[string][]RelatedEntity
	result   Result
}

func Build(root string, events []model.EventEnvelope) (Result, error) {
	b := builder{contents: contentstore.New(root), byID: map[string]int{}, byRun: map[string][]RelatedEntity{}, result: Result{RuleVersion: RuleVersion, Operations: []Operation{}, Elapsed: []Elapsed{}}}
	ordered := append([]model.EventEnvelope(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, z := eventTime(ordered[i]), eventTime(ordered[j])
		if !a.Equal(z) {
			return a.Before(z)
		}
		return idKey(ordered[i].EventID) < idKey(ordered[j].EventID)
	})
	b.indexInstructions(ordered)
	for _, event := range ordered {
		if event.EventType == model.EventOperationRequested {
			if err := b.request(event); err != nil {
				return Result{}, err
			}
		}
	}
	for _, event := range ordered {
		switch event.EventType {
		case model.EventApprovalResolved:
			if err := b.approval(event); err != nil {
				return Result{}, err
			}
		case model.EventOperationResult:
			if err := b.operationResult(event); err != nil {
				return Result{}, err
			}
		case model.EventStateChanged:
			if err := b.state(event); err != nil {
				return Result{}, err
			}
		case model.EventArtifactObserved:
			if err := b.artifact(event); err != nil {
				return Result{}, err
			}
		}
	}
	b.finish()
	return b.result, nil
}

func (b *builder) request(event model.EventEnvelope) error {
	var payload model.OperationPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	key := refKey(payload.Operation)
	if existing, exists := b.byID[key]; exists {
		op := &b.result.Operations[existing]
		op.RequestEvidence = appendUniqueID(op.RequestEvidence, event.EventID)
		op.Input = appendUniqueContent(op.Input, payload.Input...)
		if normalized := commonKind(payload.Kind, payload.Name); op.Kind == "tool" && normalized != "tool" {
			op.Kind = normalized
		}
		if payload.Risk != nil && riskRank(payload.Risk.Level) >= riskRank(op.Risk.Level) {
			op.Risk = RiskAssessment{Level: payload.Risk.Level, RuleVersion: payload.Risk.RuleVersion, Reasons: append([]string(nil), payload.Risk.Reasons...), Availability: model.AvailabilityProviderReported, Evidence: []model.NamespacedID{event.EventID}}
		}
		if payload.RequestedTarget != nil {
			addTarget(op, TargetEvidence{Kind: payload.RequestedTarget.Kind, Value: payload.RequestedTarget.Value, Protocol: payload.RequestedTarget.Protocol, Availability: model.AvailabilityProviderReported, Method: "provider operation target", EventID: event.EventID})
		}
		b.readInputs(op, payload.Input, event.EventID)
		return nil
	}
	op := Operation{Operation: payload.Operation, Provider: event.Source.Provider, Kind: commonKind(payload.Kind, payload.Name), Name: payload.Name, Actor: payload.Actor, Scope: append([]model.EntityRef(nil), event.Scope...), RequestEventID: event.EventID, RequestEvidence: []model.NamespacedID{event.EventID}, RequestedAt: eventTime(event), FinalStatus: "requested", RequestedTargets: []TargetEvidence{}, ObservedEffects: []Effect{}, Artifacts: []ArtifactEvidence{}, Instructions: append([]RelatedEntity(nil), b.byRun[runKey(event)]...), States: []Transition{{State: "requested", At: eventTime(event), Availability: model.AvailabilityObserved, EventID: event.EventID}}, Intervals: []Interval{}, Input: append([]model.ContentRef(nil), payload.Input...)}
	op.Environment = unknownEnvironment(payload.Actor, event.EventID)
	op.Risk = RiskAssessment{RuleVersion: RiskRuleVersion, Availability: model.AvailabilityUnknown, Reasons: []string{"operation input did not contain enough information for risk classification"}}
	if payload.Risk != nil {
		op.Risk = RiskAssessment{Level: payload.Risk.Level, RuleVersion: payload.Risk.RuleVersion, Reasons: append([]string(nil), payload.Risk.Reasons...), Availability: model.AvailabilityProviderReported, Evidence: []model.NamespacedID{event.EventID}}
	}
	if payload.RequestedTarget != nil {
		op.RequestedTargets = append(op.RequestedTargets, TargetEvidence{Kind: payload.RequestedTarget.Kind, Value: payload.RequestedTarget.Value, Protocol: payload.RequestedTarget.Protocol, Availability: model.AvailabilityProviderReported, Method: "provider operation target", EventID: event.EventID})
	}
	b.readInputs(&op, payload.Input, event.EventID)
	if op.Kind == "shell" && op.Environment.Shell.Availability == model.AvailabilityUnknown {
		setEvidence(&op.Environment.Shell, payload.Name, model.AvailabilityInferred, "inferred from operation tool name", event.EventID)
	}
	b.result.Operations = append(b.result.Operations, op)
	b.byID[key] = len(b.result.Operations) - 1
	return nil
}

func (b *builder) indexInstructions(events []model.EventEnvelope) {
	for _, event := range events {
		var instruction model.EntityRef
		switch event.EventType {
		case model.EventInstructionRecorded:
			var payload model.InstructionPayload
			if json.Unmarshal(event.Payload, &payload) != nil {
				continue
			}
			instruction = payload.Instruction
		case model.EventModelCall:
			var payload model.ModelCallPayload
			if json.Unmarshal(event.Payload, &payload) != nil {
				continue
			}
			instruction = payload.Instruction
		default:
			continue
		}
		key := runKey(event)
		if key == "" {
			continue
		}
		duplicate := false
		for _, existing := range b.byRun[key] {
			if existing.Entity == instruction {
				duplicate = true
				break
			}
		}
		if !duplicate {
			b.byRun[key] = append(b.byRun[key], RelatedEntity{Entity: instruction, Availability: model.AvailabilityInferred, Method: "shared provider run scope", EventID: event.EventID})
		}
	}
}

func (b *builder) approval(event model.EventEnvelope) error {
	var payload model.ApprovalPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	if payload.Operation == nil {
		b.result.OrphanApprovals = append(b.result.OrphanApprovals, event.EventID)
		return nil
	}
	op := b.find(*payload.Operation)
	if op == nil {
		b.result.OrphanApprovals = append(b.result.OrphanApprovals, event.EventID)
		return nil
	}
	state := strings.ToLower(payload.Decision)
	if state == "requested" {
		state = "approval_pending"
	}
	op.States = append(op.States, Transition{State: state, At: eventTime(event), Availability: model.AvailabilityProviderReported, EventID: event.EventID})
	if state == "denied" {
		op.FinalStatus = "denied"
	}
	return nil
}

func (b *builder) operationResult(event model.EventEnvelope) error {
	var payload model.OperationResultPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	op := b.find(payload.Operation)
	if op == nil {
		b.result.OrphanResults = append(b.result.OrphanResults, event.EventID)
		return nil
	}
	at := eventTime(event)
	status := strings.ToLower(payload.Status)
	if status == "" {
		status = "unknown"
	}
	if status != "denied" && status != "rejected" {
		start := at
		basis := model.AvailabilityInferred
		measured := true
		if payload.DurationMillis != nil {
			start = at.Add(-time.Duration(*payload.DurationMillis) * time.Millisecond)
		} else if !op.RequestedAt.IsZero() && op.RequestedAt.Before(at) {
			start = op.RequestedAt
			basis = model.AvailabilityInferred
		} else {
			basis = model.AvailabilityUnknown
			measured = false
			op.Missing = append(op.Missing, model.FieldAvailability{Path: "/intervals/tool", Status: model.AvailabilityUnknown, Reason: "provider supplied neither a duration nor distinct request and result timestamps"})
		}
		op.States = append(op.States, Transition{State: "started", At: start, Availability: basis, EventID: event.EventID})
		if measured {
			duration := at.Sub(start).Milliseconds()
			op.Intervals = append(op.Intervals, Interval{Kind: "tool", Start: start, End: at, DurationMillis: duration, Availability: basis, Evidence: []model.NamespacedID{event.EventID}})
		}
	}
	if payload.Async {
		status = "async_" + status
	}
	op.States = append(op.States, Transition{State: status, At: at, Availability: model.AvailabilityProviderReported, EventID: event.EventID})
	op.FinalStatus, op.ExitCode, op.Async, op.Output = status, payload.ExitCode, payload.Async, append([]model.ContentRef(nil), payload.Output...)
	for index := range op.Validations {
		op.Validations[index].Status = status
		op.Validations[index].Availability = model.AvailabilityInferred
		op.Validations[index].ResultEvent = &event.EventID
	}
	if !op.RequestedAt.IsZero() && at.After(op.RequestedAt) {
		op.Intervals = append(op.Intervals, Interval{Kind: "operation_wall", Start: op.RequestedAt, End: at, DurationMillis: at.Sub(op.RequestedAt).Milliseconds(), Availability: model.AvailabilityObserved, Evidence: []model.NamespacedID{op.RequestEventID, event.EventID}})
	}
	return nil
}

func (b *builder) state(event model.EventEnvelope) error {
	var payload model.StateTransitionPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	op := b.find(payload.Subject)
	if op == nil {
		return nil
	}
	op.States = append(op.States, Transition{State: payload.To, At: eventTime(event), Availability: model.AvailabilityProviderReported, EventID: event.EventID})
	op.FinalStatus = payload.To
	return nil
}

func (b *builder) artifact(event model.EventEnvelope) error {
	var payload model.ArtifactPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	var op *Operation
	for _, relation := range event.Relations {
		if relation.Kind == model.RelationResultOf {
			if relation.From.Kind == model.EntityArtifact {
				op = b.find(relation.To)
			}
			if relation.To.Kind == model.EntityArtifact {
				op = b.find(relation.From)
			}
		}
	}
	if op == nil {
		b.result.OrphanArtifacts = append(b.result.OrphanArtifacts, event.EventID)
		return nil
	}
	measurement := Measurement{Method: "provider artifact record", Scope: "reported artifact"}
	if payload.Content != nil {
		if data, err := b.contents.Get(*payload.Content); err == nil && strings.Contains(payload.Content.MediaType, "diff") {
			measurement = measureDiff(data, payload.Locator, b.contents)
		} else if err != nil {
			op.Missing = append(op.Missing, model.FieldAvailability{Path: "/artifacts/content", Status: model.AvailabilityUnknown, Reason: "artifact content object is unavailable", Evidence: []model.NamespacedID{event.EventID}})
		}
	}
	op.Artifacts = append(op.Artifacts, ArtifactEvidence{Artifact: payload.Artifact, Kind: payload.Kind, Change: payload.Change, Locator: payload.Locator, Content: payload.Content, EventID: event.EventID, Measurement: measurement})
	value := payload.Artifact.ID.Value
	if payload.Locator != nil {
		if data, err := b.contents.Get(*payload.Locator); err == nil {
			value = string(data)
		} else {
			op.Missing = append(op.Missing, model.FieldAvailability{Path: "/artifacts/locator", Status: model.AvailabilityUnknown, Reason: "artifact locator content object is unavailable", Evidence: []model.NamespacedID{event.EventID}})
		}
	}
	op.ObservedEffects = append(op.ObservedEffects, Effect{Kind: payload.Kind, Value: value, Change: payload.Change, Availability: model.AvailabilityProviderReported, ObservationScope: "provider artifact notification", Method: measurement.Method, EventID: event.EventID})
	return nil
}

func (b *builder) find(ref model.EntityRef) *Operation {
	index, ok := b.byID[refKey(ref)]
	if !ok {
		return nil
	}
	return &b.result.Operations[index]
}

func (b *builder) readInputs(op *Operation, refs []model.ContentRef, eventID model.NamespacedID) {
	for _, ref := range refs {
		data, err := b.contents.Get(ref)
		if err != nil {
			op.Missing = append(op.Missing, model.FieldAvailability{Path: "/requestedTargets", Status: model.AvailabilityUnknown, Reason: "operation input content is unavailable", Evidence: []model.NamespacedID{eventID}})
			continue
		}
		var value any
		if json.Unmarshal(data, &value) != nil {
			continue
		}
		b.inspectValue(op, value, eventID, "tool input")
	}
}

func (b *builder) inspectValue(op *Operation, value any, eventID model.NamespacedID, method string) {
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	if environment, ok := object["environment"].(map[string]any); ok {
		setEnvironment(&op.Environment, environment, eventID)
	}
	for _, key := range []string{"cwd", "working_directory", "workingDirectory"} {
		if text, ok := object[key].(string); ok && text != "" {
			setEvidence(&op.Environment.WorkingDirectory, text, model.AvailabilityProviderReported, "provider operation context", eventID)
		}
	}
	if parsed, ok := object["parsedCommand"].([]any); ok {
		for _, item := range parsed {
			entry, _ := item.(map[string]any)
			path, _ := entry["path"].(string)
			kind, _ := entry["type"].(string)
			if path != "" {
				addTarget(op, TargetEvidence{Kind: targetKind(kind), Value: path, Availability: model.AvailabilityProviderReported, Method: "provider parsed command", EventID: eventID})
			}
		}
	}
	for _, key := range []string{"file_path", "path", "notebook_path"} {
		if text, ok := object[key].(string); ok && text != "" {
			addTarget(op, TargetEvidence{Kind: "file", Value: text, Availability: model.AvailabilityProviderReported, Method: method + " field " + key, EventID: eventID})
		}
	}
	if paths, ok := object["paths"].([]any); ok {
		for _, raw := range paths {
			if text, ok := raw.(string); ok && text != "" {
				addTarget(op, TargetEvidence{Kind: "file", Value: text, Availability: model.AvailabilityProviderReported, Method: method + " paths", EventID: eventID})
			}
		}
	}
	for _, key := range []string{"url", "uri", "endpoint"} {
		if text, ok := object[key].(string); ok && text != "" {
			addURLTarget(op, text, model.AvailabilityProviderReported, method+" field "+key, eventID)
		}
	}
	if command, ok := object["command"].([]any); ok {
		args := make([]string, 0, len(command))
		for _, item := range command {
			if text, ok := item.(string); ok {
				args = append(args, text)
			}
		}
		inspectCommand(op, args, eventID)
	}
	if command, ok := object["command"].(string); ok {
		inspectCommandText(op, command, eventID)
	}
	if toolInput, ok := object["toolInput"]; ok {
		b.inspectValue(op, toolInput, eventID, "provider tool input")
	}
}

func (b *builder) finish() {
	for index := range b.result.Operations {
		op := &b.result.Operations[index]
		if len(op.ObservedEffects) == 0 {
			op.Missing = append(op.Missing, model.FieldAvailability{Path: "/observedEffects", Status: model.AvailabilityUnknown, Reason: "no operation-correlated effect observation was collected"})
		}
		for _, target := range op.RequestedTargets {
			if target.Kind == "network_endpoint" && !hasEffectKind(op.ObservedEffects, "network_endpoint") {
				op.Missing = append(op.Missing, model.FieldAvailability{Path: "/observedEffects/networkEndpoints", Status: model.AvailabilityUnknown, Reason: "a network endpoint was inferred from the request but no network telemetry was collected"})
				break
			}
		}
		sort.SliceStable(op.States, func(i, j int) bool { return op.States[i].At.Before(op.States[j].At) })
		observedStates := append([]Transition(nil), op.States...)
		for i, state := range observedStates {
			if state.State != "approval_pending" {
				continue
			}
			hasResolution := false
			for _, resolved := range observedStates[i+1:] {
				if resolved.State == "allowed" || resolved.State == "approved" || resolved.State == "denied" || resolved.State == "rejected" {
					hasResolution = true
					op.Intervals = append(op.Intervals, Interval{Kind: "approval_wait", Start: state.At, End: resolved.At, DurationMillis: resolved.At.Sub(state.At).Milliseconds(), Availability: resolved.Availability, Evidence: []model.NamespacedID{state.EventID, resolved.EventID}})
					break
				}
			}
			if !hasResolution {
				for _, next := range observedStates[i+1:] {
					if next.State == "requested" || next.State == "started" {
						op.States = append(op.States, Transition{State: "approved", At: next.At, Availability: model.AvailabilityInferred, EventID: next.EventID})
						op.Intervals = append(op.Intervals, Interval{Kind: "approval_wait", Start: state.At, End: next.At, DurationMillis: next.At.Sub(state.At).Milliseconds(), Availability: model.AvailabilityInferred, Evidence: []model.NamespacedID{state.EventID, next.EventID}})
						break
					}
				}
			}
		}
		if op.FinalStatus == "requested" {
			op.Missing = append(op.Missing, model.FieldAvailability{Path: "/states/completion", Status: model.AvailabilityUnknown, Reason: "no correlated operation result or terminal transition was collected"})
		}
		sort.SliceStable(op.States, func(i, j int) bool { return op.States[i].At.Before(op.States[j].At) })
		sort.Slice(op.RequestedTargets, func(i, j int) bool {
			return op.RequestedTargets[i].Kind+op.RequestedTargets[i].Value < op.RequestedTargets[j].Kind+op.RequestedTargets[j].Value
		})
	}
	sort.Slice(b.result.Operations, func(i, j int) bool {
		if !b.result.Operations[i].RequestedAt.Equal(b.result.Operations[j].RequestedAt) {
			return b.result.Operations[i].RequestedAt.Before(b.result.Operations[j].RequestedAt)
		}
		return refKey(b.result.Operations[i].Operation) < refKey(b.result.Operations[j].Operation)
	})
	byKind := map[string][]Interval{}
	for _, op := range b.result.Operations {
		for _, interval := range op.Intervals {
			byKind[interval.Kind] = append(byKind[interval.Kind], interval)
		}
	}
	for kind, intervals := range byKind {
		b.result.Elapsed = append(b.result.Elapsed, Elapsed{Kind: kind, WallMillis: unionMillis(intervals), IntervalCount: len(intervals)})
	}
	sort.Slice(b.result.Elapsed, func(i, j int) bool { return b.result.Elapsed[i].Kind < b.result.Elapsed[j].Kind })
	sort.Slice(b.result.OrphanApprovals, func(i, j int) bool { return idKey(b.result.OrphanApprovals[i]) < idKey(b.result.OrphanApprovals[j]) })
	sort.Slice(b.result.OrphanArtifacts, func(i, j int) bool { return idKey(b.result.OrphanArtifacts[i]) < idKey(b.result.OrphanArtifacts[j]) })
	sort.Slice(b.result.OrphanResults, func(i, j int) bool { return idKey(b.result.OrphanResults[i]) < idKey(b.result.OrphanResults[j]) })
}

func unknownEnvironment(actor model.EntityRef, eventID model.NamespacedID) Environment {
	unknown := func(reason string) EvidenceValue {
		return EvidenceValue{Availability: model.AvailabilityUnknown, Reason: reason}
	}
	return Environment{WorkingDirectory: unknown("provider operation record did not expose a working directory"), OS: unknown("OS was not reported for this operation"), Shell: unknown("shell was not reported or inferable"), ExecutionSubject: EvidenceValue{Value: actor.ID.Namespace + ":" + actor.ID.Value, Availability: model.AvailabilityObserved, Evidence: []model.NamespacedID{eventID}}, PermissionMode: unknown("permission mode was not reported for this operation"), Permissions: unknown("permission profile was not reported for this operation"), Sandbox: unknown("sandbox was not reported for this operation"), NetworkAccess: unknown("network permission was not reported for this operation")}
}

func setEnvironment(environment *Environment, values map[string]any, eventID model.NamespacedID) {
	fields := []struct {
		names  []string
		target *EvidenceValue
	}{{[]string{"cwd", "workingDirectory"}, &environment.WorkingDirectory}, {[]string{"os"}, &environment.OS}, {[]string{"shell"}, &environment.Shell}, {[]string{"permissionMode", "approvalPolicy"}, &environment.PermissionMode}, {[]string{"permissions", "permissionProfile"}, &environment.Permissions}, {[]string{"sandbox", "sandboxPolicy"}, &environment.Sandbox}, {[]string{"networkAccess"}, &environment.NetworkAccess}}
	for _, field := range fields {
		for _, name := range field.names {
			if raw, exists := values[name]; exists {
				text := stringify(raw)
				if text != "" {
					setEvidence(field.target, text, model.AvailabilityProviderReported, "provider operation context", eventID)
					break
				}
			}
		}
	}
}

func setEvidence(target *EvidenceValue, value string, status model.AvailabilityStatus, reason string, eventID model.NamespacedID) {
	target.Value, target.Availability, target.Reason, target.Evidence = value, status, reason, []model.NamespacedID{eventID}
}

func stringify(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		data, _ := json.Marshal(v)
		return string(data)
	}
}

func addTarget(op *Operation, target TargetEvidence) {
	for _, existing := range op.RequestedTargets {
		if existing.Kind == target.Kind && existing.Value == target.Value {
			return
		}
	}
	op.RequestedTargets = append(op.RequestedTargets, target)
}

func appendUniqueID(values []model.NamespacedID, value model.NamespacedID) []model.NamespacedID {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendUniqueContent(values []model.ContentRef, added ...model.ContentRef) []model.ContentRef {
	for _, value := range added {
		found := false
		for _, existing := range values {
			if existing.ID == value.ID {
				found = true
				break
			}
		}
		if !found {
			values = append(values, value)
		}
	}
	return values
}

func addURLTarget(op *Operation, raw string, status model.AvailabilityStatus, method string, eventID model.NamespacedID) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return
	}
	addTarget(op, TargetEvidence{Kind: "network_endpoint", Value: parsed.Hostname(), Protocol: parsed.Scheme, Availability: status, Method: method, EventID: eventID})
}

var urlPattern = regexp.MustCompile(`https?://[^\s"']+`)

func inspectCommand(op *Operation, args []string, eventID model.NamespacedID) {
	if len(args) > 0 {
		shell := filepath.Base(args[0])
		setEvidence(&op.Environment.Shell, shell, model.AvailabilityInferred, "inferred from command argv[0]", eventID)
		if strings.Contains(args[0], "\\") {
			setEvidence(&op.Environment.OS, "windows", model.AvailabilityInferred, "inferred from executable path", eventID)
		} else if strings.HasPrefix(args[0], "/") {
			setEvidence(&op.Environment.OS, "unix", model.AvailabilityInferred, "inferred from executable path", eventID)
		}
	}
	if len(args) > 0 {
		assessCommand(op, strings.Join(args, " "), eventID)
		if name := validationCommand(strings.Join(args, " ")); name != "" {
			op.Validations = append(op.Validations, Validation{Name: name, Status: "requested", Availability: model.AvailabilityInferred, RequestEvent: eventID})
		}
	}
	for _, arg := range args {
		addURLTarget(op, arg, model.AvailabilityInferred, "parsed from requested command argument", eventID)
		inspectCommandText(op, arg, eventID)
	}
}

func inspectCommandText(op *Operation, command string, eventID model.NamespacedID) {
	assessCommand(op, command, eventID)
	if name := validationCommand(command); name != "" && !hasValidation(op.Validations, name) {
		op.Validations = append(op.Validations, Validation{Name: name, Status: "requested", Availability: model.AvailabilityInferred, RequestEvent: eventID})
	}
	for _, match := range urlPattern.FindAllString(command, -1) {
		addURLTarget(op, strings.TrimRight(match, ").,;"), model.AvailabilityInferred, "parsed from requested command text", eventID)
	}
}

func assessCommand(op *Operation, command string, eventID model.NamespacedID) {
	lower := strings.ToLower(command)
	high := []struct{ pattern, reason string }{
		{"rm -rf", "recursive forced deletion requested"},
		{"remove-item -recurse", "recursive deletion requested"},
		{"git reset --hard", "destructive git reset requested"},
		{"git clean -f", "forced removal of untracked files requested"},
		{"drop table", "destructive database statement requested"},
	}
	medium := []struct{ pattern, reason string }{
		{"sudo ", "privilege elevation requested"},
		{"chmod 777", "broad filesystem permissions requested"},
		{"force", "force option or operation requested"},
	}
	level := "low"
	reasons := []string{"no configured high or medium risk command pattern matched"}
	for _, rule := range medium {
		if strings.Contains(lower, rule.pattern) {
			level, reasons = "medium", []string{rule.reason}
		}
	}
	for _, rule := range high {
		if strings.Contains(lower, rule.pattern) {
			level, reasons = "high", []string{rule.reason}
			break
		}
	}
	if riskRank(level) >= riskRank(op.Risk.Level) || op.Risk.Availability == model.AvailabilityUnknown {
		op.Risk = RiskAssessment{Level: level, RuleVersion: RiskRuleVersion, Reasons: reasons, Availability: model.AvailabilityInferred, Evidence: []model.NamespacedID{eventID}}
	}
}

func validationCommand(command string) string {
	lower := strings.ToLower(command)
	for _, candidate := range []string{"go test", "go vet", "npm test", "npm run build", "pytest", "cargo test", "cargo check", "dotnet test", "mvn test"} {
		if strings.Contains(lower, candidate) {
			return candidate
		}
	}
	return ""
}

func riskRank(level string) int {
	switch level {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func hasValidation(values []Validation, name string) bool {
	for _, value := range values {
		if value.Name == name {
			return true
		}
	}
	return false
}

func hasEffectKind(values []Effect, kind string) bool {
	for _, value := range values {
		if value.Kind == kind {
			return true
		}
	}
	return false
}

func commonKind(kind, name string) string {
	value := strings.ToLower(kind + " " + name)
	switch {
	case strings.Contains(value, "shell") || strings.Contains(value, "bash") || strings.Contains(value, "commandexecution"):
		return "shell"
	case strings.Contains(value, "edit") || strings.Contains(value, "write") || strings.Contains(value, "patch"):
		return "edit"
	case strings.Contains(value, "browser") || strings.Contains(value, "chrome"):
		return "browser"
	case strings.Contains(value, "mcp") || strings.Contains(value, "connector"):
		return "connector"
	case strings.Contains(value, "http") || strings.Contains(value, "api") || strings.Contains(value, "webfetch") || strings.Contains(value, "web_search") || strings.Contains(value, "web__"):
		return "external_api"
	default:
		return kind
	}
}

func targetKind(providerKind string) string {
	switch strings.ToLower(providerKind) {
	case "read", "write", "update", "delete":
		return "file"
	default:
		return "command_target"
	}
}

func measureDiff(data []byte, locator *model.ContentRef, contents *contentstore.Store) Measurement {
	added, removed := 0, 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added++
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removed++
		}
	}
	files := 1
	byLanguage := map[string]int{}
	if locator != nil {
		if raw, err := contents.Get(*locator); err == nil {
			if language := languageForPath(string(raw)); language != "" {
				byLanguage[language] = added - removed
			}
		}
	}
	return Measurement{Method: "unified_diff_line_prefix_v1", Scope: "provider-reported artifact content", FilesChanged: &files, Added: &added, Removed: &removed, ByLanguage: byLanguage}
}

func languageForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "Go"
	case ".js", ".mjs", ".cjs":
		return "JavaScript"
	case ".ts", ".tsx":
		return "TypeScript"
	case ".py":
		return "Python"
	case ".md":
		return "Markdown"
	case ".json":
		return "JSON"
	case ".html":
		return "HTML"
	case ".css":
		return "CSS"
	default:
		return ""
	}
}

func unionMillis(intervals []Interval) int64 {
	if len(intervals) == 0 {
		return 0
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].Start.Before(intervals[j].Start) })
	start, end := intervals[0].Start, intervals[0].End
	var total time.Duration
	for _, interval := range intervals[1:] {
		if !interval.Start.After(end) {
			if interval.End.After(end) {
				end = interval.End
			}
			continue
		}
		total += end.Sub(start)
		start, end = interval.Start, interval.End
	}
	total += end.Sub(start)
	return total.Milliseconds()
}

func eventTime(event model.EventEnvelope) time.Time {
	if event.Time.ProviderAt != nil {
		return *event.Time.ProviderAt
	}
	if event.Time.ObservedAt != nil {
		return *event.Time.ObservedAt
	}
	return event.Time.ReceivedAt
}
func refKey(ref model.EntityRef) string  { return string(ref.Kind) + ":" + idKey(ref.ID) }
func idKey(id model.NamespacedID) string { return id.Namespace + ":" + id.Value }

func runKey(event model.EventEnvelope) string {
	for _, scope := range event.Scope {
		if scope.Kind == model.EntityRun {
			return event.Source.Provider + "|" + scope.ID.Value
		}
	}
	return ""
}
