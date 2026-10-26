package store

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// This file brings a database written by an older release up to baseSchemaSQL, in place, before any read
// runs. It adds columns and nothing else; docs/storage.md owns the rule and the reasoning behind it.

// columnSpec is one declared column: its name and the text written after it, which is what an
// ALTER TABLE ADD COLUMN needs to declare the same column again.
type columnSpec struct {
	name        string
	declaration string
}

// tableSpec is one CREATE TABLE in a DDL string, with its columns in declaration order.
type tableSpec struct {
	name    string
	columns []columnSpec
}

// parseTables reads the table declarations out of already-split statements. Anything it cannot read is an
// error, so a DDL this file does not understand cannot be quietly skipped.
func parseTables(statements []string) ([]tableSpec, error) {
	var out []tableSpec
	for _, stmt := range statements {
		name, body, ok := createTableOf(stmt)
		if !ok {
			continue
		}
		columns, err := parseColumns(name, body)
		if err != nil {
			return nil, err
		}
		out = append(out, tableSpec{name: name, columns: columns})
	}
	if len(out) == 0 {
		return nil, errors.New("schema: the DDL declares no table")
	}
	return out, nil
}

// splitStatements cuts a DDL string into statements at the semicolons that end them. A semicolon inside a
// comment or a quoted value belongs to that text, so both are stepped over rather than stripped.
func splitStatements(sql string) []string {
	var (
		out   []string
		stmt  strings.Builder
		quote rune // the open quote, or 0 outside quoted text
	)
	runes := []rune(sql)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case quote != 0:
			stmt.WriteRune(c)
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"' || c == '`':
			quote = c
			stmt.WriteRune(c)
		case c == '-' && i+1 < len(runes) && runes[i+1] == '-' && quote == 0:
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
			stmt.WriteRune('\n')
		case c == ';':
			if text := strings.TrimSpace(stmt.String()); text != "" {
				out = append(out, text)
			}
			stmt.Reset()
		default:
			stmt.WriteRune(c)
		}
	}
	if text := strings.TrimSpace(stmt.String()); text != "" {
		out = append(out, text)
	}
	return out
}

// createTableOf reports whether a statement creates a table, and returns its name and the body written
// inside its parentheses.
func createTableOf(stmt string) (name, body string, ok bool) {
	rest, found := cutPrefixFold(stmt, "CREATE TABLE")
	if !found {
		return "", "", false
	}
	if withoutIfNotExists, found := cutPrefixFold(strings.TrimLeft(rest, " \n\t"), "IF NOT EXISTS"); found {
		rest = withoutIfNotExists
	}
	open := strings.IndexByte(rest, '(')
	closing := strings.LastIndexByte(rest, ')')
	if open < 0 || closing < open {
		return "", "", false
	}
	ident := strings.TrimSpace(rest[:open])
	if err := checkIdentifier(ident); err != nil {
		return "", "", false
	}
	return ident, rest[open+1 : closing], true
}

// cutPrefixFold strips a keyword prefix whatever case it was written in, reporting whether it was there.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return s, false
	}
	return s[len(prefix):], true
}

// parseColumns reads the column lines of a table body. A line that opens with a table-level constraint
// constrains the table and names no column.
func parseColumns(table, body string) ([]columnSpec, error) {
	var columns []columnSpec
	for _, item := range splitTopLevelCommas(body) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		fields := strings.Fields(item)
		name := fields[0]
		declaration := strings.TrimSpace(strings.TrimPrefix(item, name))
		if isTableConstraint(name) {
			continue
		}
		if err := checkIdentifier(name); err != nil {
			return nil, fmt.Errorf("schema: table %s: %w", table, err)
		}
		if declaration == "" {
			return nil, fmt.Errorf("schema: column %s.%s declares no type", table, name)
		}
		columns = append(columns, columnSpec{name: name, declaration: declaration})
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("schema: table %s declares no column", table)
	}
	return columns, nil
}

// isTableConstraint reports whether a body line opens with a table-level constraint instead of a column
// name. None of these words is a legal column name in this DDL.
func isTableConstraint(first string) bool {
	switch strings.ToUpper(first) {
	case "CONSTRAINT", "PRIMARY", "UNIQUE", "FOREIGN", "CHECK":
		return true
	}
	return false
}

// splitTopLevelCommas cuts at the commas between table-body items. A comma inside parentheses or inside
// quotes belongs to the item around it.
func splitTopLevelCommas(body string) []string {
	var (
		out     []string
		item    strings.Builder
		depth   int
		quote   rune
		runes   = []rune(body)
		inLineC bool
	)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case inLineC:
			if c == '\n' {
				inLineC = false
				item.WriteRune(c)
			}
		case quote != 0:
			item.WriteRune(c)
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"' || c == '`':
			quote = c
			item.WriteRune(c)
		case c == '-' && i+1 < len(runes) && runes[i+1] == '-':
			inLineC = true
		case c == '(':
			depth++
			item.WriteRune(c)
		case c == ')':
			depth--
			item.WriteRune(c)
		case c == ',' && depth == 0:
			out = append(out, item.String())
			item.Reset()
		default:
			item.WriteRune(c)
		}
	}
	if strings.TrimSpace(item.String()) != "" {
		out = append(out, item.String())
	}
	return out
}

