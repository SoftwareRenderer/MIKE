package store

import "database/sql"

// The list-query loop every store file would otherwise repeat. What an ad-hoc copy tends
// to lose is rows.Err(): a truncated result set is a failed query, not a short list, and
// the two look identical to the caller. That matters most for the ID lists the
// worktree/branch GC reads — a row missing from "which tasks still exist" reads as
// "nothing owns this branch", and the GC deletes it.
//
// Both helpers close rows, which also releases the read cursor before the caller's next
// statement on the single pooled connection.

// scanRows collects one value per row using scan. An empty result is an empty, non-nil
// slice, so callers marshal [] rather than null.
func scanRows[T any](rows *sql.Rows, scan func(*sql.Rows) (T, error)) ([]T, error) {
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	// Never return the partial list beside the error: a caller that checks only
	// err == nil would act on a list with rows missing.
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// scanInto adapts a scanX(row, *T) helper to scanRows.
func scanInto[T any](scan func(scanner, *T) error) func(*sql.Rows) (T, error) {
	return func(rows *sql.Rows) (T, error) {
		var v T
		err := scan(rows, &v)
		return v, err
	}
}

// scanColumn collects a single-column result. A row that will not scan is an error, not
// a skipped row.
func scanColumn[T any](rows *sql.Rows) ([]T, error) {
	return scanRows(rows, func(r *sql.Rows) (T, error) {
		var v T
		err := r.Scan(&v)
		return v, err
	})
}
