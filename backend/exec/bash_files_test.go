package exec

import (
	"sort"
	"strings"
	"testing"
)

func TestExtractFilesFromBashCommand_Redirection(t *testing.T) {
	tests := []struct {
		name     string
		cmd      string
		expected []string
	}{
		{
			name:     "simple redirect",
			cmd:      "echo 'hello' > src/main.go",
			expected: []string{"src/main.go"},
		},
		{
			name:     "append redirect",
			cmd:      "echo 'world' >> src/main.go",
			expected: []string{"src/main.go"},
		},
		{
			name:     "cat redirect",
			cmd:      "cat > src/lib/utils.ts <<EOF\nconst x = 1;\nEOF",
			expected: []string{"src/lib/utils.ts"},
		},
		{
			name:     "printf redirect",
			cmd:      "printf '%s' 'content' > src/index.js",
			expected: []string{"src/index.js"},
		},
		{
			name:     "redirect with stderr redirect",
			cmd:      "echo 'test' > src/test.py 2>&1",
			expected: []string{"src/test.py"},
		},
		{
			name:     "pipe not redirect",
			cmd:      "echo 'test' | grep foo",
			expected: nil,
		},
		{
			name:     "heredoc not extracted as file",
			cmd:      "cat <<EOF > /dev/null\nhello\nEOF",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFilesFromBashCommand(tt.cmd)
			sort.Strings(got)
			sort.Strings(tt.expected)
			if len(got) != len(tt.expected) {
				t.Fatalf("got %d files, want %d: %v", len(got), len(tt.expected), got)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("got %q, want %q", got[i], tt.expected[i])
				}
			}
		})
	}
}

func TestExtractFilesFromBashCommand_Sed(t *testing.T) {
	tests := []struct {
		name     string
		cmd      string
		expected []string
	}{
		{
			name:     "sed in-place single file",
			cmd:      "sed -i 's/foo/bar/g' src/main.go",
			expected: []string{"src/main.go"},
		},
		{
			name:     "sed in-place with backup",
			cmd:      "sed -i.bak 's/foo/bar/g' src/main.go",
			expected: []string{"src/main.go"},
		},
		{
			name:     "sed in-place multiple files",
			cmd:      "sed -i 's/foo/bar/g' src/a.go src/b.go",
			expected: []string{"src/a.go", "src/b.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFilesFromBashCommand(tt.cmd)
			sort.Strings(got)
			sort.Strings(tt.expected)
			if len(got) != len(tt.expected) {
				t.Fatalf("got %d files, want %d: %v", len(got), len(tt.expected), got)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("got %q, want %q", got[i], tt.expected[i])
				}
			}
		})
	}
}

func TestExtractFilesFromBashCommand_Tee(t *testing.T) {
	tests := []struct {
		name     string
		cmd      string
		expected []string
	}{
		{
			name:     "tee simple",
			cmd:      "echo 'data' | tee src/output.ts",
			expected: []string{"src/output.ts"},
		},
		{
			name:     "tee with append flag",
			cmd:      "echo 'data' | tee -a src/output.ts",
			expected: []string{"src/output.ts"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFilesFromBashCommand(tt.cmd)
			sort.Strings(got)
			sort.Strings(tt.expected)
			if len(got) != len(tt.expected) {
				t.Fatalf("got %d files, want %d: %v", len(got), len(tt.expected), got)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("got %q, want %q", got[i], tt.expected[i])
				}
			}
		})
	}
}

func TestExtractFilesFromBashCommand_Mv(t *testing.T) {
	tests := []struct {
		name     string
		cmd      string
		expected []string
	}{
		{
			name:     "mv simple",
			cmd:      "mv src/old.go src/new.go",
			expected: []string{"src/new.go"},
		},
		{
			name:     "mv with force flag",
			cmd:      "mv -f src/old.ts src/new.ts",
			expected: []string{"src/new.ts"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFilesFromBashCommand(tt.cmd)
			sort.Strings(got)
			sort.Strings(tt.expected)
			if len(got) != len(tt.expected) {
				t.Fatalf("got %d files, want %d: %v", len(got), len(tt.expected), got)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("got %q, want %q", got[i], tt.expected[i])
				}
			}
		})
	}
}

func TestExtractFilesFromBashCommand_Cp(t *testing.T) {
	tests := []struct {
		name     string
		cmd      string
		expected []string
	}{
		{
			name:     "cp simple",
			cmd:      "cp src/template.go src/new.go",
			expected: []string{"src/new.go"},
		},
		{
			name:     "cp with flag",
			cmd:      "cp -f src/a.py src/b.py",
			expected: []string{"src/b.py"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFilesFromBashCommand(tt.cmd)
			sort.Strings(got)
			sort.Strings(tt.expected)
			if len(got) != len(tt.expected) {
				t.Fatalf("got %d files, want %d: %v", len(got), len(tt.expected), got)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("got %q, want %q", got[i], tt.expected[i])
				}
			}
		})
	}
}

