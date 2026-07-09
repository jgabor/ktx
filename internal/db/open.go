// Package db owns the ktx database: path resolution, ID generation, and
// the on-disk SQLite schema.
//
// The database is a single SQLite file with five tables
// (the core schema, the bench_runs table, the compressed table, and
// the recorded_streams table):
// sessions (aggregate, INSERT on open + UPDATE roll-up),
// tool_calls (append-only, INSERT only), bench_runs (the benchmark
// headline store; one row per `ktx bench run` invocation; same
// nanoid-keyed shape as the other tables), compressed (the
// reversibility layer; one row per `ktx compress` invocation;
// references tool_calls.id via source_id FK), and recorded_streams (the
// recording contract; one row per `ktx bench run` invocation, or per
// opt-in `ktx run --record-stream`; holds the verbatim JSONL stream the
// agent emitted — the authoritative cost evidence; references
// sessions.id via a NOT NULL FK and bench_runs.id via a NULLABLE FK so a
// ktx-run-captured stream can carry no bench parent). IDs are 16-char
// lowercase alpha nanoids.
// Content hashes are SHA-256 of the output field, stored as a separate
// column for verification without being the primary key.
//
// The compressed table is the reversibility layer: it
// holds compressed artifacts SEPARATE from tool_calls (which the bench
// cost-aggregates — injecting zero-cost non-evidence rows would
// corrupt the cost-summed evidence set). compressed.source_id
// REFERENCES tool_calls(id); with foreign_keys=1 enforced by Open's
// DSN, a compressed row cannot point at a non-existent tool_call.
// `ktx decompress <id>` resolves source_id and returns the source's
// original output bytes verbatim — reversal via retained originals at
// the pipeline boundary, not per-stage inversion.
//
// Dependencies are pinned here so the choice is explicit:
//
//   - github.com/matoous/go-nanoid/v2 — 16-char lowercase alpha IDs
//   - modernc.org/sqlite              — pure-Go SQLite driver, no CGO
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	// Register the pure-Go SQLite driver as "sqlite". No CGO, matching the
	// vision's "no new runtimes" principle. Open connects via database/sql
	// with this driver.
	_ "modernc.org/sqlite"
)

// ID alphabet and length per Decision 3: 16-char lowercase alpha, ~75 bits
// entropy, no ambiguous characters (0/O, 1/l).
const (
	idAlphabet = "abcdefghijklmnopqrstuvwxyz"
	idLength   = 16
)

// sqliteDriver is the registered name of the modernc.org/sqlite driver.
const sqliteDriver = "sqlite"

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
func DefaultPath() (string, error) {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "ktx", "ktx.db"), nil
}

// ResolvePath applies the database path precedence:
//
//  1. override (the --path flag value, empty when absent)
//  2. KTX_DB environment variable
//  3. DefaultPath ($XDG_DATA_HOME/ktx/ktx.db)
//
// The --path flag is per-invocation only; nothing is persisted.
func ResolvePath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if envPath := os.Getenv("KTX_DB"); envPath != "" {
		return envPath, nil
	}
	return DefaultPath()
}

// IDPattern is the canonical regex for ktx IDs. Exported so
// the cli layer can validate sampled IDs without re-encoding the
// alphabet/length contract. Lives next to the schema / open helpers
// because the pattern is the on-the-wire contract for the IDs the
// schema and the open path produce together.
var IDPattern = regexp.MustCompile(`^[a-z]{16}$`)

// Open opens (or creates) the ktx database at path and ensures the schema is
// migrated. The parent directory is created if missing. Foreign
// keys are enforced so the tool_calls.session_id REFERENCES clause is
// meaningful, not decorative.
//
// The schema migration is idempotent: CREATE TABLE IF NOT EXISTS runs on
// every open, so re-opening an existing database is a no-op. The
// in-place cost-publication migration (ALTER TABLE ADD COLUMN for tokens_in/cost/etc.) is
// equally idempotent — the column-existence check via PRAGMA table_info
// makes a second run on a fully migrated database a no-op, and a
// partial-set database (e.g. only tokens_in added) is forward-fixed on
// the next open. No versioned migration framework is needed for the
// additive cost-publication column set (SQLite ALTER TABLE, no custom
// framework).
func Open(path string) (*DB, error) {
	if path == "" {
		return nil, errors.New("db: open: path is empty")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("db: open: create directory for %q: %w", path, err)
	}

	// modernc.org/sqlite reads _pragma query params and applies them per
	// connection. foreign_keys(1) enforces the tool_calls -> sessions FK on
	// every pooled connection, not just the first.
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"

	db, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open %q: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("db: open %q: %w", path, err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("db: migrate %q: %w", path, err)
	}

	return &DB{db: db}, nil
}

