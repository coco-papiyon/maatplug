package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// AppSettings holds maatplug-web's own configuration — as opposed to
// DataArea, which is data the dashboard reads, this is settings for how the
// dashboard itself behaves. Kept in its own file (appSettingsFileName)
// rather than folded into data-areas.json: the two are edited from
// different screens (see docs on the "データ" vs "設定" nav split) and
// conceptually don't belong together.
type AppSettings struct {
	// RemoteSyncIntervalMinutes is how often internal/remotesync.Poller
	// re-syncs every configured SSH data area. Zero means "not yet
	// configured" — GetAppSettings never returns zero here (see its doc
	// comment); zero only appears transiently in a PUT request that means
	// "reset to the default".
	RemoteSyncIntervalMinutes int `json:"remoteSyncIntervalMinutes"`
}

const appSettingsFileName = "settings.json"

// DefaultRemoteSyncIntervalMinutes is what a fresh install (no
// settings.json yet, and no --remote-sync-interval override) uses.
const DefaultRemoteSyncIntervalMinutes = 10

// GetAppSettings reads settings.json, filling in DefaultRemoteSyncIntervalMinutes
// for a field that's zero or the file doesn't exist yet — callers never
// have to special-case "unconfigured" themselves.
func (s *Store) GetAppSettings() (AppSettings, error) {
	b, err := os.ReadFile(filepath.Join(s.Root, appSettingsFileName))
	if errors.Is(err, os.ErrNotExist) {
		return AppSettings{RemoteSyncIntervalMinutes: DefaultRemoteSyncIntervalMinutes}, nil
	}
	if err != nil {
		return AppSettings{}, err
	}
	var settings AppSettings
	if err := json.Unmarshal(b, &settings); err != nil {
		return AppSettings{}, fmt.Errorf("decode %s: %w", appSettingsFileName, err)
	}
	if settings.RemoteSyncIntervalMinutes <= 0 {
		settings.RemoteSyncIntervalMinutes = DefaultRemoteSyncIntervalMinutes
	}
	return settings, nil
}

// SaveAppSettings persists settings, validating RemoteSyncIntervalMinutes is
// a sane positive value first (a poller ticking every 0 or negative minutes
// would be a busy-loop, not a slow-down).
func (s *Store) SaveAppSettings(settings AppSettings) error {
	if settings.RemoteSyncIntervalMinutes <= 0 {
		return errors.New("remote sync interval must be at least 1 minute")
	}
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(filepath.Join(s.Root, appSettingsFileName), append(b, '\n'), 0o600)
}
