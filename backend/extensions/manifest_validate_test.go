package extensions

import (
	"encoding/json"
	"os"
	"path/filepath"
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
			// An author turning both shape rules off, and declaring nothing else, has asked for a
			// grammar that styles nothing. The refusal is the answer, not a blank editor.
			name: "profile that only refuses the shape rules",
			mutate: func(c *LSPConfig) {
				off := false
				c.Syntax["ts"] = SyntaxSpec{CapitalizedTypes: &off, CallAsFunction: &off}
			},
			wantErr: "declares nothing to tokenize",
		},
		{
			name:    "shape rule asked for on its own",
			mutate:  func(c *LSPConfig) { on := true; c.Syntax["ts"] = SyntaxSpec{CallAsFunction: &on} },
			wantErr: "",
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

// --- Translation packs (capabilities.i18n) -----------------------------------------------

// i18nPack is a pack in the shape one publishes: a language tag, its catalog beside the manifest,
// and a prompt overlay. Every refusal below has to name the field, because the author is reading
// this in an install failure with nothing but the manifest open.
func i18nPack() *I18NConfig {
	return &I18NConfig{Locale: "ja", MessagesURL: "messages.json", PromptsDir: "prompts"}
}

func TestI18NConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*I18NConfig)
		wantErr string
	}{
		{name: "a declared pack is valid", wantErr: ""},
		{
			name:    "a catalog with no prompt overlay is a complete pack",
			mutate:  func(c *I18NConfig) { c.PromptsDir = "" },
			wantErr: "",
		},
		{name: "a two-letter tag is valid", mutate: func(c *I18NConfig) { c.Locale = "fr" }, wantErr: ""},
		{name: "an extended subtag is valid", mutate: func(c *I18NConfig) { c.Locale = "hsn" }, wantErr: ""},
		{
			name:    "no locale declared",
			mutate:  func(c *I18NConfig) { c.Locale = "" },
			wantErr: "locale is required",
		},
		{
			name:    "an uppercase tag never matches a browser language",
			mutate:  func(c *I18NConfig) { c.Locale = "FR" },
			wantErr: "locale \"FR\" must be lowercase",
		},
		{
			// Strict, and the error says so: zh-CN and zh-TW are not the same language, and a
			// core that silently picked one would be deciding for the reader.
			name:    "region form is refused, not reduced",
			mutate:  func(c *I18NConfig) { c.Locale = "zh-CN" },
			wantErr: "no region, script or modifier",
		},
		{
			name:    "a POSIX locale is not a subtag",
			mutate:  func(c *I18NConfig) { c.Locale = "fr_FR@euro" },
			wantErr: "capabilities.i18n.locale",
		},
		{
			name:    "a path masquerading as a locale",
			mutate:  func(c *I18NConfig) { c.Locale = "fr/../../" },
			wantErr: "bare BCP-47 language subtag",
		},
		{name: "one letter is not a language", mutate: func(c *I18NConfig) { c.Locale = "j" }, wantErr: "language subtag"},
		{name: "a four-letter tag is reserved, not a language", mutate: func(c *I18NConfig) { c.Locale = "japa" }, wantErr: "language subtag"},
		{
			name:    "no catalog declared",
			mutate:  func(c *I18NConfig) { c.MessagesURL = "" },
			wantErr: "messages_url is required",
		},
		{
			name:    "an absolute catalog is read off the host",
			mutate:  func(c *I18NConfig) { c.MessagesURL = "/etc/passwd" },
			wantErr: "messages_url \"/etc/passwd\" is absolute",
		},
		{
			name:    "a catalog above the pack directory",
			mutate:  func(c *I18NConfig) { c.MessagesURL = "../other-pack/messages.json" },
			wantErr: "must not contain '.' or '..'",
		},
		{
			// The one spelling of every pack path: the value is projected into the registry index
			// and matched by the loader, so "./messages.json" is refused rather than normalized.
			name:    "a redundant self-reference",
			mutate:  func(c *I18NConfig) { c.MessagesURL = "./messages.json" },
			wantErr: "must not contain '.' or '..'",
		},
		{
			name:    "an https catalog is a host nobody chose",
			mutate:  func(c *I18NConfig) { c.MessagesURL = "https://cdn.example/messages.json" },
			wantErr: "is an external reference",
		},
		{
			name:    "a protocol-relative catalog reaches the same host",
			mutate:  func(c *I18NConfig) { c.MessagesURL = "//cdn.example/messages.json" },
			wantErr: "is an external reference",
		},
		{
			name:    "a data URL is a scheme like any other",
			mutate:  func(c *I18NConfig) { c.MessagesURL = "data:,{}" },
			wantErr: "is an external reference",
		},
		{
			name:    "a trailing slash makes the last segment nothing",
			mutate:  func(c *I18NConfig) { c.MessagesURL = "messages.json/" },
			wantErr: "empty path segment",
		},
		{
			name:    "a windows separator is not a path here",
			mutate:  func(c *I18NConfig) { c.MessagesURL = "lang\\messages.json" },
			wantErr: "must use '/' separators",
		},
		{
			name:    "an optional prompts_dir may be empty, not blank-looking",
			mutate:  func(c *I18NConfig) { c.PromptsDir = "prompts/../prompts" },
			wantErr: "prompts_dir",
		},
		{
			name:    "prompts from a host nobody chose",
			mutate:  func(c *I18NConfig) { c.PromptsDir = "https://example.com/prompts" },
			wantErr: "is an external reference",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := i18nPack()
			if tt.mutate != nil {
				tt.mutate(cfg)
			}
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("a valid pack was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted, want an error naming %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not say %q", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), "capabilities.i18n.") {
				t.Errorf("error %q does not name the capabilities.i18n field it refused", err)
			}
		})
	}

	// A nil capability validates: the callers run this on every manifest, and most declare no pack.
	if err := (*I18NConfig)(nil).Validate(); err != nil {
		t.Errorf("nil capability: %v", err)
	}
}

