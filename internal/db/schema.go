package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const schemaVersion = 16

const compressedTimestampMigrationBatch = 128

const createCompressedTimestampMigration = `CREATE TABLE IF NOT EXISTS compressed_timestamp_migration (
		singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
		last_id   TEXT
	)`

// MaxSupportedSchema returns the newest database schema this binary can open.
// It is DB-free and shares the migration code's single schema-version owner.
func MaxSupportedSchema() int { return schemaVersion }

// SchemaTooNewError reports that the database was created by a newer ktx
// schema. Callers can classify this failure without parsing its message.
type SchemaTooNewError struct {
	Actual  int
	Maximum int
}

func (e *SchemaTooNewError) Error() string {
	return fmt.Sprintf("db: schema version %d is newer than supported version %d; install and verify a runtime supporting schema %d or newer, then retry; do not lower PRAGMA user_version", e.Actual, e.Maximum, e.Actual)
}

const createRuntimeEventIntervalsTable = `CREATE TABLE IF NOT EXISTS runtime_event_intervals (
		id                    TEXT PRIMARY KEY,
		session_id            TEXT NOT NULL UNIQUE REFERENCES sessions(id),
		attempt_id            TEXT NOT NULL UNIQUE,
		runtime               TEXT NOT NULL,
		runtime_version       TEXT NOT NULL,
		source                TEXT NOT NULL CHECK (source = 'event_hook'),
		contract_version      TEXT NOT NULL,
		opened_at             TEXT NOT NULL,
		closed_at             TEXT,
		close_state           TEXT NOT NULL CHECK (close_state IN ('open','complete','incomplete','contradictory')),
		first_ordinal         INTEGER,
		last_ordinal          INTEGER,
		event_count           INTEGER NOT NULL DEFAULT 0 CHECK (event_count >= 0),
		collector_epoch_count INTEGER NOT NULL DEFAULT 0 CHECK (collector_epoch_count >= 0),
		contradiction_count   INTEGER NOT NULL DEFAULT 0 CHECK (contradiction_count >= 0),
		failure_reason        TEXT,
		CHECK ((close_state = 'open') = (closed_at IS NULL)),
		CHECK ((event_count = 0 AND first_ordinal IS NULL AND last_ordinal IS NULL) OR
		       (event_count > 0 AND first_ordinal = 1 AND last_ordinal = event_count))
	)`

const createRuntimeEventsTable = `CREATE TABLE IF NOT EXISTS runtime_events (
		interval_id       TEXT NOT NULL REFERENCES runtime_event_intervals(id),
		ordinal           INTEGER NOT NULL CHECK (ordinal > 0),
		epoch_id          TEXT NOT NULL,
		epoch_ordinal     INTEGER NOT NULL CHECK (epoch_ordinal >= 0),
		kind              TEXT NOT NULL CHECK (kind IN ('collector_open','model_step','retry','compaction','interruption','other')),
		event_type        TEXT NOT NULL,
		source_session_id TEXT,
		message_id        TEXT,
		part_id           TEXT,
		captured_at       TEXT NOT NULL,
		model_step_classification TEXT CHECK (model_step_classification IN ('accepted','exact_duplicate','conflicting_duplicate')),
		PRIMARY KEY (interval_id, ordinal),
		UNIQUE (interval_id, epoch_id, epoch_ordinal),
		CHECK ((kind = 'collector_open' AND epoch_ordinal = 0) OR
		       (kind <> 'collector_open' AND epoch_ordinal > 0)),
		CHECK ((kind = 'model_step') = (model_step_classification IS NOT NULL)),
		CHECK (kind <> 'model_step' OR
		       (source_session_id IS NOT NULL AND source_session_id <> '' AND
		        message_id IS NOT NULL AND message_id <> '' AND part_id IS NOT NULL AND part_id <> ''))
	)`

const createRuntimeEventIndexes = `CREATE INDEX IF NOT EXISTS runtime_events_interval_session_idx
	ON runtime_events(interval_id, source_session_id, ordinal)`

const createRuntimeEventMessageIndex = `CREATE INDEX IF NOT EXISTS runtime_events_interval_message_part_idx
	ON runtime_events(interval_id, message_id, part_id, ordinal)`

const createCompressionDispositionsTable = `CREATE TABLE IF NOT EXISTS compression_dispositions (
		source_id    TEXT PRIMARY KEY REFERENCES tool_calls(id),
		profile      TEXT,
		route        TEXT CHECK (route IS NULL OR route IN ('matched','default_fallback')),
		disposition  TEXT NOT NULL CHECK (disposition IN ('artifact_persisted','intentional_passthrough','anti_worsening_rejected')),
		artifact_id  TEXT REFERENCES compressed(id),
		observed_at  TEXT NOT NULL,
		CHECK ((disposition = 'artifact_persisted') = (artifact_id IS NOT NULL))
	)`

