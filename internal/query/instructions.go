package query

import (
	"encoding/json"
	"sort"
	"time"

	"maatplug/internal/contentstore"
	"maatplug/internal/model"
)

// InstructionView is a session detail's human-facing view of one submitted
// instruction: the prompt text resolved from its ContentRef, so the Web UI
// can show "what was instructed" without making callers dump raw event JSON
// and follow content references themselves.
type InstructionView struct {
	Instruction  model.EntityRef          `json:"instruction"`
	EventID      model.NamespacedID       `json:"eventId"`
	RequestedAt  time.Time                `json:"requestedAt"`
	Actor        model.EntityRef          `json:"actor"`
	State        string                   `json:"state"`
	Text         string                   `json:"text,omitempty"`
	Availability model.AvailabilityStatus `json:"availability"`
	Reason       string                   `json:"reason,omitempty"`
	// Tokens and Activity are filled in after construction, by
	// attachInstructionStats (instruction_stats.go) — they need the
	// session's reconciled usage/attribution/operations, which
	// buildInstructions itself doesn't have. Tokens is nil when no usage
	// could be attributed to this instruction at all (as opposed to
	// InstructionTokens.Complete, which is false for a known-but-approximate
	// attribution).
	Tokens   *InstructionTokens  `json:"tokens,omitempty"`
	Activity InstructionActivity `json:"activity"`
}

// buildInstructions extracts one InstructionView per instruction.recorded
// event, in submission order.
func buildInstructions(root string, events []model.EventEnvelope) ([]InstructionView, error) {
	contents := contentstore.New(root)
	result := []InstructionView{}
	for _, event := range events {
		if event.EventType != model.EventInstructionRecorded {
			continue
		}
		var payload model.InstructionPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return nil, err
		}
		view := InstructionView{Instruction: payload.Instruction, EventID: event.EventID, RequestedAt: event.Time.ReceivedAt, Actor: payload.Actor, State: payload.State}
		ref, ok := bestTextContent(payload.Content)
		if !ok {
			view.Availability = model.AvailabilityUnavailable
			view.Reason = "instruction content is not available as text"
			result = append(result, view)
			continue
		}
		data, err := contents.Get(ref)
		if err != nil {
			view.Availability = model.AvailabilityUnknown
			view.Reason = "instruction content object is unavailable"
			result = append(result, view)
			continue
		}
		view.Text = string(data)
		view.Availability = model.AvailabilityObserved
		result = append(result, view)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].RequestedAt.Before(result[j].RequestedAt) })
	return result, nil
}

// bestTextContent picks the most complete text-shaped content reference,
// preferring the unredacted body but falling back to a redacted or
// summarized one when that's all collection retained.
func bestTextContent(refs []model.ContentRef) (model.ContentRef, bool) {
	for _, variant := range []model.ContentVariant{model.ContentRaw, model.ContentRedacted, model.ContentSummary} {
		for _, ref := range refs {
			if ref.Variant == variant && ref.Availability == model.AvailabilityObserved {
				return ref, true
			}
		}
	}
	return model.ContentRef{}, false
}
