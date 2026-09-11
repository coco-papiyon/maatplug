package model

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestEventSchemaMatchesGoContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "event.schema.json"))
	if err != nil {
		t.Fatal(err)
	}

	var schema struct {
		Properties struct {
			SchemaVersion struct {
				Const int `json:"const"`
			} `json:"schemaVersion"`
			EventType struct {
				Enum []EventKind `json:"enum"`
			} `json:"eventType"`
		} `json:"properties"`
		AllOf []struct {
			If struct {
				Properties struct {
					EventType struct {
						Const EventKind `json:"const"`
					} `json:"eventType"`
				} `json:"properties"`
			} `json:"if"`
			Then struct {
				Properties struct {
					Payload struct {
						Ref string `json:"$ref"`
					} `json:"payload"`
				} `json:"properties"`
			} `json:"then"`
		} `json:"allOf"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if schema.Properties.SchemaVersion.Const != SchemaVersion {
		t.Fatalf("schema version = %d, Go version = %d", schema.Properties.SchemaVersion.Const, SchemaVersion)
	}

	goKinds := EventKinds()
	schemaKinds := append([]EventKind(nil), schema.Properties.EventType.Enum...)
	sort.Slice(goKinds, func(i, j int) bool { return goKinds[i] < goKinds[j] })
	sort.Slice(schemaKinds, func(i, j int) bool { return schemaKinds[i] < schemaKinds[j] })
	if !reflect.DeepEqual(schemaKinds, goKinds) {
		t.Fatalf("schema event kinds = %v, Go event kinds = %v", schemaKinds, goKinds)
	}

	mapped := make(map[EventKind]string, len(schema.AllOf))
	for _, condition := range schema.AllOf {
		mapped[condition.If.Properties.EventType.Const] = condition.Then.Properties.Payload.Ref
	}
	for _, kind := range goKinds {
		if mapped[kind] == "" {
			t.Errorf("schema has no payload mapping for %q", kind)
		}
	}

	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	assertJSONFields(t, reflect.TypeOf(EventEnvelope{}), document)
	definitions := document["$defs"].(map[string]any)
	schemaTypes := map[string]reflect.Type{
		"namespacedId":           reflect.TypeOf(NamespacedID{}),
		"entityRef":              reflect.TypeOf(EntityRef{}),
		"eventSource":            reflect.TypeOf(EventSource{}),
		"eventTime":              reflect.TypeOf(EventTime{}),
		"eventSequence":          reflect.TypeOf(EventSequence{}),
		"fieldAvailability":      reflect.TypeOf(FieldAvailability{}),
		"relation":               reflect.TypeOf(Relation{}),
		"revision":               reflect.TypeOf(Revision{}),
		"adoption":               reflect.TypeOf(Adoption{}),
		"contentRef":             reflect.TypeOf(ContentRef{}),
		"quantity":               reflect.TypeOf(Quantity{}),
		"usageBucket":            reflect.TypeOf(UsageBucket{}),
		"usageSubset":            reflect.TypeOf(UsageSubset{}),
		"instructionPayload":     reflect.TypeOf(InstructionPayload{}),
		"actorPayload":           reflect.TypeOf(ActorPayload{}),
		"modelCallPayload":       reflect.TypeOf(ModelCallPayload{}),
		"modelAttemptPayload":    reflect.TypeOf(ModelAttemptPayload{}),
		"usagePayload":           reflect.TypeOf(UsagePayload{}),
		"inputPayload":           reflect.TypeOf(InputElementPayload{}),
		"operationPayload":       reflect.TypeOf(OperationPayload{}),
		"approvalPayload":        reflect.TypeOf(ApprovalPayload{}),
		"operationResultPayload": reflect.TypeOf(OperationResultPayload{}),
		"statePayload":           reflect.TypeOf(StateTransitionPayload{}),
		"artifactPayload":        reflect.TypeOf(ArtifactPayload{}),
		"pricingPayload":         reflect.TypeOf(PricingPayload{}),
		"spendPayload":           reflect.TypeOf(SpendPayload{}),
		"collectionStatePayload": reflect.TypeOf(CollectionStatePayload{}),
	}
	for name, goType := range schemaTypes {
		definition, ok := definitions[name].(map[string]any)
		if !ok {
			t.Errorf("schema definition %q is missing", name)
			continue
		}
		if _, ok := definition["properties"].(map[string]any); !ok {
			t.Errorf("schema definition %q has no properties", name)
			continue
		}
		assertJSONFields(t, goType, definition)
	}
	assertSchemaRefs(t, document, definitions)
}

func assertJSONFields(t *testing.T, goType reflect.Type, schemaDefinition map[string]any) {
	t.Helper()
	var goFields []string
	var goRequired []string
	for index := 0; index < goType.NumField(); index++ {
		parts := strings.Split(goType.Field(index).Tag.Get("json"), ",")
		tag := parts[0]
		if tag != "" && tag != "-" {
			goFields = append(goFields, tag)
			if len(parts) == 1 || parts[1] != "omitempty" {
				goRequired = append(goRequired, tag)
			}
		}
	}
	schemaProperties := schemaDefinition["properties"].(map[string]any)
	var schemaFields []string
	for name := range schemaProperties {
		schemaFields = append(schemaFields, name)
	}
	var schemaRequired []string
	for _, value := range schemaDefinition["required"].([]any) {
		schemaRequired = append(schemaRequired, value.(string))
	}
	sort.Strings(goFields)
	sort.Strings(goRequired)
	sort.Strings(schemaFields)
	sort.Strings(schemaRequired)
	if !reflect.DeepEqual(goFields, schemaFields) {
		t.Errorf("%s JSON fields = %v, schema fields = %v", goType.Name(), goFields, schemaFields)
	}
	if !reflect.DeepEqual(goRequired, schemaRequired) {
		t.Errorf("%s required JSON fields = %v, schema required fields = %v", goType.Name(), goRequired, schemaRequired)
	}
}

func assertSchemaRefs(t *testing.T, value any, definitions map[string]any) {
	t.Helper()
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if key == "$ref" {
				ref, ok := child.(string)
				if !ok {
					continue
				}
				const prefix = "#/$defs/"
				if strings.HasPrefix(ref, prefix) {
					if _, exists := definitions[strings.TrimPrefix(ref, prefix)]; !exists {
						t.Errorf("schema reference %q has no definition", ref)
					}
				}
				continue
			}
			assertSchemaRefs(t, child, definitions)
		}
	case []any:
		for _, child := range node {
			assertSchemaRefs(t, child, definitions)
		}
	}
}

func TestAcceptanceFixturesValidateAndCoverAllCases(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "events")
	file, err := os.Open(filepath.Join(root, "acceptance.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	eventIDs := map[string]bool{}
	runIDs := map[string]bool{}
	kinds := map[EventKind]bool{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		data := bytes.TrimSpace(scanner.Bytes())
		if len(data) == 0 {
			continue
		}
		if err := ValidateJSON(data); err != nil {
			t.Fatalf("acceptance.jsonl:%d: %v", line, err)
		}
		var envelope EventEnvelope
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatalf("acceptance.jsonl:%d: %v", line, err)
		}
		eventIDs[envelope.EventID.Value] = true
		kinds[envelope.EventType] = true
		for _, scope := range envelope.Scope {
			if scope.Kind == EntityRun {
				runIDs[scope.ID.Value] = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}

	var expectations struct {
		SchemaVersion int `json:"schemaVersion"`
		Cases         []struct {
			ID               string   `json:"id"`
			SelectedEventIDs []string `json:"selectedEventIds"`
			ExcludedEventIDs []string `json:"excludedEventIds"`
		} `json:"cases"`
	}
	data, err := os.ReadFile(filepath.Join(root, "expectations.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &expectations); err != nil {
		t.Fatalf("expectations.json: %v", err)
	}
	if expectations.SchemaVersion != SchemaVersion {
		t.Fatalf("expectation version = %d, want %d", expectations.SchemaVersion, SchemaVersion)
	}
	if len(expectations.Cases) != 15 {
		t.Fatalf("acceptance cases = %d, want 15", len(expectations.Cases))
	}
	for _, scenario := range expectations.Cases {
		if !runIDs[scenario.ID] {
			t.Errorf("case %q has no event fixture", scenario.ID)
		}
		for _, id := range append(append([]string(nil), scenario.SelectedEventIDs...), scenario.ExcludedEventIDs...) {
			if !eventIDs[id] {
				t.Errorf("case %q refers to missing event %q", scenario.ID, id)
			}
		}
	}
	for _, kind := range EventKinds() {
		if !kinds[kind] {
			t.Errorf("event kind %q is not represented in acceptance fixtures", kind)
		}
	}
}

func TestValidateJSONRejectsUnknownEnvelopeField(t *testing.T) {
	data := []byte(`{"schemaVersion":1,"eventId":{"namespace":"test","value":"event"},"eventType":"actor.observed","source":{"provider":"test","surface":"test","kind":"collector"},"time":{"receivedAt":"2026-01-01T00:00:00Z"},"scope":[{"kind":"run","id":{"namespace":"test","value":"run"}}],"payload":{"actor":{"kind":"actor","id":{"namespace":"test","value":"actor"}},"kind":"main"},"unexpected":true}`)
	if err := ValidateJSON(data); err == nil {
		t.Fatal("ValidateJSON accepted an unknown envelope field")
	}
}

func TestQuantityDistinguishesZeroFromUnknown(t *testing.T) {
	zero := int64(0)
	if err := (Quantity{Value: &zero, Availability: AvailabilityObserved}).validate("quantity"); err != nil {
		t.Fatalf("observed zero was rejected: %v", err)
	}
	if err := (Quantity{Availability: AvailabilityUnknown, Reason: "provider omitted field"}).validate("quantity"); err != nil {
		t.Fatalf("explicit unknown was rejected: %v", err)
	}
	if err := (Quantity{Availability: AvailabilityObserved}).validate("quantity"); err == nil {
		t.Fatal("observed quantity without a value was accepted")
	}
}

func TestCumulativeUsageRequiresGeneration(t *testing.T) {
	value := int64(1)
	payload := UsagePayload{
		Usage:     EntityRef{Kind: EntityUsage, ID: NamespacedID{Namespace: "test", Value: "usage"}},
		AppliesTo: EntityRef{Kind: EntityThread, ID: NamespacedID{Namespace: "test", Value: "thread"}},
		Raw: RawUsage{
			Unit:   "tokens",
			Fields: map[string]json.RawMessage{"total_tokens": json.RawMessage("1")},
		},
		Normalized: &NormalizedUsage{ExclusiveBuckets: []UsageBucket{{
			Name:   "all",
			Tokens: Quantity{Value: &value, Availability: AvailabilityObserved},
		}}},
		Counter: CounterSemantics{Kind: CounterCumulative},
		State:   MeasurementFinal,
	}
	if err := payload.validate(); err == nil {
		t.Fatal("cumulative usage without a generation was accepted")
	}
}

func TestValidDecimalUsesCanonicalSyntaxWithoutFloatRange(t *testing.T) {
	valid := []string{"0", "-0", "10", "0.125", "123456789012345678901234567890.12345678901234567890"}
	for _, value := range valid {
		if !validDecimal(value) {
			t.Errorf("validDecimal(%q) = false", value)
		}
	}
	invalid := []string{"", "+1", "01", ".5", "1.", "1e3", "1/2", "NaN", "Inf"}
	for _, value := range invalid {
		if validDecimal(value) {
			t.Errorf("validDecimal(%q) = true", value)
		}
	}
}
