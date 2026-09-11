package billing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"

	maatstore "maatplug/internal/store"
)

type DerivationStore struct{ Root string }

func (store DerivationStore) SaveConversion(value Conversion) error {
	rate, rateOK := new(big.Rat).SetString(value.Rate)
	if !safeIdentifier(value.Version) || !validDecimal(value.Original.Amount) || !validDecimal(value.Converted.Amount) || !validDecimal(value.Rate) || !rateOK || rate.Sign() < 0 {
		return errors.New("invalid conversion")
	}
	return store.save("conversions", value.Version, value)
}

func (store DerivationStore) SaveAllocation(value Allocation) error {
	if !safeIdentifier(value.Version) || !validDecimal(value.Original.Amount) || !validDecimal(value.Unallocated.Amount) {
		return errors.New("invalid allocation")
	}
	return store.save("allocations", value.Version, value)
}

func (store DerivationStore) save(kind, version string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(store.Root, "billing", "derivations", kind, version+".json")
	return maatstore.WithFileLock(path, func() error {
		existing, err := os.ReadFile(path)
		if err == nil {
			if bytes.Equal(existing, data) {
				return nil
			}
			return fmt.Errorf("billing derivation %q is immutable", version)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return maatstore.AtomicWriteFile(path, data, 0o600)
	})
}
