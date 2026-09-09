package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"maatplug/internal/provider/claude"
	"maatplug/internal/provider/codex"
	"maatplug/internal/recorder"
	"maatplug/internal/store"
)

// runHook implements `maatplug hook <provider> --event <Name>`: it is the
// short-lived process Claude Code's hooks spawn per docs/design.md section
// 3.1 — read stdin, record one event, exit.
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

	switch provider {
	case "claude":
		return runClaudeHook(*event)
	case "codex":
		return runCodexHook(*event)
	default:
		return fmt.Errorf("unsupported provider %q (want \"claude\" or \"codex\")", provider)
	}
}

func runCodexHook(event string) error {
	if event == "" {
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
	return codex.NewHandler(recorder.New(s)).Handle(event, stdin)
}

func runClaudeHook(event string) error {
	if event == "" {
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

	handler := claude.NewHandler(recorder.New(s))
	return handler.Handle(event, stdin)
}
