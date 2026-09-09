package usage

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// FetchCodex asks Codex CLI's app-server for the account's current
// rate-limit windows via its `account/rateLimits/read` JSON-RPC method.
// Unlike Claude Code, Codex has no simpler CLI-only usage command — the
// app-server protocol is the only way to reach this data, following the
// reference implementation this mirrors (github.com/coco-papiyon/maatgen's
// Codex adapter).
//
// The handshake starts an *ephemeral* thread (ephemeral:true) purely to get
// a session context to call account/rateLimits/read in. Verified live
// (Codex CLI 0.153.4): a non-ephemeral thread/start writes a rollout file
// under ~/.codex/sessions on every call, which a periodic background poller
// must not leave behind each cycle; ephemeral:true reports thread.path as
// null and writes nothing.
func FetchCodex(ctx context.Context, dir string) ([]Window, error) {
	binary, err := exec.LookPath("codex")
	if err != nil {
		return nil, fmt.Errorf("codex executable not found: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	conn, err := startCodexAppServer(ctx, binary, dir)
	if err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}
	defer conn.close()

	if err := conn.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "maatplug-web", "version": "0.1.0"},
		"capabilities": map[string]bool{"experimentalApi": true},
	}, nil); err != nil {
		return nil, fmt.Errorf("codex app-server initialize: %w", err)
	}
	if err := conn.notify("initialized", map[string]any{}); err != nil {
		return nil, fmt.Errorf("codex app-server initialized: %w", err)
	}

	if err := conn.call(ctx, "thread/start", map[string]any{
		"cwd": dir, "sandbox": "workspace-write", "approvalPolicy": "on-request", "ephemeral": true,
	}, nil); err != nil {
		return nil, fmt.Errorf("codex app-server thread/start: %w", err)
	}

	var response codexRateLimitsResponse
	if err := conn.call(ctx, "account/rateLimits/read", map[string]any{}, &response); err != nil {
		return nil, fmt.Errorf("codex account/rateLimits/read: %w", err)
	}

	windows := response.windows()
	if len(windows) == 0 {
		return nil, errors.New("codex rate limits response had no usage windows (no ChatGPT plan on this account?)")
	}
	return windows, nil
}

// codexRateLimitsResponse mirrors the subset of account/rateLimits/read's
// result this package uses, verified live against Codex CLI 0.153.4's
// actual JSON (the full response carries more fields — plan type, spend
// credits, upsell info — none of which maatplug-web's usage badge needs).
type codexRateLimitsResponse struct {
	RateLimits *codexRateLimitSnapshot `json:"rateLimits"`
}

type codexRateLimitSnapshot struct {
	Primary   *codexRateLimitWindow `json:"primary"`
	Secondary *codexRateLimitWindow `json:"secondary"`
}

type codexRateLimitWindow struct {
	UsedPercent int   `json:"usedPercent"`
	ResetsAt    int64 `json:"resetsAt"`
}

func (r codexRateLimitsResponse) windows() []Window {
	if r.RateLimits == nil {
		return nil
	}
	var windows []Window
	for _, item := range []struct {
		name   string
		window *codexRateLimitWindow
	}{
		{"primary", r.RateLimits.Primary},
		{"secondary", r.RateLimits.Secondary},
	} {
		if item.window == nil {
			continue
		}
		used := item.window.UsedPercent
		switch {
		case used < 0:
			used = 0
		case used > 100:
			used = 100
		}
		resetLabel := ""
		if item.window.ResetsAt > 0 {
			resetLabel = time.Unix(item.window.ResetsAt, 0).Local().Format("Jan 2, 3:04pm")
		}
		windows = append(windows, Window{
			Name: item.name, UsedPercent: used, RemainingPercent: 100 - used, ResetLabel: resetLabel,
		})
	}
	return windows
}