// checkIdentifier accepts only the identifier shape this package's DDL uses. Every name reaching here
// comes from that constant, and an identifier is the part of an ALTER that cannot be a bound parameter.
func checkIdentifier(name string) error {
	if name == "" {
		return errors.New("empty identifier")
	}
	for i, r := range name {
		legal := r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' && i > 0
		if !legal {
			return fmt.Errorf("identifier %q is not a name this package writes", name)
		}
	}
	return nil
}

// applySchema runs one DDL string, adding what the file lacks, and the caller holds s.mu. Indexes run
// last, because an index naming a column this pass adds fails before that column exists.
func (s *Store) applySchema(ddl string) ([]string, error) {
	statements := splitStatements(ddl)
	specs, err := parseTables(statements)
	if err != nil {
		return nil, err
	}
	for _, stmt := range statements {
		if _, _, isTable := createTableOf(stmt); !isTable {
			continue
		}
		if _, err := s.db.Exec(stmt); err != nil {
			return nil, fmt.Errorf("create table: %w", err)
		}
	}

	added, err := s.addMissingColumns(specs)
	if err != nil {
		return nil, err
	}

	for _, stmt := range statements {
		if _, _, isTable := createTableOf(stmt); isTable {
			continue
		}
		if _, err := s.db.Exec(stmt); err != nil {
			return nil, fmt.Errorf("schema: %w", err)
		}
	}
	if err := s.checkColumns(specs); err != nil {
		return nil, err
	}
	return added, nil
}

// addMissingColumns adds the columns the DDL declares and the file lacks, and returns names such as
// "tasks.title_llm". A table this boot created already has them, so a new database runs no ALTER.
func (s *Store) addMissingColumns(specs []tableSpec) ([]string, error) {
	var added []string
	for _, spec := range specs {
		live, kind, err := s.liveColumns(spec.name)
		if err != nil {
			return nil, err
		}
		if kind != "table" {
			// The table was created moments ago, or the name holds something else. checkColumns refuses
			// the second case, because it can say what it found.
			continue
		}
		for _, column := range spec.columns {
			if slices.Contains(live, column.name) {
				continue
			}
			if _, err := s.db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`,
				quoteIdent(spec.name), column.name, column.declaration)); err != nil {
				return nil, fmt.Errorf("schema upgrade: adding %s.%s to an existing database: %w",
					spec.name, column.name, err)
			}
			added = append(added, spec.name+"."+column.name)
		}
	}
	return added, nil
}

// checkColumns confirms that the file now has every column the DDL declares. A column still missing after
// the ALTERs cannot be added by this mechanism, so the boot stops and names it.
func (s *Store) checkColumns(specs []tableSpec) error {
	for _, spec := range specs {
		live, kind, err := s.liveColumns(spec.name)
		if err != nil {
			return err
		}
		if kind != "table" {
			return fmt.Errorf("schema upgrade refused: sqlite_master calls %s %q where this release reads a table — "+
				"restore a backup from before this release or bring the file up by hand", spec.name, kind)
		}
		var missing []string
		for _, column := range spec.columns {
			if !slices.Contains(live, column.name) {
				missing = append(missing, column.name)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("schema upgrade refused: %s has no %s, and this release needs it — "+
				"restore a backup from before this release or bring the file up by hand", spec.name, strings.Join(missing, ", "))
		}
	}
	return nil
}

// liveColumns returns a table's columns as the file has them, and what sqlite_master holds under that
// name: "table", another kind, or "" when the name is absent.
func (s *Store) liveColumns(table string) (columns []string, kind string, err error) {
	err = s.db.QueryRow(`SELECT type FROM sqlite_master WHERE name = ?`, table).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("schema: looking for %s: %w", table, err)
	}
	if kind != "table" {
		return nil, kind, nil
	}

	// A PRAGMA takes no bound parameter, so the name goes into the statement. Every name reaching here is
	// one this package declared, which is what checkIdentifier keeps true.
	pragma, err := s.db.Query(`PRAGMA table_info(` + quoteIdent(table) + `)`)
	if err != nil {
		return nil, kind, fmt.Errorf("schema: reading %s: %w", table, err)
	}
	defer func() { _ = pragma.Close() }()

	for pragma.Next() {
		var (
			cid      int
			column   string
			declared string
			notNull  int
			pk       int
			def      sql.NullString
		)
		if err := pragma.Scan(&cid, &column, &declared, &notNull, &def, &pk); err != nil {
			return nil, kind, fmt.Errorf("schema: reading %s: %w", table, err)
		}
		columns = append(columns, column)
	}
	if err := pragma.Err(); err != nil {
		return nil, kind, fmt.Errorf("schema: reading %s: %w", table, err)
	}
	return columns, kind, nil
}

// quoteIdent writes an identifier the way SQLite reads it. A doubled quote is the only escape, and no
// identifier this package declares needs one.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
