package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// claudeUsageWindowRe matches one line of the `/usage` slash command's
// output, verified against real output from `claude --print /usage
// --output-format json`, e.g.:
//
//	Current session: 49% used · resets Sep 9, 1:39am (Asia/Tokyo)
//	Current week (all models): 11% used · resets Sep 14, 12:59pm (Asia/Tokyo)
//
// An account with no Claude Code plan to report against (API-key-only
// billing) doesn't print these lines at all, which is what lets FetchClaude
// double as the "is there a contract to poll" check: no matches means no
// error from the CLI itself, just nothing here to report (see Poller).
var claudeUsageWindowRe = regexp.MustCompile(`(?m)^Current (session|week \(all models\)):\s*(\d+)% used\s*[·•|]\s*resets\s+(.+)$`)

// FetchClaude asks Claude Code's own `/usage` slash command for the
// account's current usage-limit windows, via headless print mode so this
// can run unattended (no TUI). dir just needs to be an existing directory —
// usage is account-wide, not tied to what's in it.
func FetchClaude(ctx context.Context, dir string) ([]Window, error) {
	binary, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("claude executable not found: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--print", "/usage", "--output-format", "json")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("claude /usage: %w: %s", err, exitErr.Stderr)
		}
		return nil, fmt.Errorf("claude /usage: %w", err)
	}

	var response struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
	}
	if err := json.Unmarshal(out, &response); err != nil {
		return nil, fmt.Errorf("decode claude /usage output: %w", err)
	}
	if response.IsError {
		return nil, errors.New("claude /usage command failed")
	}

	return parseClaudeUsageWindows(response.Result)
}

func parseClaudeUsageWindows(text string) ([]Window, error) {
	matches := claudeUsageWindowRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil, errors.New("no Claude Code usage windows in /usage output (no Claude Code plan on this account?)")
	}
	windows := make([]Window, 0, len(matches))
	for _, m := range matches {
		used, err := strconv.Atoi(m[2])
		if err != nil || used < 0 || used > 100 {
			return nil, fmt.Errorf("claude /usage: invalid percentage %q", m[2])
		}
		name := m[1]
		if name == "week (all models)" {
			name = "week"
		}
		windows = append(windows, Window{
			Name: name, UsedPercent: used, RemainingPercent: 100 - used,
			ResetLabel: strings.TrimSpace(m[3]),
		})
	}
	return windows, nil
}
