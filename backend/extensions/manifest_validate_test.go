package extensions

import (
	"strings"
	"testing"
)

// tsConfig is a manifest in the shape an extension publishes today: per-file language ids,
// because one server serves the whole JavaScript family.
func tsConfig() *LSPConfig {
	return &LSPConfig{
		Language: "typescript",
		FileTypes: []LSPFileType{
			{Extension: "ts", LanguageID: "typescript", Label: "TypeScript", Syntax: "ts"},
			{Extension: "tsx", LanguageID: "typescriptreact", Label: "TypeScript JSX", Syntax: "ts"},
		},
		Syntax: map[string]SyntaxSpec{
			"ts": {
				LineComment:  "//",
				BlockComment: &CommentToken{Open: "/*", Close: "*/"},
				Strings:      []StringToken{{Quote: `"`}, {Quote: `'`}, {Quote: "`", Multiline: true}},
				Pairs:        []CommentToken{{Open: "{", Close: "}"}, {Open: "(", Close: ")"}},
				Keywords:     []string{"function", "return", "interface"},
				Numbers:      true,
			},
		},
	}
}

// TestSyntaxSpecValidateRealWorldShapes covers the two shapes the JavaScript fixture cannot
// exercise: a repeated-character quote (Python's triple quote) and a second line-comment marker.
func TestSyntaxSpecValidateRealWorldShapes(t *testing.T) {
	python := SyntaxSpec{
		LineComment: "#",
		Strings: []StringToken{
			{Quote: "\"\"\"", Escapes: true, Multiline: true},
			{Quote: "'''", Escapes: true, Multiline: true},
			{Quote: "\"", Escapes: true},
			{Quote: "'", Escapes: true},
		},
		Pairs:    []CommentToken{{Open: "{", Close: "}"}, {Open: "(", Close: ")"}, {Open: "[", Close: "]"}},
		Keywords: []string{"def", "class", "return", "import", "from", "with", "as", "yield"},
		Types:    []string{"int", "str", "bool", "bytes", "float"},
		Numbers:  true,
	}
	if err := python.Validate(); err != nil {
		t.Errorf("Python-shaped profile rejected: %v", err)
	}

	php := SyntaxSpec{
		LineComment:  "//",
		LineComments: []string{"#"},
		BlockComment: &CommentToken{Open: "/*", Close: "*/"},
		Strings:      []StringToken{{Quote: "\""}, {Quote: "'"}},
		Pairs:        []CommentToken{{Open: "{", Close: "}"}, {Open: "(", Close: ")"}},
		Keywords:     []string{"function", "class", "public", "static", "echo", "return"},
		Numbers:      true,
	}
	if err := php.Validate(); err != nil {
		t.Errorf("PHP-shaped profile rejected: %v", err)
	}

	if err := (SyntaxSpec{Strings: []StringToken{{Quote: "`"}, {Quote: "`"}}}).Validate(); err == nil ||
		!strings.Contains(err.Error(), "declared twice") {
		t.Errorf("duplicate quote accepted: %v", err)
	}
}

func TestLSPConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*LSPConfig)
		wantErr string
	}{
		{name: "a declared manifest is valid", wantErr: ""},
		{
			name:    "no file type declared",
			mutate:  func(c *LSPConfig) { c.FileTypes = nil },
			wantErr: "at least one file type",
		},
		{
			name:    "leading dot",
			mutate:  func(c *LSPConfig) { c.FileTypes[0].Extension = ".ts" },
			wantErr: "must not start with a dot",
		},
		{
			name:    "uppercase extension never matches",
			mutate:  func(c *LSPConfig) { c.FileTypes[0].Extension = "TS" },
			wantErr: "must be lowercase",
		},
		{
			name:    "path in the extension",
			mutate:  func(c *LSPConfig) { c.FileTypes[0].Extension = "a/b" },
			wantErr: "bare suffix",
		},
		{
			name: "same extension twice",
			mutate: func(c *LSPConfig) {
				c.FileTypes = append(c.FileTypes, LSPFileType{Extension: "ts", LanguageID: "typescript"})
			},
			wantErr: "declared twice",
		},
		{
			name:    "languageId missing",
			mutate:  func(c *LSPConfig) { c.FileTypes[1].LanguageID = "" },
			wantErr: "language_id is empty",
		},
		{
			name:    "languageId capitalized is not a well-known id",
			mutate:  func(c *LSPConfig) { c.FileTypes[0].LanguageID = "TypeScript" },
			wantErr: "language_id \"TypeScript\" must be lowercase",
		},
		{
			name:    "syntax profile that does not exist",
			mutate:  func(c *LSPConfig) { c.FileTypes[0].Syntax = "typescript-ish" },
			wantErr: "names no profile",
		},
		{
			name:    "profile that tokenizes nothing",
			mutate:  func(c *LSPConfig) { c.Syntax["empty"] = SyntaxSpec{}; c.FileTypes[0].Syntax = "empty" },
			wantErr: "declares nothing to tokenize",
		},
		{
			name:    "block comment with no closer",
			mutate:  func(c *LSPConfig) { c.Syntax["ts"] = SyntaxSpec{BlockComment: &CommentToken{Open: "/*"}} },
			wantErr: "needs both open and close",
		},
		{
			// A quote is one character, or that character repeated (Python's triple quote). A
			// multi-character sequence is a delimiter, not a quote, and belongs elsewhere.
			name:    "string quote must be one character repeated",
			mutate:  func(c *LSPConfig) { c.Syntax["ts"] = SyntaxSpec{Strings: []StringToken{{Quote: "<|"}}} },
			wantErr: "one character repeated",
		},
		{
			name: "line comment marker declared twice",
			mutate: func(c *LSPConfig) {
				spec := c.Syntax["ts"]
				spec.LineComments = []string{"//"}
				c.Syntax["ts"] = spec
			},
			wantErr: "repeats line_comment",
		},
		{
			name:    "bracket pair longer than one character cannot be folded",
			mutate:  func(c *LSPConfig) { c.Syntax["ts"] = SyntaxSpec{Pairs: []CommentToken{{Open: "<%", Close: "%>"}}} },
			wantErr: "single characters",
		},
		{
			name:    "bracket pair whose halves are identical",
			mutate:  func(c *LSPConfig) { c.Syntax["ts"] = SyntaxSpec{Pairs: []CommentToken{{Open: "|", Close: "|"}}} },
			wantErr: "identical",
		},
		{
			name:    "keyword containing a space never matches",
			mutate:  func(c *LSPConfig) { c.Syntax["ts"] = SyntaxSpec{Keywords: []string{"export default"}} },
			wantErr: "must be a single word",
		},
		{
			name: "region marker missing its end",
			mutate: func(c *LSPConfig) {
				spec := c.Syntax["ts"]
				spec.Region = &CommentToken{Open: "#region"}
				c.Syntax["ts"] = spec
			},
			wantErr: "region needs both",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tsConfig()
			if tt.mutate != nil {
				tt.mutate(cfg)
			}
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want no error", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestNormalizeFileTypes(t *testing.T) {
	cfg := &LSPConfig{Language: "typescript", LegacyExtensions: []string{"ts", ".tsx", "JS", "", "ts"}}
	if !cfg.NormalizeFileTypes() {
		t.Fatal("NormalizeFileTypes() reported no change for a legacy manifest")
	}
	if len(cfg.FileTypes) != 3 {
		t.Fatalf("got %d file types %v, want 3 (deduplicated, empty dropped)", len(cfg.FileTypes), cfg.FileTypes)
	}
	if cfg.FileTypes[0].Extension != "ts" || cfg.FileTypes[1].Extension != "tsx" || cfg.FileTypes[2].Extension != "js" {
		t.Errorf("got extensions %v %v %v, want [ts tsx js] — dots stripped, lowercased, in declaration order",
			cfg.FileTypes[0].Extension, cfg.FileTypes[1].Extension, cfg.FileTypes[2].Extension)
	}
	// What the legacy shape cannot say stays unsaid: every synthesized entry gets the manifest
	// language, which is why such an extension needs an update to open .tsx correctly.
	for _, ft := range cfg.FileTypes {
		if ft.LanguageID != "typescript" {
			t.Errorf("file type %q got languageId %q, want the manifest language", ft.Extension, ft.LanguageID)
		}
	}
	if cfg.LegacyExtensions != nil {
		t.Errorf("LegacyExtensions = %v, want cleared so nothing reads it twice", cfg.LegacyExtensions)
	}
	if cfg.NormalizeFileTypes() {
		t.Error("NormalizeFileTypes() is not idempotent — it ran again on a normalized manifest")
	}
}

func TestNormalizeFileTypesKeepsDeclaredTypes(t *testing.T) {
	cfg := tsConfig()
	cfg.LegacyExtensions = []string{"tsx", "mjs"}
	cfg.NormalizeFileTypes()
	if len(cfg.FileTypes) != 3 {
		t.Fatalf("got %d file types %v, want the two declared types plus only the one legacy type that is new",
			len(cfg.FileTypes), cfg.FileTypes)
	}
	// The declared per-file id survives; the synthesized one borrows the language.
	if cfg.FileTypes[1].LanguageID != "typescriptreact" {
		t.Errorf("declared file type was overwritten: %v", cfg.FileTypes[1])
	}
}

func TestFileSuffix(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"src/lib/util.ts", "ts"},
		{"A.TSX", "tsx"},
		{"main.test.mjs", "mjs"},
		{"Makefile", ""},
		{".gitignore", ""},
		{"archive.tar.gz", "gz"},
		{"trailing.", ""},
		{"", ""},
		{"dir.d/main.go", "go"},
	}
	for _, tt := range tests {
		if got := fileSuffix(tt.name); got != tt.want {
			t.Errorf("fileSuffix(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestFileTypeResolution(t *testing.T) {
	cfg := tsConfig()

	// The whole point of file_types: two suffixes, one extension, two languageIds.
	if got := cfg.LanguageIDFor("src/App.tsx"); got != "typescriptreact" {
		t.Errorf("LanguageIDFor(.tsx) = %q, want typescriptreact", got)
	}
	if got := cfg.LanguageIDFor("src/util.ts"); got != "typescript" {
		t.Errorf("LanguageIDFor(.ts) = %q, want typescript", got)
	}
	if cfg.MatchesFile("README.md") {
		t.Error("MatchesFile(.md) = true, want false — this extension declares no .md file type")
	}
	// An unclaimed suffix still gets the manifest language rather than an empty document
	// language: workspace-level lookups arrive with no suffix at all.
	if got := cfg.LanguageIDFor("workspace/dir.d"); got != "typescript" {
		t.Errorf("LanguageIDFor(suffixless) = %q, want the manifest language", got)
	}
	if ft := cfg.fileTypeFor("app.tsx"); ft == nil || ft.Label != "TypeScript JSX" {
		t.Errorf("FileTypeFor(.tsx) = %v, want the declared label", ft)
	}
	// A file named after the language reaches its own server, as it did under the flat list.
	named := &LSPConfig{Language: "go", FileTypes: []LSPFileType{{Extension: "go", LanguageID: "go"}}}
	if !named.MatchesFile("worktree/go") {
		t.Error("MatchesFile(\"worktree/go\") = false, want true for a file named after the language")
	}
}
