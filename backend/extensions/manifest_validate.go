package extensions

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"unicode/utf8"

	"mike/backend/fsutil"
	"mike/backend/prompts"
	"mike/pkg/sandboxruntime"
)

// Manifest validation: a malformed syntax profile or a duplicated file type is rejected at install
// time instead of stored and left to degrade silently. Same spirit as mount_policy.go.

// Validate checks an extension's file types and the syntax profiles they name. Run on install,
// update and PUT, so a bad manifest is never persisted.
func (c *LSPConfig) Validate() error {
	if c == nil {
		return nil
	}
	if len(c.FileTypes) == 0 {
		return fmt.Errorf("capabilities.lsp.file_types must declare at least one file type")
	}

	seen := map[string]bool{}
	for i, ft := range c.FileTypes {
		where := fmt.Sprintf("capabilities.lsp.file_types[%d]", i)
		if ft.Extension == "" {
			return fmt.Errorf("%s.extension is empty", where)
		}
		if strings.HasPrefix(ft.Extension, ".") {
			return fmt.Errorf("%s.extension %q must not start with a dot", where, ft.Extension)
		}
		if strings.ContainsAny(ft.Extension, "./\\ ") {
			return fmt.Errorf("%s.extension %q must be a bare suffix (no dot, slash or space)", where, ft.Extension)
		}
		if ft.Extension != strings.ToLower(ft.Extension) {
			return fmt.Errorf("%s.extension %q must be lowercase — file suffixes are matched lowercased", where, ft.Extension)
		}
		if ft.LanguageID == "" {
			return fmt.Errorf("%s.language_id is empty (the language server is told this as the document languageId)", where)
		}
		// Lowercase is the LSP convention; a capitalized id reads as a broken server, not a bad manifest.
		if ft.LanguageID != strings.ToLower(ft.LanguageID) {
			return fmt.Errorf("%s.language_id %q must be lowercase", where, ft.LanguageID)
		}
		if seen[ft.Extension] {
			return fmt.Errorf("%s.extension %q is declared twice", where, ft.Extension)
		}
		seen[ft.Extension] = true

		if ft.Syntax != "" {
			if _, ok := c.Syntax[ft.Syntax]; !ok {
				return fmt.Errorf("%s.syntax %q names no profile in capabilities.lsp.syntax", where, ft.Syntax)
			}
		}
	}

	for name, spec := range c.Syntax {
		if err := spec.Validate(); err != nil {
			return fmt.Errorf("capabilities.lsp.syntax.%s: %w", name, err)
		}
	}

	// The sandbox block holds three things the runtime acts on directly. Each is checked here,
	// where a manifest is refused rather than stored and then worked around at every start.
	if c.Sandbox != nil {
		// The host directory is a grant: asked of a human, never read from a manifest. An extension that
		// names a directory on the machine it happens to be installed on has declared where *it* looks and
		// then reached for the host. Nothing here is derived from a container path either, so a manifest
		// cannot name a host directory by not filling one in.
		if c.Sandbox.DependencyHostPath != "" {
			return fmt.Errorf("capabilities.lsp.sandbox.dependency_host_path is a grant the user applies under Settings -> Extensions and may not come from a manifest (got %q)", c.Sandbox.DependencyHostPath)
		}
		if err := c.Sandbox.Dependency.Validate(); err != nil {
			return fmt.Errorf("capabilities.lsp.sandbox.dependency_cache: %w", err)
		}
		if err := validateReloadFiles(c.Sandbox.ReloadFiles); err != nil {
			return fmt.Errorf("capabilities.lsp.sandbox.reload_files: %w", err)
		}
		if err := c.Sandbox.Environment.Validate(); err != nil {
			return fmt.Errorf("capabilities.lsp.sandbox.environment: %w", err)
		}
	}
	return nil
}

