package remotesync

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// execLookPath and the run* helpers below are the package's only points of
// contact with an actual subprocess — kept together and thin so the real
// logic (parseRemoteFileList, needsFetch) stays plain, unit-testable Go.

func execLookPath(name string) (string, error) {
	return exec.LookPath(name)
}

// runSSH runs `ssh <args...>` and returns its stdout, wrapping a failure
// with whatever the command wrote to stderr (ssh itself is informative
// there — "Could not resolve hostname", "Permission denied
// (publickey)", etc.).
func runSSH(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, sshBinary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, wrapSSHError(args, err, stderr.String())
	}
	return out, nil
}

// runSSHInto runs `ssh <args...>` with stdout streamed directly to w
// (fetchFile's destination file), rather than buffered — the same
// wrap-stderr-on-failure behavior as runSSH.
func runSSHInto(ctx context.Context, w io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, sshBinary, args...)
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return wrapSSHError(args, err, stderr.String())
	}
	return nil
}

func wrapSSHError(args []string, err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if stderr != "" {
		return fmt.Errorf("ssh %s: %w: %s", strings.Join(args, " "), err, stderr)
	}
	return fmt.Errorf("ssh %s: %w", strings.Join(args, " "), err)
}
