package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// SchemaVersion is the first version of the provider-independent event
// contract. It is intentionally unrelated to provider transcript versions.
const SchemaVersion = 1

// EventKind selects the concrete shape stored in EventEnvelope.Payload.
type EventKind string

const (
	EventInstructionRecorded EventKind = "instruction.recorded"
	EventActorObserved       EventKind = "actor.observed"
	EventModelCall           EventKind = "model.call"
	EventModelAttempt        EventKind = "model.attempt"
	EventUsageReported       EventKind = "usage.reported"
	EventInputObserved       EventKind = "input.observed"
	EventOperationRequested  EventKind = "operation.requested"
	EventApprovalResolved    EventKind = "approval.resolved"
	EventOperationResult     EventKind = "operation.result"
	EventStateChanged        EventKind = "state.changed"
	EventArtifactObserved    EventKind = "artifact.observed"
	EventPricingPublished    EventKind = "pricing.published"
	EventSpendReported       EventKind = "spend.reported"
	EventCollectionState     EventKind = "collection.state"
)

var eventKinds = []EventKind{
	EventInstructionRecorded,
	EventActorObserved,
	EventModelCall,
	EventModelAttempt,
	EventUsageReported,
	EventInputObserved,
	EventOperationRequested,
	EventApprovalResolved,
	EventOperationResult,
	EventStateChanged,
	EventArtifactObserved,
	EventPricingPublished,
	EventSpendReported,
	EventCollectionState,
}

// EventKinds returns a copy of all event kinds defined by this schema version.
func EventKinds() []EventKind {
	return append([]EventKind(nil), eventKinds...)
}

func validEventKind(kind EventKind) bool {
	for _, candidate := range eventKinds {
		if kind == candidate {
			return true
		}
	}
	return false
}

// NamespacedID prevents IDs issued by different providers or subsystems from
// becoming equal merely because their string values happen to match.
type NamespacedID struct {
	Namespace string `json:"namespace"`
	Value     string `json:"value"`
}

func (id NamespacedID) validate(path string) error {
	if strings.TrimSpace(id.Namespace) == "" {
		return fmt.Errorf("%s.namespace is required", path)
	}
	if strings.TrimSpace(id.Value) == "" {
		return fmt.Errorf("%s.value is required", path)
	}
	return nil
}

type EntityKind string

const (
	EntityProject      EntityKind = "project"
	EntityRun          EntityKind = "run"
	EntitySession      EntityKind = "session"
	EntityThread       EntityKind = "thread"
	EntityInstruction  EntityKind = "instruction"
	EntityTurn         EntityKind = "turn"
	EntityActor        EntityKind = "actor"
	EntityAgent        EntityKind = "agent"
	EntityModelCall    EntityKind = "model_call"
	EntityModelAttempt EntityKind = "model_attempt"
	EntityResponse     EntityKind = "response"
	EntityUsage        EntityKind = "usage"
	EntityInputElement EntityKind = "input_element"
	EntityOperation    EntityKind = "operation"
	EntityApproval     EntityKind = "approval"
	EntityArtifact     EntityKind = "artifact"
	EntityPrice        EntityKind = "price"
	EntitySpend        EntityKind = "spend"
	EntityStream       EntityKind = "stream"
)

type EntityRef struct {
	Kind EntityKind   `json:"kind"`
	ID   NamespacedID `json:"id"`
}

func (ref EntityRef) validate(path string) error {
	if ref.Kind == "" {
		return fmt.Errorf("%s.kind is required", path)
	}
	return ref.ID.validate(path + ".id")
}

type SourceKind string

const (
	SourceHook       SourceKind = "hook"
	SourceTelemetry  SourceKind = "telemetry"
	SourceTranscript SourceKind = "transcript"
	SourceCLI        SourceKind = "cli_output"
	SourceAppServer  SourceKind = "app_server"
	SourceCollector  SourceKind = "collector"
	SourceVCS        SourceKind = "vcs"
	SourceOS         SourceKind = "os"
	SourcePricing    SourceKind = "pricing"
	SourceBilling    SourceKind = "billing"
	SourceUser       SourceKind = "user"
)

// EventSource describes the observation surface separately from the engine
// that produced it. CLI and extension bundles often carry different engines.
type EventSource struct {
	Provider       string        `json:"provider"`
	Surface        string        `json:"surface"`
	SurfaceVersion string        `json:"surfaceVersion,omitempty"`
	EngineVersion  string        `json:"engineVersion,omitempty"`
	Kind           SourceKind    `json:"kind"`
	RecordID       *NamespacedID `json:"recordId,omitempty"`
}

