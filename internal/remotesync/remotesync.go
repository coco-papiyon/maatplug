// Package remotesync mirrors a remote data area's maatplug data directory
// onto local disk over SSH, incrementally: only files that are missing or
// have grown since the last sync are fetched.
//
// The connection method is deliberately fixed to plain `ssh` (the OpenSSH
// client — present by default on macOS/Linux and on Windows 10 1809+),
// never rsync or scp: it's the one transport guaranteed to already be on
// the machine, and it's all that's needed here. maatplug never accepts a
// username, password, or key path for a remote area — sshHost is always a
// Host alias the user has already configured (with User/IdentityFile) in
// their own ~/.ssh/config, and every invocation here runs with
// `-o BatchMode=yes` so a misconfigured host fails fast instead of hanging
// a background sync on an interactive password prompt.
//
// maatplug's own data layout keeps this simple: project/session directory
// and file names are always sha256-derived hex or the provider's own
// session-id shape (see internal/store.ProjectID, SessionPath) — never
// user-chosen, so never containing spaces or shell metacharacters. The
// remote listing script below still shell-quotes the one genuinely
// user-supplied value (remotePath) defensively.
package remotesync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Result summarizes one Sync call.
type Result struct {
	FilesChecked int
	FilesFetched int
	BytesFetched int64
}

// sshBinary is looked up once; overridable in tests.
const sshBinary = "ssh"

// sshBaseArgs are prepended to every ssh invocation this package makes:
// BatchMode disables all interactive prompting (password, host-key
// confirmation) so a background sync against a broken/unreachable host
// fails within ConnectTimeout instead of hanging indefinitely.
func sshBaseArgs(sshHost string) []string {
	return []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", sshHost}
}

// Check verifies sshHost is reachable (via the user's own ~/.ssh/config)
// and remotePath exists there as a directory — a fast, side-effect-free
// connectivity check for immediate feedback right after a user adds an SSH
// data area, rather than waiting for the next background Poller cycle to
// discover a typo.
func Check(ctx context.Context, sshHost, remotePath string) error {
	if _, err := lookSSH(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	args := append(sshBaseArgs(sshHost), "test", "-d", remotePath)
	if _, err := runSSH(ctx, args...); err != nil {
		return fmt.Errorf("cannot reach %q at %s: %w", remotePath, sshHost, err)
	}
	return nil
}

// Sync mirrors remotePath (on sshHost) into localDir, fetching only files
// that are new or have grown since the last sync — see the package doc
// comment for why "grown" (not just "missing") matters: maatplug's own
// session files are append-only logs that keep growing while a session is
// active, so a file already present locally can still be incomplete.
// Unchanged files (same size) are skipped entirely, at zero network cost
// beyond the one listing call.
func Sync(ctx context.Context, sshHost, remotePath, localDir string) (Result, error) {
	if _, err := lookSSH(); err != nil {
		return Result{}, err
	}

	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	remoteFiles, err := listRemoteFiles(listCtx, sshHost, remotePath)
	if err != nil {
		return Result{}, err
	}

	result := Result{FilesChecked: len(remoteFiles)}
	for relPath, remoteSize := range remoteFiles {
		localPath := filepath.Join(localDir, filepath.FromSlash(relPath))
		if !needsFetch(localPath, remoteSize) {
			continue
		}

		remoteAbs := path.Join(remotePath, relPath)
		fetchCtx, fetchCancel := context.WithTimeout(ctx, 60*time.Second)
		err := fetchFile(fetchCtx, sshHost, remoteAbs, localPath)
		fetchCancel()
		if err != nil {
			return result, fmt.Errorf("fetch %s: %w", relPath, err)
		}
		result.FilesFetched++
		result.BytesFetched += remoteSize
	}
	return result, nil
}

// needsFetch reports whether localPath must be (re-)downloaded: missing
// entirely, or present but a different size than the remote copy (covers
// both "grew since last sync" and "shrank/changed" — either way the local
// copy no longer matches).
func needsFetch(localPath string, remoteSize int64) bool {
	info, err := os.Stat(localPath)
	if err != nil {
		return true
	}
	return info.Size() != remoteSize
}

// listRemoteFiles runs a small POSIX-sh script over ssh that lists every
// regular file under remotePath with its byte size, and returns them keyed
// by path relative to remotePath (forward-slash separated, matching the
// remote's own convention regardless of the local OS). `wc -c` (not `find
// -printf`, which is GNU-only and absent on macOS's BSD find) keeps this
// portable across whatever *nix the remote host runs.
func listRemoteFiles(ctx context.Context, sshHost, remotePath string) (map[string]int64, error) {
	script := fmt.Sprintf(
		`cd %s && find . -type f | while IFS= read -r f; do printf '%%s\t%%s\n' "$(wc -c < "$f")" "$f"; done`,
		shellQuote(remotePath),
	)
	args := append(sshBaseArgs(sshHost), script)
	out, err := runSSH(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("list remote files at %s: %w", sshHost, err)
	}
	return parseRemoteFileList(out)
}

// parseRemoteFileList parses listRemoteFiles' "<size>\t<./relative/path>"
// output into a map keyed by relative path with the "./" prefix stripped.
// Split out as a pure function (no exec involved) so the parsing itself —
// the part actually at risk of a subtly wrong remote script or an
// unexpected line shape — is unit-testable without a real ssh binary.
func parseRemoteFileList(out []byte) (map[string]int64, error) {
	files := map[string]int64{}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		sizeStr, relPath, ok := strings.Cut(line, "\t")
		if !ok {
			continue // tolerate a stray/malformed line rather than failing the whole sync
		}
		size, err := strconv.ParseInt(strings.TrimSpace(sizeStr), 10, 64)
		if err != nil {
			continue
		}
		relPath = strings.TrimPrefix(relPath, "./")
		if relPath == "" {
			continue
		}
		files[relPath] = size
	}
	return files, nil
}

// fetchFile downloads one remote file to localPath, via `ssh sshHost cat
// <path>` rather than scp: it reuses the exact same transport/auth
// resolution (~/.ssh/config) as every other call in this package, with one
// fewer external binary to depend on. Written to a temp file first and
// renamed into place, so a failed or interrupted transfer never leaves a
// corrupt partial file at localPath — needsFetch's size comparison depends
// on a complete-or-absent invariant to work correctly next time.
func fetchFile(ctx context.Context, sshHost, remoteAbsPath, localPath string) error {
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(localPath), filepath.Base(localPath)+".download-*")
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)

	args := append(sshBaseArgs(sshHost), "cat", shellQuote(remoteAbsPath))
	runErr := runSSHInto(ctx, f, args...)
	closeErr := f.Close()
	if runErr != nil {
		return runErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmpPath, localPath)
}

// shellQuote single-quotes s for the remote POSIX shell, escaping any
// embedded single quotes — remotePath is the one genuinely user-supplied
// value passed through to a remote shell here (see the package doc
// comment on why every other path segment is safe by construction).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func lookSSH() (string, error) {
	p, err := execLookPath(sshBinary)
	if err != nil {
		return "", errors.New("ssh executable not found (OpenSSH client is required for remote data areas)")
	}
	return p, nil
}
