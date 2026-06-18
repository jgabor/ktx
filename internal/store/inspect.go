// Database inspection helpers for `ktx doctor` (Task 8).
//
// The doctor command is a read-only health check (Decision 2). The store
// exposes schema and ID-format probes here so the cli layer can render a
// structured report without reaching into the unexported *sql.DB. Each
// probe returns a plain error (or nil) so the cli can format the detail
// without coupling to a specific error type.
//
// These helpers are read-only — they query but never mutate. `store.Open`
// is the one place that creates the database file and runs migrations;
// doctor only invokes Open after a pre-flight existence check in cli,
// so a missing database is reported rather than silently created.
package store

import (
	"database/sql"
	"fmt"
	"regexp"
	"sort"
)

// expectedSchema mirrors the two-table schema from Decision 7. Each table
// lists the columns ktx expects to find; the doctor schema check verifies
// every expected column is present (extra columns are tolerated — a
// future M2 migration can add tokens_in/cost/etc. without breaking the
// doctor contract).
var expectedSchema = map[string][]string{
	"sessions": {
		"id",
		"agent",
		"started_at",
		"ended_at",
		"last_seen_at",
		"tool_call_count",
	},
	"tool_calls": {
		"id",
		"session_id",
		"tool",
		"input",
		"output",
		"content_hash",
		"timestamp",
		"event_type",
	},
}

// CheckSchema verifies the database schema matches Decision 7: the
// `sessions` and `tool_calls` tables exist and carry the expected
// columns. Extra columns are ignored (forward-compatible with M2 cost
// fields). It returns nil on a healthy schema, or an error naming the
// first missing table or column. Used by `ktx doctor`.
//
// Implementation notes:
//   - PRAGMA table_info(<table>) is the standard SQLite introspection.
//     It returns one row per column: cid, name, type, notnull, dflt_value, pk.
//   - The PRAGMA itself does not error when the table is absent — it
//     returns zero rows. We rely on that: an empty column set triggers
//     the "missing column" branch which is the right user-facing message.
//   - The columns slice is built in a single Scan loop to keep the
//     query footprint small. A 6-column table is 6 rows; iterating once
//     and comparing is O(n*m) at worst, but n and m are tiny and this
//     runs only on doctor invocation.
func (d *DB) CheckSchema() error {
	for table, wantCols := range expectedSchema {
		have, err := tableColumns(d.db, table)
		if err != nil {
			return fmt.Errorf("inspect table %q: %w", table, err)
		}
		for _, want := range wantCols {
			if _, ok := have[want]; !ok {
				return fmt.Errorf("table %q missing column %q (have %v)", table, want, sortedKeys(have))
			}
		}
	}
	return nil
}

// tableColumns returns the set of column names for table. Empty when
// the table does not exist (PRAGMA table_info returns zero rows for a
// missing table). The error path is reserved for genuine query/scan
// failures — the "table absent" case is reported as an empty set so the
// caller can compose a single, clear error message.
func tableColumns(db *sql.DB, table string) (map[string]struct{}, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, fmt.Errorf("pragma table_info: %w", err)
	}
	defer rows.Close()

	cols := map[string]struct{}{}
	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notnull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return nil, fmt.Errorf("scan column: %w", err)
		}
		cols[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate columns: %w", err)
	}
	return cols, nil
}

// SampleIDs returns up to limit IDs from each table, ordered as the
// rows are stored (insertion order in practice). Used by `ktx doctor`
// to validate that persisted IDs match Decision 3: 16-char lowercase
// alphabetic. Empty databases return empty slices with a nil error —
// the caller treats "no samples" as a vacuously-true ID format check.
//
// Sample size is bounded so the doctor invocation does not scan a large
// production database end-to-end. A 100-row sample catches corruption
// from a manual edit or schema mismatch quickly; the random case where
// only the 101st row is malformed is exceedingly rare and would surface
// on the next doctor invocation once records accumulate.
func (d *DB) SampleIDs(limit int) (sessions, toolCalls []string, err error) {
	sessions, err = selectIDs(d.db, "sessions", limit)
	if err != nil {
		return nil, nil, fmt.Errorf("sample sessions: %w", err)
	}
	toolCalls, err = selectIDs(d.db, "tool_calls", limit)
	if err != nil {
		return nil, nil, fmt.Errorf("sample tool_calls: %w", err)
	}
	return sessions, toolCalls, nil
}

// selectIDs reads up to limit IDs from table. Used by SampleIDs.
func selectIDs(db *sql.DB, table string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := db.Query("SELECT id FROM "+table+" LIMIT ?", limit)
	if err != nil {
		return nil, fmt.Errorf("select ids: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ids: %w", err)
	}
	return ids, nil
}

// IDPattern is the canonical regex for ktx IDs (Decision 3). Exported so
// the cli layer can validate sampled IDs without re-encoding the
// alphabet/length contract.
var IDPattern = regexp.MustCompile(`^[a-z]{16}$`)

// sortedKeys returns the keys of m in deterministic order. Used to
// format a clear "expected X, have Y" error message in CheckSchema.
func sortedKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