func (source EventSource) validate() error {
	if strings.TrimSpace(source.Provider) == "" {
		return errors.New("source.provider is required")
	}
	if strings.TrimSpace(source.Surface) == "" {
		return errors.New("source.surface is required")
	}
	if source.Kind == "" {
		return errors.New("source.kind is required")
	}
	if source.RecordID != nil {
		return source.RecordID.validate("source.recordId")
	}
	return nil
}

// EventTime keeps the provider clock, observer clock, and collector receipt
// clock distinct. ReceivedAt is the only required timestamp.
type EventTime struct {
	ProviderAt *time.Time `json:"providerAt,omitempty"`
	ObservedAt *time.Time `json:"observedAt,omitempty"`
	ReceivedAt time.Time  `json:"receivedAt"`
}

// EventSequence identifies a position in a replaceable or rotating stream.
// Position is a pointer because zero is a valid first position.
type EventSequence struct {
	Stream     NamespacedID `json:"stream"`
	Generation string       `json:"generation"`
	Position   *uint64      `json:"position,omitempty"`
	Provider   string       `json:"providerSequence,omitempty"`
}

func (sequence EventSequence) validate() error {
	if err := sequence.Stream.validate("sequence.stream"); err != nil {
		return err
	}
	if strings.TrimSpace(sequence.Generation) == "" {
		return errors.New("sequence.generation is required")
	}
	return nil
}

type AvailabilityStatus string

const (
	AvailabilityObserved         AvailabilityStatus = "observed"
	AvailabilityProviderReported AvailabilityStatus = "provider_reported"
	AvailabilityInferred         AvailabilityStatus = "inferred"
	AvailabilityNotCollected     AvailabilityStatus = "not_collected"
	AvailabilityUnavailable      AvailabilityStatus = "unavailable"
	AvailabilityUnknown          AvailabilityStatus = "unknown"
	AvailabilityConflict         AvailabilityStatus = "conflict"
)

func validAvailability(status AvailabilityStatus) bool {
	switch status {
	case AvailabilityObserved, AvailabilityProviderReported, AvailabilityInferred,
		AvailabilityNotCollected, AvailabilityUnavailable, AvailabilityUnknown,
		AvailabilityConflict:
		return true
	default:
		return false
	}
}

// FieldAvailability records absence and uncertainty explicitly. Path is a
// JSON Pointer into the logical event, not a provider field name.
type FieldAvailability struct {
	Path     string             `json:"path"`
	Status   AvailabilityStatus `json:"status"`
	Reason   string             `json:"reason,omitempty"`
	Evidence []NamespacedID     `json:"evidence,omitempty"`
}

