package db

import (
	"database/sql"
	"fmt"
)

// schemaStatements creates the ktx tables plus the
// bench_runs table, the compressed table,
// and the recorded_streams table. The statements are idempotent
// (CREATE TABLE IF NOT EXISTS), so Open runs them on every call without
// a migration framework; a versioned migration layer is YAGNI for the
// five-table schema.
//
// Column definitions match the task contract exactly: NOT NULL and DEFAULT
// appear only where the schema specifies them (session_id, tool_call_count,
// event_type). The tool_calls.session_id REFERENCES sessions(id) clause is
// enforced because Open enables foreign_keys per connection.
//
// The cost-publication columns (tokens_in, tokens_out, cost, latency_ms,
// model, provider) are part of the CREATE TABLE so fresh databases include
// them from the start; older databases pick them up via the in-place
// ALTER TABLE step in migrate(). These columns are nullable and have no
// DEFAULT — NULL is the proper "field absent" marker so cost reconstruction
// can distinguish "caller did not supply" from "caller supplied zero".
//
// The bench_runs table stores one row per `ktx bench run`
// invocation. Columns mirror the methodology_version contract
// (`methodology_version TEXT` references `tools/bench/methodology-v1.yaml`)
// and the reconstructability inputs (tokens_in + tokens_out + cost +
// model). `id TEXT PRIMARY KEY` matches the nanoid-keyed shape used by
// sessions and tool_calls. `timestamp INTEGER` is Unix epoch
// seconds (the integer variant matches the reproducibility contract:
// "a third party reading this file plus the persisted `bench_runs` row
// can compute `cost_per_correct` exactly"). Every column is nullable
// except `id` — the bench row is the persisted record, not a normalized
// accounting row; NULL preserves "field not yet supplied by the runner"
// without coercing zero values.
//
// The recorded_streams table stores one row per `ktx bench run`
// invocation (paired 1:1 with a bench_runs row), or per opt-in
// `ktx run --record-stream` (in which case bench_run_id is NULL — the
// stream was captured outside a bench context, so no bench_runs row
// exists to reference). It holds the verbatim JSONL stream the agent
// emitted (the authoritative cost evidence — OpenCode 1.17.8
// emits tokens/cost/model on step_finish events that the wrapper pipes
// through stdout but does NOT persist as tool_call rows). session_id is
// NOT NULL FK; bench_run_id is a NULLABLE FK so the ktx-run path can
// persist a stream without fabricating a bench_runs parent. The table
// is the first persistent session-to-bench_run linkage (bench_runs has
// no session_id column). No ALTER TABLE column add — the CREATE TABLE
// IF NOT EXISTS alone handles the fresh-DB and unmigrated self-heal paths
// (resolved judgment call: no partial-column-forward-fix ALTER loop,
// per the literal reading of the "no ALTER TABLE" clause
// for recorded_streams).
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS sessions (
		id              TEXT PRIMARY KEY,
		agent           TEXT,
		started_at      TEXT,
		ended_at        TEXT,
		last_seen_at    TEXT,
		tool_call_count INTEGER DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS tool_calls (
		id           TEXT PRIMARY KEY,
		session_id   TEXT NOT NULL REFERENCES sessions(id),
		tool         TEXT,
		input        TEXT,
		output       TEXT,
		content_hash TEXT,
		timestamp    TEXT,
		event_type   TEXT DEFAULT 'tool_call',
		tokens_in    INTEGER,
		tokens_out   INTEGER,
		cost         REAL,
		latency_ms   INTEGER,
		model        TEXT,
		provider     TEXT,
		exit_code    INTEGER,
		is_error     INTEGER
	)`,
	`CREATE TABLE IF NOT EXISTS bench_runs (
		id                  TEXT PRIMARY KEY,
		agent               TEXT,
		model               TEXT,
		task                TEXT,
		score               REAL,
		tokens_in           INTEGER,
		tokens_out          INTEGER,
		cost                REAL,
		methodology_version TEXT,
		timestamp           INTEGER
	)`,
	// compressed (the reversibility layer): one row per `ktx compress`
	// invocation. Holds compressed artifacts SEPARATE from tool_calls
	// (bench cost-aggregates tool_calls; injecting zero-cost
	// non-evidence rows would corrupt the cost-summed evidence set).
	// source_id REFERENCES tool_calls(id); foreign_keys=1 (enforced by
	// Open's DSN) makes the FK meaningful. pipeline is a JSON array of
	// stage names that ran ("a record of what stages ran") — JSON
	// is inspectable and trivially stored as TEXT. The 6-column shape
	// mirrors the acceptance contract: id, source_id, output,
	// content_hash, pipeline, timestamp.
	`CREATE TABLE IF NOT EXISTS compressed (
		id           TEXT PRIMARY KEY,
		source_id    TEXT NOT NULL REFERENCES tool_calls(id),
		output       TEXT,
		content_hash TEXT,
		pipeline     TEXT,
		timestamp    TEXT
	)`,
	// compressed_source_id_idx accelerates `ktx decompress <id>`'s
	// source_id lookup (the reversal primitive GetSourceOutput). The
	// index is created idempotently alongside the table; re-open is a
	// no-op via CREATE INDEX IF NOT EXISTS.
	`CREATE INDEX IF NOT EXISTS compressed_source_id_idx ON compressed(source_id)`,
	// recorded_streams (the recording contract): one row per captured
	// `ktx bench run` invocation (or `ktx run --record-stream`, which
	// opts in to the same recording without a bench_runs parent). Holds
	// the verbatim JSONL stream the agent emitted (the authoritative
	// cost evidence — OpenCode 1.17.8 emits tokens/cost/model
	// on step_finish events that the wrapper pipes through stdout but
	// does NOT persist as tool_call rows). session_id is NOT NULL FK;
	// bench_run_id is a NULLABLE FK (NULL when the stream was captured
	// via `ktx run --record-stream` outside a bench context, so no
	// bench_runs row exists to reference). The table is the first
	// persistent session-to-bench_run linkage (bench_runs has no
	// session_id column). The 7-column shape follows the compressed
	// table template: id (nanoid), session_id FK, bench_run_id FK
	// (nullable), stream_text (the verbatim JSONL), content_hash
	// (SHA-256 of stream_text), stream_format (metadata, e.g.
	// 'opencode-jsonl-1'), captured_at (RFC3339Nano). Default capture
	// trigger is `ktx bench run` (runBenchRun); `ktx run` is a
	// transparent proxy by default and only persists the stream when
	// --record-stream is explicitly passed (opt-in, off by default to
	// honour the bench-run-only rule for the default case).
	`CREATE TABLE IF NOT EXISTS recorded_streams (
		id           TEXT PRIMARY KEY,
		session_id   TEXT NOT NULL REFERENCES sessions(id),
		bench_run_id TEXT REFERENCES bench_runs(id),
		stream_text  TEXT,
		content_hash TEXT,
		stream_format TEXT,
		captured_at  TEXT
	)`,
	// recorded_streams_session_id_idx accelerates the --replay
	// <session-id> lookup (the public --replay arg).
	`CREATE INDEX IF NOT EXISTS recorded_streams_session_id_idx ON recorded_streams(session_id)`,
	// recorded_streams_bench_run_id_idx accelerates the internal FK
	// JOIN to bench_runs (used by --replay to load the original
	// comparison row).
	`CREATE INDEX IF NOT EXISTS recorded_streams_bench_run_id_idx ON recorded_streams(bench_run_id)`,
}

// costColumn enumerates a single cost-publication column on tool_calls.
// The list is the single source of truth for the migration: it drives both
// the columns added to the CREATE TABLE statement (above) and the
// ALTER TABLE ADD COLUMN step in migrate() for older databases.
//
// Order matters only for deterministic output (doctor reports missing
// columns in the order they appear here). Types match the flat-JSON
// payload so callers can persist numbers and strings without a SQLite
// type-coercion surprise.
type costColumn struct {
	Name string
	Type string
}

// costColumns are the cost-publication columns on tool_calls. Each entry
// pairs the column name with its SQL type for the ALTER TABLE ADD COLUMN
// statement. Idempotency is handled by checking PRAGMA table_info before
// altering — SQLite has no IF NOT EXISTS for ADD COLUMN, so a column
// existence check is the only pure-Go mechanism. PRAGMA table_info runs
// inside the migration transaction without issue; the query is read-only
// and the connection's schema view is consistent within a transaction.
var costColumns = []costColumn{
	{Name: "tokens_in", Type: "INTEGER"},
	{Name: "tokens_out", Type: "INTEGER"},
	{Name: "cost", Type: "REAL"},
	{Name: "latency_ms", Type: "INTEGER"},
	{Name: "model", Type: "TEXT"},
	{Name: "provider", Type: "TEXT"},
}

// failureSignalColumns are the failure-signal columns on
// tool_calls. exit_code is diagnostic; is_error is the
// canonical gating signal stored as INTEGER 0/1/NULL and converted to
// *bool in GetToolCall. Idempotency mirrors costColumns.
var failureSignalColumns = []costColumn{
	{Name: "exit_code", Type: "INTEGER"},
	{Name: "is_error", Type: "INTEGER"},
}

// benchRunsColumn enumerates a single bench_runs column. The list is the
// single source of truth for the migration: it drives the columns in the
// CREATE TABLE statement (above) and the ALTER TABLE ADD COLUMN step in
// migrate() for older databases (or partial bench_runs schemas). Order
// matches the CREATE TABLE column order so doctor reports missing columns
// in the order they appear in the schema.
//
// `id TEXT PRIMARY KEY` is in this list for completeness — the CREATE
// TABLE establishes the PRIMARY KEY, and the ALTER loop only adds `id`
// when a partial bench_runs is missing the column entirely. SQLite's
// ALTER TABLE ADD COLUMN cannot add a PRIMARY KEY constraint; in that
// pathological case the added `id TEXT` is a non-PK column, which is
// degraded but functional. The realistic forward-fix case is "table
// exists, some non-PK columns missing", where `id` is already present
// and the ALTER loop skips it.
type benchRunsColumn struct {
	Name string
	Type string
}

// benchRunsColumns are the bench_runs columns. The list covers all 10
// columns per the reproducibility contract: `id` (nanoid), `agent`,
// `model`, `task`, `score`, `tokens_in`, `tokens_out`, `cost`,
// `methodology_version` (the methodology column), `timestamp` (Unix epoch
// seconds). Idempotency is handled identically to costColumns — PRAGMA
// table_info is consulted before ALTER TABLE ADD COLUMN, so a re-open
// of a fully migrated database is a no-op and a partial bench_runs
// schema is forward-fixed on the next open.
var benchRunsColumns = []benchRunsColumn{
	{Name: "id", Type: "TEXT"},
	{Name: "agent", Type: "TEXT"},
	{Name: "model", Type: "TEXT"},
	{Name: "task", Type: "TEXT"},
	{Name: "score", Type: "REAL"},
	{Name: "tokens_in", Type: "INTEGER"},
	{Name: "tokens_out", Type: "INTEGER"},
	{Name: "cost", Type: "REAL"},
	{Name: "methodology_version", Type: "TEXT"},
	{Name: "timestamp", Type: "INTEGER"},
}

// compressedColumn enumerates a single `compressed` table column. The
// list drives both the CREATE TABLE above and the ALTER TABLE ADD
// COLUMN loop in migrate() for older databases (or partial compressed
// schemas). Order matches the CREATE TABLE column order so doctor and
// the partial-forward-fix path report missing columns in the order
// they appear in the schema.
//
// `id TEXT PRIMARY KEY` is in this list for completeness — the CREATE
// TABLE establishes the PRIMARY KEY, and the ALTER loop only adds `id`
// when a partial compressed is missing the column entirely. SQLite's
// ALTER TABLE ADD COLUMN cannot add a PRIMARY KEY constraint; in the
// pathological case the added `id TEXT` is a non-PK column, degraded
// but functional. The realistic forward-fix case is "table exists,
// some non-PK columns missing", where `id` is already present and the
// ALTER loop skips it — same contract as benchRunsColumns.
type compressedColumn struct {
	Name string
	Type string
}

// compressedColumns are the 6 columns of the `compressed` table:
// id (nanoid), source_id (FK → tool_calls.id),
// output (the compressed bytes), content_hash (SHA-256 of compressed
// output, recomputed per the integrity rule), pipeline (JSON array of
// stage names that ran), timestamp. Idempotency mirrors costColumns and
// benchRunsColumns — PRAGMA table_info is consulted before ALTER TABLE
// ADD COLUMN, so a re-open of a fully migrated database is a no-op
// and a partial compressed schema is forward-fixed on the next open.
var compressedColumns = []compressedColumn{
	{Name: "id", Type: "TEXT"},
	{Name: "source_id", Type: "TEXT"},
	{Name: "output", Type: "TEXT"},
	{Name: "content_hash", Type: "TEXT"},
	{Name: "pipeline", Type: "TEXT"},
	{Name: "timestamp", Type: "TEXT"},
}

// DefaultPath resolves the default database path:
// $XDG_DATA_HOME/ktx/ktx.db. When XDG_DATA_HOME is unset, it falls back to
// ~/.local/share/ktx/ktx.db (XDG base directory specification).

// tableColumns returns the set of column names for table. Empty when
// the table does not exist (PRAGMA table_info returns zero rows for a
// missing table). The error path is reserved for genuine query/scan
// failures — the "table absent" case is reported as an empty set so the
// caller can compose a single, clear error message.
//
// The queryer parameter accepts *sql.DB (read-only callers like doctor)
// or *sql.Tx (the migrate path, which needs the in-transaction schema
// view to decide which ALTER TABLE statements to emit). The interface
// lives in inspect.go so the inspect surface owns its own contract —
// open.go just borrows the seam for the migration transaction.
func tableColumns(q queryer, table string) (map[string]struct{}, error) {
	rows, err := q.Query("PRAGMA table_info(" + table + ")")
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

// migrate runs the schema statements and the cost-publication in-place column migration
// inside a single transaction so the database is never left with a partial
// schema. Fresh databases get the cost-publication columns from the CREATE TABLE itself;
// older databases pick them up via ALTER TABLE ADD COLUMN. The same
// transaction also forward-fixes a partial set: any cost-publication column absent
// from tool_calls is added; any cost-publication column already present is skipped. A
// re-open of a fully migrated database is therefore a pure no-op (all
// PRAGMA checks find the column, no ALTER runs).
//
// The bench_runs migration follows the same pattern: the
// CREATE TABLE statement above establishes the 10-column table on a
// fresh database or an older database (no bench_runs at all); the
// ALTER TABLE ADD COLUMN loop below forward-fixes a partial bench_runs
// schema on the next open (e.g. a buggy prior migration created the
// table with only some columns). The cost-publication and bench_runs column loops are
// independent — an older database gets the cost-publication columns added to
// tool_calls AND gets bench_runs created; a database that already had
// cost-publication columns but not bench_runs
// gets bench_runs created with no work on tool_calls.
//
// The compressed migration follows the same pattern
// for the reversibility-layer table: the CREATE TABLE statement above
// establishes the 6-column table on a fresh database or an older
// database (no compressed at all); the ALTER TABLE ADD COLUMN loop
// below forward-fixes a partial compressed schema on the next open.
// The compressed loop is independent of the cost-publication and bench_runs loops — an older
// database gets compressed created alongside whatever migration work the
// same migrate call performs. A re-open of a fully migrated database
// is a pure no-op.
//
// The recorded_streams migration follows the leaner pattern: the
// CREATE TABLE IF NOT EXISTS statement above establishes the 7-column
// table on a fresh database or an older database (no recorded_streams
// at all). No ALTER TABLE ADD COLUMN loop — reading the
// "no ALTER TABLE" clause literally. The CREATE
// TABLE IF NOT EXISTS alone handles the fresh-DB and unmigrated self-heal
// paths; a partial-recorded_streams schema is too pathological to
// warrant defense-in-depth. A re-open of a fully migrated database
// is a pure no-op.
//
// The column-existence check uses PRAGMA table_info inside the migration
// transaction. PRAGMA table_info is a read query that returns one row per
// column on the connected schema view; running it inside a transaction is
// safe because the transaction has not yet altered the schema and the
// connection's schema cache is consistent within the transaction.
func migrate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after a successful Commit

	for _, stmt := range schemaStatements {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("db: create schema: %w", err)
		}
	}

	have, err := tableColumns(tx, "tool_calls")
	if err != nil {
		return fmt.Errorf("db: inspect tool_calls: %w", err)
	}
	for _, col := range costColumns {
		if _, ok := have[col.Name]; ok {
			continue
		}
		stmt := "ALTER TABLE tool_calls ADD COLUMN " + col.Name + " " + col.Type
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("db: add cost column %q: %w", col.Name, err)
		}
	}

	for _, col := range failureSignalColumns {
		if _, ok := have[col.Name]; ok {
			continue
		}
		stmt := "ALTER TABLE tool_calls ADD COLUMN " + col.Name + " " + col.Type
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("db: add failure-signal column %q: %w", col.Name, err)
		}
	}

	haveBR, err := tableColumns(tx, "bench_runs")
	if err != nil {
		return fmt.Errorf("db: inspect bench_runs: %w", err)
	}
	for _, col := range benchRunsColumns {
		if _, ok := haveBR[col.Name]; ok {
			continue
		}
		stmt := "ALTER TABLE bench_runs ADD COLUMN " + col.Name + " " + col.Type
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("db: add bench_runs column %q: %w", col.Name, err)
		}
	}

	haveComp, err := tableColumns(tx, "compressed")
	if err != nil {
		return fmt.Errorf("db: inspect compressed: %w", err)
	}
	for _, col := range compressedColumns {
		if _, ok := haveComp[col.Name]; ok {
			continue
		}
		stmt := "ALTER TABLE compressed ADD COLUMN " + col.Name + " " + col.Type
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("db: add compressed column %q: %w", col.Name, err)
		}
	}

	// recorded_streams bench_run_id NULL relaxation (post-recording-schema): the
	// original recorded_streams schema created bench_run_id as NOT NULL. The
	// --record-stream feature (ktx run) needs to persist streams
	// without a bench_runs parent (NULL bench_run_id). New databases
	// get the nullable column from the CREATE TABLE above; existing
	// databases with the old NOT NULL constraint need a table rebuild.
	// SQLite has no ALTER COLUMN; the standard approach is rename +
	// create + copy + drop.
	haveRS, err := tableColumns(tx, "recorded_streams")
	if err != nil {
		return fmt.Errorf("db: inspect recorded_streams: %w", err)
	}
	if _, ok := haveRS["bench_run_id"]; ok {
		// Check if bench_run_id is NOT NULL by inspecting PRAGMA
		// table_info. The notnull field is column index 3 in the
		// PRAGMA output (cid, name, type, notnull, dflt_value, pk).
		var notNull int
		err := tx.QueryRow(`SELECT "notnull" FROM pragma_table_info('recorded_streams') WHERE name = 'bench_run_id'`).Scan(&notNull)
		if err == nil && notNull == 1 {
			// Rebuild the table with a nullable bench_run_id.
			// SQLite's table rebuild: rename old, create new, copy,
			// drop old. Done inside the migration transaction.
			rebuild := []string{
				`ALTER TABLE recorded_streams RENAME TO recorded_streams_old`,
				`CREATE TABLE recorded_streams (
					id           TEXT PRIMARY KEY,
					session_id   TEXT NOT NULL REFERENCES sessions(id),
					bench_run_id TEXT REFERENCES bench_runs(id),
					stream_text  TEXT,
					content_hash TEXT,
					stream_format TEXT,
					captured_at  TEXT
				)`,
				`INSERT INTO recorded_streams (id, session_id, bench_run_id, stream_text, content_hash, stream_format, captured_at)
				 SELECT id, session_id, bench_run_id, stream_text, content_hash, stream_format, captured_at FROM recorded_streams_old`,
				`DROP TABLE recorded_streams_old`,
				`CREATE INDEX IF NOT EXISTS recorded_streams_session_id_idx ON recorded_streams(session_id)`,
				`CREATE INDEX IF NOT EXISTS recorded_streams_bench_run_id_idx ON recorded_streams(bench_run_id)`,
			}
			for _, stmt := range rebuild {
				if _, err := tx.Exec(stmt); err != nil {
					return fmt.Errorf("db: rebuild recorded_streams for nullable bench_run_id: %w", err)
				}
			}
		}
	}

	return tx.Commit()
}
