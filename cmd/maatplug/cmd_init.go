package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"maatplug/internal/provider/claude"
)

// runInit previews (default) or applies (--write) the merge of maatplug's
// Claude Code hooks into settings.json. It defaults to a dry run because
// settings.json is shared, hand-edited configuration outside this repo —
// see the operating guidance on confirming before touching state like that.
func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	write := fs.Bool("write", false, "apply the change instead of just previewing it")
	scope := fs.String("scope", "project", "where to register hooks: \"user\" (~/.claude/settings.json) or \"project\" (.claude/settings.json in the current directory)")
	binary := fs.String("binary", "maatplug", "command used in the registered hooks (override if maatplug is not on PATH)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	path, err := settingsPath(*scope)
	if err != nil {
		return err
	}

	settings, err := claude.LoadSettings(path)
	if err != nil {
		return err
	}

	changed := claude.MergeHooks(settings, *binary)
	if !changed {
		fmt.Printf("maatplug hooks are already registered in %s\n", path)
		return nil
	}

	out, err := claude.MarshalSettings(settings)
	if err != nil {
		return err
	}

	if !*write {
		fmt.Printf("Would write the following to %s (rerun with --write to apply):\n\n%s\n", path, out)
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Printf("Registered maatplug hooks in %s\n", path)
	return nil
}

func settingsPath(scope string) (string, error) {
	switch scope {
	case "user":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		return filepath.Join(home, ".claude", "settings.json"), nil
	case "project":
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current directory: %w", err)
		}
		return filepath.Join(cwd, ".claude", "settings.json"), nil
	default:
		return "", fmt.Errorf("unknown --scope %q (want \"user\" or \"project\")", scope)
	}
}