func (availability FieldAvailability) validate(path string) error {
	if !strings.HasPrefix(availability.Path, "/") {
		return fmt.Errorf("%s.path must be a JSON Pointer", path)
	}
	if !validAvailability(availability.Status) {
		return fmt.Errorf("%s.status is invalid", path)
	}
	if availability.Status != AvailabilityObserved && strings.TrimSpace(availability.Reason) == "" {
		return fmt.Errorf("%s.reason is required for %s", path, availability.Status)
	}
	for index, evidence := range availability.Evidence {
		if err := evidence.validate(fmt.Sprintf("%s.evidence[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

type RelationKind string

const (
	RelationParentOf     RelationKind = "parent_of"
	RelationContains     RelationKind = "contains"
	RelationSharedWith   RelationKind = "shared_with"
	RelationRetryOf      RelationKind = "retry_of"
	RelationCausedBy     RelationKind = "caused_by"
	RelationAttributedTo RelationKind = "attributed_to"
	RelationSupersedes   RelationKind = "supersedes"
	RelationCorrects     RelationKind = "corrects"
	RelationAdopts       RelationKind = "adopts"
	RelationDuplicateOf  RelationKind = "duplicate_of"
	RelationDerivedFrom  RelationKind = "derived_from"
	RelationInputTo      RelationKind = "input_to"
	RelationResultOf     RelationKind = "result_of"
)

type RelationCertainty string

const (
	CertaintyObserved RelationCertainty = "observed"
	CertaintyReported RelationCertainty = "provider_reported"
	CertaintyInferred RelationCertainty = "inferred"
	CertaintyDisputed RelationCertainty = "disputed"
)

// Relation carries topology and usage-containment as separate facts. Includes
// is nil if the source does not say whether one measurement contains another.
type Relation struct {
	Kind      RelationKind      `json:"kind"`
	From      EntityRef         `json:"from"`
	To        EntityRef         `json:"to"`
	Certainty RelationCertainty `json:"certainty"`
	Includes  *bool             `json:"includes,omitempty"`
	Reason    string            `json:"reason,omitempty"`
}

func (relation Relation) validate(path string) error {
	if relation.Kind == "" {
		return fmt.Errorf("%s.kind is required", path)
	}
	if err := relation.From.validate(path + ".from"); err != nil {
		return err
	}
	if err := relation.To.validate(path + ".to"); err != nil {
		return err
	}
	switch relation.Certainty {
	case CertaintyObserved, CertaintyReported, CertaintyInferred, CertaintyDisputed:
	default:
		return fmt.Errorf("%s.certainty is invalid", path)
	}
	if relation.Certainty != CertaintyObserved && strings.TrimSpace(relation.Reason) == "" {
		return fmt.Errorf("%s.reason is required for %s", path, relation.Certainty)
	}
	return nil
}

// Revision records corrections without mutating or hiding the source events.
type Revision struct {
	Number     uint32         `json:"number"`
	Supersedes []NamespacedID `json:"supersedes,omitempty"`
	Corrects   []NamespacedID `json:"corrects,omitempty"`
}

type AdoptionStatus string

const (
	AdoptionCandidate  AdoptionStatus = "candidate"
	AdoptionAdopted    AdoptionStatus = "adopted"
	AdoptionRejected   AdoptionStatus = "rejected"
	AdoptionSuperseded AdoptionStatus = "superseded"
)

// Adoption explains whether a candidate measurement is selected for a
// derived view. Selection can change without changing the original event.
type Adoption struct {
	Status      AdoptionStatus `json:"status"`
	DecisionID  *NamespacedID  `json:"decisionId,omitempty"`
	RuleVersion string         `json:"ruleVersion,omitempty"`
	Reason      string         `json:"reason,omitempty"`
}

func (adoption Adoption) validate() error {
	switch adoption.Status {
	case AdoptionCandidate, AdoptionAdopted, AdoptionRejected, AdoptionSuperseded:
	default:
		return errors.New("adoption.status is invalid")
	}
	if adoption.DecisionID != nil {
		if err := adoption.DecisionID.validate("adoption.decisionId"); err != nil {
			return err
		}
	}
	if adoption.Status != AdoptionCandidate && strings.TrimSpace(adoption.Reason) == "" {
		return fmt.Errorf("adoption.reason is required for %s", adoption.Status)
	}
	return nil
}

type ContentVariant string

const (
	ContentRaw      ContentVariant = "raw"
	ContentRedacted ContentVariant = "redacted"
	ContentSummary  ContentVariant = "summary"
	ContentHashOnly ContentVariant = "hash_only"
	ContentOmitted  ContentVariant = "omitted"
	ContentDeleted  ContentVariant = "deleted"
)

// ContentRef refers to content stored outside the event stream. No inline
// prompt, tool output, or secret is required in the event envelope.
type ContentRef struct {
	ID           NamespacedID       `json:"id"`
	Variant      ContentVariant     `json:"variant"`
	MediaType    string             `json:"mediaType,omitempty"`
	ByteLength   *int64             `json:"byteLength,omitempty"`
	SHA256       string             `json:"sha256,omitempty"`
	Availability AvailabilityStatus `json:"availability"`
	Reason       string             `json:"reason,omitempty"`
	DerivedFrom  *NamespacedID      `json:"derivedFrom,omitempty"`
}

func (content ContentRef) validate(path string) error {
	if err := content.ID.validate(path + ".id"); err != nil {
		return err
	}
	switch content.Variant {
	case ContentRaw, ContentRedacted, ContentSummary, ContentHashOnly, ContentOmitted, ContentDeleted:
	default:
		return fmt.Errorf("%s.variant is invalid", path)
	}
	if !validAvailability(content.Availability) {
		return fmt.Errorf("%s.availability is invalid", path)
	}
	if content.ByteLength != nil && *content.ByteLength < 0 {
		return fmt.Errorf("%s.byteLength must not be negative", path)
	}
	if (content.Variant == ContentOmitted || content.Variant == ContentDeleted || content.Availability != AvailabilityObserved) && strings.TrimSpace(content.Reason) == "" {
		return fmt.Errorf("%s.reason is required for unavailable content", path)
	}
	if content.DerivedFrom != nil {
		return content.DerivedFrom.validate(path + ".derivedFrom")
	}
	return nil
}

// Validate checks a standalone content reference.
func (content ContentRef) Validate() error {
	return content.validate("content")
}

// EventEnvelope is the only top-level JSONL shape in schema version 1.
type EventEnvelope struct {
	SchemaVersion int                 `json:"schemaVersion"`
	EventID       NamespacedID        `json:"eventId"`
	EventType     EventKind           `json:"eventType"`
	Source        EventSource         `json:"source"`
	Time          EventTime           `json:"time"`
	Sequence      *EventSequence      `json:"sequence,omitempty"`
	Scope         []EntityRef         `json:"scope"`
	Availability  []FieldAvailability `json:"availability,omitempty"`
	Relations     []Relation          `json:"relations,omitempty"`
	Revision      *Revision           `json:"revision,omitempty"`
	Adoption      *Adoption           `json:"adoption,omitempty"`
	Payload       json.RawMessage     `json:"payload"`
}

// ValidateJSON rejects unknown envelope fields and validates the payload
// selected by eventType. Provider-specific raw usage fields remain open.
func ValidateJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var envelope EventEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("decode event envelope: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values in event envelope")
		}
		return fmt.Errorf("decode trailing event data: %w", err)
	}
	return envelope.Validate()
}

func (envelope EventEnvelope) Validate() error {
	if envelope.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schemaVersion %d is unsupported", envelope.SchemaVersion)
	}
	if err := envelope.EventID.validate("eventId"); err != nil {
		return err
	}
	if !validEventKind(envelope.EventType) {
		return fmt.Errorf("eventType %q is unsupported", envelope.EventType)
	}
	if err := envelope.Source.validate(); err != nil {
		return err
	}
	if envelope.Time.ReceivedAt.IsZero() {
		return errors.New("time.receivedAt is required")
	}
	if envelope.Sequence != nil {
		if err := envelope.Sequence.validate(); err != nil {
			return err
		}
	}
	if len(envelope.Scope) == 0 {
		return errors.New("scope must contain at least one entity")
	}
	for index, scope := range envelope.Scope {
		if err := scope.validate(fmt.Sprintf("scope[%d]", index)); err != nil {
			return err
		}
	}
	for index, availability := range envelope.Availability {
		if err := availability.validate(fmt.Sprintf("availability[%d]", index)); err != nil {
			return err
		}
	}
	for index, relation := range envelope.Relations {
		if err := relation.validate(fmt.Sprintf("relations[%d]", index)); err != nil {
			return err
		}
	}
	if envelope.Revision != nil {
		if envelope.Revision.Number == 0 {
			return errors.New("revision.number must be positive")
		}
		for index, id := range append(append([]NamespacedID(nil), envelope.Revision.Supersedes...), envelope.Revision.Corrects...) {
			if err := id.validate(fmt.Sprintf("revision reference[%d]", index)); err != nil {
				return err
			}
		}
	}
	if envelope.Adoption != nil {
		if err := envelope.Adoption.validate(); err != nil {
			return err
		}
	}
	if len(bytes.TrimSpace(envelope.Payload)) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Payload), []byte("null")) {
		return errors.New("payload is required")
	}
	payload, err := envelope.DecodePayload()
	if err != nil {
		return err
	}
	return validatePayload(payload)
}

