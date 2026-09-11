// Package adapter contains provider-neutral plumbing used by the Claude and
// Codex normalizers. Provider field interpretation remains in each adapter.
package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"maatplug/internal/contentstore"
	"maatplug/internal/model"
)

type Hook struct {
	Event          string
	SessionID      string
	RunID          string
	Cwd            string
	TranscriptPath string
	PromptID       string
	TurnID         string
	Model          string
	Prompt         string
	Raw            []byte
	Fields         map[string]json.RawMessage
}

type Line struct {
	Number     uint64
	ByteOffset int64
	Data       []byte
}

type Transcript struct {
	Path            string
	Generation      string
	StartByteOffset int64
	NextByteOffset  int64
	StartLine       uint64
	NextLine        uint64
	Rotated         bool
	IncompleteTail  bool
	Unavailable     string
	Lines           []Line
	KnownIDs        map[string]bool
}

type Content struct {
	Reference model.ContentRef
	Policy    string
	Data      []byte
}

type Batch struct {
	Events   []model.EventEnvelope
	Contents []Content
	SeenIDs  []string
}

func (batch *Batch) AddContent(data []byte, variant model.ContentVariant, mediaType, policy string) model.ContentRef {
	ref := contentstore.Describe(data, variant, mediaType, policy)
	batch.Contents = append(batch.Contents, Content{Reference: ref, Policy: policy, Data: append([]byte(nil), data...)})
	return ref
}

func ID(namespace string, parts ...string) model.NamespacedID {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return model.NamespacedID{Namespace: namespace, Value: hex.EncodeToString(hash.Sum(nil))}
}

func Ref(kind model.EntityKind, namespace, value string) model.EntityRef {
	return model.EntityRef{Kind: kind, ID: model.NamespacedID{Namespace: namespace, Value: value}}
}

func Scope(provider, projectID, sessionID, runID string) []model.EntityRef {
	return []model.EntityRef{
		Ref(model.EntityProject, "maatplug/project", projectID),
		Ref(model.EntitySession, provider, sessionID),
		Ref(model.EntityRun, provider, runID),
	}
}

// ScopeWithRun routes transcript records that carry their own turn/prompt ID
// to that run while retaining the project and session identities.
func ScopeWithRun(scope []model.EntityRef, provider, runID string) []model.EntityRef {
	if runID == "" {
		return append([]model.EntityRef(nil), scope...)
	}
	result := append([]model.EntityRef(nil), scope...)
	for index := range result {
		if result[index].Kind == model.EntityRun {
			result[index] = Ref(model.EntityRun, provider, runID)
			return result
		}
	}
	return append(result, Ref(model.EntityRun, provider, runID))
}

func ObservedQuantity(value int64) model.Quantity {
	return model.Quantity{Value: &value, Availability: model.AvailabilityProviderReported, Reason: "provider reported"}
}
