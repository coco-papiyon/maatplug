package gitdiff

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"maatplug/internal/model"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-q")
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "config", "user.name", "test")
	return dir
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiffTrees_SameFileEditedAcrossTwoTurns(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "line1\n")
	run(t, dir, "add", "a.txt")
	run(t, dir, "commit", "-q", "-m", "init")

	// Turn 1: append a line to a.txt.
	before1, err := SnapshotGitTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "a.txt", "line1\nline2\n")
	after1, err := SnapshotGitTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	diff1, err := DiffTrees(dir, before1, after1)
	if err != nil {
		t.Fatal(err)
	}
	if diff1 != (model.DiffStat{FilesChanged: 1, Added: 1, Removed: 0}) {
		t.Fatalf("turn 1 diff = %+v, want +1/-0 on 1 file", diff1)
	}

	// Turn 2: edit the SAME file again. A cumulative-vs-HEAD subtraction
	// would report FilesChanged=0 here (still just 1 file different from
	// HEAD); the tree-diff approach must still see this turn's own edit.
	before2 := after1
	writeFile(t, dir, "a.txt", "line1\nline2\nline3\n")
	after2, err := SnapshotGitTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	diff2, err := DiffTrees(dir, before2, after2)
	if err != nil {
		t.Fatal(err)
	}
	if diff2 != (model.DiffStat{FilesChanged: 1, Added: 1, Removed: 0}) {
		t.Fatalf("turn 2 diff = %+v, want +1/-0 on 1 file", diff2)
	}
}

func TestDiffTrees_RemovingPreviouslyAddedLines(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "line1\n")
	run(t, dir, "add", "a.txt")
	run(t, dir, "commit", "-q", "-m", "init")

	// Turn 1 adds three lines.
	before1, _ := SnapshotGitTree(dir)
	writeFile(t, dir, "a.txt", "line1\nline2\nline3\nline4\n")
	after1, _ := SnapshotGitTree(dir)
	diff1, err := DiffTrees(dir, before1, after1)
	if err != nil {
		t.Fatal(err)
	}
	if diff1.Added != 3 || diff1.Removed != 0 {
		t.Fatalf("turn 1 diff = %+v, want +3/-0", diff1)
	}

	// Turn 2 deletes two of the lines turn 1 just added. A naive
	// cumulative-vs-HEAD subtraction sees Added drop (4->2 vs HEAD) and
	// clamps that negative delta to zero instead of reporting it as a
	// removal. The tree-diff must report this turn's actual removal.
	before2 := after1
	writeFile(t, dir, "a.txt", "line1\nline2\n")
	after2, _ := SnapshotGitTree(dir)
	diff2, err := DiffTrees(dir, before2, after2)
	if err != nil {
		t.Fatal(err)
	}
	if diff2.Added != 0 || diff2.Removed != 2 {
		t.Fatalf("turn 2 diff = %+v, want +0/-2", diff2)
	}
}

func TestDiffTrees_UntrackedFileCountedAsAdded(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, dir, "a.txt", "line1\n")
	run(t, dir, "add", "a.txt")
	run(t, dir, "commit", "-q", "-m", "init")

	before, _ := SnapshotGitTree(dir)
	writeFile(t, dir, "new.txt", "hello\nworld\n") // never `git add`ed
	after, _ := SnapshotGitTree(dir)

	diff, err := DiffTrees(dir, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if diff != (model.DiffStat{FilesChanged: 1, Added: 2, Removed: 0}) {
		t.Fatalf("diff = %+v, want a new untracked file counted as +2 lines", diff)
	}
}

func TestDiffTrees_BinaryFileCountsTowardFilesChanged(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.bin"), []byte{0, 1, 2, 0, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "a.bin")
	run(t, dir, "commit", "-q", "-m", "init")

	before, _ := SnapshotGitTree(dir)
	if err := os.WriteFile(filepath.Join(dir, "a.bin"), []byte{9, 9, 9, 9, 9, 9}, 0o644); err != nil {
		t.Fatal(err)
	}
	after, _ := SnapshotGitTree(dir)

	diff, err := DiffTrees(dir, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if diff.FilesChanged != 1 {
		t.Fatalf("FilesChanged = %d, want 1 (binary files must still count as a changed file)", diff.FilesChanged)
	}
}

func TestDiffTrees_NoCommitsYet(t *testing.T) {
	dir := initRepo(t) // no commits: gitutil.HeadCommit(dir) == ""

	before, err := SnapshotGitTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "a.txt", "line1\n")
	after, err := SnapshotGitTree(dir)
	if err != nil {
		t.Fatal(err)
	}

	diff, err := DiffTrees(dir, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if diff != (model.DiffStat{FilesChanged: 1, Added: 1, Removed: 0}) {
		t.Fatalf("diff = %+v, want +1/-0 on 1 file even with no prior commit", diff)
	}
}

func TestDiffCloc(t *testing.T) {
	cases := []struct {
		name          string
		before, after *model.ClocResult
		want          model.DiffStat
	}{
		{"nil before", nil, &model.ClocResult{Code: 10, Files: 1}, model.DiffStat{}},
		{"net addition", &model.ClocResult{Code: 10, Files: 1}, &model.ClocResult{Code: 25, Files: 2}, model.DiffStat{Added: 15, FilesChanged: 1}},
		{"net removal", &model.ClocResult{Code: 25, Files: 2}, &model.ClocResult{Code: 10, Files: 1}, model.DiffStat{Removed: 15, FilesChanged: 1}},
		{"same file count, content changed", &model.ClocResult{Code: 10, Files: 1}, &model.ClocResult{Code: 12, Files: 1}, model.DiffStat{Added: 2, FilesChanged: 1}},
		{"no change", &model.ClocResult{Code: 10, Files: 1}, &model.ClocResult{Code: 10, Files: 1}, model.DiffStat{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DiffCloc(tc.before, tc.after)
			if got != tc.want {
				t.Errorf("DiffCloc() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
