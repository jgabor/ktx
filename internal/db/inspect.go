// Database inspection helpers for `ktx doctor` (Task 8).
//
// The doctor command is a read-only health check (Decision 2). The store
// exposes schema and ID-format probes here so the cli layer can render a
// structured report without reaching into the unexported *sql.DB. Each
// probe returns a plain error (or nil) so the cli can format the detail
// without coupling to a specific error type.
//
// These helpers are read-only — they query but never mutate. `db.Open`
// is the one place that creates the database file and runs migrations;
// doctor only invokes Open after a pre-flight existence check in cli,
// so a missing database is reported rather than silently created.
//
// M1 vs M2 schema checks (Task 2): the M1 columns are a hard requirement
// for `ktx doctor` (a missing M1 column means the database cannot be
// loaded), while the M2 columns are a soft check (a missing M2 column
// means the database is pre-M2; M1 records still load, M2 fields just
// stay NULL on insert). The split keeps the doctor exit code honest: a
// pre-M2 database is a "warning" / "degraded" state, not a hard error.
//
// M3 bench_runs check (plan Task 3): the bench_runs table is a soft
// requirement for `ktx doctor` (a missing bench_runs table means the
// database is pre-M3; M1/M2 records are still queryable). The doctor
// reports a pre-M3 database as a normal-severity warning — degraded,
// not error — and the next ktx invocation applies the in-place CREATE
// / ALTER migration. A partial bench_runs schema (some columns missing)
// is also a warning: the forward-fix path in migrate() adds the missing
// columns on the next open.
package db

import (
	"database/sql"
	"fmt"
	"sort"
)

// queryer is the minimal interface satisfied by both *sql.DB and *sql.Tx.
// It lets tableColumns run inside or outside a transaction — the migrate
// path inspects the schema inside the migration transaction (so the
// existence check sees the not-yet-committed state), while the doctor
// path inspects outside any transaction (read-only).
type queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// expectedSchema mirrors the M1 two-table schema from Decision 7. Each
// table lists the columns ktx requires for M1 record I/O to function;
// the doctor schema check verifies every expected column is present.
// Extra columns are tolerated (forward-compatible with M2 cost fields).
//
// The M2 columns live in m2Schema below; they are checked separately by
// HasM2Columns so a pre-M2 database is reported as a warning rather than
// a hard error. See CheckSchema / HasM2Columns.
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

// m2Schema is the M2 cost-publication column set added to tool_calls by
// the in-place ALTER TABLE migration. HasM2Columns checks for these
// columns specifically so the doctor can report a missing set as a
// soft warning. The order matches m2Columns in db.go so doctor
// output is deterministic.
var m2Schema = []string{
	"tokens_in",
	"tokens_out",
	"cost",
	"latency_ms",
	"model",
	"provider",
}

// benchRunsSchema is the expected bench_runs column set per D12's
// reproducibility contract. HasBenchRunsColumns checks for these
// columns specifically so the doctor can report a partial bench_runs
// schema as a soft warning (forward-fix on next open, not a hard
// error). The order matches benchRunsColumns in db.go so doctor
// output is deterministic.
var benchRunsSchema = []string{
	"id",
	"agent",
	"model",
	"task",
	"score",
	"tokens_in",
	"tokens_out",
	"cost",
	"methodology_version",
	"timestamp",
}

// CheckSchema verifies the database schema matches the M1 core of
// Decision 7: the `sessions` and `tool_calls` tables exist and carry the
// expected columns. Extra columns are ignored (forward-compatible with
// M2 cost fields). It returns nil on a healthy M1 schema, or an error
// naming the first missing table or column. Used by `ktx doctor` for
// the hard schema requirement.
//
// M2 columns are intentionally NOT checked here — see HasM2Columns for
// the soft (warning-level) check that the doctor invokes separately.
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

// HasM2Columns reports whether the tool_calls table carries every M2
// cost-publication column. Returns (true, nil, nil) when every m2Schema
// column is present, (false, [missing], nil) when one or more are
// absent, and a non-nil error only when the table_info query itself
// fails (e.g. the table is missing — which is a separate error from a
// missing column and belongs in the M1 hard check).
//
// The boolean drives the doctor's m2_columns check status (ok / warning);
// the missing slice drives the detail string (lists the absent columns
// by name so the user can verify the migration ran, or hand-fix a
// partial set before the next open).
func (d *DB) HasM2Columns() (bool, []string, error) {
	have, err := tableColumns(d.db, "tool_calls")
	if err != nil {
		return false, nil, fmt.Errorf("inspect tool_calls: %w", err)
	}
	var missing []string
	for _, want := range m2Schema {
		if _, ok := have[want]; !ok {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return false, missing, nil
	}
	return true, nil, nil
}

// HasBenchRunsTable reports whether the `bench_runs` table exists in the
// schema. Returns (true, nil) when sqlite_master lists a table with that
// name, (false, nil) when absent, and (false, err) when the query itself
// fails (a corrupt schema view or an unreadable database).
//
// Unlike HasM2Columns, this is a table-level probe rather than a
// column-level probe. The bench_runs table is created at the M3
// milestone; a pre-M3 database has no such table. The doctor uses this
// probe to distinguish three states:
//
//   - Table present + all 10 columns present → "ok"
//   - Table present + some columns missing → "warning" (forward-fix on next open)
//   - Table absent entirely → "warning" (pre-M3 database; M1/M2 records
//     still queryable, the migration creates the table on next open)
//
// The split matters because the three states have different remediation
// messages: forward-fix is automatic on next open for a partial table,
// while a missing table is a pre-M3 marker that disappears on the very
// next ktx invocation that opens the database.
func (d *DB) HasBenchRunsTable() (bool, error) {
	var name string
	err := d.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='bench_runs'`,
	).Scan(&name)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect sqlite_master for bench_runs: %w", err)
	}
	return name == "bench_runs", nil
}

// HasBenchRunsColumns reports whether the bench_runs table carries every
// expected column per D12's reproducibility contract. Returns
// ([], nil) when every benchRunsSchema column is present, ([missing], nil)
// when one or more are absent, and a non-nil error when the table_info
// query fails (e.g. the table is missing — which is a separate signal
// surfaced by HasBenchRunsTable, not conflated here).
//
// The missing slice drives the doctor detail string: it lists the
// absent columns by name so the user can verify the migration ran, or
// hand-fix a partial set before the next open. An empty slice means
// every expected column is present.
func (d *DB) HasBenchRunsColumns() (missing []string, err error) {
	have, err := tableColumns(d.db, "bench_runs")
	if err != nil {
		return nil, fmt.Errorf("inspect bench_runs: %w", err)
	}
	for _, want := range benchRunsSchema {
		if _, ok := have[want]; !ok {
			missing = append(missing, want)
		}
	}
	return missing, nil
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
