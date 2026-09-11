// Command maatplug-web is the browser dashboard and settings UI for maatplug's
// flat-file usage log. It is a separate binary from maatplug (the CLI/hook
// receiver) and never calls it — see docs/design.md section 9.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"maatplug/internal/remotesync"
	"maatplug/internal/store"
	"maatplug/internal/usage"
	"maatplug/internal/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4173", "address to listen on (local-only by default; see docs/design.md section 9)")
	dataDir := flag.String("data-dir", "", "maatplug data directory to read (default: $MAATPLUG_HOME, or the OS user config dir)")
	usageInterval := flag.Duration("usage-poll-interval", 5*time.Minute, "how often to re-check Claude Code/Codex CLI remaining usage (see internal/usage)")
	remoteSyncInterval := flag.Duration("remote-sync-interval", 10*time.Minute, "how often to re-sync SSH data areas (see internal/remotesync)")
	flag.Parse()

	s, err := resolveStore(*dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "maatplug-web: "+err.Error())
		os.Exit(1)
	}

	poller := usage.NewPoller(usageCheckDir(s.Root), *usageInterval)
	poller.Start(context.Background())

	syncPoller := remotesync.NewPoller(s, resolveRemoteSyncInterval(s, *remoteSyncInterval))
	syncPoller.Start(context.Background())

	handler := web.NewServer(s, poller, syncPoller)
	fmt.Printf("maatplug-web: serving %s on http://%s\n", s.Root, *addr)
	if err := http.ListenAndServe(*addr, handler); err != nil {
		fmt.Fprintln(os.Stderr, "maatplug-web: "+err.Error())
		os.Exit(1)
	}
}

// usageCheckDir is the working directory Claude Code/Codex CLI are run in
// to check remaining usage (internal/usage). Usage is per-account, not tied
// to any particular project, so in principle any existing directory would
// do — but it must not be a large or "interesting" one: verified live, the
// same maatplug SessionStart hook this project registers for itself
// (docs/design.md 6.1) makes Claude Code's own `--print` invocation
// silently fail when run from a directory expensive to look at (e.g. the
// user's home directory, where a cloc/git scan can take long enough to blow
// past whatever the hook's time budget is) — Claude Code aborts the whole
// session rather than surface that as ordinary output. A small dedicated
// subdirectory under the data store keeps every such scan trivially fast.
func usageCheckDir(storeRoot string) string {
	dir := store.UsageCheckDir(storeRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		// Fall back to the store root itself rather than failing startup over
		// this — usage checks will simply fail (and be excluded, per
		// internal/usage.Poller's startup contract check) if this dir can't be
		// created either.
		return storeRoot
	}
	return dir
}

func resolveStore(dataDir string) (*store.Store, error) {
	if dataDir != "" {
		return &store.Store{Root: dataDir}, nil
	}
	return store.New()
}

// resolveRemoteSyncInterval prefers an explicitly-passed --remote-sync-interval
// flag over whatever the settings screen has persisted (store.AppSettings)
// — an explicit flag is a deliberate operator override; otherwise the
// persisted value (edited from the web UI — see internal/web's
// handleSaveAppSettings) is the current setting, and flagDefault (the
// flag's own built-in default, unchanged) only applies to a fresh install
// with no settings.json yet, where GetAppSettings already returns that same
// default anyway.
func resolveRemoteSyncInterval(s *store.Store, flagDefault time.Duration) time.Duration {
	explicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "remote-sync-interval" {
			explicit = true
		}
	})
	if explicit {
		return flagDefault
	}
	settings, err := s.GetAppSettings()
	if err != nil {
		return flagDefault
	}
	return time.Duration(settings.RemoteSyncIntervalMinutes) * time.Minute
}