const createSessionTelemetryTable = `CREATE TABLE IF NOT EXISTS session_telemetry (
		session_id        TEXT NOT NULL REFERENCES sessions(id),
		source_session_id TEXT NOT NULL,
		message_id        TEXT NOT NULL,
		part_id           TEXT NOT NULL,
		ordinal           INTEGER NOT NULL,
		reason            TEXT NOT NULL,
		cost              REAL,
		tokens_input       INTEGER,
		tokens_output      INTEGER,
		tokens_reasoning   INTEGER,
		cache_read         INTEGER,
		cache_write        INTEGER,
		provider           TEXT,
		model              TEXT,
		runtime            TEXT NOT NULL,
		runtime_version    TEXT NOT NULL,
		source             TEXT NOT NULL DEFAULT 'legacy',
		captured_at        TEXT NOT NULL,
		captured_at_unix_nano INTEGER,
		cost_state              TEXT NOT NULL DEFAULT 'measured' CHECK (cost_state IN ('measured','unknown','malformed')),
		tokens_input_state       TEXT NOT NULL DEFAULT 'measured' CHECK (tokens_input_state IN ('measured','unknown','malformed')),
		tokens_output_state      TEXT NOT NULL DEFAULT 'measured' CHECK (tokens_output_state IN ('measured','unknown','malformed')),
		tokens_reasoning_state   TEXT NOT NULL DEFAULT 'measured' CHECK (tokens_reasoning_state IN ('measured','unknown','malformed')),
		cache_read_state         TEXT NOT NULL DEFAULT 'measured' CHECK (cache_read_state IN ('measured','unknown','malformed')),
		cache_write_state        TEXT NOT NULL DEFAULT 'measured' CHECK (cache_write_state IN ('measured','unknown','malformed')),
		CHECK ((cost_state = 'measured') = (cost IS NOT NULL)),
		CHECK ((tokens_input_state = 'measured') = (tokens_input IS NOT NULL)),
		CHECK ((tokens_output_state = 'measured') = (tokens_output IS NOT NULL)),
		CHECK ((tokens_reasoning_state = 'measured') = (tokens_reasoning IS NOT NULL)),
		CHECK ((cache_read_state = 'measured') = (cache_read IS NOT NULL)),
		CHECK ((cache_write_state = 'measured') = (cache_write IS NOT NULL)),
		PRIMARY KEY (runtime, source_session_id, message_id, part_id)
	)`

const createSessionTelemetryOrderIndex = `CREATE INDEX IF NOT EXISTS session_telemetry_session_order_idx
	ON session_telemetry(session_id, ordinal, runtime, source_session_id, message_id, part_id)`

const createCollectionIntervalsTable = `CREATE TABLE IF NOT EXISTS collection_intervals (
		id TEXT PRIMARY KEY,
		database_target TEXT NOT NULL,
		supported_ingresses TEXT NOT NULL,
		opened_at TEXT NOT NULL
	)`

const createIngressContinuityTable = `CREATE TABLE IF NOT EXISTS ingress_continuity (
		interval_id TEXT NOT NULL REFERENCES collection_intervals(id),
		ingress TEXT NOT NULL,
		instance_id TEXT NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('started','stopped','interrupted')),
		observed_at TEXT NOT NULL,
		authority TEXT NOT NULL CHECK (authority = 'ingress_process'),
		PRIMARY KEY (interval_id, ingress, instance_id, state, observed_at)
	)`

const createRuntimeSourceObservationsTable = `CREATE TABLE IF NOT EXISTS runtime_source_observations (
		collection_id TEXT NOT NULL REFERENCES collection_intervals(id),
		session_id TEXT NOT NULL REFERENCES sessions(id),
		source_id TEXT NOT NULL REFERENCES tool_calls(id),
		latency_ms INTEGER,
		latency_state TEXT NOT NULL CHECK (latency_state IN ('measured','unknown','malformed')),
		latency_unit TEXT NOT NULL CHECK (latency_unit = 'milliseconds'),
		latency_authority TEXT NOT NULL CHECK (latency_authority IN ('runtime','unknown')),
		delivered_bytes INTEGER CHECK (delivered_bytes IS NULL OR delivered_bytes >= 0),
		recovery_cue_observed INTEGER NOT NULL DEFAULT 0 CHECK (recovery_cue_observed IN (0,1)),
		observed_at TEXT NOT NULL,
		PRIMARY KEY (collection_id, source_id),
		CHECK ((latency_state = 'measured') = (latency_ms IS NOT NULL)),
		CHECK (latency_ms IS NULL OR latency_ms >= 0)
	)`

