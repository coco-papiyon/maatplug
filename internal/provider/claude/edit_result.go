package claude

import (
	"encoding/json"
	"strings"
)

type FileChange struct {
	Path, Method, Change string
	Added, Removed       int
}

// DecodeFileChange accepts observed Edit/Write results, never requested edits.
// Unknown or incomplete result shapes remain unmeasured.
func DecodeFileChange(raw []byte) (FileChange, bool) {
	var result struct {
		Type            string  `json:"type"`
		FilePath        string  `json:"filePath"`
		Content         *string `json:"content"`
		StructuredPatch []struct {
			OldLines *int     `json:"oldLines"`
			NewLines *int     `json:"newLines"`
			Lines    []string `json:"lines"`
		} `json:"structuredPatch"`
	}
	if json.Unmarshal(raw, &result) != nil || result.FilePath == "" {
		return FileChange{}, false
	}
	change := FileChange{Path: result.FilePath, Change: "modified", Method: "claude_structured_patch_v1"}
	if len(result.StructuredPatch) > 0 {
		for _, hunk := range result.StructuredPatch {
			oldLines, newLines := 0, 0
			if hunk.OldLines == nil || hunk.NewLines == nil {
				return FileChange{}, false
			}
			for _, line := range hunk.Lines {
				if line == "" {
					return FileChange{}, false
				}
				switch line[0] {
				case '+':
					change.Added++
					newLines++
				case '-':
					change.Removed++
					oldLines++
				case ' ':
					oldLines++
					newLines++
				case '\\':
					if line != `\ No newline at end of file` {
						return FileChange{}, false
					}
				default:
					return FileChange{}, false
				}
			}
			if oldLines != *hunk.OldLines || newLines != *hunk.NewLines {
				return FileChange{}, false
			}
		}
		return change, true
	}
	if result.Type == "create" && result.Content != nil {
		change.Change, change.Method = "created", "claude_created_file_lines_v1"
		if *result.Content != "" {
			change.Added = strings.Count(strings.TrimSuffix(*result.Content, "\n"), "\n") + 1
		}
		return change, true
	}
	return FileChange{}, false
}
