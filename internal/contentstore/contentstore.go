// Package contentstore persists prompt, tool, path, and diagnostic content
// outside the append-only event stream.
package contentstore

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"maatplug/internal/model"
	"maatplug/internal/store"
)

const objectSchemaVersion = 1

type Store struct {
	Root string
	Now  func() time.Time
}

type objectFile struct {
	SchemaVersion int                  `json:"schemaVersion"`
	ID            model.NamespacedID   `json:"id"`
	Variant       model.ContentVariant `json:"variant"`
	MediaType     string               `json:"mediaType,omitempty"`
	Policy        string               `json:"policy"`
	ByteLength    int64                `json:"byteLength"`
	SHA256        string               `json:"sha256"`
	Encoding      string               `json:"encoding"`
	Data          string               `json:"data"`
	CreatedAt     time.Time            `json:"createdAt"`
}

func New(root string) *Store {
	return &Store{Root: root, Now: time.Now}
}

// Describe computes the durable reference without writing. It lets a
// short-lived producer place the reference and bytes in one atomic inbox
// record; the collector writes the object before the referencing event.
func Describe(data []byte, variant model.ContentVariant, mediaType, policy string) model.ContentRef {
	contentHash := sha256.Sum256(data)
	identity := sha256.New()
	_, _ = identity.Write([]byte("maatplug-content-v1\x00"))
	_, _ = identity.Write([]byte(variant))
	_, _ = identity.Write([]byte{0})
	_, _ = identity.Write([]byte(mediaType))
	_, _ = identity.Write([]byte{0})
	_, _ = identity.Write([]byte(policy))
	_, _ = identity.Write([]byte{0})
	_, _ = identity.Write(data)
	length := int64(len(data))
	return model.ContentRef{
		ID:           model.NamespacedID{Namespace: "sha256", Value: hex.EncodeToString(identity.Sum(nil))},
		Variant:      variant,
		MediaType:    mediaType,
		ByteLength:   &length,
		SHA256:       hex.EncodeToString(contentHash[:]),
		Availability: model.AvailabilityObserved,
	}
}

func (s *Store) Put(data []byte, variant model.ContentVariant, mediaType, policy string) (model.ContentRef, error) {
	if !storableVariant(variant) {
		return model.ContentRef{}, fmt.Errorf("content variant %q does not store bytes", variant)
	}
	ref := Describe(data, variant, mediaType, policy)
	if err := s.PutExpected(ref, data, policy); err != nil {
		return model.ContentRef{}, err
	}
	return ref, nil
}

// PutExpected rejects an inbox item whose bytes do not match its declared
// reference. Existing valid objects make the operation idempotent.
func (s *Store) PutExpected(ref model.ContentRef, data []byte, policy string) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if !storableVariant(ref.Variant) {
		return fmt.Errorf("content variant %q does not store bytes", ref.Variant)
	}
	expected := Describe(data, ref.Variant, ref.MediaType, policy)
	if expected.ID != ref.ID || expected.SHA256 != ref.SHA256 || ref.ByteLength == nil || *expected.ByteLength != *ref.ByteLength {
		return errors.New("content bytes or policy do not match declared reference")
	}
	path, err := s.path(ref.ID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return s.ValidateRef(ref)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	object := objectFile{
		SchemaVersion: objectSchemaVersion,
		ID:            ref.ID,
		Variant:       ref.Variant,
		MediaType:     ref.MediaType,
		Policy:        policy,
		ByteLength:    int64(len(data)),
		SHA256:        ref.SHA256,
		Encoding:      "base64",
		Data:          base64.StdEncoding.EncodeToString(data),
		CreatedAt:     now().UTC(),
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return err
	}
	if err := store.AtomicWriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		return fmt.Errorf("write content object: %w", err)
	}
	return s.ValidateRef(ref)
}

func (s *Store) Get(ref model.ContentRef) ([]byte, error) {
	if err := s.ValidateRef(ref); err != nil {
		return nil, err
	}
	path, err := s.path(ref.ID)
	if err != nil {
		return nil, err
	}
	object, err := readObject(path)
	if err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(object.Data)
	if err != nil {
		return nil, fmt.Errorf("decode content object: %w", err)
	}
	return data, nil
}

// ValidateRef verifies both the object identity and the hash of its decoded
// bytes. Omitted and deleted references deliberately have no object.
func (s *Store) ValidateRef(ref model.ContentRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.Availability != model.AvailabilityObserved || !storableVariant(ref.Variant) {
		return nil
	}
	path, err := s.path(ref.ID)
	if err != nil {
		return err
	}
	object, err := readObject(path)
	if err != nil {
		return err
	}
	if object.SchemaVersion != objectSchemaVersion || object.ID != ref.ID || object.Variant != ref.Variant || object.MediaType != ref.MediaType || object.SHA256 != ref.SHA256 || ref.ByteLength == nil || object.ByteLength != *ref.ByteLength {
		return errors.New("content object metadata does not match reference")
	}
	if object.Encoding != "base64" {
		return fmt.Errorf("unsupported content encoding %q", object.Encoding)
	}
	data, err := base64.StdEncoding.DecodeString(object.Data)
	if err != nil {
		return fmt.Errorf("decode content object: %w", err)
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != object.ByteLength || hex.EncodeToString(sum[:]) != object.SHA256 {
		return errors.New("content object bytes failed length or SHA-256 validation")
	}
	expected := Describe(data, object.Variant, object.MediaType, object.Policy)
	if expected.ID != object.ID {
		return errors.New("content object identity does not match bytes and policy")
	}
	return nil
}

func storableVariant(variant model.ContentVariant) bool {
	return variant == model.ContentRaw || variant == model.ContentRedacted || variant == model.ContentSummary
}

func (s *Store) path(id model.NamespacedID) (string, error) {
	if id.Namespace != "sha256" || len(id.Value) != sha256.Size*2 {
		return "", errors.New("content ID must be a full sha256 identifier")
	}
	if _, err := hex.DecodeString(id.Value); err != nil {
		return "", errors.New("content ID is not hexadecimal")
	}
	return filepath.Join(s.Root, "objects", "sha256", id.Value[:2], id.Value+".json"), nil
}

func readObject(path string) (objectFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return objectFile{}, err
	}
	var object objectFile
	if err := json.Unmarshal(data, &object); err != nil {
		return objectFile{}, fmt.Errorf("decode content object: %w", err)
	}
	return object, nil
}