const createRecoveryCueDeliveriesTable = `CREATE TABLE IF NOT EXISTS recovery_cue_deliveries (
		collection_id TEXT NOT NULL REFERENCES collection_intervals(id),
		source_id TEXT NOT NULL REFERENCES tool_calls(id),
		artifact_id TEXT NOT NULL REFERENCES compressed(id),
		session_id TEXT NOT NULL REFERENCES sessions(id),
		runtime_session_id TEXT NOT NULL,
		runtime_call_id TEXT NOT NULL,
		delivered_at TEXT NOT NULL,
		authority TEXT NOT NULL CHECK (authority = 'opencode_adapter'),
		PRIMARY KEY (collection_id, source_id)
	)`

const createRecoveryAttemptsTable = `CREATE TABLE IF NOT EXISTS recovery_attempts (
		attempt_source_id TEXT PRIMARY KEY REFERENCES tool_calls(id),
		collection_id TEXT NOT NULL,
		cue_source_id TEXT NOT NULL,
		attempted_at TEXT NOT NULL,
		result_state TEXT NOT NULL CHECK (result_state IN ('succeeded','failed')),
		byte_exact INTEGER CHECK (byte_exact IN (0,1)),
		authority TEXT NOT NULL CHECK (authority = 'opencode_hook'),
		FOREIGN KEY (collection_id,cue_source_id) REFERENCES recovery_cue_deliveries(collection_id,source_id),
		CHECK ((result_state = 'succeeded') = (byte_exact IS NOT NULL))
	)`