// DecodePayload returns the concrete Go payload selected by EventType and
// rejects fields not defined for that payload.
func (envelope EventEnvelope) DecodePayload() (any, error) {
	var payload any
	switch envelope.EventType {
	case EventInstructionRecorded:
		payload = &InstructionPayload{}
	case EventActorObserved:
		payload = &ActorPayload{}
	case EventModelCall:
		payload = &ModelCallPayload{}
	case EventModelAttempt:
		payload = &ModelAttemptPayload{}
	case EventUsageReported:
		payload = &UsagePayload{}
	case EventInputObserved:
		payload = &InputElementPayload{}
	case EventOperationRequested:
		payload = &OperationPayload{}
	case EventApprovalResolved:
		payload = &ApprovalPayload{}
	case EventOperationResult:
		payload = &OperationResultPayload{}
	case EventStateChanged:
		payload = &StateTransitionPayload{}
	case EventArtifactObserved:
		payload = &ArtifactPayload{}
	case EventPricingPublished:
		payload = &PricingPayload{}
	case EventSpendReported:
		payload = &SpendPayload{}
	case EventCollectionState:
		payload = &CollectionStatePayload{}
	default:
		return nil, fmt.Errorf("eventType %q is unsupported", envelope.EventType)
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.Payload))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(payload); err != nil {
		return nil, fmt.Errorf("decode %s payload: %w", envelope.EventType, err)
	}
	return payload, nil
}

