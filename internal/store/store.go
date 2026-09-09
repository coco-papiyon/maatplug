// Package store reads and writes maatplug's flat-file JSONL event log.
// It is the only package that knows about the on-disk layout described in
// docs/design.md section 5.1, so every provider adapter, the CLI's
// report/status commands, and (later) the web app all go through it.
package store

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"maatplug/internal/model"
)

// Store resolves paths under the maatplug data root and appends/reads
// session event files.
type Store struct {
	Root string
}

// New resolves the data root: $MAATPLUG_HOME if set, otherwise the OS user
// config dir joined with "maatplug".
func New() (*Store, error) {
	if root := os.Getenv("MAATPLUG_HOME"); root != "" {
		return &Store{Root: root}, nil
	}
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user config dir: %w", err)
	}
	return &Store{Root: filepath.Join(cfgDir, "maatplug")}, nil
}

// ProjectID derives a stable, filesystem-safe identifier for a working
// directory: the git remote URL if available, otherwise the absolute path.
func ProjectID(cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}

	key := abs
	if remote, err := gitRemoteURL(abs); err == nil && remote != "" {
		key = remote
	}

	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:12]
}

// UsageCheckDir is the reserved directory maatplug-web runs Claude Code/
// Codex CLI in to probe remaining plan usage (internal/usage.Poller). Its
// own SessionStart/etc. hooks fire from that probe like any other `claude`
// invocation, so it inevitably gets recorded as a project like any other —
// aggregate excludes it by this same path so the probe never shows up as a
// row in reports or the dashboard.
func UsageCheckDir(root string) string {
	return filepath.Join(root, "usage-check")
}

