package main

// Message-catalogue loading and parity rules.
//
// The English catalogue is the contract (frontend/src/lib/i18n/en.ts) and a locale pack mirrors
// it (messages.json). Neither side may be trusted to be complete, so this file answers exactly
// three questions: which keys does each side declare, which placeholders does each value carry,
// and where do the two disagree. It deliberately reports nothing about whether a translation
// reads well — a checker that guesses at prose produces findings nobody acts on.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Catalog is one message catalogue flattened to dotted keys.
type Catalog struct {
	Path   string
	Values map[string]string
	// Tables maps a counted key to the plural forms its value declared. A message whose value is
	// an object of plural categories (`{one, other}`) flattens to `key.one`, `key.other`, which is
	// right for comparing placeholders and wrong for asking "does the pack have this message": the
	// pack of a language with two forms legitimately has no `key.one`. Shape is recorded here at
	// load time rather than inferred from a key's last segment, so a message legitimately named
	// `…other` cannot be mistaken for a plural form.
	Tables map[string]map[string]string
	// Duplicates are keys declared more than once. The last declaration wins in both JS and Go,
	// so a duplicate silently discards a message instead of failing a build.
	Duplicates []string
	// Notes are load-time problems worth reporting (a non-string value, a TS expression we could
	// not fold into a string). They are not parity failures on their own.
	Notes []string
}

// recordTable notes that the object value at `key` was a counted message, reading its forms back
// out of the flattened values the loader has already stored.
func (c *Catalog) recordTable(key string, members map[string]string) {
	forms := make(map[string]string, len(members))
	for form := range members {
		forms[form] = c.Values[key+"."+form]
	}
	if c.Tables == nil {
		c.Tables = map[string]map[string]string{}
	}
	c.Tables[key] = forms
}

// baseKeys returns every message with its plural forms collapsed back into the one key the UI
// looks up. This is the set a pack and the contract are compared on: comparing flattened keys
// instead demands that every language spell every English form, which is false for a fifth of
// the world's languages and made every real pack fail.
func (c *Catalog) baseKeys() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(c.Values))
	for k := range c.Values {
		base := c.baseOf(k)
		if !seen[base] {
			seen[base] = true
			out = append(out, base)
		}
	}
	sort.Strings(out)
	return out
}

// baseOf maps a flattened key to the message it belongs to: `board.card.fileCount.one` is the
// `one` form of `board.card.fileCount`, and every other key is its own message.
func (c *Catalog) baseOf(key string) string {
	if i := strings.LastIndex(key, "."); i > 0 {
		if _, isForm := c.Tables[key[:i]]; isForm {
			return key[:i]
		}
	}
	return key
}

// placeholder is one `{name}` token, kept as its exact source bytes: "byte-identical" is a
// claim about the bytes, so a token is never normalised, trimmed or re-cased.
type placeholder struct {
	Text string // "{count}"
	Kind string // "named", "positional", "double-brace", "unclosed", "empty", "malformed"
}

// LoadCatalog reads a catalogue by extension: .json is JSON, anything else is read as a
// TypeScript module exporting one object literal.
func LoadCatalog(path string) (*Catalog, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return loadJSONCatalog(path, raw)
	default:
		return loadTSCatalog(path, raw)
	}
}

