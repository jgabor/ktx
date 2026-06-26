// Package db owns the ktx database: path resolution, ID generation, and
// the on-disk SQLite schema.
//
// The database is a single SQLite file (Decision 6) with four tables
// (Decision 7 + M3 plan Task 3 + M4 plan Task 1): sessions (aggregate,
// INSERT on open + UPDATE roll-up), tool_calls (append-only, INSERT
// only), bench_runs (M3 benchmark headline store; one row per `ktx
// bench run` invocation; same nanoid-keyed shape as the other tables),
// and compressed (M4 reversibility layer; one row per `ktx compress`
// invocation; references tool_calls.id via source_id FK). IDs are
// 16-char lowercase alpha nanoids (Decision 3). Content hashes are
// SHA-256 of the output field, stored as a separate column for
// verification without being the primary key (Decision 3).
//
// The compressed table is the reversibility layer (Decision 14): it
// holds compressed artifacts SEPARATE from tool_calls (which M3 bench
// cost-aggregates — injecting zero-cost non-evidence rows would
// corrupt the cost-summed evidence set). compressed.source_id
// REFERENCES tool_calls(id); with foreign_keys=1 enforced by Open's
// DSN, a compressed row cannot point at a non-existent tool_call.
// `ktx decompress <id>` resolves source_id and returns the source's
// original output bytes verbatim — reversal via retained originals at
// the pipeline boundary, not per-stage inversion (D14).
//
// Dependencies are pinned here so the choice is explicit:
//
//   - github.com/matoous/go-nanoid/v2 — 16-char lowercase alpha IDs (D3)
//   - modernc.org/sqlite              — pure-Go SQLite driver, no CGO (D6)
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

// schemaStatements creates the ktx tables per Decision 7 plus the M3
// bench_runs table (plan Task 3). The statements are idempotent (CREATE
// TABLE IF NOT EXISTS), so Open runs them on every call without a migration
// framework; a versioned migration layer is YAGNI for the three-table
// M1/M2/M3 schema.
//
// Column definitions match the task contract exactly: NOT NULL and DEFAULT
// appear only where Decision 7/9 specify them (session_id, tool_call_count,
// event_type). The tool_calls.session_id REFERENCES sessions(id) clause is
// enforced because Open enables foreign_keys per connection.
//
// The M2 cost-publication columns (tokens_in, tokens_out, cost, latency_ms,
// model, provider) are part of the CREATE TABLE so fresh databases include
// them from the start; pre-M2 databases pick them up via the in-place
// ALTER TABLE step in migrate(). The M2 columns are nullable and have no
// DEFAULT — NULL is the proper "field absent" marker so cost reconstruction
// can distinguish "caller did not supply" from "caller supplied zero".
//
// The bench_runs table (M3 plan Task 3) stores one row per `ktx bench run`
// invocation. Columns mirror D12's methodology_version contract
// (`methodology_version TEXT` references `.agentera/bench/methodology-v1.yaml`)
// and D11's reconstructability inputs (tokens_in + tokens_out + cost +
// model). `id TEXT PRIMARY KEY` matches the nanoid-keyed shape used by
// sessions and tool_calls (D3). `timestamp INTEGER` is Unix epoch
// seconds (the integer variant matches D12's reproducibility contract:
// "a third party reading this file plus the persisted `bench_runs` row
// can compute `cost_per_correct` exactly"). Every column is nullable
// except `id` — the bench row is the persisted record, not a normalized
// accounting row; NULL preserves "field not yet supplied by the runner"
// without coercing zero values.
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
	// compressed (D14 reversibility layer): one row per `ktx compress`
	// invocation. Holds compressed artifacts SEPARATE from tool_calls
	// (M3 bench cost-aggregates tool_calls; injecting zero-cost
	// non-evidence rows would corrupt the cost-summed evidence set).
	// source_id REFERENCES tool_calls(id); foreign_keys=1 (enforced by
	// Open's DSN) makes the FK meaningful. pipeline is a JSON array of
	// stage names that ran (D14: "a record of what stages ran") — JSON
	// is inspectable and trivially stored as TEXT. The 6-column shape
	// mirrors the M4 plan Task 1 acceptance: id, source_id, output,
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
}

// m2Column enumerates a single M2 cost-publication column on tool_calls.
// The list is the single source of truth for the migration: it drives both
// the columns added to the CREATE TABLE statement (above) and the
// ALTER TABLE ADD COLUMN step in migrate() for pre-M2 databases.
//
// Order matters only for deterministic output (doctor reports missing
// columns in the order they appear here). Types match the D9 flat-JSON
// payload so callers can persist numbers and strings without a SQLite
// type-coercion surprise.
type m2Column struct {
	Name string
	Type string
}

// m2Columns are the M2 cost-publication columns on tool_calls. Each entry
// pairs the column name with its SQL type for the ALTER TABLE ADD COLUMN
// statement. Idempotency is handled by checking PRAGMA table_info before
// altering — SQLite has no IF NOT EXISTS for ADD COLUMN, so a column
// existence check is the only pure-Go mechanism. PRAGMA table_info runs
// inside the migration transaction without issue; the query is read-only
// and the connection's schema view is consistent within a transaction.
var m2Columns = []m2Column{
	{Name: "tokens_in", Type: "INTEGER"},
	{Name: "tokens_out", Type: "INTEGER"},
	{Name: "cost", Type: "REAL"},
	{Name: "latency_ms", Type: "INTEGER"},
	{Name: "model", Type: "TEXT"},
	{Name: "provider", Type: "TEXT"},
}