// OpenWithoutMigration wraps an externally-managed *sql.DB in a *DB
// without applying the schema migration. The seam exists for two
// use cases:
//
//   - Test fixtures that need to construct a database with a specific
//     schema (e.g. an unmigrated fixture missing bench_runs, or a partial
//     cost-publication fixture missing one additive column). The dispatch layer's
//     doctor tests exercise this path: opening a raw *sql.DB with
//     core + cost-publication columns but no bench_runs table, wrapping it in *DB
//     without a migration, and asserting the doctor reports the
//     bench_runs check as a warning. Running Open would create
//     bench_runs via CREATE TABLE IF NOT EXISTS and defeat the
//     fixture; OpenWithoutMigration preserves the caller's schema
//     verbatim.
//
//   - Future production code paths that need to read or write against
//     a database whose schema is already established by an out-of-band
//     process (a sidecar migration tool, a separately-managed
//     connection pool, a benchmark harness pre-creating the
//     schema). The seam gives those callers a clean entry point
//     without exposing the *DB struct's unexported field via
//     reflection or unsafe.
//
// The caller owns three responsibilities that Open handles
// implicitly:
//
//  1. The *sql.DB connection lifecycle: OpenWithoutMigration does
//     not call sql.Open or Close. The returned *DB's Close closes
//     the underlying pool — the caller should not also close the
//     sql.DB handle.
//  2. The foreign_keys pragma. Open applies _pragma=foreign_keys(1)
//     via the DSN so the tool_calls -> sessions FK is enforced on
//     every pooled connection. OpenWithoutMigration does not
//     override the caller's DSN; if the caller's DSN did not set
//     the pragma, the FK is not enforced. Tests that depend on FK
//     enforcement should use `file::memory:?_pragma=foreign_keys(1)`
//     or run `PRAGMA foreign_keys = ON` on the connection before
//     wrapping.
//  3. Schema validity. The returned *DB does not check that the
//     schema satisfies the core + cost-publication + bench_runs contract; methods that read
//     columns (PersistToolCall, GetToolCall, HasBenchRunsTable,
//     etc.) surface query errors against an incompatible schema as
//     ordinary errors.
//
// OpenWithoutMigration does not touch the filesystem and does not
// require a path; the schema lives entirely in the caller's
// *sql.DB. The function is exported (production-reusable) rather
// than build-tag-scoped because the seam is small, the contract is
// documented, and future deepening work (D-side reorganisations,
// the db file re-cut) can adopt it without an internal fork.
func OpenWithoutMigration(sqlDB *sql.DB) (*DB, error) {
	if sqlDB == nil {
		return nil, errors.New("db: open without migration: sqlDB is nil")
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("db: open without migration: ping: %w", err)
	}
	return &DB{db: sqlDB}, nil
}

// OpenMemory returns a *DB backed by an in-memory SQLite database with the
// full schema migrated and foreign_keys=1 enforced. The seam exists for
// tests (and any future in-process tooling) that need a real ktx DB
// without touching the filesystem: no path resolution, no MkdirAll, no
// XDG_DATA_HOME probing. The returned *DB has the same surface as Open
// (Close, PersistToolCall, GetRecord, etc.) so tests can exercise the
// full persistence path with bytes.Buffer / httptest isolation rather
// than a t.TempDir() SQLite file.
//
// Each call returns a fresh database: the modernc.org/sqlite driver
// treats `file::memory:?cache=shared` as connection-scoped, so two
// OpenMemory calls do not share state. The DSN's _pragma=foreign_keys(1)
// applies per connection, so foreign_keys=1 holds on every pooled
// connection — the same FK guarantee Open provides on file-backed DBs.
//
// Production code paths do not use this seam; Open remains the only
// way ktx opens the user's database on disk.
func OpenMemory() (*DB, error) {
	dsn := "file::memory:?_pragma=foreign_keys(1)"

	db, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open memory: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("db: open memory: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("db: migrate memory: %w", err)
	}

	return &DB{db: db}, nil
}

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

// DB is a handle to the ktx database. The underlying *sql.DB is unexported
// because callers reach the database through methods, not by reaching into
// the handle.
type DB struct {
	db *sql.DB
}

// Close closes the database handle, releasing the underlying SQLite
// connection pool. It is safe to call on a nil *DB.
func (d *DB) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}

// DB returns the underlying *sql.DB handle. Production callers should
// prefer the typed methods on *DB (GetToolCall, PersistToolCall,
// InsertCompressed, …); this escape hatch exists for failure-mode
// tests that need to simulate out-of-band corruption (e.g. deleting a
// source tool_call row with FKs temporarily disabled to verify the
// compressed-row not-found path — a tool_calls INSERT-only contract violation simulation). The
// returned handle is the live pool; callers must not Close it (use
// Close on the enclosing *DB instead).
func (d *DB) DB() *sql.DB {
	if d == nil {
		return nil
	}
	return d.db
}