// loadJSONCatalog walks the token stream rather than unmarshalling into a map, because
// encoding/json silently keeps the last of a duplicate key — the exact failure this checks for.
func loadJSONCatalog(path string, raw []byte) (*Catalog, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	c := &Catalog{Path: path, Values: map[string]string{}, Tables: map[string]map[string]string{}}

	// decodeObject reads the members of an object whose '{' has already been consumed.
	var decodeObject func(prefix string) error
	decodeObject = func(prefix string) error {
		seen := map[string]bool{}
		members := map[string]string{} // member name -> value, for the string members only
		notATable := false             // a member that is not a plain string value
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := kt.(string)
			if !ok {
				return fmt.Errorf("expected an object key, found %v", kt)
			}
			full := key
			if prefix != "" {
				full = prefix + "." + key
			}
			if seen[key] {
				c.Duplicates = append(c.Duplicates, full)
			}
			seen[key] = true

			nt, err := dec.Token()
			if err != nil {
				return err
			}
			if d, isDelim := nt.(json.Delim); isDelim && d == '{' {
				members[key] = ""
				notATable = true
				if err := decodeObject(full); err != nil {
					return err
				}
				continue
			}
			if d, isDelim := nt.(json.Delim); isDelim && d == '[' {
				members[key] = ""
				notATable = true
				c.Notes = append(c.Notes, fmt.Sprintf("%s: value is an array, which cannot be a message", full))
				if err := skipValue(dec); err != nil {
					return err
				}
				continue
			}
			switch v := nt.(type) {
			case string:
				members[key] = v
				c.set(full, v)
			case json.Number, bool, nil:
				members[key] = ""
				notATable = true
				c.Values[full] = fmt.Sprint(v)
				c.Notes = append(c.Notes, fmt.Sprintf("%s: value is %v, not a string — message values must be quoted", full, v))
			default:
				return fmt.Errorf("%s: unexpected value %v", full, nt)
			}
		}
		_, err := dec.Token() // consume '}'
		if prefix != "" && !notATable && isPluralTable(members, false) {
			c.recordTable(prefix, members)
		}
		return err
	}

	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("%s: expected a JSON object of messages, found %v", path, tok)
	}
	if err := decodeObject(""); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Duplicates = dedupe(c.Duplicates)
	return c, nil
}

// dedupe keeps a repeated key reported once even when two rules both noticed it.
func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// skipValue consumes a value the caller has decided not to keep, including nested containers.
func skipValue(dec *json.Decoder) error {
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '[', '{':
				depth++
			case ']', '}':
				depth--
				if depth == 0 {
					return nil
				}
			}
			continue
		}
		if depth == 0 {
			return nil
		}
	}
}

func (c *Catalog) set(key, value string) {
	// Collision on the flattened key is the failure that matters: JS keeps the last value and
	// says nothing. {a:{b:'x'}} and {'a.b':'y'} flatten to the same key and one is dropped.
	if _, dup := c.Values[key]; dup {
		c.Duplicates = append(c.Duplicates, key)
	}
	c.Values[key] = value
}

// englishOnlyNamespaces are keys a pack must NOT carry, for the reason en.ts states: the
// first-run offer (`i18n.offer.*`) renders only while no pack is installed — a Chinese UI does
// not offer to install Chinese — and `i18n.test.*` renders nowhere but the runtime's own tests.
// A pack carrying them is not more complete, it is carrying strings no code path looks up.
var englishOnlyNamespaces = []string{"i18n.offer.", "i18n.test."}

// englishOnlyExpected is how many keys the contract currently holds English. It is a tripwire on
// this checker, not a rule about packs: when a new offer string lands, someone has to decide it
// is English-only too, rather than have the checker quietly start calling every pack one short.
const englishOnlyExpected = 8