type InstructionPayload struct {
	Instruction EntityRef    `json:"instruction"`
	Actor       EntityRef    `json:"actor"`
	Content     []ContentRef `json:"content"`
	State       string       `json:"state"`
}

type ActorPayload struct {
	Actor  EntityRef  `json:"actor"`
	Kind   string     `json:"kind"`
	Parent *EntityRef `json:"parent,omitempty"`
}

type ModelCallPayload struct {
	Call           EntityRef  `json:"call"`
	Instruction    EntityRef  `json:"instruction"`
	Actor          EntityRef  `json:"actor"`
	RequestedModel string     `json:"requestedModel,omitempty"`
	ActualModel    string     `json:"actualModel,omitempty"`
	State          string     `json:"state"`
	Response       *EntityRef `json:"response,omitempty"`
}

type ModelAttemptPayload struct {
	Attempt  EntityRef   `json:"attempt"`
	Call     EntityRef   `json:"call"`
	Number   uint32      `json:"number"`
	State    string      `json:"state"`
	Response *EntityRef  `json:"response,omitempty"`
	Error    *ContentRef `json:"error,omitempty"`
}

type MeasurementState string

const (
	MeasurementProvisional MeasurementState = "provisional"
	MeasurementFinal       MeasurementState = "final"
	MeasurementCorrected   MeasurementState = "corrected"
)

type CounterKind string

const (
	CounterDelta      CounterKind = "delta"
	CounterCumulative CounterKind = "cumulative"
	CounterSnapshot   CounterKind = "snapshot"
)

type CounterSemantics struct {
	Kind       CounterKind   `json:"kind"`
	Generation string        `json:"generation,omitempty"`
	Baseline   *NamespacedID `json:"baselineEventId,omitempty"`
}

// RawUsage preserves provider field names and values without assuming their
// inclusion semantics.
type RawUsage struct {
	Unit   string                     `json:"unit"`
	Fields map[string]json.RawMessage `json:"fields"`
}

// Quantity makes zero, missing, unavailable, and conflicting values distinct.
type Quantity struct {
	Value        *int64             `json:"value,omitempty"`
	Availability AvailabilityStatus `json:"availability"`
	Reason       string             `json:"reason,omitempty"`
}

func (quantity Quantity) validate(path string) error {
	if !validAvailability(quantity.Availability) {
		return fmt.Errorf("%s.availability is invalid", path)
	}
	if quantity.Value != nil && *quantity.Value < 0 {
		return fmt.Errorf("%s.value must not be negative", path)
	}
	hasValue := quantity.Value != nil
	canHaveValue := quantity.Availability == AvailabilityObserved || quantity.Availability == AvailabilityProviderReported || quantity.Availability == AvailabilityInferred
	if hasValue != canHaveValue {
		return fmt.Errorf("%s.value presence conflicts with availability %s", path, quantity.Availability)
	}
	if quantity.Availability != AvailabilityObserved && strings.TrimSpace(quantity.Reason) == "" {
		return fmt.Errorf("%s.reason is required for %s", path, quantity.Availability)
	}
	return nil
}

type UsageBucket struct {
	Name   string   `json:"name"`
	Tokens Quantity `json:"tokens"`
}

type UsageSubset struct {
	Name   string   `json:"name"`
	Of     string   `json:"of"`
	Tokens Quantity `json:"tokens"`
}

// NormalizedUsage separates mutually exclusive billable buckets from subsets
// and totals. A subset must never be added to its parent bucket.
type NormalizedUsage struct {
	ExclusiveBuckets []UsageBucket `json:"exclusiveBuckets"`
	Subsets          []UsageSubset `json:"subsets,omitempty"`
	Totals           []UsageBucket `json:"totals,omitempty"`
}

type UsagePayload struct {
	Usage      EntityRef        `json:"usage"`
	AppliesTo  EntityRef        `json:"appliesTo"`
	Raw        RawUsage         `json:"raw"`
	Normalized *NormalizedUsage `json:"normalized,omitempty"`
	Counter    CounterSemantics `json:"counter"`
	State      MeasurementState `json:"state"`
}

type InputElementPayload struct {
	Element        EntityRef   `json:"element"`
	Call           EntityRef   `json:"call"`
	Kind           string      `json:"kind"`
	Content        *ContentRef `json:"content,omitempty"`
	Tokens         *Quantity   `json:"tokens,omitempty"`
	Transformation string      `json:"transformation,omitempty"`
}