func TestExtractFilesFromBashCommand_Touch(t *testing.T) {
	tests := []struct {
		name     string
		cmd      string
		expected []string
	}{
		{
			name:     "touch simple",
			cmd:      "touch src/new.go",
			expected: []string{"src/new.go"},
		},
		{
			name:     "touch multiple files",
			cmd:      "touch src/a.go src/b.go src/c.go",
			expected: []string{"src/a.go", "src/b.go", "src/c.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFilesFromBashCommand(tt.cmd)
			sort.Strings(got)
			sort.Strings(tt.expected)
			if len(got) != len(tt.expected) {
				t.Fatalf("got %d files, want %d: %v", len(got), len(tt.expected), got)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("got %q, want %q", got[i], tt.expected[i])
				}
			}
		})
	}
}

func TestExtractFilesFromBashCommand_Gofmt(t *testing.T) {
	tests := []struct {
		name     string
		cmd      string
		expected []string
	}{
		{
			name:     "gofmt -w",
			cmd:      "gofmt -w src/main.go",
			expected: []string{"src/main.go"},
		},
		{
			name:     "clang-format -i via -w pattern",
			cmd:      "clang-format -i -w src/main.cpp",
			expected: []string{"src/main.cpp"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFilesFromBashCommand(tt.cmd)
			sort.Strings(got)
			sort.Strings(tt.expected)
			if len(got) != len(tt.expected) {
				t.Fatalf("got %d files, want %d: %v", len(got), len(tt.expected), got)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("got %q, want %q", got[i], tt.expected[i])
				}
			}
		})
	}
}

func TestExtractFilesFromBashCommand_SkipsNonCodeFiles(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
	}{
		{
			name: "no extension",
			cmd:  "echo 'test' > README",
		},
		{
			name: "image file",
			cmd:  "echo 'test' > image.png",
		},
		{
			name: "dev null",
			cmd:  "echo 'test' > /dev/null",
		},
		{
			name: "empty command",
			cmd:  "",
		},
		{
			name: "comment only",
			cmd:  "# this is a comment",
		},
		{
			name: "git command (no file paths)",
			cmd:  "git status",
		},
		{
			name: "npm install (no file paths)",
			cmd:  "npm install",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFilesFromBashCommand(tt.cmd)
			if len(got) != 0 {
				t.Errorf("expected no files, got: %v", got)
			}
		})
	}
}

func TestExtractFilesFromBashCommand_MultiLine(t *testing.T) {
	cmd := "echo 'first' > src/a.go\necho 'second' > src/b.ts"
	got := extractFilesFromBashCommand(cmd)
	sort.Strings(got)
	expected := []string{"src/a.go", "src/b.ts"}
	sort.Strings(expected)
	if len(got) != len(expected) {
		t.Fatalf("got %d files, want %d: %v", len(got), len(expected), got)
	}
	for i := range got {
		if got[i] != expected[i] {
			t.Errorf("got %q, want %q", got[i], expected[i])
		}
	}
}

func TestExtractFilesFromBashCommand_ChainedCommands(t *testing.T) {
	cmd := "echo 'test' > src/a.go && echo 'more' >> src/b.go"
	got := extractFilesFromBashCommand(cmd)
	sort.Strings(got)
	expected := []string{"src/a.go", "src/b.go"}
	sort.Strings(expected)
	if len(got) != len(expected) {
		t.Fatalf("got %d files, want %d: %v", len(got), len(expected), got)
	}
	for i := range got {
		if got[i] != expected[i] {
			t.Errorf("got %q, want %q", got[i], expected[i])
		}
	}
}

func TestExtractFilesFromBashCommand_NoDuplicates(t *testing.T) {
	cmd := "echo 'test' > src/a.go && echo 'more' >> src/a.go"
	got := extractFilesFromBashCommand(cmd)
	if len(got) != 1 {
		t.Fatalf("expected 1 file (no duplicates), got %d: %v", len(got), got)
	}
	if got[0] != "src/a.go" {
		t.Errorf("got %q, want %q", got[0], "src/a.go")
	}
}

func TestExtractFilesFromBashCommand_Quotes(t *testing.T) {
	// Quoted filenames without spaces work fine
	cmd := `echo 'test' > "src/file.go"`
	got := extractFilesFromBashCommand(cmd)
	if len(got) != 1 {
		t.Fatalf("expected 1 file, got %d: %v", len(got), got)
	}
	// Quotes should be trimmed
	if strings.Contains(got[0], "\"") || strings.Contains(got[0], "'") {
		t.Errorf("quotes not trimmed: %q", got[0])
	}
	if got[0] != "src/file.go" {
		t.Errorf("got %q, want %q", got[0], "src/file.go")
	}
}

func TestExtractFilesFromBashCommand_DotFiles(t *testing.T) {
	// Files like "." or "-" should be skipped
	cmd := "cat > . <<EOF\nhello\nEOF"
	got := extractFilesFromBashCommand(cmd)
	if len(got) != 0 {
		t.Errorf("expected no files for dot redirect, got: %v", got)
	}
}