// TestValidateI18NIsTheOnlyCapability is the "no sandbox, no mounts, no network, no permissions"
// rule. A pack is refused rather than stripped, for the same reason a self-granted dependency host
// path is refused: a manifest that grants itself something has said something the user did not agree
// to, and silently dropping it would install an extension that is not the one that was reviewed.
// TestCSSConfigValidate holds the stylesheet declaration to the same containment the i18n pack's
// files get. The field path in the message is part of the assertion: an author told the wrong
// field is wrong leaves them editing the one that was never the problem.
func TestCSSConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		css     *CSSConfig
		wantErr string
	}{
		{name: "a declared stylesheet is valid", css: &CSSConfig{CSSURL: "styles.css"}, wantErr: ""},
		{name: "a subdirectory is still inside the extension", css: &CSSConfig{CSSURL: "styles/dark.css"}, wantErr: ""},
		{name: "no css capability declares nothing to validate", css: nil, wantErr: ""},
		{
			// The gap this closes: the browser concatenates this into a URL and resolves the
			// dot segments itself, so it is an authenticated request for /api/config made by
			// the app — and it lands in the storage the pre-paint script reads on every load.
			name:    "a stylesheet above the extension directory",
			css:     &CSSConfig{CSSURL: "../../../../api/config"},
			wantErr: "must not contain '.' or '..'",
		},
		{
			name:    "a stylesheet declared from the server root",
			css:     &CSSConfig{CSSURL: "/etc/passwd"},
			wantErr: "capabilities.css.css_url \"/etc/passwd\" is absolute",
		},
		{
			name:    "a stylesheet on another host",
			css:     &CSSConfig{CSSURL: "https://cdn.example/theme.css"},
			wantErr: "is an external reference",
		},
		{
			name:    "a protocol-relative stylesheet is a host too",
			css:     &CSSConfig{CSSURL: "//cdn.example/theme.css"},
			wantErr: "is an external reference",
		},
		{
			// A css extension with no stylesheet is an installed extension that does nothing,
			// invisibly — the failure this package refuses rather than stores.
			name:    "a css capability with no stylesheet",
			css:     &CSSConfig{},
			wantErr: "capabilities.css.css_url is required",
		},
		{
			name:    "a windows separator is not a path here",
			css:     &CSSConfig{CSSURL: "styles\\dark.css"},
			wantErr: "must use '/' separators",
		},
		{
			name:    "a control character in a path",
			css:     &CSSConfig{CSSURL: "styles.css\x00"},
			wantErr: "contains a control character",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.css.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("a valid declaration was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%q was accepted", tt.wantErr)
			}
			for _, want := range []string{"capabilities.css.css_url", tt.wantErr} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%q does not say %q", err, want)
				}
			}
		})
	}
}