// envName is a shell variable name: the only thing a manifest may name in dependency_cache.env.
// An assignment ("GOMODCACHE=/x") or a value with a metacharacter in it is refused here rather
// than sorted out where it is applied, and the value is not the manifest's to give anyway.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Validate checks a declared dependency location. It checks the *declaration*, never whether
// some host directory exists: that is the user's setting and the runtime's problem, and a
// manifest cannot be held to a machine it does not run on.
func (d *DependencyCache) Validate() error {
	if d == nil {
		return nil
	}
	if strings.TrimSpace(d.ContainerPath) == "" {
		return fmt.Errorf("container_path is required (where this server reads its dependencies from)")
	}
	if err := sandboxruntime.ValidateContainerPath(d.ContainerPath); err != nil {
		return err
	}
	if !envName.MatchString(d.Env) {
		return fmt.Errorf("env %q is not a variable name — letters, digits and underscore, not starting with a digit", d.Env)
	}
	// The cache is mounted read-only, always, and a server that cannot write where it thinks
	// its state lives publishes nothing at all about a file it cannot load. Refuse the location here,
	// where the message can be acted on, rather than unmounting it in silence at every container start.
	//
	// A manifest that still carries the removed `read_only` key validates and ignores it, and that
	// silence is deliberate: the registry pins already published point at commits whose manifests have
	// the key, so refusing it would be an install failure for an extension that asks for nothing.
	if ShadowsToolState(d.ContainerPath) {
		return fmt.Errorf("container_path %q covers the server's own writable state (the container's tmpfs or its user's .cache) — point it at a location the server only reads from", d.ContainerPath)
	}
	return nil
}

// validateReloadFiles checks the paths that mean "the project changed". They are matched
// against a workspace-relative changed-file list, so a path that escapes the workspace or names
// the whole tree would recycle the server on every turn of every project.
func validateReloadFiles(files []string) error {
	for _, f := range files {
		trimmed := strings.TrimSpace(f)
		if trimmed == "" {
			return fmt.Errorf("an entry is blank")
		}
		if strings.HasPrefix(trimmed, "/") {
			return fmt.Errorf("%q is absolute — these are workspace-relative paths", trimmed)
		}
		if strings.Contains(trimmed, "\\") {
			return fmt.Errorf("%q must use '/' separators", trimmed)
		}
		for _, seg := range strings.Split(trimmed, "/") {
			if seg == ".." {
				return fmt.Errorf("%q must not contain '..'", trimmed)
			}
		}
	}
	return nil
}

// Validate checks the environment gate: a gate whose check is empty would be stored, run as
// nothing, and read as "the workspace is fine" forever — which is the silence this whole field
// exists to prevent.
func (g *EnvironmentGate) Validate() error {
	if g == nil {
		return nil
	}
	if _, ok := g.Check.Argv(); !ok {
		return fmt.Errorf("check.command is empty — declare the command that answers whether this server can resolve the workspace, or omit environment entirely")
	}
	for _, code := range g.SuppressCodes {
		if strings.TrimSpace(code) == "" {
			return fmt.Errorf("suppress_codes contains a blank entry")
		}
	}
	return nil
}

// declaresNames reports whether the profile asks for the shape rules: a called name is a function, an
// upper-case name is a type. Only an explicit true counts — a profile whose only declaration is
// `capitalized_types: false` declares nothing, and the refusal below is the answer it deserves.
func (s SyntaxSpec) declaresNames() bool {
	return (s.CapitalizedTypes != nil && *s.CapitalizedTypes) || (s.CallAsFunction != nil && *s.CallAsFunction)
}