// isEnglishOnly reports whether the contract keeps this key English by design.
func isEnglishOnly(key string) bool {
	for _, prefix := range englishOnlyNamespaces {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- TypeScript reader

// loadTSCatalog reads a TS module exporting a single object literal and flattens it to dotted
// keys. The exact shape of en.ts is T3's to choose, so both conventions are accepted: flat keys
// ('a.b': 'x') and nested objects ({a: {b: 'x'}}), which flatten to the same dotted key. A key
// spelled both ways is a duplicate and reported as one.
func loadTSCatalog(path string, raw []byte) (*Catalog, error) {
	r := &tsReader{src: []rune(string(raw))}
	c := &Catalog{Path: path, Values: map[string]string{}, Tables: map[string]map[string]string{}}

	start, err := r.findObjectStart()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	r.pos = start
	if err := r.readObjectInto(c, ""); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Duplicates = dedupe(c.Duplicates)
	return c, nil
}

type tsReader struct {
	src []rune
	pos int
}

func (r *tsReader) eof() bool { return r.pos >= len(r.src) }

func (r *tsReader) cur() rune {
	if r.eof() {
		return 0
	}
	return r.src[r.pos]
}

func (r *tsReader) skipTrivia() {
	for !r.eof() {
		ch := r.src[r.pos]
		switch {
		case unicode.IsSpace(ch):
			r.pos++
		case ch == '/' && r.pos+1 < len(r.src) && r.src[r.pos+1] == '/':
			for !r.eof() && r.src[r.pos] != '\n' {
				r.pos++
			}
		case ch == '/' && r.pos+1 < len(r.src) && r.src[r.pos+1] == '*':
			r.pos += 2
			for r.pos+1 < len(r.src) && !(r.src[r.pos] == '*' && r.src[r.pos+1] == '/') {
				r.pos++
			}
			r.pos += 2
		default:
			return
		}
	}
}

// findObjectStart returns the index of the '{' that opens the exported object literal, skipping
// `export const en: Messages =` / `export default` and any `satisfies` clause that follows it.
func (r *tsReader) findObjectStart() (int, error) {
	depth := 0
	for i := 0; i < len(r.src); i++ {
		switch r.src[i] {
		case '(':
			depth++
		case ')':
			depth--
		case '\'', '"', '`':
			// Skip a string literal so its braces are never mistaken for structure.
			end, err := r.scanStringLiteral(i)
			if err != nil {
				return 0, err
			}
			i = end
		case '=':
			if depth != 0 || i+1 >= len(r.src) {
				continue
			}
			next := r.src[i+1]
			if next == '=' || next == '>' || next == '<' || next == '!' {
				continue
			}
			if i > 0 {
				prev := r.src[i-1]
				if prev == '!' || prev == '<' || prev == '>' {
					continue
				}
			}
			j := i + 1
			for j < len(r.src) && unicode.IsSpace(r.src[j]) {
				j++
			}
			if j < len(r.src) && r.src[j] == '{' {
				return j, nil
			}
		}
	}
	return 0, fmt.Errorf("no exported object literal found (expected `export const ... = {`)")
}

// scanStringLiteral returns the index of the closing quote, with escapes handled.
func (r *tsReader) scanStringLiteral(start int) (int, error) {
	quote := r.src[start]
	i := start + 1
	for i < len(r.src) {
		switch r.src[i] {
		case '\\':
			i += 2
			continue
		case quote:
			return i, nil
		}
		i++
	}
	return 0, fmt.Errorf("unterminated string literal at offset %d", start)
}

func (r *tsReader) readObjectInto(c *Catalog, prefix string) error {
	if r.cur() != '{' {
		return fmt.Errorf("expected '{' at offset %d, found %q", r.pos, string(r.cur()))
	}
	r.pos++
	seen := map[string]bool{}
	members := map[string]string{} // member name -> value, for the string members only
	notATable := false             // a member that is not a plain string value
	// A nested object is a counted message when its members are plural categories; the root object
	// never is, whatever it contains.
	record := func() {
		if prefix != "" && !notATable && isPluralTable(members, false) {
			c.recordTable(prefix, members)
		}
	}
	for {
		r.skipTrivia()
		if r.eof() {
			return fmt.Errorf("unexpected end of file inside object %q", objectName(prefix))
		}
		if r.cur() == '}' {
			r.pos++
			record()
			return nil
		}
		key, err := r.readKey()
		if err != nil {
			return err
		}
		full := key
		if prefix != "" {
			full = prefix + "." + key
		}
		if seen[key] {
			c.Duplicates = append(c.Duplicates, full)
		}
		seen[key] = true

		r.skipTrivia()
		if r.cur() != ':' {
			return fmt.Errorf("expected ':' after key %q at offset %d, found %q", full, r.pos, string(r.cur()))
		}
		r.pos++

		value, kind, err := r.readValue(c, full)
		if err != nil {
			return err
		}
		switch kind {
		case "string":
			members[key] = value
			c.set(full, value)
		default:
			// A nested namespace or a folded expression: either way this object is not a table of
			// plural forms, so its members are ordinary dotted keys.
			members[key] = ""
			notATable = true
		}

		r.skipTrivia()
		if r.cur() == ',' || r.cur() == ';' {
			r.pos++
			continue
		}
		if r.cur() == '}' {
			r.pos++
			record()
			return nil
		}
		return fmt.Errorf("expected ',' or '}' after %q at offset %d, found %q", full, r.pos, string(r.cur()))
	}
}

func objectName(prefix string) string {
	if prefix == "" {
		return "<root>"
	}
	return prefix
}

func (r *tsReader) readKey() (string, error) {
	r.skipTrivia()
	switch ch := r.cur(); ch {
	case '\'', '"', '`':
		s, err := r.readStringLiteral()
		if err != nil {
			return "", err
		}
		return s, nil
	default:
		start := r.pos
		for !r.eof() {
			ch := r.cur()
			if unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_' || ch == '$' || ch == '-' {
				r.pos++
				continue
			}
			break
		}
		if r.pos == start {
			return "", fmt.Errorf("expected an object key at offset %d, found %q", r.pos, string(r.cur()))
		}
		return string(r.src[start:r.pos]), nil
	}
}

// readStringLiteral reads one quoted literal and applies the escapes TS would apply at runtime,
// so a placeholder is compared against the bytes the UI actually renders.
func (r *tsReader) readStringLiteral() (string, error) {
	quote := r.cur()
	raw, err := r.readRawString()
	if err != nil {
		return "", err
	}
	if quote == '`' {
		return raw, nil // a template literal is raw text apart from escapes, already applied
	}
	return unescapeJS(raw), nil
}

func (r *tsReader) readRawString() (string, error) {
	quote := r.cur()
	if quote != '\'' && quote != '"' && quote != '`' {
		return "", fmt.Errorf("expected a string literal at offset %d, found %q", r.pos, string(r.cur()))
	}
	r.pos++
	var b strings.Builder
	for !r.eof() {
		ch := r.cur()
		if ch == '\\' {
			if r.pos+1 >= len(r.src) {
				return "", fmt.Errorf("truncated escape at offset %d", r.pos)
			}
			b.WriteRune('\\')
			b.WriteRune(r.src[r.pos+1])
			r.pos += 2
			continue
		}
		if ch == quote {
			r.pos++
			return b.String(), nil
		}
		b.WriteRune(ch)
		r.pos++
	}
	return "", fmt.Errorf("unterminated string literal at offset %d", r.pos)
}

// unescapeJS resolves the escapes a JS string literal would have produced.
func unescapeJS(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '0':
			b.WriteByte(0)
		case 'u', 'x':
			// Hex escapes are rare in message files; keep the text verbatim rather than
			// guessing at a width, and let the parity diff show it.
			b.WriteByte('\\')
			b.WriteByte(s[i])
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// readValue returns the value and its kind: "string", "object" or "other". Strings joined with
// `+` across lines are folded, because a generated catalogue often splits a long message.
func (r *tsReader) readValue(c *Catalog, full string) (string, string, error) {
	r.skipTrivia()
	switch r.cur() {
	case '{':
		return "", "object", r.readObjectInto(c, full)
	case '\'', '"', '`':
		s, err := r.readStringLiteral()
		if err != nil {
			return "", "", err
		}
		// Fold `"a" + "b"` across lines: a generated catalogue often splits a long message.
		for {
			save := r.pos
			r.skipTrivia()
			if r.cur() != '+' {
				r.pos = save
				break
			}
			r.pos++
			r.skipTrivia()
			if r.cur() != '\'' && r.cur() != '"' && r.cur() != '`' {
				r.pos = save
				break
			}
			next, err := r.readStringLiteral()
			if err != nil {
				return "", "", err
			}
			s += next
		}
		return s, "string", nil
	default:
		start := r.pos
		for !r.eof() && !strings.ContainsRune(",;}\n", r.cur()) {
			r.pos++
		}
		text := strings.TrimSpace(string(r.src[start:r.pos]))
		c.Notes = append(c.Notes, fmt.Sprintf("%s: value %q is not a string literal — read verbatim", full, truncate(text, 40)))
		return text, "other", nil
	}
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// ------------------------------------------------------------------ placeholders

// ExtractPlaceholders returns every placeholder token in source order, keeping the exact bytes.
// Doubled braces (`{{count}}`) are Go template syntax and never a message placeholder: a message
// file containing one is reported rather than silently matched against itself on both sides.
func ExtractPlaceholders(s string) (tokens []placeholder) {
	for i := 0; i < len(s); {
		if s[i] != '{' {
			i++
			continue
		}
		// `${name}` is JS interpolation, not a message placeholder. It appears when a catalogue is
		// written with template literals and the expression is copied verbatim; counting its inner
		// braces as a `{name}` token would compare two mistakes as an agreement.
		if i > 0 && s[i-1] == '$' {
			closeRel := strings.IndexByte(s[i:], '}')
			if closeRel < 0 {
				tokens = append(tokens, placeholder{Text: s[i-1:], Kind: "unclosed"})
				return tokens
			}
			end := i + closeRel + 1
			tokens = append(tokens, placeholder{Text: s[i-1 : end], Kind: "interpolation"})
			i = end
			continue
		}
		open := 0
		for i+open < len(s) && s[i+open] == '{' {
			open++
		}
		if open >= 2 {
			end := strings.Index(s[i:], strings.Repeat("}", open))
			if end < 0 {
				end = len(s) - i
			} else {
				end += len(strings.Repeat("}", open))
			}
			tokens = append(tokens, placeholder{Text: s[i : i+end], Kind: "double-brace"})
			i += end
			continue
		}
		closeRel := strings.IndexByte(s[i+1:], '}')
		if closeRel < 0 {
			tokens = append(tokens, placeholder{Text: s[i:], Kind: "unclosed"})
			return tokens
		}
		end := i + 1 + closeRel + 1
		tok := s[i:end]
		inner := tok[1 : len(tok)-1]
		switch {
		case inner == "":
			tokens = append(tokens, placeholder{Text: tok, Kind: "empty"})
		case isNamedPlaceholder(inner) || isPositionalPlaceholder(inner):
			tokens = append(tokens, placeholder{Text: tok, Kind: "named"})
		default:
			tokens = append(tokens, placeholder{Text: tok, Kind: "malformed"})
		}
		i = end
	}
	return tokens
}

func isNamedPlaceholder(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		ok := r == '_' || r == '.' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return false
		}
		if i == 0 && (r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func isPositionalPlaceholder(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// countPlaceholders groups tokens by exact text. Repetition is significant: a message that uses
// `{count}` twice does not accept a translation that uses it once.
func countPlaceholders(tokens []placeholder) map[string]int {
	m := map[string]int{}
	for _, t := range tokens {
		m[t.Text]++
	}
	return m
}

// diffCounts names the counts that moved. `locale` labels the pack side so a German pack is
// reported as "de×1", not as "zh×1" — the finding is read by a person deciding what to fix.
func diffCounts(en, pack map[string]int, locale string) (missing, extra []string) {
	for _, tok := range sortedKeys(en) {
		if pack[tok] < en[tok] {
			missing = append(missing, fmt.Sprintf("%s (en×%d, %s×%d)", tok, en[tok], locale, pack[tok]))
		}
	}
	for _, tok := range sortedKeys(pack) {
		if pack[tok] > en[tok] {
			extra = append(extra, fmt.Sprintf("%s (en×%d, %s×%d)", tok, en[tok], locale, pack[tok]))
		}
	}
	return missing, extra
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// hasCJK reports whether a value contains any CJK ideograph. Only used to flag a value that was
// copied rather than translated; it is not a correctness rule, since a German or French pack has
// no CJK either.
func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			return true
		}
	}
	return false
}
