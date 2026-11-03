package claude

import "testing"

func TestDecodeFileChange(t *testing.T) {
	for _, tt := range []struct {
		name, raw      string
		added, removed int
		ok             bool
	}{
		{"edit", `{"filePath":"a.go","structuredPatch":[{"oldLines":2,"newLines":3,"lines":[" context","-old","+new","+extra"]}]}`, 2, 1, true},
		{"prefix is content, not diff header", `{"filePath":"a","structuredPatch":[{"oldLines":1,"newLines":1,"lines":["---old","+++new"]}]}`, 1, 1, true},
		{"create CRLF", `{"type":"create","filePath":"a","content":"a\r\nb\r\n","structuredPatch":[]}`, 2, 0, true},
		{"create empty", `{"type":"create","filePath":"a","content":""}`, 0, 0, true},
		{"truncated patch", `{"filePath":"a","structuredPatch":[{"oldLines":2,"newLines":1,"lines":["-old","+new"]}]}`, 0, 0, false},
		{"requested change is not a result", `{"file_path":"a","old_string":"old","new_string":"new"}`, 0, 0, false},
		{"missing create content", `{"type":"create","filePath":"a"}`, 0, 0, false},
		{"write update without patch", `{"type":"update","filePath":"a","content":"new"}`, 0, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DecodeFileChange([]byte(tt.raw))
			if ok != tt.ok || (ok && (got.Added != tt.added || got.Removed != tt.removed)) {
				t.Fatalf("got=%+v ok=%t", got, ok)
			}
		})
	}
}
