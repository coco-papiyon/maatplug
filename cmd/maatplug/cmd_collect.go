package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"maatplug/internal/collector"
	"maatplug/internal/pricing"
	"maatplug/internal/store"
)

func runCollect(args []string) error {
	fs := flag.NewFlagSet("collect", flag.ContinueOnError)
	once := fs.Bool("once", false, "drain all ready inbox records and exit")
	watch := fs.Bool("watch", false, "keep collecting until interrupted")
	interval := fs.Duration("interval", time.Second, "watch polling interval")
	heartbeat := fs.Duration("heartbeat", 30*time.Second, "watch heartbeat interval (0 disables heartbeats)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *once == *watch {
		return errors.New("collect requires exactly one of --once or --watch")
	}
	if *heartbeat < 0 {
		return errors.New("collect heartbeat must not be negative")
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	worker := collector.New(s.Root)
	worker.Jobs = append(worker.Jobs, pricing.DefaultRefreshWorker(s.Root))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	logger := log.New(os.Stdout, "", log.LstdFlags)
	mode := "watch"
	if *once {
		mode = "once"
	}
	logger.Printf("collector started: mode=%s heartbeat=%s", mode, heartbeatLabel(*heartbeat))
	worker.OnProgress = func(progress collector.TranscriptProgress) {
		logger.Printf("collector progress: provider=%s bytes=%d/%d lines=%d more=%t", progress.Provider, progress.ProcessedBytes, progress.TotalBytes, progress.ProcessedLines, progress.HasMore)
	}
	worker.OnError = func(err error) {
		logger.Printf("collector cycle error: %v; retrying in %s", err, *interval)
	}
	waitHeartbeat := startCollectorHeartbeat(ctx, *heartbeat, logger, worker)
	if *once {
		stats, err := worker.RunOnce(ctx)
		stop()
		waitHeartbeat()
		printStatsJSON(stats)
		return err
	}
	logger.Printf("collector watching: interval=%s", *interval)
	err = worker.Watch(ctx, *interval, func(stats collector.Stats) {
		if stats.Claimed > 0 || stats.Recovered > 0 || stats.Quarantined > 0 {
			logger.Printf("collector cycle: claimed=%d completed=%d recovered=%d retried=%d failed=%d duplicates=%d catalogs=%d", stats.Claimed, stats.Completed, stats.Recovered, stats.Retried, stats.Quarantined, stats.Duplicates, stats.Catalogs)
		}
	})
	stop()
	waitHeartbeat()
	if errors.Is(err, context.Canceled) {
		logger.Print("collector stopped")
		return nil
	}
	return err
}

func startCollectorHeartbeat(ctx context.Context, interval time.Duration, logger *log.Logger, worker *collector.Collector) func() {
	var heartbeatWG sync.WaitGroup
	if interval > 0 {
		heartbeatWG.Add(1)
		go func() {
			defer heartbeatWG.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					logCollectorHeartbeat(logger, worker)
				}
			}
		}()
	}
	return func() {
		heartbeatWG.Wait()
	}
}

func heartbeatLabel(interval time.Duration) string {
	if interval == 0 {
		return "disabled"
	}
	return interval.String()
}

func logCollectorHeartbeat(logger *log.Logger, worker *collector.Collector) {
	diagnostics, err := worker.Inbox.Diagnostics()
	if err != nil {
		logger.Printf("collector heartbeat: diagnostics_error=%q", err)
		return
	}
	status := "unknown"
	if state, err := worker.LoadWorkerState(); err == nil && state.Status != "" {
		status = state.Status
	}
	logger.Printf("collector heartbeat: status=%s pending=%d processing=%d failed=%d", status, diagnostics.Pending, diagnostics.Processing, diagnostics.Failed)
}

func printStatsJSON(value any) {
	data, err := json.Marshal(value)
	if err == nil {
		fmt.Println(string(data))
	}
}
