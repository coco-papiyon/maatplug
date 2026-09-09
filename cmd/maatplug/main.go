// Command maatplug is the write path (Claude Code hook receiver, plus the
// `init` command that registers those hooks) and a terminal-only read path
// (status/report) for maatplug's flat-file usage log. See docs/design.md.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "maatplug: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}

	switch args[0] {
	case "init":
		return runInit(args[1:])
	case "hook":
		return runHook(args[1:])
	case "status":
		return runStatus(args[1:])
	case "report":
		return runReport(args[1:])
	case "-h", "--help", "help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q (see: maatplug --help)", args[0])
	}
}

func usageError() error {
	printUsage()
	return fmt.Errorf("no command given")
}

func printUsage() {
	fmt.Fprint(os.Stderr, `maatplug — usage/token tracker for AI coding agents

Usage:
  maatplug init [--write] [--scope user|project]
      Register (or preview, without --write) maatplug's Claude Code hooks.

  maatplug hook claude --event <SessionStart|UserPromptSubmit|Stop|SessionEnd>
  maatplug hook codex --event <SessionStart|UserPromptSubmit|Stop|SessionEnd>
      Read a Claude Code or Codex hook payload from stdin and record it.
      Hook configuration invokes this; you should not need to run it by hand.

  maatplug status [--project <id>]
      Print a one-line summary per project.

  maatplug report [--project <id>] [--format table|json]
      Print a detailed summary per project.
`)
}