// Validate checks one syntax profile; the grammar builder cannot guess what a half-declared delimiter meant.
func (s SyntaxSpec) Validate() error {
	if s.LineComment == "" && len(s.LineComments) == 0 && s.BlockComment == nil && len(s.Strings) == 0 && len(s.Pairs) == 0 &&
		len(s.Keywords) == 0 && len(s.Types) == 0 && len(s.Constants) == 0 && len(s.Builtins) == 0 && !s.Numbers && !s.declaresNames() {
		return fmt.Errorf("declares nothing to tokenize — at least one of line_comment, block_comment, strings, pairs, keywords, types, constants, builtins, numbers, capitalized_types or call_as_function is required")
	}
	for i, marker := range s.LineComments {
		if strings.TrimSpace(marker) == "" {
			return fmt.Errorf("line_comments[%d] is empty", i)
		}
		if marker == s.LineComment {
			return fmt.Errorf("line_comments[%d] %q repeats line_comment — one marker needs declaring once", i, marker)
		}
	}
	if s.BlockComment != nil {
		if err := validatePair("block_comment", *s.BlockComment); err != nil {
			return err
		}
	}
	if s.Region != nil {
		if s.Region.Open == "" || s.Region.Close == "" {
			return fmt.Errorf("region needs both open and end markers (got open=%q close=%q)", s.Region.Open, s.Region.Close)
		}
	}
	seenQuote := map[string]bool{}
	for i, str := range s.Strings {
		n := utf8.RuneCountInString(str.Quote)
		if n < 1 || n > 3 {
			// One character, or that character repeated; longer is a delimiter, not a quote.
			return fmt.Errorf("strings[%d].quote %q must be 1 to 3 characters", i, str.Quote)
		}
		if strings.TrimSpace(str.Quote) != str.Quote || strings.ContainsAny(str.Quote, "\r\n\t") {
			return fmt.Errorf("strings[%d].quote %q must not contain whitespace", i, str.Quote)
		}
		for _, r := range str.Quote {
			if r != rune([]rune(str.Quote)[0]) {
				return fmt.Errorf("strings[%d].quote %q must be one character repeated (a quote, not a sequence)", i, str.Quote)
			}
		}
		if seenQuote[str.Quote] {
			return fmt.Errorf("strings[%d].quote %q is declared twice", i, str.Quote)
		}
		seenQuote[str.Quote] = true
	}
	for i, pair := range s.Pairs {
		if err := validatePair(fmt.Sprintf("pairs[%d]", i), pair); err != nil {
			return err
		}
		if utf8.RuneCountInString(pair.Open) != 1 || utf8.RuneCountInString(pair.Close) != 1 {
			// The fold provider pairs single characters, so a multi-character opener would fold the wrong range.
			return fmt.Errorf("pairs[%d] must be single characters (got %q/%q)", i, pair.Open, pair.Close)
		}
	}
	for _, words := range []struct {
		name  string
		items []string
	}{
		{"keywords", s.Keywords}, {"types", s.Types}, {"constants", s.Constants}, {"builtins", s.Builtins},
	} {
		for i, w := range words.items {
			if strings.TrimSpace(w) == "" {
				return fmt.Errorf("%s[%d] is empty", words.name, i)
			}
			if strings.ContainsAny(w, " \t\n") {
				return fmt.Errorf("%s[%d] %q must be a single word — words are matched whole", words.name, i, w)
			}
		}
	}
	return nil
}

// --- Translation packs (capabilities.i18n) -------------------------------------
//
// A pack is data: two paths inside its own directory and a language tag. Everything that makes
// that promise true is checked here, where the manifest is refused, because the alternative is a
// pack that fails in the browser (a catalog that will not parse), in the next executor turn (a
// template that will not compile), or in the host (a path that resolved somewhere else).

// langSubtag is a bare BCP-47 language subtag: 2-3 letters for a primary subtag ("zh", "fr"),
// 5-8 for an extended one. Nothing may follow it — see validateLocale for why the region form is
// refused rather than reduced.
var langSubtag = regexp.MustCompile(`^(?:[a-z]{2,3}|[a-z]{5,8})$`)

