package contentstore

import (
	"os"
	"testing"

	"maatplug/internal/model"
)

func TestPutIsContentAndPolicyAddressed(t *testing.T) {
	s := New(t.TempDir())
	raw, err := s.Put([]byte("secret"), model.ContentRaw, "text/plain", "retain-30d")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Put([]byte("secret"), model.ContentRaw, "text/plain", "retain-30d")
	if err != nil {
		t.Fatal(err)
	}
	otherPolicy := Describe([]byte("secret"), model.ContentRaw, "text/plain", "retain-7d")
	if raw.ID != again.ID {
		t.Fatal("same content and policy produced different IDs")
	}
	if raw.ID == otherPolicy.ID {
		t.Fatal("different retention policies produced the same content ID")
	}
	data, err := s.Get(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "secret" {
		t.Fatalf("content = %q", data)
	}
}

func TestPutExpectedRejectsMismatchedBytes(t *testing.T) {
	s := New(t.TempDir())
	ref := Describe([]byte("expected"), model.ContentRaw, "text/plain", "default")
	if err := s.PutExpected(ref, []byte("different"), "default"); err == nil {
		t.Fatal("PutExpected accepted bytes that do not match the reference")
	}
}

func TestValidateRefDetectsCorruption(t *testing.T) {
	s := New(t.TempDir())
	ref, err := s.Put([]byte("content"), model.ContentRedacted, "text/plain", "default")
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.path(ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateRef(ref); err == nil {
		t.Fatal("ValidateRef accepted a corrupt object")
	}
}