func TestValidateI18NIsTheOnlyCapability(t *testing.T) {
	pack := &Capabilities{I18N: i18nPack()}
	if err := validateI18N(pack, ""); err != nil {
		t.Fatalf("a pack declaring only i18n was refused: %v", err)
	}

	for _, tt := range []struct {
		name string
		caps *Capabilities
		want string
	}{
		{"network", &Capabilities{I18N: i18nPack(), Network: &NetworkConfig{AllowedDomains: []string{"example.com"}}}, "network"},
		{"lsp", &Capabilities{I18N: i18nPack(), LSP: &LSPConfig{Language: "go"}}, "lsp"},
		{"agent", &Capabilities{I18N: i18nPack(), Agent: &AgentConfig{WasmModule: "a.wasm"}}, "agent"},
		{"ui", &Capabilities{I18N: i18nPack(), UI: &UIConfig{InjectionPoints: []string{"taskCard"}}}, "ui"},
		{"css", &Capabilities{I18N: i18nPack(), CSS: &CSSConfig{CSSURL: "theme.css"}}, "css"},
		{"llm", &Capabilities{I18N: i18nPack(), LLM: &LLMConfig{}}, "llm"},
	} {
		err := validateI18N(tt.caps, "")
		if err == nil {
			t.Errorf("a pack that also declares %s was accepted", tt.name)
			continue
		}
		for _, want := range []string{tt.want, "capabilities.i18n"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the %s refusal %q does not name %q", tt.name, err, want)
			}
		}
	}

	// Both names when two are declared: the author has to be told every field to delete.
	two := &Capabilities{I18N: i18nPack(), Network: &NetworkConfig{}, UI: &UIConfig{}}
	if err := validateI18N(two, ""); err == nil ||
		!strings.Contains(err.Error(), "network") || !strings.Contains(err.Error(), "ui") {
		t.Errorf("a pack declaring two siblings was refused with %v — both names expected", err)
	}
}