// externalRef is any reference carrying a scheme ("https:", "data:", "file:"). It is the same
// shape the browser-side asset resolver rejects, so a pack cannot point the UI at a host by
// spelling its path in one of the two ways a browser would follow.
var externalRef = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.\-]*:`)

// Validate checks the shape of a translation pack's declaration. The files it points at are the
// caller's business — see validateI18N, which checks both once the pack is on disk.
func (c *I18NConfig) Validate() error {
	if c == nil {
		return nil
	}
	if err := validateLocale(c.Locale); err != nil {
		return err
	}
	if err := validateAssetRelPath("capabilities.i18n.messages_url", c.MessagesURL, true); err != nil {
		return err
	}
	return validateAssetRelPath("capabilities.i18n.prompts_dir", c.PromptsDir, false)
}

// Validate checks the shape of a css pack's declaration. `css_url` is the one manifest field the
// browser resolves before any of this package can run: the theme stylesheet is put in a <link> and,
// because it must be right on the first frame, into the storage the pre-paint script in
// index.html reads. The same refusals the i18n paths get apply here for the same reason.
func (c *CSSConfig) Validate() error {
	if c == nil {
		return nil
	}
	return validateAssetRelPath("capabilities.css.css_url", c.CSSURL, true)
}

// validateLocale accepts one bare language subtag and nothing else.
//
// Strict on purpose, and the error says so: "zh-CN" and "zh-TW" are not the same language, and a
// core that silently truncated a region tag would hand a Traditional-Chinese reader a pack that
// decided for them. One tag per pack is also what makes the registry's pre-install offer — a
// browser language matched against the index — mean the same thing on both sides.
func validateLocale(locale string) error {
	if strings.TrimSpace(locale) == "" {
		return fmt.Errorf("capabilities.i18n.locale is required — a bare language subtag such as \"zh\", \"fr\" or \"ko\"")
	}
	if langSubtag.MatchString(locale) {
		return nil
	}
	// Only claim the lowercase rule when that is the whole mistake. "zh-CN" has an uppercase letter
	// *and* a region, and its author needs to hear about the region — telling them to lowercase it
	// would send them off with a tag that is still refused.
	if langSubtag.MatchString(strings.ToLower(locale)) {
		return fmt.Errorf("capabilities.i18n.locale %q must be lowercase — a language tag is matched as written", locale)
	}
	return fmt.Errorf("capabilities.i18n.locale %q is not a bare BCP-47 language subtag — 2-3 or 5-8 lowercase letters, with no region, script or modifier after it: use \"zh\", not \"zh-CN\" or \"fr_FR@euro\"", locale)
}

// maxPackAssetPathLen bounds a declared asset path. Catalog names, prompt directories and
// stylesheets are short by nature; the bound exists so a manifest cannot smuggle a megabyte of
// path into a log line or an error message.
const maxPackAssetPathLen = 200

// validateAssetRelPath is the containment rule for every path a manifest declares inside its own
// directory: the i18n catalog, the prompt overlay, a stylesheet. The browser turns each one into a
// request against this server, so a path that escapes the directory with `..` would be an
// authenticated read of anything the app serves.
func validateAssetRelPath(where, value string, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s is required — the extension's own file, relative to its own directory", where)
		}
		return nil
	}
	if len(value) > maxPackAssetPathLen {
		return fmt.Errorf("%s is longer than %d characters", where, maxPackAssetPathLen)
	}
	if strings.ContainsAny(value, "\x00\n\r\t") || strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return fmt.Errorf("%s %q contains a control character", where, value)
	}
	if externalRef.MatchString(value) || strings.HasPrefix(value, "//") {
		return fmt.Errorf("%s %q is an external reference — an extension ships its files inside its own directory, and a resource loaded from a host nobody chose is not that file", where, value)
	}
	if filepath.IsAbs(value) || filepath.VolumeName(value) != "" || strings.HasPrefix(value, "/") {
		return fmt.Errorf("%s %q is absolute — the path is resolved inside the extension's own directory", where, value)
	}
	if strings.Contains(value, "\\") {
		return fmt.Errorf("%s %q must use '/' separators", where, value)
	}
	for _, seg := range strings.Split(value, "/") {
		switch seg {
		case "":
			return fmt.Errorf("%s %q has an empty path segment", where, value)
		case ".", "..":
			return fmt.Errorf("%s %q must not contain '.' or '..' — it is a plain path inside the extension's own directory", where, value)
		}
	}
	return nil
}

// packFilePath resolves a pack-relative path inside the pack's directory and proves the result is
// still inside it. The lexical check above is the readable error; this is the enforcement, because
// filepath.Join collapses what a check like the one above refused to see.
func packFilePath(packDir, rel string) (string, bool) {
	rel = strings.TrimSpace(rel)
	if rel == "" || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", false
	}
	full := filepath.Clean(filepath.Join(packDir, rel))
	inside, err := filepath.Rel(filepath.Clean(packDir), full)
	if err != nil || !fsutil.RelBelowRoot(inside) {
		return "", false
	}
	return full, true
}

// validateI18N refuses a manifest whose i18n block cannot be used as declared. Pass extDir "" when nothing was fetched.
func validateI18N(caps *Capabilities, extDir string) error {
	c := caps.GetI18N()
	if c == nil {
		return nil
	}
	if others := caps.otherCapabilityNames(); len(others) > 0 {
		return fmt.Errorf("capabilities.i18n: a translation pack declares i18n and nothing else, but this manifest also declares %s. A pack gets no sandbox, no mounts, no network and no permissions — install the pack and the other extension separately",
			strings.Join(others, ", "))
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if extDir == "" {
		return nil
	}
	if st, err := os.Stat(extDir); err != nil || !st.IsDir() {
		// The pack is not on disk, so there is nothing to read and nothing to refuse.
		return nil
	}
	return validateI18NFiles(c, extDir)
}

// otherCapabilityNames lists every capability key declared beside i18n, in the order Capabilities
// declares them. The names are what the refusal quotes: an author has to be told which field to
// delete, not that something about the manifest is wrong.
func (c *Capabilities) otherCapabilityNames() []string {
	if c == nil {
		return nil
	}
	var others []string
	if c.LSP != nil {
		others = append(others, "lsp")
	}
	if c.UI != nil {
		others = append(others, "ui")
	}
	if c.CSS != nil {
		others = append(others, "css")
	}
	if c.Agent != nil {
		others = append(others, "agent")
	}
	if c.Network != nil {
		others = append(others, "network")
	}
	if c.LLM != nil {
		others = append(others, "llm")
	}
	if len(c.Tools) > 0 {
		others = append(others, "tools")
	}
	return others
}

// pluralCategories are the forms `Intl.PluralRules` can answer with. A counted message may
// be shipped as one of these instead of a string — {"card.tasks": {"one": "…", "other": "…"}} —
// because "1 task / 3 tasks" is not one string with a plural inside it, and each language
// decides its own forms: some answer one/other, some one/few/many/other, and some only ever
// `other`, which is why `other` is required and sufficient. The frontend parses and renders
// exactly this shape (`parseCatalog` and `I18nRuntime.render` in `$lib/i18n`), so the two
// halves accept the same packs: refusing here what the runtime would render, or accepting
// what it would silently drop, are the same bug in opposite directions.
var pluralCategories = map[string]bool{
	"zero": true, "one": true, "two": true, "few": true, "many": true, "other": true,
}

// validateCatalogValue accepts a translated string or a plural table of them, and refuses
// everything else with the offending value quoted into the message — the installer shows
// this string, and "app.title is null" is the diagnosis.
func validateCatalogValue(messagesURL, key string, value json.RawMessage) error {
	notAString := fmt.Errorf("capabilities.i18n.messages_url %q: %s is %s, not a translated string — every value is a string or a plural table of them, and a null renders as a blank where a label belongs", messagesURL, key, value)
	if len(value) == 0 {
		return notAString
	}
	// Decoded as `any` rather than into a string: a JSON null must be refused (it is the
	// loader being handed nothing where a label belongs), and a nested object is only a
	// plural table if its keys say so.
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return notAString
	}
	if _, isString := decoded.(string); isString {
		return nil
	}
	forms, isObject := decoded.(map[string]any)
	if !isObject {
		return notAString
	}
	return validatePluralForms(messagesURL, key, forms)
}

// validatePluralForms refuses a table nothing could select a form from: an unknown category
// is a typo that would never be read, a non-string form is the null problem again, and a
// table without `other` has no answer for the counts its own language has no name for.
func validatePluralForms(messagesURL, key string, forms map[string]any) error {
	names := make([]string, 0, len(forms))
	for name := range forms {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !pluralCategories[name] {
			return fmt.Errorf("capabilities.i18n.messages_url %q: %s is not a translated string — %q is not a plural category Intl can answer with (zero, one, two, few, many, other)", messagesURL, key, name)
		}
		if _, isText := forms[name].(string); !isText {
			return fmt.Errorf("capabilities.i18n.messages_url %q: %s is not a translated string — the %q plural form is not a string", messagesURL, key, name)
		}
	}
	if _, ok := forms["other"]; !ok {
		return fmt.Errorf("capabilities.i18n.messages_url %q: %s is not a translated string — a plural table needs the \"other\" form, the only category every language has", messagesURL, key)
	}
	return nil
}

// validateI18NFiles reads what the declaration points at. A catalog that will not parse is not
// discovered when the interface first paints in another language, and a template that will not
// compile is not discovered mid-turn: both are refused here, once, at install.
func validateI18NFiles(c *I18NConfig, packDir string) error {
	msgPath, ok := packFilePath(packDir, c.MessagesURL)
	if !ok {
		return fmt.Errorf("capabilities.i18n.messages_url %q does not resolve inside the pack's own directory", c.MessagesURL)
	}
	data, err := os.ReadFile(msgPath)
	if err != nil {
		return fmt.Errorf("capabilities.i18n.messages_url %q cannot be read: %w", c.MessagesURL, err)
	}
	var catalog map[string]json.RawMessage
	if err := json.Unmarshal(data, &catalog); err != nil {
		return fmt.Errorf("capabilities.i18n.messages_url %q is not a message catalog — a JSON object of message key to translated string: %w", c.MessagesURL, err)
	}
	// Decoded as raw values rather than straight into map[string]string, which would take a JSON
	// null as the empty string. A null is not a translation: it is the loader being handed nothing
	// where a label belongs, and it is a mistake a translator makes with a find-and-replace.
	keys := make([]string, 0, len(catalog))
	for key := range catalog {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := validateCatalogValue(c.MessagesURL, key, catalog[key]); err != nil {
			return err
		}
	}
	if c.PromptsDir == "" {
		return nil
	}

	dir, ok := packFilePath(packDir, c.PromptsDir)
	if !ok {
		return fmt.Errorf("capabilities.i18n.prompts_dir %q does not resolve inside the pack's own directory", c.PromptsDir)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("capabilities.i18n.prompts_dir %q is not a directory the pack ships", c.PromptsDir)
	}
	count := 0
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(d.Name()), templateExt) {
			return nil
		}
		count++
		// Named relative to the pack, not by the absolute path: this string reaches the browser
		// as the install failure, and it has to say which of the pack's own files broke.
		name, relErr := filepath.Rel(packDir, path)
		if relErr != nil {
			name = filepath.Base(path)
		}
		// Parsed with the renderer's own function names: an unregistered function is a parse
		// error, so a check that parsed without them would refuse a template the loader accepts.
		if _, parseErr := template.New(filepath.Base(path)).Funcs(prompts.FuncMap()).ParseFiles(path); parseErr != nil {
			return fmt.Errorf("%s does not parse as a prompt template: %w", name, parseErr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("capabilities.i18n.prompts_dir: %w", err)
	}
	// A directory that overrides nothing is the dependency-cache failure again: the declaration
	// reads as a translated prompt set, and every turn silently renders the English original.
	if count == 0 {
		return fmt.Errorf("capabilities.i18n.prompts_dir %q contains no %s file — drop the declaration or ship the templates it promises", c.PromptsDir, templateExt)
	}
	return nil
}

// templateExt is the one file extension a prompt overlay is made of.
const templateExt = ".tmpl"

func validatePair(where string, p CommentToken) error {
	if p.Open == "" || p.Close == "" {
		return fmt.Errorf("%s needs both open and close (got open=%q close=%q)", where, p.Open, p.Close)
	}
	if p.Open == p.Close {
		return fmt.Errorf("%s open and close are identical (%q) — nothing can tell them apart", where, p.Open)
	}
	return nil
}
