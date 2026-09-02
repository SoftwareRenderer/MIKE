package extensions

import (
	"fmt"
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
	return nil
}

// Validate checks one syntax profile; the grammar builder cannot guess what a half-declared delimiter meant.
func (s SyntaxSpec) Validate() error {
	if s.LineComment == "" && len(s.LineComments) == 0 && s.BlockComment == nil && len(s.Strings) == 0 && len(s.Pairs) == 0 &&
		len(s.Keywords) == 0 && len(s.Types) == 0 && len(s.Constants) == 0 && len(s.Builtins) == 0 && !s.Numbers {
		return fmt.Errorf("declares nothing to tokenize — at least one of line_comment, block_comment, strings, pairs, keywords, types, constants, builtins or numbers is required")
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