// writePack lays out a pack on disk the way a checkout does, and returns the directory. The
// templates use the renderer's own `inc`, because the published ja and ko packs do: an install-time
// parse that did not know that name would refuse them.
func writePack(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if files == nil {
		files = map[string]string{}
	}
	if _, ok := files["messages.json"]; !ok {
		files["messages.json"] = `{"app.title":"タイトル"}`
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestValidateI18NFiles(t *testing.T) {
	goodPrompts := map[string]string{
		"prompts/base.tmpl":                 `{{if .PlanMode}}計画モード{{end}}`,
		"prompts/answer_question_user.tmpl": `{{range $i, $q := .Questions}}{{$i | inc}}{{end}}`,
		"prompts/nested/deep/override.tmpl": `上書き`,
	}

	t.Run("a pack whose files are all good is accepted", func(t *testing.T) {
		dir := writePack(t, goodPrompts)
		if err := validateI18N(&Capabilities{I18N: i18nPack()}, dir); err != nil {
			t.Fatalf("refused a good pack: %v", err)
		}
	})

	t.Run("an unparsable template names the file that broke", func(t *testing.T) {
		files := map[string]string{}
		for k, v := range goodPrompts {
			files[k] = v
		}
		files["prompts/summarize.tmpl"] = `{{if .PreviousSummary}}unclosed`
		dir := writePack(t, files)
		err := validateI18N(&Capabilities{I18N: i18nPack()}, dir)
		if err == nil {
			t.Fatal("a template that does not compile was accepted")
		}
		for _, want := range []string{"capabilities.i18n.prompts_dir", "prompts/summarize.tmpl", "does not parse"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q does not say %q", err, want)
			}
		}
	})

	t.Run("a template nested in a subdirectory is still parsed", func(t *testing.T) {
		files := map[string]string{"prompts/prompts/base.tmpl": `{{end}}`}
		dir := writePack(t, files)
		if err := validateI18N(&Capabilities{I18N: i18nPack()}, dir); err == nil {
			t.Fatal("an unparsable template below the declared directory was accepted")
		}
	})

	t.Run("a catalog that is not an object of strings", func(t *testing.T) {
		for _, tt := range []struct {
			name    string
			catalog string
			want    string
		}{
			{"an array", `["タイトル"]`, "is not a message catalog"},
			{"a nested object", `{"nav":{"title":"タイトル"}}`, "not a translated string"},
			{"not json at all", `{`, "is not a message catalog"},
			{"a bare string", `"タイトル"`, "is not a message catalog"},
			// Unmarshalling into map[string]string would take both of these: to it a JSON null *is*
			// the empty string, and a nested value is a type error it never reports. Neither is a
			// translation, and both reach the UI as a blank where a label belongs.
			{"a null value", `{"app.title":null}`, "app.title is null, not a translated string"},
			{"a numeric value", `{"app.count":3}`, "app.count is 3, not a translated string"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				dir := writePack(t, map[string]string{"messages.json": tt.catalog, "prompts/base.tmpl": `ok`})
				err := validateI18N(&Capabilities{I18N: i18nPack()}, dir)
				if err == nil {
					t.Fatalf("catalog %s was accepted", tt.catalog)
				}
				for _, want := range []string{"capabilities.i18n.messages_url", tt.want} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("%q does not say %q", err, want)
					}
				}
			})
		}
	})

	t.Run("a plural table is a value too", func(t *testing.T) {
		// A counted message ships as { one, other, … } because "1 task / 3 tasks" is not one
		// string with a plural inside it. The runtime selects the form through
		// Intl.PluralRules ($lib/i18n), so refusing one here would leave every counted
		// string in the UI English in every language — the one thing a pack exists to fix.
		for _, catalog := range []string{
			`{"card.tasks":{"one":"{count} タスク","other":"{count} タスク"}}`,
			`{"card.tasks":{"other":"{count} task"}}`, // a single-form language needs only this
			`{"card.tasks":{"zero":"a","one":"b","two":"c","few":"d","many":"e","other":"f"}}`,
			`{"card.tasks":{"one":"a","other":"b"},"app.title":"タイトル"}`, // mixed with plain strings
		} {
			t.Run(catalog, func(t *testing.T) {
				dir := writePack(t, map[string]string{"messages.json": catalog, "prompts/base.tmpl": `ok`})
				if err := validateI18N(&Capabilities{I18N: i18nPack()}, dir); err != nil {
					t.Fatalf("a catalog a pack may legitimately ship was refused: %v", err)
				}
			})
		}
	})

	t.Run("a plural table nothing could select a form from", func(t *testing.T) {
		for _, tt := range []struct {
			name    string
			catalog string
			want    string
		}{
			{"no other form", `{"card.tasks":{"one":"a"}}`, `needs the "other" form`},
			{"an empty table", `{"card.tasks":{}}`, `needs the "other" form`},
			{"an invented category", `{"card.tasks":{"onet":"a","other":"b"}}`, "not a plural category"},
			{"a numeric form", `{"card.tasks":{"one":3,"other":"b"}}`, "plural form is not a string"},
			{"a null form", `{"card.tasks":{"other":null}}`, "plural form is not a string"},
			// A nested catalog, not a plural table: `title` is no category.
			{"a nested catalog", `{"nav":{"title":"タイトル"}}`, "not a translated string"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				dir := writePack(t, map[string]string{"messages.json": tt.catalog, "prompts/base.tmpl": `ok`})
				err := validateI18N(&Capabilities{I18N: i18nPack()}, dir)
				if err == nil {
					t.Fatalf("catalog %s was accepted", tt.catalog)
				}
				for _, want := range []string{"capabilities.i18n.messages_url", tt.want} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("%q does not say %q", err, want)
					}
				}
			})
		}
	})

	t.Run("a declared catalog that is missing", func(t *testing.T) {
		dir := writePack(t, map[string]string{"messages.json": ""})
		if err := os.Remove(filepath.Join(dir, "messages.json")); err != nil {
			t.Fatal(err)
		}
		err := validateI18N(&Capabilities{I18N: i18nPack()}, dir)
		if err == nil || !strings.Contains(err.Error(), "capabilities.i18n.messages_url") {
			t.Fatalf("a pack with no catalog at its declared path was accepted: %v", err)
		}
	})

	t.Run("a prompts_dir the pack does not ship", func(t *testing.T) {
		dir := writePack(t, nil)
		err := validateI18N(&Capabilities{I18N: i18nPack()}, dir)
		if err == nil || !strings.Contains(err.Error(), "is not a directory the pack ships") {
			t.Fatalf("a declared prompts_dir with no directory behind it was accepted: %v", err)
		}
	})

	t.Run("a prompts_dir that overrides nothing", func(t *testing.T) {
		dir := writePack(t, map[string]string{"prompts/README.md": "translated prompts land here"})
		err := validateI18N(&Capabilities{I18N: i18nPack()}, dir)
		if err == nil || !strings.Contains(err.Error(), "contains no .tmpl file") {
			t.Fatalf("an empty prompts_dir was accepted: %v", err)
		}
	})

	t.Run("an empty catalog is a catalog", func(t *testing.T) {
		// The published packs ship `{}` while their strings are translated, and they parse. The gate
		// is "is this a catalog", not "is it finished".
		dir := writePack(t, map[string]string{"messages.json": `{}`, "prompts/base.tmpl": `ok`})
		if err := validateI18N(&Capabilities{I18N: i18nPack()}, dir); err != nil {
			t.Errorf("a pack with an empty catalog was refused: %v", err)
		}
	})

	t.Run("no directory on disk is the shape check only", func(t *testing.T) {
		// A registry entry with no git pin installs a row before any file exists, so the declaration
		// is all there is to check — and a missing file must not fail that install.
		missing := filepath.Join(t.TempDir(), "not-fetched-yet")
		if err := validateI18N(&Capabilities{I18N: i18nPack()}, missing); err != nil {
			t.Errorf("an unmaterialized pack was refused: %v", err)
		}
		if err := validateI18N(&Capabilities{I18N: i18nPack()}, ""); err != nil {
			t.Errorf("the no-directory case was refused: %v", err)
		}
	})

	t.Run("a pack that declares no prompts needs no prompts directory", func(t *testing.T) {
		pack := i18nPack()
		pack.PromptsDir = ""
		dir := writePack(t, nil)
		if err := validateI18N(&Capabilities{I18N: pack}, dir); err != nil {
			t.Errorf("refused a catalog-only pack over a directory with no prompts: %v", err)
		}
	})
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

