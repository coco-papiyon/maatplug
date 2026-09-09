// Package cloc wraps the optional `cloc` binary. Per docs/design.md section
// 7, its absence must never fail the caller: Available reports whether to
// bother, and Run degrades to (nil, nil) rather than an error if cloc
// disappears between the check and the call.
package cloc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"

	"maatplug/internal/gitutil"
	"maatplug/internal/model"
)

// Available reports whether the cloc binary is on PATH.
func Available() bool {
	_, err := exec.LookPath("cloc")
	return err == nil
}

// clocLangEntry mirrors one language's block in `cloc --json` output.
type clocLangEntry struct {
	NFiles  int `json:"nFiles"`
	Blank   int `json:"blank"`
	Comment int `json:"comment"`
	Code    int `json:"code"`
}

// Run executes cloc against dir and returns the aggregate line counts plus a
// per-language breakdown. It returns (nil, nil) if cloc is not installed, so
// callers can treat that as "skip" rather than a failure.
//
// Inside a git repo, the file list is built from `git ls-files --cached
// --others --exclude-standard` (tracked + untracked-but-not-ignored) and fed
// to cloc via --list-file=-, instead of using cloc's own --vcs=git. cloc's
// --vcs=git runs a plain `git ls-files`, which omits untracked files — that
// would silently exclude brand new files an agent just created and hasn't
// `git add`ed yet, undercounting exactly the change this tool exists to
// measure.
func Run(dir string) (*model.ClocResult, error) {
	if !Available() {
		return nil, nil
	}

	cmd, err := buildClocCmd(dir)
	if err != nil {
		return nil, err
	}
	if cmd == nil {
		return nil, nil // git repo with no files to count
	}

	out, err := cmd.Output()
	if err != nil {
		// cloc exits non-zero when it finds nothing to count; treat that the
		// same as "nothing to report" rather than surfacing an error.
		if len(out) == 0 {
			return nil, nil
		}
	}

	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}

	result := &model.ClocResult{ByLang: map[string]int{}}
	for lang, msg := range raw {
		if lang == "header" {
			continue
		}
		var entry clocLangEntry
		if err := json.Unmarshal(msg, &entry); err != nil {
			continue
		}
		if lang == "SUM" {
			result.Files = entry.NFiles
			result.Blank = entry.Blank
			result.Comment = entry.Comment
			result.Code = entry.Code
			continue
		}
		result.ByLang[lang] = entry.Code
	}
	return result, nil
}

// buildClocCmd returns the cloc invocation for dir: a file-list-on-stdin
// form inside a git repo (see the Run doc comment), or a plain directory
// scan otherwise. A (nil, nil) result means "git repo with nothing to
// count" — distinct from a cloc/git failure, which is returned as an error.
func buildClocCmd(dir string) (*exec.Cmd, error) {
	if !gitutil.IsRepo(dir) {
		return exec.Command("cloc", "--json", dir), nil
	}

	files, err := gitFileList(dir)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}

	cmd := exec.Command("cloc", "--json", "--list-file=-")
	cmd.Stdin = strings.NewReader(strings.Join(files, "\n"))
	return cmd, nil
}

// gitFileList returns absolute paths for every tracked file plus every
// untracked-but-not-ignored file under dir.
func gitFileList(dir string) ([]string, error) {
	cmd := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var files []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		rel := strings.TrimSpace(scanner.Text())
		if rel == "" {
			continue
		}
		files = append(files, filepath.Join(dir, rel))
	}
	return files, scanner.Err()
}