type OperationPayload struct {
	Operation       EntityRef       `json:"operation"`
	Actor           EntityRef       `json:"actor"`
	Kind            string          `json:"kind"`
	Name            string          `json:"name"`
	Input           []ContentRef    `json:"input,omitempty"`
	RequestedTarget *Target         `json:"requestedTarget,omitempty"`
	Risk            *RiskAssessment `json:"risk,omitempty"`
}

type Target struct {
	Kind     string `json:"kind"`
	Value    string `json:"value"`
	Protocol string `json:"protocol,omitempty"`
}

type RiskAssessment struct {
	Level       string   `json:"level"`
	RuleVersion string   `json:"ruleVersion"`
	Reasons     []string `json:"reasons,omitempty"`
}

type ApprovalPayload struct {
	Approval  EntityRef   `json:"approval"`
	Operation *EntityRef  `json:"operation,omitempty"`
	Decision  string      `json:"decision"`
	DecidedBy *EntityRef  `json:"decidedBy,omitempty"`
	Reason    *ContentRef `json:"reason,omitempty"`
}

type OperationResultPayload struct {
	Operation      EntityRef    `json:"operation"`
	Status         string       `json:"status"`
	ExitCode       *int         `json:"exitCode,omitempty"`
	Output         []ContentRef `json:"output,omitempty"`
	DurationMillis *int64       `json:"durationMillis,omitempty"`
	Async          bool         `json:"async,omitempty"`
}

type StateTransitionPayload struct {
	Subject EntityRef   `json:"subject"`
	From    string      `json:"from,omitempty"`
	To      string      `json:"to"`
	Reason  *ContentRef `json:"reason,omitempty"`
}

type ArtifactPayload struct {
	Artifact   EntityRef   `json:"artifact"`
	Kind       string      `json:"kind"`
	Locator    *ContentRef `json:"locator,omitempty"`
	Content    *ContentRef `json:"content,omitempty"`
	Change     string      `json:"change"`
	ByteLength *int64      `json:"byteLength,omitempty"`
	SHA256     string      `json:"sha256,omitempty"`
}

type PriceRate struct {
	UsageClass string `json:"usageClass"`
	Unit       string `json:"unit"`
	Amount     string `json:"amount"`
}

type PricingPayload struct {
	Price         EntityRef                  `json:"price"`
	Provider      string                     `json:"provider"`
	ModelPattern  string                     `json:"modelPattern"`
	Currency      string                     `json:"currency"`
	EffectiveFrom time.Time                  `json:"effectiveFrom"`
	EffectiveTo   *time.Time                 `json:"effectiveTo,omitempty"`
	Rates         []PriceRate                `json:"rates"`
	Conditions    map[string]json.RawMessage `json:"conditions,omitempty"`
	Source        *ContentRef                `json:"source,omitempty"`
}

type SpendPayload struct {
	Spend      EntityRef   `json:"spend"`
	Kind       string      `json:"kind"`
	Amount     string      `json:"amount"`
	Currency   string      `json:"currency"`
	Usage      []EntityRef `json:"usage,omitempty"`
	Coverage   []EntityRef `json:"coverage,omitempty"`
	OccurredAt *time.Time  `json:"occurredAt,omitempty"`
}

type CollectionStatePayload struct {
	Stream           EntityRef           `json:"stream"`
	State            string              `json:"state"`
	Cursor           string              `json:"cursor,omitempty"`
	Generation       string              `json:"generation"`
	LastGoodPosition *uint64             `json:"lastGoodPosition,omitempty"`
	Error            *ContentRef         `json:"error,omitempty"`
	Evidence         []ContentRef        `json:"evidence,omitempty"`
	Coverage         []FieldAvailability `json:"coverage,omitempty"`
}

