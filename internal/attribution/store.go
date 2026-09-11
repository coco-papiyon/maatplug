package attribution

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	maatstore "maatplug/internal/store"
)

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type PolicyStore struct{ Root string }

func (s PolicyStore) Save(projectID string, policy Policy) error {
	if !safeName.MatchString(projectID) || !safeName.MatchString(policy.Version) {
		return errors.New("project and policy version must be safe identifiers")
	}
	if policy.Version == DefaultPolicyVersion {
		return fmt.Errorf("policy version %q is reserved for automatic attribution", DefaultPolicyVersion)
	}
	if err := policy.Validate(); err != nil {
		return err
	}
	if policy.BasedOn != "" && policy.BasedOn != DefaultPolicyVersion {
		if _, err := os.Stat(s.policyPath(projectID, policy.BasedOn)); err != nil {
			return fmt.Errorf("load basedOn policy %q: %w", policy.BasedOn, err)
		}
	}
	data, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := s.policyPath(projectID, policy.Version)
	return maatstore.WithFileLock(path, func() error {
		if existing, err := os.ReadFile(path); err == nil {
			if bytes.Equal(existing, data) {
				return nil
			}
			return fmt.Errorf("attribution policy %q already exists with different content", policy.Version)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return maatstore.AtomicWriteFile(path, data, 0o600)
	})
}

func (s PolicyStore) Activate(projectID, version string) error {
	if !safeName.MatchString(projectID) || !safeName.MatchString(version) {
		return errors.New("project and policy version must be safe identifiers")
	}
	activePath := s.activePath(projectID)
	return maatstore.WithFileLock(activePath, func() error {
		if version == DefaultPolicyVersion {
			if err := os.Remove(activePath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return nil
		}
		if _, err := os.Stat(s.policyPath(projectID, version)); err != nil {
			return fmt.Errorf("load attribution policy %q: %w", version, err)
		}
		data, _ := json.Marshal(struct {
			Version string `json:"version"`
		}{version})
		return maatstore.AtomicWriteFile(activePath, append(data, '\n'), 0o600)
	})
}

func (s PolicyStore) SaveAndActivate(projectID string, policy Policy) error {
	if err := s.Save(projectID, policy); err != nil {
		return err
	}
	return s.Activate(projectID, policy.Version)
}

func (s PolicyStore) LoadActive(projectID string) (Policy, error) {
	data, err := os.ReadFile(s.activePath(projectID))
	if errors.Is(err, os.ErrNotExist) {
		return Policy{Version: DefaultPolicyVersion}, nil
	}
	if err != nil {
		return Policy{}, err
	}
	var active struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &active); err != nil {
		return Policy{}, err
	}
	return s.Load(projectID, active.Version)
}

func (s PolicyStore) List(projectID string) ([]Policy, error) {
	if !safeName.MatchString(projectID) {
		return nil, errors.New("project must be a safe identifier")
	}
	dir := filepath.Join(s.Root, "projects", projectID, "derivations", "attribution-policies")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, strings.TrimSuffix(entry.Name(), ".json"))
		}
	}
	sort.Strings(names)
	result := make([]Policy, 0, len(names))
	for _, name := range names {
		policy, err := s.Load(projectID, name)
		if err != nil {
			return nil, err
		}
		result = append(result, policy)
	}
	return result, nil
}

func (s PolicyStore) Load(projectID, version string) (Policy, error) {
	if !safeName.MatchString(projectID) || !safeName.MatchString(version) {
		return Policy{}, errors.New("project and policy version must be safe identifiers")
	}
	data, err := os.ReadFile(s.policyPath(projectID, version))
	if err != nil {
		return Policy{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var policy Policy
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, err
	}
	return policy, policy.Validate()
}

func (s PolicyStore) policyPath(projectID, version string) string {
	return filepath.Join(s.Root, "projects", projectID, "derivations", "attribution-policies", version+".json")
}
func (s PolicyStore) activePath(projectID string) string {
	return filepath.Join(s.Root, "projects", projectID, "derivations", "attribution-active.json")
}
