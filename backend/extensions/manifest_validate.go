package extensions

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
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
	if err := ValidateContainerPath(d.ContainerPath); err != nil {
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

func validatePair(where string, p CommentToken) error {
	if p.Open == "" || p.Close == "" {
		return fmt.Errorf("%s needs both open and close (got open=%q close=%q)", where, p.Open, p.Close)
	}
	if p.Open == p.Close {
		return fmt.Errorf("%s open and close are identical (%q) — nothing can tell them apart", where, p.Open)
	}
	return nil
}

// maxPackAssetPathLen bounds a declared pack path. Catalog names and prompt
// directories are short by nature; the bound only exists so a manifest cannot
// smuggle a megabyte of path into a log line or an error message.
const maxPackAssetPathLen = 200

// bcp47ish accepts a language tag of the shape a pack is allowed to declare:
// a 2-3 letter primary subtag followed by hyphen-separated alphanumeric
// subtags ("zh", "zh-CN", "pt-BR", "sr-Latn-RS"). Deliberately stricter than
// RFC 5646 — a tag this loose still cannot smuggle a path or a URL, and a
// locale nothing can render is not worth the full grammar.
var bcp47ish = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8})*$`)

// packAssetSchemeRe matches a leading URL scheme ("https:", "data:", "file:").
// It is the same shape the client's resolveAssetURL fence uses
// (frontend/src/lib/services/ui-injector.svelte.ts) — the two ends of one rule,
// kept in step deliberately: the client refuses to resolve an external asset,
// and the server refuses to store a manifest that declares one. Change the
// shape in one and the other stops agreeing.
var packAssetSchemeRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

// Validate checks a translation pack's declaration. Run on install, update and
// PUT, so a pack that could not be loaded is never persisted: an i18n pack's
// whole surface is files inside its own directory, so anything that escapes it
// (a remote origin, a `..` hop) is a refusal rather than something to survive.
func (c *I18NConfig) Validate() error {
	if c == nil {
		return nil
	}
	locale := strings.TrimSpace(c.Locale)
	switch {
	case locale == "":
		return fmt.Errorf("capabilities.i18n.locale is required (a pack with no locale is not a language)")
	case len(locale) > 35:
		return fmt.Errorf("capabilities.i18n.locale %q is longer than 35 characters", locale)
	case !bcp47ish.MatchString(locale):
		return fmt.Errorf("capabilities.i18n.locale %q is not a language tag (expected e.g. \"zh\", \"zh-CN\", \"sr-Latn-RS\")", locale)
	}

	if err := validatePackAssetPath("capabilities.i18n.messages_url", c.MessagesURL, true); err != nil {
		return err
	}
	return validatePackAssetPath("capabilities.i18n.prompts_dir", c.PromptsDir, false)
}

// validatePackAssetPath checks a path a pack declares for a file or directory
// inside its own served directory. required distinguishes the catalog (must be
// declared) from the prompt templates (optional).
func validatePackAssetPath(where, path string, required bool) error {
	path = strings.TrimSpace(path)
	if path == "" {
		if required {
			return fmt.Errorf("%s is required (the catalog is loaded from the extension's own directory)", where)
		}
		return nil
	}
	if len(path) > maxPackAssetPathLen {
		return fmt.Errorf("%s %q is longer than %d characters", where, path, maxPackAssetPathLen)
	}
	// A scheme or a protocol-relative URL would point the app at someone else's
	// origin. resolveAssetURL already rejects these client-side; refusing them
	// here is what keeps an un-installable pack from ever being stored.
	if packAssetSchemeRe.MatchString(path) || strings.HasPrefix(path, "//") {
		return fmt.Errorf("%s %q must be a file in the extension's own directory — an external URL is not allowed", where, path)
	}
	if strings.ContainsAny(path, `\`) {
		return fmt.Errorf("%s %q must use \"/\" separators", where, path)
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." {
			return fmt.Errorf("%s %q must stay inside the extension's own directory (\"..\" escapes it)", where, path)
		}
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s %q contains a control character", where, path)
		}
	}
	return nil
}
