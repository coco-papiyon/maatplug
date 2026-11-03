// Command maatplug-web is the browser dashboard and settings UI for
// maatplug's flat-file usage log. HTTP request handling is self-contained —
// it reads via internal/query directly and never shells out to maatplug per
// request (see docs/design.md section 9) — but at startup it spawns
// `maatplug collect --watch` as its own supervised child process (see
// collector.go) so that a single maatplug-web invocation both serves the
// dashboard and keeps collection running; pass --no-collect to skip that
// when collection is managed separately (e.g. a remote data area collected
// on another host).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
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
	noCollect := flag.Bool("no-collect", false, "don't spawn 'maatplug collect --watch' as a child process (use when collection is already managed separately)")
	flag.Parse()

	s, err := resolveStore(*dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "maatplug-web: "+err.Error())
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *noCollect {
		fmt.Println("maatplug-web: --no-collect set, not spawning a collector")
	} else if binary, err := resolveMaatplugBinary(); err != nil {
		fmt.Fprintf(os.Stderr, "maatplug-web: collector disabled: maatplug binary not found: %v\n", err)
	} else {
		fmt.Printf("maatplug-web: spawning collector: %s collect --watch\n", binary)
		startCollector(ctx, binary, os.Stdout)
	}

	poller := usage.NewPoller(usageCheckDir(s.Root), *usageInterval)
	poller.Start(ctx)

	syncPoller := remotesync.NewPoller(s, resolveRemoteSyncInterval(s, *remoteSyncInterval))
	syncPoller.Start(ctx)

	handler := web.NewServer(s, poller, syncPoller)
	server := &http.Server{Addr: *addr, Handler: handler}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
	}()

	fmt.Printf("maatplug-web: serving %s on http://%s\n", s.Root, *addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
