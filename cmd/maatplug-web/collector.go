package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// collectorMaxBackoff caps how long startCollector waits between restart
// attempts after the child "maatplug collect --watch" process exits
// unexpectedly.
const collectorMaxBackoff = 30 * time.Second

// resolveMaatplugBinary locates the maatplug CLI binary that startCollector
// spawns. go install ./cmd/maatplug ./cmd/maatplug-web (start.bat) and
// scripts/package.ps1 both place maatplug(.exe) directly alongside
// maatplug-web(.exe), so that sibling path is tried first; PATH is the
// fallback for anyone who has moved the binaries apart.
func resolveMaatplugBinary() (string, error) {
	name := "maatplug"
	if runtime.GOOS == "windows" {
		name = "maatplug.exe"
	}
	if self, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(self), name)
		if _, statErr := os.Stat(sibling); statErr == nil {
			return sibling, nil
		}
	}
	return exec.LookPath(name)
}

// startCollector runs "<binary> collect --watch" as maatplug-web's own child
// process for the lifetime of ctx, so a single maatplug-web invocation is
// enough to both serve the dashboard and keep collection running (see
// docs/design.md section 9). Output from the child is copied to out with a
// "[collect] " prefix so it's distinguishable from maatplug-web's own log
// lines in a shared console window.
//
// collect --watch already recovers cleanly from being interrupted mid-cycle
// (internal/collector's inbox claim/recover protocol), so if the child exits
// on its own — a transient store error, for instance — startCollector just
// restarts it after a short backoff rather than treating it as fatal to
// maatplug-web. The backoff resets once a run has stayed up longer than
// collectorMaxBackoff, so a crash loop backs off but a single blip doesn't
// permanently slow future restarts. It returns immediately; the child and
// its supervision loop run on a background goroutine and are killed when ctx
// is canceled.
func startCollector(ctx context.Context, binary string, out io.Writer) {
	go func() {
		const prefix = "[collect] "
		backoff := time.Second
		for ctx.Err() == nil {
			cmd := exec.CommandContext(ctx, binary, "collect", "--watch")
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				fmt.Fprintf(out, "%sfailed to attach stdout: %v\n", prefix, err)
				return
			}
			stderr, err := cmd.StderrPipe()
			if err != nil {
				fmt.Fprintf(out, "%sfailed to attach stderr: %v\n", prefix, err)
				return
			}
			if err := cmd.Start(); err != nil {
				fmt.Fprintf(out, "%sfailed to start: %v\n", prefix, err)
				return
			}
			go streamPrefixed(stdout, prefix, out)
			go streamPrefixed(stderr, prefix, out)

			startedAt := time.Now()
			runErr := cmd.Wait()
			if ctx.Err() != nil {
				return
			}
			if runErr != nil {
				fmt.Fprintf(out, "%sexited: %v\n", prefix, runErr)
			}
			if time.Since(startedAt) > collectorMaxBackoff {
				backoff = time.Second
			}
			fmt.Fprintf(out, "%srestarting in %s\n", prefix, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff *= 2; backoff > collectorMaxBackoff {
				backoff = collectorMaxBackoff
			}
		}
	}()
}

func streamPrefixed(r io.Reader, prefix string, out io.Writer) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		fmt.Fprintf(out, "%s%s\n", prefix, scanner.Text())
	}
}
