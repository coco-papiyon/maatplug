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
	case "collect":
		return runCollect(args[1:])
	case "rebuild-catalog":
		return runRebuildCatalog(args[1:])
	case "attribution":
		return runAttribution(args[1:])
	case "pricing":
		return runPricing(args[1:])
	case "billing":
		return runBilling(args[1:])
	case "operations":
		return runOperations(args[1:])
	case "doctor":
		return runDoctor(args[1:])
	case "query":
		return runQuery(args[1:])
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

  maatplug hook claude --event <hook-event>
  maatplug hook codex --event <hook-event>
      Read a Claude Code or Codex hook payload from stdin and record it.
      Hook configuration invokes this; you should not need to run it by hand.

  maatplug status [--project <id>]
      Print a one-line summary per project.

  maatplug report [--project <id>] [--format table|json]
      Print a detailed summary per project.

  maatplug collect --once
  maatplug collect --watch [--interval <duration>]
      Move inbox records into content objects, journals, and catalogs.

  maatplug rebuild-catalog [--project <id>]
      Rebuild disposable catalogs and summaries from journal streams.

  maatplug attribution apply --project <id> --file <policy.json>
  maatplug attribution activate --project <id> --version <version>
  maatplug attribution list --project <id>
      Save, select, and inspect immutable usage-attribution policies.

  maatplug pricing import --file <version.json> [--activate]
  maatplug pricing activate --version <version>
  maatplug pricing list
  maatplug pricing calculate --project <id> --version <version> [--service <name>] [--contract <name>] [--at <RFC3339>]
  maatplug pricing conflicts --project <id> [--version <version>] [--service <name>] [--contract <name>]
      Manage immutable pricing versions and reproducible cost projections.

  maatplug billing import --file <records.json|records.csv> [--format json|csv]
  maatplug billing list
  maatplug billing convert --file <conversion.json>
  maatplug billing allocate --file <allocation.json>
      Import and inspect bills, payments, adjustments, credits, and refunds.

  maatplug operations --project <id> [--kind <kind>] [--status <status>]
      Rebuild and print operation lifecycle, environment, effects, and artifacts.

  maatplug query projects|project|work|periods|sessions|session|run|instruction|call [flags]
      Query new-format journals through the shared CLI/Web read service.

  maatplug doctor collection [--json] [--record-baseline]
      Report pending, quarantined, damaged, or stale collection state.
`)
}
