package api

import (
	"encoding/json/v2"
	"net/http"
	"strings"
	"testing"

	"mike/backend/types"
)

// GET /api/projects/{id}/git/diff/file reads a file the request names inside the project. The cap is
// the one GET /api/fs/read applies, and the rule is the same: stat, decide, and only then open — so
// an oversized file is answered by its size and never by its bytes, and a file the cap lets through
// is answered whole and is NOT reported as large (a present file_size is what the panel renders as a
// "Large file" notice). `?meta=1` asks for the same decision without the body, so both spellings are
// asserted over both sides of the cap.

// repeat is a table's shorthand for "n copies of the same line".
func repeat(line string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = line
	}
	return out
}

// diffFileAnswer is what the route answers, in either mode.
type diffFileAnswer struct {
	Lines     []string `json:"lines"`
	LineCount int      `json:"line_count"`
	FileSize  int64    `json:"file_size"`
	IsBinary  bool     `json:"isBinary"`
}

func TestHandleProjectDiffFileAppliesTheCapBeforeReading(t *testing.T) {
	const overCap = "y" // the marker whose bytes must never reach a response
	const marker = "DO-NOT-READ-ME-9c1a"

	for _, tt := range []struct {
		name string
		// meta is the ?meta=1 half of the route.
		meta bool
		// content is the file the project's working tree holds when the request reads it.
		content func() string
		// wantLines/wantLineCount are the body half of the answer; wantLines is nil for meta.
		wantLines     []string
		wantLineCount int
		// wantFileSize is 0 for "the answer must not claim this file is large".
		wantFileSize int64
		// leakIs is the substring that must not appear in the body when the file is over the cap.
		leakIs string
	}{
		{
			name:          "an ordinary file is read whole and is not reported large",
			content:       func() string { return "hello\nworld\n" },
			wantLines:     []string{"hello", "world"},
			wantLineCount: 2,
		},
		{
			name:          "a file with many ordinary lines still comes back whole",
			content:       func() string { return strings.Repeat("line\n", 1000) },
			wantLines:     repeat("line", 1000),
			wantLineCount: 1000,
		},
		{
			name:          "an oversized file is answered by its size, never its bytes",
			content:       func() string { return strings.Repeat(overCap, 2<<20) + marker + "\n" },
			wantLineCount: 0,
			wantFileSize:  int64(2<<20) + int64(len(marker)) + 1,
			leakIs:        marker,
		},
		{
			name:          "meta: a small file answers its line count and no size",
			meta:          true,
			content:       func() string { return "line1\nline2\nline3\n" },
			wantLineCount: 3,
		},
		{
			name:         "meta: a file just over the cap reports its size",
			meta:         true,
			content:      func() string { return strings.Repeat("x", 1<<20+1) + "\n" },
			wantFileSize: 1<<20 + 2,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, s, _, _ := newTestGitHandler(t)
			projPath := t.TempDir()
			initGitRepo(t, projPath)
			content := tt.content()
			createFile(t, projPath, "asked.txt", content)
			id := "diff-file-" + strings.ToLower(strings.ReplaceAll(strings.Split(tt.name, " ")[0], "_", "-"))
			if err := s.CreateProject(&types.Project{ID: id, Name: id, Path: projPath}); err != nil {
				t.Fatal(err)
			}

			target := "/api/projects/" + id + "/git/diff/file?path=asked.txt"
			if tt.meta {
				target += "&meta=1"
			}
			rr := doRequest(t, newTestGitMux(h), http.MethodGet, target)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
			}
			body := rr.Body.String()
			if tt.leakIs != "" && strings.Contains(body, tt.leakIs) {
				t.Fatalf("the cap did not stop the read — the file's own bytes reached the response")
			}

			var got diffFileAnswer
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatalf("decode answer: %v (%s)", err, body)
			}
			if !tt.meta {
				if strings.Join(got.Lines, "|") != strings.Join(tt.wantLines, "|") {
					t.Errorf("lines = %q, want %q", strings.Join(got.Lines, "|"), strings.Join(tt.wantLines, "|"))
				}
			}
			if got.LineCount != tt.wantLineCount {
				t.Errorf("line_count = %d, want %d", got.LineCount, tt.wantLineCount)
			}
			if got.FileSize != tt.wantFileSize {
				t.Errorf("file_size = %d, want %d", got.FileSize, tt.wantFileSize)
			}
			if got.IsBinary {
				t.Error("isBinary = true for a file holding no NUL byte")
			}
		})
	}
}