// failureSignalColumns are the D15 failure-signal columns on
// tool_calls (Decision 15). exit_code is diagnostic; is_error is the
// canonical gating signal stored as INTEGER 0/1/NULL and converted to
// *bool in GetToolCall. Idempotency mirrors m2Columns.
var failureSignalColumns = []m2Column{
	{Name: "exit_code", Type: "INTEGER"},
	{Name: "is_error", Type: "INTEGER"},
}

// benchRunsColumn enumerates a single bench_runs column. The list is the
// single source of truth for the migration: it drives the columns in the
// CREATE TABLE statement (above) and the ALTER TABLE ADD COLUMN step in
// migrate() for pre-M3 databases (or partial bench_runs schemas). Order
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
// columns per D12's reproducibility contract: `id` (D3 nanoid), `agent`,
// `model`, `task`, `score`, `tokens_in`, `tokens_out`, `cost`,
// `methodology_version` (the D12 column), `timestamp` (Unix epoch
// seconds). Idempotency is handled identically to m2Columns — PRAGMA
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
// COLUMN loop in migrate() for pre-M4 databases (or partial compressed
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

// compressedColumns are the 6 columns of the `compressed` table per
// D14 / M4 plan Task 1: id (D3 nanoid), source_id (FK → tool_calls.id),
// output (the compressed bytes), content_hash (SHA-256 of compressed
// output, recomputed per D9 integrity rule), pipeline (JSON array of
// stage names that ran), timestamp. Idempotency mirrors m2Columns and
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

// DefaultPath resolves the default database path per Decision 8:
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

// ResolvePath applies the database path precedence from Decision 8:
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

// IDPattern is the canonical regex for ktx IDs (Decision 3). Exported so
// the cli layer can validate sampled IDs without re-encoding the
// alphabet/length contract. Lives next to the schema / open helpers
// because the pattern is the on-the-wire contract for the IDs the
// schema and the open path produce together.
var IDPattern = regexp.MustCompile(`^[a-z]{16}$`)

// Open opens (or creates) the ktx database at path and ensures the schema is
// migrated. The parent directory is created if missing (Decision 6). Foreign
// keys are enforced so the tool_calls.session_id REFERENCES clause is
// meaningful, not decorative (Decision 7).
//
// The schema migration is idempotent: CREATE TABLE IF NOT EXISTS runs on
// every open, so re-opening an existing database is a no-op. The M2
// in-place migration (ALTER TABLE ADD COLUMN for tokens_in/cost/etc.) is
// equally idempotent — the column-existence check via PRAGMA table_info
// makes a second run on a fully migrated database a no-op, and a
// partial-set database (e.g. only tokens_in added) is forward-fixed on
// the next open. No versioned migration framework is needed for the
// additive M2 column set (per the plan: SQLite ALTER TABLE, no custom
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
	dsn := "file:" + path + "?_pragma=foreign_keys(1)"

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
//     schema (e.g. a pre-M3 fixture missing bench_runs, or a partial
//     M2 fixture missing one additive column). The dispatch layer's
//     doctor tests exercise this path: opening a raw *sql.DB with
//     M1+M2 columns but no bench_runs table, wrapping it in *DB
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
//     schema satisfies the M1/M2/M3 contract; methods that read
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

// migrate runs the schema statements and the M2 in-place column migration
// inside a single transaction so the database is never left with a partial
// schema. Fresh databases get the M2 columns from the CREATE TABLE itself;
// pre-M2 databases pick them up via ALTER TABLE ADD COLUMN. The same
// transaction also forward-fixes a partial M2 set: any M2 column absent
// from tool_calls is added; any M2 column already present is skipped. A
// re-open of a fully migrated database is therefore a pure no-op (all
// PRAGMA checks find the column, no ALTER runs).
//
// The bench_runs migration (M3 plan Task 3) follows the same pattern: the
// CREATE TABLE statement above establishes the 10-column table on a
// fresh database or a pre-M3 database (no bench_runs at all); the
// ALTER TABLE ADD COLUMN loop below forward-fixes a partial bench_runs
// schema on the next open (e.g. a buggy prior migration created the
// table with only some columns). The M2 and M3 column loops are
// independent — a pre-M2 database gets the M2 columns added to
// tool_calls AND gets bench_runs created; a post-M2 pre-M3 database
// gets bench_runs created with no work on tool_calls.
//
// The compressed migration (M4 plan Task 1) follows the same pattern
// for the reversibility-layer table: the CREATE TABLE statement above
// establishes the 6-column table on a fresh database or a pre-M4
// database (no compressed at all); the ALTER TABLE ADD COLUMN loop
// below forward-fixes a partial compressed schema on the next open.
// The compressed loop is independent of the M2 and M3 loops — a pre-M4
// database gets compressed created alongside whatever M2/M3 work the
// same migrate call performs. A re-open of a fully migrated database
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
	for _, col := range m2Columns {
		if _, ok := have[col.Name]; ok {
			continue
		}
		stmt := "ALTER TABLE tool_calls ADD COLUMN " + col.Name + " " + col.Type
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("db: add m2 column %q: %w", col.Name, err)
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

	return tx.Commit()
}

// DB is a handle to the ktx database. The underlying *sql.DB is unexported
// because callers reach the database through methods, not by reaching into
// the handle. Persistence and query methods are added by Tasks 3-7.
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
// compressed-row not-found path — D7 violation simulation). The
// returned handle is the live pool; callers must not Close it (use
// Close on the enclosing *DB instead).
func (d *DB) DB() *sql.DB {
	if d == nil {
		return nil
	}
	return d.db
}