// schemaStatements creates the current database schema. The statements remain
// idempotent so stale and partially migrated databases can be repaired safely.
//
// The cost-publication columns (tokens_in, tokens_out, cost, latency_ms,
// model, provider) are part of the CREATE TABLE so fresh databases include
// them from the start; older databases pick them up via the in-place
// ALTER TABLE step in migrate(). These columns are nullable and have no
// DEFAULT — NULL is the proper "field absent" marker so cost reconstruction
// can distinguish "caller did not supply" from "caller supplied zero".
//
// bench_runs stores the inputs and result of each benchmark invocation.
// Every column except id is nullable so absent measurements remain distinct
// from zero values. timestamp uses Unix epoch seconds.
//
// recorded_streams stores verbatim agent output. bench_run_id is nullable
// because `ktx run --record-stream` captures streams outside a benchmark.
// session_id always identifies the wrapper session that emitted the stream.
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS sessions (
		id              TEXT PRIMARY KEY,
		agent           TEXT,
		agent_attribution_authority TEXT NOT NULL DEFAULT 'unknown' CHECK (agent_attribution_authority IN ('identified','unknown')),
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
		timestamp_unix_nano INTEGER,
		event_type   TEXT DEFAULT 'tool_call',
		tokens_in    INTEGER,
		tokens_out   INTEGER,
		cost         REAL,
		latency_ms   INTEGER,
		latency_state TEXT NOT NULL DEFAULT 'unknown' CHECK (latency_state IN ('measured','unknown','malformed')),
		latency_unit TEXT NOT NULL DEFAULT 'milliseconds' CHECK (latency_unit = 'milliseconds'),
		latency_authority TEXT NOT NULL DEFAULT 'unknown' CHECK (latency_authority IN ('runtime','unknown')),
		model        TEXT,
		provider     TEXT,
		exit_code    INTEGER,
		is_error     INTEGER,
		runtime      TEXT,
		producer     TEXT
	)`,
	createCollectionIntervalsTable,
	createIngressContinuityTable,
	createRuntimeSourceObservationsTable,
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
		timestamp           INTEGER,
		cache_read          INTEGER,
		cache_write         INTEGER,
		reasoning           INTEGER,
		estimated_api_cost  REAL
	)`,
	// compressed (the reversibility layer): one row per `ktx compress`
	// invocation. Holds compressed artifacts SEPARATE from tool_calls
	// (bench cost-aggregates tool_calls; injecting zero-cost
	// non-evidence rows would corrupt the cost-summed evidence set).
	// source_id REFERENCES tool_calls(id); foreign_keys=1 (enforced by
	// Open's DSN) makes the FK meaningful. pipeline is a JSON object with
	// selected profile, route provenance, and retained stages.
	`CREATE TABLE IF NOT EXISTS compressed (
		id           TEXT PRIMARY KEY,
		source_id    TEXT NOT NULL REFERENCES tool_calls(id),
		output       TEXT,
		content_hash TEXT,
		pipeline     TEXT,
		timestamp    TEXT,
		timestamp_unix_nano INTEGER,
		producer     TEXT
	)`,
	// compressed_source_id_idx accelerates `ktx decompress <id>`'s
	// source_id lookup (the reversal primitive GetSourceOutput). The
	// index is created idempotently alongside the table; re-open is a
	// no-op via CREATE INDEX IF NOT EXISTS.
	`CREATE INDEX IF NOT EXISTS compressed_source_id_idx ON compressed(source_id)`,
	createRecoveryCueDeliveriesTable,
	createRecoveryAttemptsTable,
	createCompressionDispositionsTable,
	// recorded_streams: one row per captured
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
	createSessionTelemetryTable,
	createSessionTelemetryOrderIndex,
	createRuntimeEventIntervalsTable,
	createRuntimeEventsTable,
	createRuntimeEventIndexes,
	createRuntimeEventMessageIndex,
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
type schemaColumn struct {
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
var costColumns = []schemaColumn{
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
var failureSignalColumns = []schemaColumn{
	{Name: "exit_code", Type: "INTEGER"},
	{Name: "is_error", Type: "INTEGER"},
}

// runtimeColumns are the nullable runtime fields on tool_calls.
var runtimeColumns = []schemaColumn{
	{Name: "runtime", Type: "TEXT"},
}

var latencyEvidenceColumns = []schemaColumn{
	{Name: "latency_state", Type: "TEXT NOT NULL DEFAULT 'unknown'"},
	{Name: "latency_unit", Type: "TEXT NOT NULL DEFAULT 'milliseconds'"},
	{Name: "latency_authority", Type: "TEXT NOT NULL DEFAULT 'unknown'"},
}

// producerColumns are nullable so migration leaves historical ownership
// unknown.
var producerColumns = []schemaColumn{
	{Name: "producer", Type: "TEXT"},
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
// benchRunsColumns are the bench_runs columns. The list covers all 14
// columns per the reproducibility contract: `id` (nanoid), `agent`,
// `model`, `task`, `score`, `tokens_in`, `tokens_out`, `cost`,
// `methodology_version` (the methodology column), `timestamp` (Unix epoch
// seconds), `cache_read` and `cache_write` (additive NULL-when-absent
// cache-telemetry columns sourced from the runtime's step_finish event),
// `reasoning`, and `estimated_api_cost`. Idempotency is handled identically
// to costColumns — PRAGMA table_info is consulted before ALTER TABLE ADD
// COLUMN, so a re-open of a fully migrated database is a no-op and a partial
// bench_runs schema is forward-fixed on the next open.
var benchRunsColumns = []schemaColumn{
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
	{Name: "cache_read", Type: "INTEGER"},
	{Name: "cache_write", Type: "INTEGER"},
	{Name: "reasoning", Type: "INTEGER"},
	{Name: "estimated_api_cost", Type: "REAL"},
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
// compressedColumns are the columns of the `compressed` table:
// id (nanoid), source_id (FK → tool_calls.id),
// output (the compressed bytes), content_hash (SHA-256 of compressed
// output, recomputed per the integrity rule), pipeline (JSON selection and
// retained-stage record), timestamp. Idempotency mirrors costColumns and
// benchRunsColumns — PRAGMA table_info is consulted before ALTER TABLE
// ADD COLUMN, so a re-open of a fully migrated database is a no-op
// and a partial compressed schema is forward-fixed on the next open.
var compressedColumns = []schemaColumn{
	{Name: "id", Type: "TEXT"},
	{Name: "source_id", Type: "TEXT"},
	{Name: "output", Type: "TEXT"},
	{Name: "content_hash", Type: "TEXT"},
	{Name: "pipeline", Type: "TEXT"},
	{Name: "timestamp", Type: "TEXT"},
	{Name: "timestamp_unix_nano", Type: "INTEGER"},
	{Name: "producer", Type: "TEXT"},
}

// tableColumns returns the set of column names for table. Empty when
// the table does not exist (PRAGMA table_info returns zero rows for a
// missing table). The error path is reserved for genuine query/scan
// failures — the "table absent" case is reported as an empty set so the
// caller can compose a single, clear error message. q may be a database or
// transaction so migrations can inspect their in-transaction schema view.
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

func addMissingColumns(tx *sql.Tx, table string, have map[string]struct{}, groups ...[]schemaColumn) error {
	for _, columns := range groups {
		for _, column := range columns {
			if _, ok := have[column.Name]; ok {
				continue
			}
			if _, err := tx.Exec("ALTER TABLE " + table + " ADD COLUMN " + column.Name + " " + column.Type); err != nil {
				return fmt.Errorf("db: add %s column %q: %w", table, column.Name, err)
			}
			have[column.Name] = struct{}{}
		}
	}
	return nil
}

// migrate gates schema work with PRAGMA user_version. The first read is outside
// a transaction so opening an already-current database never acquires a write
// lock. A stale opener checks the version again after beginning its immediate
// transaction, allowing concurrent openers to observe the winner's migration.
func migrate(db *sql.DB) error {
	for {
		done, err := migrateStep(db, migrateTx)
		if err != nil || done {
			return err
		}
	}
}

// migrateWith keeps lock acquisition attempt-bounded while allowing an
// acquired migration transaction to finish independently of the shorter
// deadline used for ordinary writes. apply is injected only to make that
// distinction deterministic in tests.
func migrateWith(db *sql.DB, apply func(*sql.Tx) error) error {
	for {
		done, err := migrateStep(db, apply)
		if err != nil || done {
			return err
		}
	}
}

func migrateStep(db *sql.DB, apply func(*sql.Tx) error) (bool, error) {
	version, err := readSchemaVersionWithRetry(db, startupLockRetryPolicy())
	if err != nil {
		return false, fmt.Errorf("db: read schema version: %w", err)
	}
	if version == schemaVersion {
		return true, checkRequiredColumns(db)
	}
	if version > schemaVersion {
		return false, newerSchemaError(version)
	}
	if version == 15 {
		return migrateCompressedTimestampStep(db)
	}

	ctx := context.Background()
	err = withWriteTx(ctx, db, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
			return fmt.Errorf("db: recheck schema version: %w", err)
		}
		if version == schemaVersion {
			return nil
		}
		if version > schemaVersion {
			return newerSchemaError(version)
		}
		if err := apply(tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return fmt.Errorf("db: set schema version: %w", err)
		}
		return nil
	})
	return err == nil, err
}

// migrateCompressedTimestampStep keeps schema 15 usable between bounded
// commits. The migration cursor advances in the same transaction as each row
// batch. Malformed timestamps remain NULL but still advance the cursor.
func migrateCompressedTimestampStep(db *sql.DB) (bool, error) {
	updated, err := migrateCompressedTimestampBatch(db)
	if err != nil {
		return false, err
	}
	if updated != 0 {
		return false, nil
	}

	ctx := context.Background()
	remaining := false
	err = withWriteTx(ctx, db, func(tx *sql.Tx) error {
		var version int
		if err := tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
			return fmt.Errorf("db: recheck schema version: %w", err)
		}
		if version > schemaVersion {
			return newerSchemaError(version)
		}
		if version == schemaVersion {
			return nil
		}
		if version != 15 {
			return fmt.Errorf("db: schema changed from 15 to %d during migration", version)
		}

		var lastID sql.NullString
		if err := tx.QueryRow(`SELECT last_id FROM compressed_timestamp_migration WHERE singleton = 1`).Scan(&lastID); err != nil {
			return fmt.Errorf("db: read compressed timestamp migration cursor: %w", err)
		}
		query := `SELECT 1 FROM compressed LIMIT 1`
		var args []any
		if lastID.Valid {
			query = `SELECT 1 FROM compressed WHERE id > ? LIMIT 1`
			args = append(args, lastID.String)
		}
		var one int
		if err := tx.QueryRow(query, args...).Scan(&one); err == nil {
			remaining = true
			return nil
		} else if err != sql.ErrNoRows {
			return fmt.Errorf("db: check compressed timestamp migration completion: %w", err)
		}
		if _, err := tx.Exec(`DROP TABLE compressed_timestamp_migration`); err != nil {
			return fmt.Errorf("db: remove compressed timestamp migration cursor: %w", err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return fmt.Errorf("db: set schema version: %w", err)
		}
		return nil
	})
	if remaining {
		return false, err
	}
	return err == nil, err
}

func migrateCompressedTimestampBatch(db *sql.DB) (int64, error) {
	var updated int64
	ctx := context.Background()
	err := withWriteTx(ctx, db, func(tx *sql.Tx) error {
		var version int
		if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
			return fmt.Errorf("db: recheck schema version: %w", err)
		}
		if version == schemaVersion {
			return nil
		}
		if version > schemaVersion {
			return newerSchemaError(version)
		}
		if version != 15 {
			return fmt.Errorf("db: schema changed from 15 to %d during migration", version)
		}
		columns, err := tableColumns(tx, "compressed")
		if err != nil {
			return fmt.Errorf("db: inspect compressed: %w", err)
		}
		if len(columns) == 0 {
			// A version-only fixture has no history, so the ordinary repair path
			// is bounded. Real schema-15 databases skip it and only repair the
			// compressed shape below.
			if err := migrateTx(tx); err != nil {
				return fmt.Errorf("db: repair empty schema 15: %w", err)
			}
			columns, err = tableColumns(tx, "compressed")
			if err != nil {
				return fmt.Errorf("db: inspect repaired compressed table: %w", err)
			}
		}
		if err := addMissingColumns(tx, "compressed", columns, compressedColumns); err != nil {
			return err
		}
		if _, err := tx.Exec(createCompressionDispositionsTable); err != nil {
			return fmt.Errorf("db: create compression dispositions: %w", err)
		}
		if _, err := tx.Exec(createCompressedTimestampMigration); err != nil {
			return fmt.Errorf("db: create compressed timestamp migration cursor: %w", err)
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO compressed_timestamp_migration (singleton, last_id) VALUES (1, NULL)`); err != nil {
			return fmt.Errorf("db: initialize compressed timestamp migration cursor: %w", err)
		}

		var lastID sql.NullString
		if err := tx.QueryRow(`SELECT last_id FROM compressed_timestamp_migration WHERE singleton = 1`).Scan(&lastID); err != nil {
			return fmt.Errorf("db: read compressed timestamp migration cursor: %w", err)
		}
		query := `SELECT id FROM compressed ORDER BY id LIMIT ?`
		args := []any{compressedTimestampMigrationBatch}
		if lastID.Valid {
			query = `SELECT id FROM compressed WHERE id > ? ORDER BY id LIMIT ?`
			args = []any{lastID.String, compressedTimestampMigrationBatch}
		}
		rows, err := tx.Query(query, args...)
		if err != nil {
			return fmt.Errorf("db: select compressed timestamp migration batch: %w", err)
		}
		ids := make([]string, 0, compressedTimestampMigrationBatch)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return fmt.Errorf("db: scan compressed timestamp migration batch: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("db: close compressed timestamp migration batch: %w", err)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("db: select compressed timestamp migration batch: %w", err)
		}
		if len(ids) == 0 {
			return nil
		}

		values := make([]any, len(ids))
		for i := range ids {
			values[i] = ids[i]
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		result, err := tx.Exec(`UPDATE compressed
			SET timestamp_unix_nano = ktx_unix_nano(timestamp)
			WHERE id IN (`+placeholders+`)`, values...)
		if err != nil {
			return fmt.Errorf("db: backfill compressed sortable timestamps: %w", err)
		}
		updated, err = result.RowsAffected()
		if err != nil {
			return fmt.Errorf("db: inspect compressed timestamp migration batch: %w", err)
		}
		if updated != int64(len(ids)) {
			return fmt.Errorf("db: backfilled %d compressed timestamps, want %d", updated, len(ids))
		}
		if _, err := tx.Exec(`UPDATE compressed_timestamp_migration SET last_id = ? WHERE singleton = 1`, ids[len(ids)-1]); err != nil {
			return fmt.Errorf("db: advance compressed timestamp migration cursor: %w", err)
		}
		return nil
	})
	return updated, err
}

func newerSchemaError(actual int) error {
	return &SchemaTooNewError{Actual: actual, Maximum: schemaVersion}
}

// readSchemaVersionWithRetry is deliberately limited to migration's initial
// non-transactional read. The migration transaction keeps its own retry owner;
// retrying the whole migration here could replay schema work.
func readSchemaVersionWithRetry(db *sql.DB, policy sqliteLockRetryPolicy) (int, error) {
	var version int
	err := retrySQLiteLock(policy, func() error {
		return db.QueryRow(`PRAGMA user_version`).Scan(&version)
	})
	return version, err
}

// migrateTx creates missing tables and adds or repairs all historical schema
// shapes. The caller owns the transaction and schema-version update.
// It repairs partial tool_calls, bench_runs, and compressed schemas. The
// recorded_streams table is created when absent but does not support partial
// column repair.
func migrateTx(tx *sql.Tx) error {
	for _, stmt := range schemaStatements {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("db: create schema: %w", err)
		}
	}
	if err := migrateSessionTelemetryV5(tx); err != nil {
		return err
	}
	if err := migrateSessionTelemetryV6(tx); err != nil {
		return err
	}
	if err := migrateRuntimeEventsV8(tx); err != nil {
		return err
	}

	sessionColumns, err := tableColumns(tx, "sessions")
	if err != nil {
		return fmt.Errorf("db: inspect sessions: %w", err)
	}
	if _, ok := sessionColumns["agent_attribution_authority"]; !ok {
		if _, err := tx.Exec(`ALTER TABLE sessions ADD COLUMN agent_attribution_authority TEXT NOT NULL DEFAULT 'unknown' CHECK (agent_attribution_authority IN ('identified','unknown'))`); err != nil {
			return fmt.Errorf("db: add session attribution authority: %w", err)
		}
	}

	have, err := tableColumns(tx, "tool_calls")
	if err != nil {
		return fmt.Errorf("db: inspect tool_calls: %w", err)
	}
	if err := addMissingColumns(tx, "tool_calls", have, costColumns, failureSignalColumns, runtimeColumns, latencyEvidenceColumns, producerColumns); err != nil {
		return err
	}
	if _, ok := have["timestamp_unix_nano"]; !ok {
		if _, err := tx.Exec(`ALTER TABLE tool_calls ADD COLUMN timestamp_unix_nano INTEGER`); err != nil {
			return fmt.Errorf("db: add sortable tool-call timestamp: %w", err)
		}
	}

	telemetryColumns, err := tableColumns(tx, "session_telemetry")
	if err != nil {
		return fmt.Errorf("db: inspect session_telemetry: %w", err)
	}
	if _, ok := telemetryColumns["captured_at_unix_nano"]; !ok {
		if _, err := tx.Exec(`ALTER TABLE session_telemetry ADD COLUMN captured_at_unix_nano INTEGER`); err != nil {
			return fmt.Errorf("db: add sortable telemetry timestamp: %w", err)
		}
	}
	if err := backfillSortableTimestamps(tx, "tool_calls", "id", "timestamp", "timestamp_unix_nano"); err != nil {
		return err
	}
	if err := backfillSortableTimestamps(tx, "session_telemetry", "rowid", "captured_at", "captured_at_unix_nano"); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS tool_calls_timestamp_unix_nano_idx ON tool_calls(timestamp_unix_nano)`); err != nil {
		return fmt.Errorf("db: create tool-call time index: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS session_telemetry_runtime_time_idx ON session_telemetry(runtime, runtime_version, captured_at_unix_nano)`); err != nil {
		return fmt.Errorf("db: create telemetry time index: %w", err)
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
	if err := backfillSortableTimestamps(tx, "compressed", "id", "timestamp", "timestamp_unix_nano"); err != nil {
		return err
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

	return nil
}

func backfillSortableTimestamps(tx *sql.Tx, table, key, textColumn, sortableColumn string) error {
	rows, err := tx.Query(`SELECT ` + key + `, ` + textColumn + ` FROM ` + table + ` WHERE ` + sortableColumn + ` IS NULL`)
	if err != nil {
		return fmt.Errorf("db: read %s timestamps: %w", table, err)
	}
	type update struct {
		key any
		ns  int64
	}
	var updates []update
	for rows.Next() {
		var keyValue any
		var text sql.NullString
		if err := rows.Scan(&keyValue, &text); err != nil {
			_ = rows.Close()
			return fmt.Errorf("db: scan %s timestamp: %w", table, err)
		}
		if text.Valid {
			if parsed, err := time.Parse(time.RFC3339Nano, text.String); err == nil {
				updates = append(updates, update{key: keyValue, ns: parsed.UnixNano()})
			}
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("db: iterate %s timestamps: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("db: close %s timestamps: %w", table, err)
	}
	for _, item := range updates {
		if _, err := tx.Exec(`UPDATE `+table+` SET `+sortableColumn+` = ? WHERE `+key+` = ?`, item.ns, item.key); err != nil {
			return fmt.Errorf("db: backfill %s timestamp: %w", table, err)
		}
	}
	return nil
}

func migrateRuntimeEventsV8(tx *sql.Tx) error {
	intervalColumns, err := tableColumns(tx, "runtime_event_intervals")
	if err != nil {
		return fmt.Errorf("db: inspect runtime event interval fields: %w", err)
	}
	if _, ok := intervalColumns["contradiction_count"]; !ok {
		if _, err := tx.Exec(`ALTER TABLE runtime_event_intervals ADD COLUMN contradiction_count INTEGER NOT NULL DEFAULT 0 CHECK (contradiction_count >= 0)`); err != nil {
			return fmt.Errorf("db: add runtime event contradiction count: %w", err)
		}
	}

	eventColumns, err := tableColumns(tx, "runtime_events")
	if err != nil {
		return fmt.Errorf("db: inspect runtime event fields: %w", err)
	}
	if _, ok := eventColumns["model_step_classification"]; ok {
		return nil
	}
	statements := []string{
		`ALTER TABLE runtime_events RENAME TO runtime_events_v7`,
		createRuntimeEventsTable,
		`INSERT INTO runtime_events (
			interval_id, ordinal, epoch_id, epoch_ordinal, kind, event_type,
			source_session_id, message_id, part_id, captured_at, model_step_classification
		) SELECT
			interval_id, ordinal, epoch_id, epoch_ordinal, kind, event_type,
			source_session_id, message_id, part_id, captured_at,
			CASE WHEN kind = 'model_step' THEN 'accepted' ELSE NULL END
		  FROM runtime_events_v7`,
		`DROP TABLE runtime_events_v7`,
		createRuntimeEventIndexes,
		createRuntimeEventMessageIndex,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("db: classify existing runtime events: %w", err)
		}
	}
	return nil
}

func migrateSessionTelemetryV5(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT name FROM pragma_table_info('session_telemetry') WHERE pk > 0 ORDER BY pk`)
	if err != nil {
		return fmt.Errorf("db: inspect session_telemetry primary key: %w", err)
	}
	var primaryKey []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("db: inspect session_telemetry primary key: %w", err)
		}
		primaryKey = append(primaryKey, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("db: inspect session_telemetry primary key: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("db: inspect session_telemetry primary key: %w", err)
	}

	provenance, err := tx.Query(`SELECT runtime, runtime_version FROM session_telemetry`)
	if err != nil {
		return fmt.Errorf("db: validate legacy session_telemetry provenance: %w", err)
	}
	for provenance.Next() {
		var runtime, runtimeVersion string
		if err := provenance.Scan(&runtime, &runtimeVersion); err != nil {
			provenance.Close()
			return fmt.Errorf("db: validate legacy session_telemetry provenance: %w", err)
		}
		if strings.TrimSpace(runtime) == "" || strings.TrimSpace(runtimeVersion) == "" {
			provenance.Close()
			return fmt.Errorf("db: migrate session_telemetry: blank runtime or runtime_version")
		}
	}
	if err := provenance.Err(); err != nil {
		provenance.Close()
		return fmt.Errorf("db: validate legacy session_telemetry provenance: %w", err)
	}
	if err := provenance.Close(); err != nil {
		return fmt.Errorf("db: validate legacy session_telemetry provenance: %w", err)
	}
	if len(primaryKey) == 4 && primaryKey[0] == "runtime" && primaryKey[1] == "source_session_id" &&
		primaryKey[2] == "message_id" && primaryKey[3] == "part_id" {
		return nil
	}

	statements := []string{
		`ALTER TABLE session_telemetry RENAME TO session_telemetry_v4`,
		createSessionTelemetryTable,
		`INSERT INTO session_telemetry (
			session_id, source_session_id, message_id, part_id, ordinal, reason, cost,
			tokens_input, tokens_output, tokens_reasoning, cache_read, cache_write,
			provider, model, runtime, runtime_version, captured_at
		) SELECT
			session_id, source_session_id, message_id, part_id, ordinal, reason, cost,
			tokens_input, tokens_output, tokens_reasoning, cache_read, cache_write,
			provider, model, runtime, runtime_version, captured_at
		FROM session_telemetry_v4`,
		`DROP TABLE session_telemetry_v4`,
		createSessionTelemetryOrderIndex,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("db: rebuild session_telemetry: %w", err)
		}
	}
	return nil
}

func migrateSessionTelemetryV6(tx *sql.Tx) error {
	columns, err := tableColumns(tx, "session_telemetry")
	if err != nil {
		return fmt.Errorf("db: inspect session_telemetry fields: %w", err)
	}
	required := []string{"source", "cost_state", "tokens_input_state", "tokens_output_state", "tokens_reasoning_state", "cache_read_state", "cache_write_state"}
	complete := true
	for _, name := range required {
		if _, ok := columns[name]; !ok {
			complete = false
			break
		}
	}
	if complete {
		return nil
	}
	statements := []string{
		`ALTER TABLE session_telemetry RENAME TO session_telemetry_v5`,
		createSessionTelemetryTable,
		`INSERT INTO session_telemetry (
			session_id, source_session_id, message_id, part_id, ordinal, reason, cost,
			tokens_input, tokens_output, tokens_reasoning, cache_read, cache_write,
			provider, model, runtime, runtime_version, captured_at
		) SELECT
			session_id, source_session_id, message_id, part_id, ordinal, reason, cost,
			tokens_input, tokens_output, tokens_reasoning, cache_read, cache_write,
			provider, model, runtime, runtime_version, captured_at
		FROM session_telemetry_v5`,
		`DROP TABLE session_telemetry_v5`,
		createSessionTelemetryOrderIndex,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("db: add session_telemetry field states: %w", err)
		}
	}
	return nil
}