// TestSyntaxSpecNameRulesRoundTrip exists because of how these two fields are declared: the browser
// reads a missing key as the default (true), so an author's explicit "no" has to survive arriving here,
// being stored, and being sent back out. A plain bool with omitempty would drop a false on the way out
// and the refusal would reach the browser as a request.
func TestSyntaxSpecNameRulesRoundTrip(t *testing.T) {
	yes, no := true, false

	for _, tt := range []struct {
		name string
		json string
		want *bool
	}{
		{"absent stays absent", `{"keywords":["go"]}`, nil},
		{"false stays false", `{"keywords":["go"],"capitalized_types":false}`, &no},
		{"true stays true", `{"keywords":["go"],"capitalized_types":true}`, &yes},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var spec SyntaxSpec
			if err := json.Unmarshal([]byte(tt.json), &spec); err != nil {
				t.Fatalf("decode the manifest: %v", err)
			}
			out, err := json.Marshal(spec)
			if err != nil {
				t.Fatalf("encode it back for the browser: %v", err)
			}
			var again SyntaxSpec
			if err := json.Unmarshal(out, &again); err != nil {
				t.Fatalf("decode what the browser got: %v (%s)", err, out)
			}

			switch {
			case tt.want == nil && again.CapitalizedTypes != nil:
				t.Errorf("CapitalizedTypes = %v after the round trip, want the key absent (%s)", *again.CapitalizedTypes, out)
			case tt.want != nil && again.CapitalizedTypes == nil:
				t.Errorf("CapitalizedTypes came back absent, want %v — absent reads as the default true in the browser (%s)", *tt.want, out)
			case tt.want != nil && *again.CapitalizedTypes != *tt.want:
				t.Errorf("CapitalizedTypes = %v, want %v (%s)", *again.CapitalizedTypes, *tt.want, out)
			}
		})
	}

	// The two fields are independent: turning one off must not silently turn the other off too.
	var spec SyntaxSpec
	if err := json.Unmarshal([]byte(`{"keywords":["go"],"call_as_function":false}`), &spec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if spec.CallAsFunction == nil || *spec.CallAsFunction {
		t.Errorf("CallAsFunction = %v, want an explicit false", spec.CallAsFunction)
	}
	if spec.CapitalizedTypes != nil {
		t.Errorf("CapitalizedTypes = %v, want it untouched by the other field's refusal", *spec.CapitalizedTypes)
	}
}
