package store

import "testing"

func TestAppSettings_DefaultsWhenUnconfigured(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	got, err := s.GetAppSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.RemoteSyncIntervalMinutes != DefaultRemoteSyncIntervalMinutes {
		t.Errorf("RemoteSyncIntervalMinutes = %d, want default %d", got.RemoteSyncIntervalMinutes, DefaultRemoteSyncIntervalMinutes)
	}
}

func TestAppSettings_SaveAndReload(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if err := s.SaveAppSettings(AppSettings{RemoteSyncIntervalMinutes: 45}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAppSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.RemoteSyncIntervalMinutes != 45 {
		t.Errorf("RemoteSyncIntervalMinutes = %d, want 45", got.RemoteSyncIntervalMinutes)
	}
}

func TestAppSettings_RejectsNonPositiveInterval(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if err := s.SaveAppSettings(AppSettings{RemoteSyncIntervalMinutes: 0}); err == nil {
		t.Error("SaveAppSettings(0) succeeded, want an error")
	}
	if err := s.SaveAppSettings(AppSettings{RemoteSyncIntervalMinutes: -5}); err == nil {
		t.Error("SaveAppSettings(-5) succeeded, want an error")
	}
}
