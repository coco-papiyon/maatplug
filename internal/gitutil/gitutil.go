// Package gitutil holds the small git primitives shared by internal/cloc
// (repo detection, for --vcs=git) and internal/gitdiff (diff/HEAD lookups).
package gitutil

import (
	"os/exec"
	"strings"
)

// IsRepo reports whether dir is inside a git working tree.
func IsRepo(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// HeadCommit returns the current HEAD commit hash, or "" if there is none
// (e.g. a freshly initialized repo with no commits yet).
func HeadCommit(dir string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
