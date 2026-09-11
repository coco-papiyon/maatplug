package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"maatplug/internal/ingest"
	"maatplug/internal/store"
)

// runHook is deliberately short-lived: it only commits the provider payload
// to the inbox. Parsing and all other durable writes happen in collect.
func runHook(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: maatplug hook <provider> --event <Name>")
	}

	provider := args[0]
	fs := flag.NewFlagSet("hook "+provider, flag.ContinueOnError)
	event := fs.String("event", "", "hook event name")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	if provider != "claude" && provider != "codex" {
		return fmt.Errorf("unsupported provider %q (want \"claude\" or \"codex\")", provider)
	}
	if *event == "" {
		return fmt.Errorf("--event is required")
	}
	stdin, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	_, err = ingest.New(s.Root).EnqueueHook(provider, *event, stdin)
	return err
}