func validatePayload(payload any) error {
	switch value := payload.(type) {
	case *InstructionPayload:
		if err := value.Instruction.validate("payload.instruction"); err != nil {
			return err
		}
		if err := value.Actor.validate("payload.actor"); err != nil {
			return err
		}
		if value.State == "" {
			return errors.New("payload.state is required")
		}
		return validateContents(value.Content, "payload.content")
	case *ActorPayload:
		if err := value.Actor.validate("payload.actor"); err != nil {
			return err
		}
		if value.Kind == "" {
			return errors.New("payload.kind is required")
		}
		if value.Parent != nil {
			return value.Parent.validate("payload.parent")
		}
		return nil
	case *ModelCallPayload:
		if err := value.Call.validate("payload.call"); err != nil {
			return err
		}
		if err := value.Instruction.validate("payload.instruction"); err != nil {
			return err
		}
		if err := value.Actor.validate("payload.actor"); err != nil {
			return err
		}
		if value.State == "" {
			return errors.New("payload.state is required")
		}
		if value.Response != nil {
			return value.Response.validate("payload.response")
		}
		return nil
	case *ModelAttemptPayload:
		if err := value.Attempt.validate("payload.attempt"); err != nil {
			return err
		}
		if err := value.Call.validate("payload.call"); err != nil {
			return err
		}
		if value.Number == 0 {
			return errors.New("payload.number must be positive")
		}
		if value.State == "" {
			return errors.New("payload.state is required")
		}
		if value.Response != nil {
			if err := value.Response.validate("payload.response"); err != nil {
				return err
			}
		}
		if value.Error != nil {
			return value.Error.validate("payload.error")
		}
		return nil
	case *UsagePayload:
		return value.validate()
	case *InputElementPayload:
		if err := value.Element.validate("payload.element"); err != nil {
			return err
		}
		if err := value.Call.validate("payload.call"); err != nil {
			return err
		}
		if value.Kind == "" {
			return errors.New("payload.kind is required")
		}
		if value.Content != nil {
			if err := value.Content.validate("payload.content"); err != nil {
				return err
			}
		}
		if value.Tokens != nil {
			return value.Tokens.validate("payload.tokens")
		}
		return nil
	case *OperationPayload:
		if err := value.Operation.validate("payload.operation"); err != nil {
			return err
		}
		if err := value.Actor.validate("payload.actor"); err != nil {
			return err
		}
		if value.Kind == "" || value.Name == "" {
			return errors.New("payload.kind and payload.name are required")
		}
		return validateContents(value.Input, "payload.input")
	case *ApprovalPayload:
		if err := value.Approval.validate("payload.approval"); err != nil {
			return err
		}
		if value.Decision == "" {
			return errors.New("payload.decision is required")
		}
		if value.Operation != nil {
			if err := value.Operation.validate("payload.operation"); err != nil {
				return err
			}
		}
		if value.DecidedBy != nil {
			if err := value.DecidedBy.validate("payload.decidedBy"); err != nil {
				return err
			}
		}
		if value.Reason != nil {
			return value.Reason.validate("payload.reason")
		}
		return nil
	case *OperationResultPayload:
		if err := value.Operation.validate("payload.operation"); err != nil {
			return err
		}
		if value.Status == "" {
			return errors.New("payload.status is required")
		}
		if value.DurationMillis != nil && *value.DurationMillis < 0 {
			return errors.New("payload.durationMillis must not be negative")
		}
		return validateContents(value.Output, "payload.output")
	case *StateTransitionPayload:
		if err := value.Subject.validate("payload.subject"); err != nil {
			return err
		}
		if value.To == "" {
			return errors.New("payload.to is required")
		}
		if value.Reason != nil {
			return value.Reason.validate("payload.reason")
		}
		return nil
	case *ArtifactPayload:
		if err := value.Artifact.validate("payload.artifact"); err != nil {
			return err
		}
		if value.Kind == "" || value.Change == "" {
			return errors.New("payload.kind and payload.change are required")
		}
		if value.ByteLength != nil && *value.ByteLength < 0 {
			return errors.New("payload.byteLength must not be negative")
		}
		if value.Locator != nil {
			if err := value.Locator.validate("payload.locator"); err != nil {
				return err
			}
		}
		if value.Content != nil {
			return value.Content.validate("payload.content")
		}
		return nil
	case *PricingPayload:
		if err := value.Price.validate("payload.price"); err != nil {
			return err
		}
		if value.Provider == "" || value.ModelPattern == "" || value.Currency == "" || value.EffectiveFrom.IsZero() {
			return errors.New("payload provider, modelPattern, currency, and effectiveFrom are required")
		}
		if len(value.Rates) == 0 {
			return errors.New("payload.rates must not be empty")
		}
		for index, rate := range value.Rates {
			if rate.UsageClass == "" || rate.Unit == "" || !validDecimal(rate.Amount) {
				return fmt.Errorf("payload.rates[%d] is invalid", index)
			}
		}
		if value.Source != nil {
			return value.Source.validate("payload.source")
		}
		return nil
	case *SpendPayload:
		if err := value.Spend.validate("payload.spend"); err != nil {
			return err
		}
		if value.Kind == "" || value.Currency == "" || !validDecimal(value.Amount) {
			return errors.New("payload kind, decimal amount, and currency are required")
		}
		for index, ref := range append(append([]EntityRef(nil), value.Usage...), value.Coverage...) {
			if err := ref.validate(fmt.Sprintf("payload reference[%d]", index)); err != nil {
				return err
			}
		}
		return nil
	case *CollectionStatePayload:
		if err := value.Stream.validate("payload.stream"); err != nil {
			return err
		}
		if value.State == "" || value.Generation == "" {
			return errors.New("payload.state and payload.generation are required")
		}
		if value.Error != nil {
			if err := value.Error.validate("payload.error"); err != nil {
				return err
			}
		}
		if err := validateContents(value.Evidence, "payload.evidence"); err != nil {
			return err
		}
		for index, availability := range value.Coverage {
			if err := availability.validate(fmt.Sprintf("payload.coverage[%d]", index)); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported payload type %T", payload)
	}
}

// ContentReferences returns every external content object referenced by an
// event payload. Collectors use it to verify that durable objects exist before
// committing the event to the journal.
func ContentReferences(envelope EventEnvelope) ([]ContentRef, error) {
	payload, err := envelope.DecodePayload()
	if err != nil {
		return nil, err
	}
	var refs []ContentRef
	add := func(ref *ContentRef) {
		if ref != nil {
			refs = append(refs, *ref)
		}
	}
	switch value := payload.(type) {
	case *InstructionPayload:
		refs = append(refs, value.Content...)
	case *ModelAttemptPayload:
		add(value.Error)
	case *InputElementPayload:
		add(value.Content)
	case *OperationPayload:
		refs = append(refs, value.Input...)
	case *ApprovalPayload:
		add(value.Reason)
	case *OperationResultPayload:
		refs = append(refs, value.Output...)
	case *StateTransitionPayload:
		add(value.Reason)
	case *ArtifactPayload:
		add(value.Locator)
		add(value.Content)
	case *PricingPayload:
		add(value.Source)
	case *CollectionStatePayload:
		add(value.Error)
		refs = append(refs, value.Evidence...)
	}
	return refs, nil
}

func (payload UsagePayload) validate() error {
	if err := payload.Usage.validate("payload.usage"); err != nil {
		return err
	}
	if err := payload.AppliesTo.validate("payload.appliesTo"); err != nil {
		return err
	}
	if payload.Raw.Unit == "" || len(payload.Raw.Fields) == 0 {
		return errors.New("payload.raw.unit and payload.raw.fields are required")
	}
	switch payload.Counter.Kind {
	case CounterDelta, CounterCumulative, CounterSnapshot:
	default:
		return errors.New("payload.counter.kind is invalid")
	}
	if payload.Counter.Kind == CounterCumulative && payload.Counter.Generation == "" {
		return errors.New("payload.counter.generation is required for cumulative counters")
	}
	if payload.Counter.Baseline != nil {
		if err := payload.Counter.Baseline.validate("payload.counter.baselineEventId"); err != nil {
			return err
		}
	}
	switch payload.State {
	case MeasurementProvisional, MeasurementFinal, MeasurementCorrected:
	default:
		return errors.New("payload.state is invalid")
	}
	if payload.Normalized == nil {
		return nil
	}
	seen := map[string]bool{}
	for index, bucket := range payload.Normalized.ExclusiveBuckets {
		if bucket.Name == "" || seen[bucket.Name] {
			return fmt.Errorf("payload.normalized.exclusiveBuckets[%d].name is empty or duplicated", index)
		}
		seen[bucket.Name] = true
		if err := bucket.Tokens.validate(fmt.Sprintf("payload.normalized.exclusiveBuckets[%d].tokens", index)); err != nil {
			return err
		}
	}
	for index, subset := range payload.Normalized.Subsets {
		if subset.Name == "" || subset.Of == "" {
			return fmt.Errorf("payload.normalized.subsets[%d] requires name and of", index)
		}
		if !seen[subset.Of] {
			return fmt.Errorf("payload.normalized.subsets[%d].of refers to an unknown exclusive bucket", index)
		}
		if err := subset.Tokens.validate(fmt.Sprintf("payload.normalized.subsets[%d].tokens", index)); err != nil {
			return err
		}
	}
	for index, total := range payload.Normalized.Totals {
		if total.Name == "" {
			return fmt.Errorf("payload.normalized.totals[%d].name is required", index)
		}
		if err := total.Tokens.validate(fmt.Sprintf("payload.normalized.totals[%d].tokens", index)); err != nil {
			return err
		}
	}
	return nil
}

func validateContents(contents []ContentRef, path string) error {
	for index, content := range contents {
		if err := content.validate(fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func validDecimal(value string) bool {
	return decimalPattern.MatchString(value)
}

var decimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)