func gitRemoteURL(dir string) (string, error) {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// EnsureProject writes projects/<id>/project.json if it does not already
// exist, so the project's display name/path are recorded the first time
// maatplug sees it.
func (s *Store) EnsureProject(projectID, name, path string) error {
	dir := s.projectDir(projectID)
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		return fmt.Errorf("create project dir: %w", err)
	}

	metaPath := filepath.Join(dir, "project.json")
	if _, err := os.Stat(metaPath); err == nil {
		return nil // already recorded
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	meta := struct {
		ID        string    `json:"id"`
		Path      string    `json:"path"`
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"createdAt"`
	}{ID: projectID, Path: path, Name: name, CreatedAt: time.Now()}

	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(metaPath, b, 0o644)
}

func (s *Store) projectDir(projectID string) string {
	return filepath.Join(s.Root, "projects", projectID)
}

// SessionPath returns the JSONL file a given provider session is recorded
// to: projects/<id>/sessions/<provider>-<sessionID>.jsonl.
func (s *Store) SessionPath(projectID, provider, sessionID string) string {
	fileName := fmt.Sprintf("%s-%s.jsonl", provider, sessionID)
	return filepath.Join(s.projectDir(projectID), "sessions", fileName)
}

// ParseSessionFileName splits a session file's base name
// ("<provider>-<sessionID>.jsonl", as built by SessionPath) back into
// provider and sessionID. It reports ok=false if name doesn't match that
// shape. Provider names (claude, codex, copilot) never contain "-", but
// sessionIDs commonly do (e.g. Claude Code's UUIDs), so this splits on the
// first "-" only.
func ParseSessionFileName(name string) (provider, sessionID string, ok bool) {
	const suffix = ".jsonl"
	if !strings.HasSuffix(name, suffix) {
		return "", "", false
	}
	base := strings.TrimSuffix(name, suffix)
	idx := strings.Index(base, "-")
	if idx <= 0 || idx == len(base)-1 {
		return "", "", false
	}
	return base[:idx], base[idx+1:], true
}

// AppendEvent marshals event as one JSON line and appends it to the given
// session file, creating parent directories as needed.
func (s *Store) AppendEvent(sessionPath string, event any) error {
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}

	b, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	f, err := os.OpenFile(sessionPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open session file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	return nil
}

// ReadEvents reads every line of a session file as a model.RawEvent. Missing
// files return an empty slice, not an error, since a session may not have
// started recording yet.
func ReadEvents(sessionPath string) ([]model.RawEvent, error) {
	f, err := os.Open(sessionPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var events []model.RawEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var ev model.RawEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // tolerate partially-written trailing lines
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// FindInstructionBaseline scans a session file for an unresolved instruction
// event with the given instructionId and returns its baseline snapshot. It is used
// by providers that receive an explicit id back from the platform they're
// adapting (exec-wrap providers, which run start-to-finish in one process
// and so don't strictly need this, but may still want the same lookup path
// as the hook-based flow below).
func FindInstructionBaseline(sessionPath, instructionID string) (model.DiffBaseline, bool, error) {
	events, err := ReadEvents(sessionPath)
	if err != nil {
		return model.DiffBaseline{}, false, err
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Type == model.EventTurnEnd && ev.InstructionID == instructionID {
			return model.DiffBaseline{}, false, nil
		}
		if ev.Type == model.EventInstruction && ev.InstructionID == instructionID {
			return ev.Baseline, true, nil
		}
	}
	return model.DiffBaseline{}, false, nil
}

// unresolvedInstructions returns every instruction event in events that does
// not yet have a matching turn_end, in file order.
func unresolvedInstructions(events []model.RawEvent) []model.RawEvent {
	resolved := map[string]bool{}
	for _, ev := range events {
		if ev.Type == model.EventTurnEnd {
			resolved[ev.InstructionID] = true
		}
	}

	var unresolved []model.RawEvent
	for _, ev := range events {
		if ev.Type == model.EventInstruction && !resolved[ev.InstructionID] {
			unresolved = append(unresolved, ev)
		}
	}
	return unresolved
}

// FindUnresolvedInstructions returns every instruction event in a session
// that does not yet have a matching turn_end, in submission order, plus the
// OLDEST one's baseline — the snapshot a single Stop covering all of them
// should measure its diff/tokens against. Claude Code hook processes are
// short-lived and stateless (see docs/design.md section 3.1): the
// UserPromptSubmit process that recorded an instruction has already exited
// by the time Stop fires, so Stop can't be handed the id(s) it needs
// directly — it recovers "which instruction(s) just finished" from the store
// itself instead.
//
// Claude Code normally processes one turn at a time per session, so there is
// only ever one unresolved instruction to find. But a user can also queue a
// follow-up message while Claude is still mid-turn — an ordinary, common
// Claude Code feature, not a rare edge case — which fires a second
// UserPromptSubmit before the first Stop, so this can return more than one
// id even for a single terminal with a single eventual Stop. That shape on
// disk is indistinguishable from the same session_id being resumed
// concurrently from two separate terminals (there is no reliable per-turn or
// per-terminal id in the Claude Code hook payload to tell them apart). Rather
// than try to guess which case it is, the caller
// (Recorder.TurnEndForLastInstruction) resolves every id returned here
// together, as one shared turn — correct for the common queued-message case,
// and see that method's doc comment for why it's an acceptable tradeoff for
// the rare concurrent-resume case too.
func FindUnresolvedInstructions(sessionPath string) (ids []string, baseline model.DiffBaseline, found bool, err error) {
	events, err := ReadEvents(sessionPath)
	if err != nil {
		return nil, model.DiffBaseline{}, false, err
	}

	unresolved := unresolvedInstructions(events)
	if len(unresolved) == 0 {
		return nil, model.DiffBaseline{}, false, nil
	}

	ids = make([]string, len(unresolved))
	for i, ev := range unresolved {
		ids[i] = ev.InstructionID
	}
	return ids, unresolved[0].Baseline, true, nil
}

// ListProjectIDs returns the project IDs known to this store.
func (s *Store) ListProjectIDs() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, "projects"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}

// ListSessionFiles returns every session JSONL file path under a project.
func (s *Store) ListSessionFiles(projectID string) ([]string, error) {
	dir := filepath.Join(s.projectDir(projectID), "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	return paths, nil
}

// ProjectMeta reads projects/<id>/project.json.
func (s *Store) ProjectMeta(projectID string) (name, path string, err error) {
	b, err := os.ReadFile(filepath.Join(s.projectDir(projectID), "project.json"))
	if err != nil {
		return "", "", err
	}
	var meta struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return "", "", err
	}
	return meta.Name, meta.Path, nil
}
