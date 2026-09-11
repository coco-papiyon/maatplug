package main

import (
	"os"
	"path/filepath"
	"testing"

	"maatplug/internal/attribution"
	"maatplug/internal/store"
)

func TestAttributionApplyAndActivateDefault(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MAATPLUG_HOME", root)
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(policyPath, []byte(`{"version":"reviewed-v1","corrections":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runAttribution([]string{"apply", "--project", "project", "--file", policyPath}); err != nil {
		t.Fatal(err)
	}
	loaded, err := (attribution.PolicyStore{Root: root}).LoadActive("project")
	if err != nil || loaded.Version != "reviewed-v1" {
		t.Fatalf("active policy = %+v, %v", loaded, err)
	}
	if _, err := os.Stat((&store.Store{Root: root}).ProjectCatalogPath("project")); err != nil {
		t.Fatal(err)
	}
	if err := runAttribution([]string{"activate", "--project", "project", "--version", attribution.DefaultPolicyVersion}); err != nil {
		t.Fatal(err)
	}
	loaded, err = (attribution.PolicyStore{Root: root}).LoadActive("project")
	if err != nil || loaded.Version != attribution.DefaultPolicyVersion {
		t.Fatalf("default policy = %+v, %v", loaded, err)
	}
}

func TestDecodeAttributionPolicyRejectsUnknownOrTrailingJSON(t *testing.T) {
	for _, input := range []string{
		`{"version":"v1","unknown":true}`,
		`{"version":"v1"} {"version":"v2"}`,
	} {
		if _, err := decodeAttributionPolicy([]byte(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}
