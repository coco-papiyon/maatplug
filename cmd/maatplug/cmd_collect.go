package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *once == *watch {
		return errors.New("collect requires exactly one of --once or --watch")
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	worker := collector.New(s.Root)
	worker.Jobs = append(worker.Jobs, pricing.DefaultRefreshWorker(s.Root))
	if *once {
		stats, err := worker.RunOnce(context.Background())
		printStatsJSON(stats)
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = worker.Watch(ctx, *interval, func(stats collector.Stats) {
		if stats.Claimed > 0 || stats.Recovered > 0 || stats.Quarantined > 0 {
			printStatsJSON(stats)
		}
	})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func printStatsJSON(value any) {
	data, err := json.Marshal(value)
	if err == nil {
		fmt.Println(string(data))
	}
}
