// Package gitdiff measures source-change volume around an instruction, per
// docs/design.md section 6.4.
//
// The primary path snapshots the full working tree (tracked changes and
// untracked-but-not-ignored files) into a git tree object at instruction
// start and again at turn end, then diffs the two tree hashes directly.
// This is deliberately not "diff-from-HEAD, subtract two readings": two
// cumulative-vs-HEAD readings can't be subtracted into a correct per-turn
// delta once a file is touched in more than one turn (the second turn's
// file either double-counts or, if lines added in turn 1 are partly removed
// in turn 2, the arithmetic can even go negative and get clamped to zero,
// silently dropping the edit). Diffing two tree snapshots directly has
// neither failure mode: it is git's own diff between two known states, so
// the same file being edited turn after turn just keeps producing correct
// per-turn hunks.
//
// Non-git directories can't produce tree objects, so DiffCloc offers a
// coarser fallback based on two cloc line-count snapshots (see
// internal/cloc): it can only report net totals, not true added/removed
// (e.g. it can't tell "10 lines added" from "15 added, 5 removed").
package gitdiff

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"maatplug/internal/gitutil"
	"maatplug/internal/model"
)

// SnapshotGitTree stages the current working tree (tracked changes plus
// untracked-but-not-ignored files, exactly like `git add -A` would, but
// through a scratch index so the caller's real index and working tree are
// left untouched) and returns the resulting tree object hash. Call it once
// before an instruction and once after; DiffTrees compares the two hashes.
func SnapshotGitTree(dir string) (string, error) {
	tmpIndex, err := os.CreateTemp("", "maatplug-index-*")
	if err != nil {
		return "", fmt.Errorf("create scratch index: %w", err)
	}
	tmpIndexPath := tmpIndex.Name()
	tmpIndex.Close()
	// os.CreateTemp leaves a real (empty) file behind, but git treats
	// GIT_INDEX_FILE pointing at an existing zero-byte file as a corrupt
	// index rather than "start fresh" — it only auto-initializes when the
	// path doesn't exist yet. Remove it now so `git read-tree`/`git add`
	// create it themselves; only the unique path is needed from CreateTemp.
	os.Remove(tmpIndexPath)
	defer os.Remove(tmpIndexPath)

	env := append(os.Environ(), "GIT_INDEX_FILE="+tmpIndexPath)

	if head := gitutil.HeadCommit(dir); head != "" {
		if err := runGit(dir, env, "read-tree", head); err != nil {
			return "", fmt.Errorf("seed scratch index from HEAD: %w", err)
		}
	}

	if err := runGit(dir, env, "add", "-A"); err != nil {
		return "", fmt.Errorf("stage working tree into scratch index: %w", err)
	}

	cmd := exec.Command("git", "write-tree")
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("write-tree: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func runGit(dir string, env []string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	return cmd.Run()
}

// DiffTrees returns the exact line/file delta between two tree snapshots
// taken by SnapshotGitTree.
func DiffTrees(dir, treeBefore, treeAfter string) (model.DiffStat, error) {
	if treeBefore == "" || treeAfter == "" {
		return model.DiffStat{}, nil
	}
	if treeBefore == treeAfter {
		return model.DiffStat{}, nil
	}

	cmd := exec.Command("git", "diff", treeBefore, treeAfter, "--numstat")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return model.DiffStat{}, fmt.Errorf("diff scratch trees: %w", err)
	}

	var stat model.DiffStat
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 3 {
			continue
		}
		stat.FilesChanged++ // count the file even for a binary "-\t-\tpath" line
		if a, err := strconv.Atoi(fields[0]); err == nil {
			stat.Added += a
		}
		if r, err := strconv.Atoi(fields[1]); err == nil {
			stat.Removed += r
		}
	}
	return stat, scanner.Err()
}

// DiffCloc is the non-git fallback: given two cloc snapshots, it reports the
// net change in code lines as either all-added or all-removed (whichever
// direction the total moved) and a best-effort file count. It cannot detect
// simultaneous additions and removals within the same turn — that
// granularity requires the git tree-diff path above.
func DiffCloc(before, after *model.ClocResult) model.DiffStat {
	if before == nil || after == nil {
		return model.DiffStat{}
	}

	var stat model.DiffStat
	if codeDelta := after.Code - before.Code; codeDelta > 0 {
		stat.Added = codeDelta
	} else if codeDelta < 0 {
		stat.Removed = -codeDelta
	}

	filesDelta := after.Files - before.Files
	switch {
	case filesDelta > 0:
		stat.FilesChanged = filesDelta
	case filesDelta < 0:
		stat.FilesChanged = -filesDelta
	case stat.Added > 0 || stat.Removed > 0:
		stat.FilesChanged = 1 // same file count, but content moved; can't know how many files
	}
	return stat
}
