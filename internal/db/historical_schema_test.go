package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

var historicalSchemaBase = []string{
	`CREATE TABLE sessions (id TEXT PRIMARY KEY, agent TEXT, started_at TEXT, ended_at TEXT, last_seen_at TEXT, tool_call_count INTEGER DEFAULT 0)`,
	`CREATE TABLE tool_calls (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), tool TEXT, input TEXT, output TEXT, content_hash TEXT, timestamp TEXT, event_type TEXT DEFAULT 'tool_call', tokens_in INTEGER, tokens_out INTEGER, cost REAL, latency_ms INTEGER, model TEXT, provider TEXT, exit_code INTEGER, is_error INTEGER, runtime TEXT)`,
	`CREATE TABLE bench_runs (id TEXT PRIMARY KEY, agent TEXT, model TEXT, task TEXT, score REAL, tokens_in INTEGER, tokens_out INTEGER, cost REAL, methodology_version TEXT, timestamp INTEGER)`,
	`CREATE TABLE compressed (id TEXT PRIMARY KEY, source_id TEXT NOT NULL REFERENCES tool_calls(id), output TEXT, content_hash TEXT, pipeline TEXT, timestamp TEXT)`,
	`CREATE INDEX compressed_source_id_idx ON compressed(source_id)`,
	`CREATE TABLE recorded_streams (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), bench_run_id TEXT REFERENCES bench_runs(id), stream_text TEXT, content_hash TEXT, stream_format TEXT, captured_at TEXT)`,
	`CREATE INDEX recorded_streams_session_id_idx ON recorded_streams(session_id)`,
	`CREATE INDEX recorded_streams_bench_run_id_idx ON recorded_streams(bench_run_id)`,
}

const (
	historicalTelemetryV4 = `CREATE TABLE session_telemetry (
		session_id TEXT NOT NULL REFERENCES sessions(id), source_session_id TEXT NOT NULL,
		message_id TEXT NOT NULL, part_id TEXT NOT NULL, ordinal INTEGER NOT NULL,
		reason TEXT NOT NULL, cost REAL NOT NULL, tokens_input INTEGER NOT NULL,
		tokens_output INTEGER NOT NULL, tokens_reasoning INTEGER NOT NULL,
		cache_read INTEGER NOT NULL, cache_write INTEGER NOT NULL, provider TEXT, model TEXT,
		runtime TEXT NOT NULL, runtime_version TEXT NOT NULL, captured_at TEXT NOT NULL,
		PRIMARY KEY (source_session_id, message_id, part_id))`
	historicalTelemetryV5 = `CREATE TABLE session_telemetry (
		session_id TEXT NOT NULL REFERENCES sessions(id), source_session_id TEXT NOT NULL,
		message_id TEXT NOT NULL, part_id TEXT NOT NULL, ordinal INTEGER NOT NULL,
		reason TEXT NOT NULL, cost REAL, tokens_input INTEGER, tokens_output INTEGER,
		tokens_reasoning INTEGER, cache_read INTEGER, cache_write INTEGER, provider TEXT, model TEXT,
		runtime TEXT NOT NULL, runtime_version TEXT NOT NULL, captured_at TEXT NOT NULL,
		PRIMARY KEY (runtime, source_session_id, message_id, part_id))`
	historicalTelemetryV6 = `CREATE TABLE session_telemetry (
		session_id TEXT NOT NULL REFERENCES sessions(id), source_session_id TEXT NOT NULL,
		message_id TEXT NOT NULL, part_id TEXT NOT NULL, ordinal INTEGER NOT NULL,
		reason TEXT NOT NULL, cost REAL, tokens_input INTEGER, tokens_output INTEGER,
		tokens_reasoning INTEGER, cache_read INTEGER, cache_write INTEGER, provider TEXT, model TEXT,
		runtime TEXT NOT NULL, runtime_version TEXT NOT NULL,
		source TEXT NOT NULL DEFAULT 'legacy', captured_at TEXT NOT NULL,
		cost_state TEXT NOT NULL DEFAULT 'measured' CHECK (cost_state IN ('measured','unknown','malformed')),
		tokens_input_state TEXT NOT NULL DEFAULT 'measured' CHECK (tokens_input_state IN ('measured','unknown','malformed')),
		tokens_output_state TEXT NOT NULL DEFAULT 'measured' CHECK (tokens_output_state IN ('measured','unknown','malformed')),
		tokens_reasoning_state TEXT NOT NULL DEFAULT 'measured' CHECK (tokens_reasoning_state IN ('measured','unknown','malformed')),
		cache_read_state TEXT NOT NULL DEFAULT 'measured' CHECK (cache_read_state IN ('measured','unknown','malformed')),
		cache_write_state TEXT NOT NULL DEFAULT 'measured' CHECK (cache_write_state IN ('measured','unknown','malformed')),
		CHECK ((cost_state = 'measured') = (cost IS NOT NULL)),
		CHECK ((tokens_input_state = 'measured') = (tokens_input IS NOT NULL)),
		CHECK ((tokens_output_state = 'measured') = (tokens_output IS NOT NULL)),
		CHECK ((tokens_reasoning_state = 'measured') = (tokens_reasoning IS NOT NULL)),
		CHECK ((cache_read_state = 'measured') = (cache_read IS NOT NULL)),
		CHECK ((cache_write_state = 'measured') = (cache_write IS NOT NULL)),
		PRIMARY KEY (runtime, source_session_id, message_id, part_id))`
	historicalTelemetryIndex     = `CREATE INDEX session_telemetry_session_order_idx ON session_telemetry(session_id, ordinal, runtime, source_session_id, message_id, part_id)`
	historicalRuntimeIntervalsV7 = `CREATE TABLE runtime_event_intervals (
		id TEXT PRIMARY KEY, session_id TEXT NOT NULL UNIQUE REFERENCES sessions(id), attempt_id TEXT NOT NULL UNIQUE,
		runtime TEXT NOT NULL, runtime_version TEXT NOT NULL, source TEXT NOT NULL CHECK (source = 'event_hook'),
		contract_version TEXT NOT NULL, opened_at TEXT NOT NULL, closed_at TEXT,
		close_state TEXT NOT NULL CHECK (close_state IN ('open','complete','incomplete','contradictory')),
		first_ordinal INTEGER, last_ordinal INTEGER, event_count INTEGER NOT NULL DEFAULT 0 CHECK (event_count >= 0),
		collector_epoch_count INTEGER NOT NULL DEFAULT 0 CHECK (collector_epoch_count >= 0), failure_reason TEXT,
		CHECK ((close_state = 'open') = (closed_at IS NULL)),
		CHECK ((event_count = 0 AND first_ordinal IS NULL AND last_ordinal IS NULL) OR
		       (event_count > 0 AND first_ordinal = 1 AND last_ordinal = event_count)))`
	historicalRuntimeEventsV7 = `CREATE TABLE runtime_events (
		interval_id TEXT NOT NULL REFERENCES runtime_event_intervals(id), ordinal INTEGER NOT NULL CHECK (ordinal > 0),
		epoch_id TEXT NOT NULL, epoch_ordinal INTEGER NOT NULL CHECK (epoch_ordinal >= 0),
		kind TEXT NOT NULL CHECK (kind IN ('collector_open','model_step','retry','compaction','interruption','other')),
		event_type TEXT NOT NULL, source_session_id TEXT, message_id TEXT, part_id TEXT, captured_at TEXT NOT NULL,
		PRIMARY KEY (interval_id, ordinal), UNIQUE (interval_id, epoch_id, epoch_ordinal),
		CHECK ((kind = 'collector_open' AND epoch_ordinal = 0) OR (kind <> 'collector_open' AND epoch_ordinal > 0)),
		CHECK (kind <> 'model_step' OR (source_session_id IS NOT NULL AND source_session_id <> '' AND
		       message_id IS NOT NULL AND message_id <> '' AND part_id IS NOT NULL AND part_id <> '')))`
)

var historicalSchemaMigrations = [][]string{
	nil,
	nil,
	{`ALTER TABLE bench_runs ADD COLUMN cache_read INTEGER`, `ALTER TABLE bench_runs ADD COLUMN cache_write INTEGER`},
	{`ALTER TABLE bench_runs ADD COLUMN reasoning INTEGER`, `ALTER TABLE bench_runs ADD COLUMN estimated_api_cost REAL`},
	{historicalTelemetryV4, `CREATE INDEX session_telemetry_session_order_idx ON session_telemetry(session_id, ordinal)`},
	{
		`ALTER TABLE session_telemetry RENAME TO session_telemetry_v4`, historicalTelemetryV5,
		`INSERT INTO session_telemetry SELECT * FROM session_telemetry_v4`, `DROP TABLE session_telemetry_v4`, historicalTelemetryIndex,
	},
	{
		`ALTER TABLE session_telemetry RENAME TO session_telemetry_v5`, historicalTelemetryV6,
		`INSERT INTO session_telemetry (session_id,source_session_id,message_id,part_id,ordinal,reason,cost,tokens_input,tokens_output,tokens_reasoning,cache_read,cache_write,provider,model,runtime,runtime_version,captured_at)
		 SELECT session_id,source_session_id,message_id,part_id,ordinal,reason,cost,tokens_input,tokens_output,tokens_reasoning,cache_read,cache_write,provider,model,runtime,runtime_version,captured_at FROM session_telemetry_v5`,
		`DROP TABLE session_telemetry_v5`, historicalTelemetryIndex,
	},
	{
		historicalRuntimeIntervalsV7, historicalRuntimeEventsV7,
		`CREATE INDEX runtime_events_interval_session_idx ON runtime_events(interval_id, source_session_id, ordinal)`,
		`CREATE INDEX runtime_events_interval_message_part_idx ON runtime_events(interval_id, message_id, part_id, ordinal)`,
	},
	{
		`ALTER TABLE runtime_event_intervals ADD COLUMN contradiction_count INTEGER NOT NULL DEFAULT 0 CHECK (contradiction_count >= 0)`,
		`ALTER TABLE runtime_events RENAME TO runtime_events_v7`, createRuntimeEventsTable,
		`INSERT INTO runtime_events (interval_id,ordinal,epoch_id,epoch_ordinal,kind,event_type,source_session_id,message_id,part_id,captured_at,model_step_classification)
		 SELECT interval_id,ordinal,epoch_id,epoch_ordinal,kind,event_type,source_session_id,message_id,part_id,captured_at,CASE WHEN kind='model_step' THEN 'accepted' ELSE NULL END FROM runtime_events_v7`,
		`DROP TABLE runtime_events_v7`, createRuntimeEventIndexes, createRuntimeEventMessageIndex,
	},
	{createCompressionDispositionsTable},
	{
		`ALTER TABLE tool_calls ADD COLUMN latency_state TEXT NOT NULL DEFAULT 'unknown'`,
		`ALTER TABLE tool_calls ADD COLUMN latency_unit TEXT NOT NULL DEFAULT 'milliseconds'`,
		`ALTER TABLE tool_calls ADD COLUMN latency_authority TEXT NOT NULL DEFAULT 'unknown'`,
	},
	{`ALTER TABLE tool_calls ADD COLUMN producer TEXT`, `ALTER TABLE compressed ADD COLUMN producer TEXT`},
	{`ALTER TABLE sessions ADD COLUMN agent_attribution_authority TEXT NOT NULL DEFAULT 'unknown' CHECK (agent_attribution_authority IN ('identified','unknown'))`},
	{createCollectionIntervalsTable, createIngressContinuityTable},
	{createRuntimeSourceObservationsTable, createRecoveryCueDeliveriesTable, createRecoveryAttemptsTable},
	{
		`ALTER TABLE tool_calls ADD COLUMN timestamp_unix_nano INTEGER`,
		`ALTER TABLE session_telemetry ADD COLUMN captured_at_unix_nano INTEGER`,
		`CREATE INDEX tool_calls_timestamp_unix_nano_idx ON tool_calls(timestamp_unix_nano)`,
		`CREATE INDEX session_telemetry_runtime_time_idx ON session_telemetry(runtime, runtime_version, captured_at_unix_nano)`,
	},
}

func TestHistoricalSchemaPhysicalShapeMatrix(t *testing.T) {
	for version := 0; version < schemaVersion; version++ {
		version := version
		t.Run(fmt.Sprintf("schema-%d", version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "historical.db")
			raw := openHistoricalSchema(t, path, version)
			assertHistoricalSchemaShape(t, raw, version)
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}

			migrated, err := Open(path)
			if err != nil {
				t.Fatalf("migrate schema %d: %v", version, err)
			}
			defer migrated.Close()
			var got int
			if err := migrated.db.QueryRow(`PRAGMA user_version`).Scan(&got); err != nil || got != schemaVersion {
				t.Fatalf("migrated user_version = %d, %v; want %d", got, err, schemaVersion)
			}
			if err := checkRequiredColumns(migrated.db); err != nil {
				t.Fatalf("migrated schema %d: %v", version, err)
			}
		})
	}
}

func openHistoricalSchema(t *testing.T, path string, version int) *sql.DB {
	t.Helper()
	if version < 0 || version >= schemaVersion {
		t.Fatalf("historical schema version %d is outside 0-%d", version, schemaVersion-1)
	}
	raw, err := sql.Open(sqliteDriver, "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	for _, statement := range historicalSchemaBase {
		if _, err := raw.Exec(statement); err != nil {
			raw.Close()
			t.Fatalf("create schema 0: %v", err)
		}
	}
	for current := 1; current <= version; current++ {
		tx, err := raw.Begin()
		if err != nil {
			raw.Close()
			t.Fatal(err)
		}
		for _, statement := range historicalSchemaMigrations[current] {
			if _, err := tx.Exec(statement); err != nil {
				_ = tx.Rollback()
				raw.Close()
				t.Fatalf("apply historical schema %d: %v", current, err)
			}
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, current)); err != nil {
			_ = tx.Rollback()
			raw.Close()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			raw.Close()
			t.Fatal(err)
		}
	}
	return raw
}

func assertHistoricalSchemaShape(t *testing.T, raw *sql.DB, version int) {
	t.Helper()
	var gotVersion int
	if err := raw.QueryRow(`PRAGMA user_version`).Scan(&gotVersion); err != nil || gotVersion != version {
		t.Fatalf("user_version = %d, %v; want %d", gotVersion, err, version)
	}
	wantColumns, wantPK := historicalShape(version)
	wantTables := sortedMapKeys(wantColumns)
	if got := schemaObjectNames(t, raw, "table"); !reflect.DeepEqual(got, wantTables) {
		t.Fatalf("schema %d tables = %v, want %v", version, got, wantTables)
	}
	for table, want := range wantColumns {
		got, pk := schemaColumns(t, raw, table)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("schema %d %s columns = %v, want %v", version, table, got, want)
		}
		if !reflect.DeepEqual(pk, wantPK[table]) {
			t.Errorf("schema %d %s primary key = %v, want %v", version, table, pk, wantPK[table])
		}
	}
	if got, want := schemaObjectNames(t, raw, "index"), historicalIndexes(version); !reflect.DeepEqual(got, want) {
		t.Fatalf("schema %d indexes = %v, want %v", version, got, want)
	}
}

func historicalShape(version int) (map[string][]string, map[string][]string) {
	columns := map[string][]string{
		"bench_runs":       {"id", "agent", "model", "task", "score", "tokens_in", "tokens_out", "cost", "methodology_version", "timestamp"},
		"compressed":       {"id", "source_id", "output", "content_hash", "pipeline", "timestamp"},
		"recorded_streams": {"id", "session_id", "bench_run_id", "stream_text", "content_hash", "stream_format", "captured_at"},
		"sessions":         {"id", "agent", "started_at", "ended_at", "last_seen_at", "tool_call_count"},
		"tool_calls":       {"id", "session_id", "tool", "input", "output", "content_hash", "timestamp", "event_type", "tokens_in", "tokens_out", "cost", "latency_ms", "model", "provider", "exit_code", "is_error", "runtime"},
	}
	pk := map[string][]string{
		"bench_runs": {"id"}, "compressed": {"id"}, "recorded_streams": {"id"}, "sessions": {"id"}, "tool_calls": {"id"},
	}
	if version >= 2 {
		columns["bench_runs"] = append(columns["bench_runs"], "cache_read", "cache_write")
	}
	if version >= 3 {
		columns["bench_runs"] = append(columns["bench_runs"], "reasoning", "estimated_api_cost")
	}
	if version >= 4 {
		columns["session_telemetry"] = []string{"session_id", "source_session_id", "message_id", "part_id", "ordinal", "reason", "cost", "tokens_input", "tokens_output", "tokens_reasoning", "cache_read", "cache_write", "provider", "model", "runtime", "runtime_version", "captured_at"}
		pk["session_telemetry"] = []string{"source_session_id", "message_id", "part_id"}
	}
	if version >= 5 {
		pk["session_telemetry"] = []string{"runtime", "source_session_id", "message_id", "part_id"}
	}
	if version >= 6 {
		columns["session_telemetry"] = []string{"session_id", "source_session_id", "message_id", "part_id", "ordinal", "reason", "cost", "tokens_input", "tokens_output", "tokens_reasoning", "cache_read", "cache_write", "provider", "model", "runtime", "runtime_version", "source", "captured_at", "cost_state", "tokens_input_state", "tokens_output_state", "tokens_reasoning_state", "cache_read_state", "cache_write_state"}
	}
	if version >= 7 {
		columns["runtime_event_intervals"] = []string{"id", "session_id", "attempt_id", "runtime", "runtime_version", "source", "contract_version", "opened_at", "closed_at", "close_state", "first_ordinal", "last_ordinal", "event_count", "collector_epoch_count", "failure_reason"}
		columns["runtime_events"] = []string{"interval_id", "ordinal", "epoch_id", "epoch_ordinal", "kind", "event_type", "source_session_id", "message_id", "part_id", "captured_at"}
		pk["runtime_event_intervals"] = []string{"id"}
		pk["runtime_events"] = []string{"interval_id", "ordinal"}
	}
	if version >= 8 {
		columns["runtime_event_intervals"] = append(columns["runtime_event_intervals"], "contradiction_count")
		columns["runtime_events"] = append(columns["runtime_events"], "model_step_classification")
	}
	if version >= 9 {
		columns["compression_dispositions"] = []string{"source_id", "profile", "route", "disposition", "artifact_id", "observed_at"}
		pk["compression_dispositions"] = []string{"source_id"}
	}
	if version >= 10 {
		columns["tool_calls"] = append(columns["tool_calls"], "latency_state", "latency_unit", "latency_authority")
	}
	if version >= 11 {
		columns["tool_calls"] = append(columns["tool_calls"], "producer")
		columns["compressed"] = append(columns["compressed"], "producer")
	}
	if version >= 12 {
		columns["sessions"] = append(columns["sessions"], "agent_attribution_authority")
	}
	if version >= 13 {
		columns["collection_intervals"] = []string{"id", "database_target", "supported_ingresses", "opened_at"}
		columns["ingress_continuity"] = []string{"interval_id", "ingress", "instance_id", "state", "observed_at", "authority"}
		pk["collection_intervals"] = []string{"id"}
		pk["ingress_continuity"] = []string{"interval_id", "ingress", "instance_id", "state", "observed_at"}
	}
	if version >= 14 {
		columns["runtime_source_observations"] = []string{"collection_id", "session_id", "source_id", "latency_ms", "latency_state", "latency_unit", "latency_authority", "delivered_bytes", "recovery_cue_observed", "observed_at"}
		columns["recovery_cue_deliveries"] = []string{"collection_id", "source_id", "artifact_id", "session_id", "runtime_session_id", "runtime_call_id", "delivered_at", "authority"}
		columns["recovery_attempts"] = []string{"attempt_source_id", "collection_id", "cue_source_id", "attempted_at", "result_state", "byte_exact", "authority"}
		pk["runtime_source_observations"] = []string{"collection_id", "source_id"}
		pk["recovery_cue_deliveries"] = []string{"collection_id", "source_id"}
		pk["recovery_attempts"] = []string{"attempt_source_id"}
	}
	if version >= 15 {
		columns["tool_calls"] = append(columns["tool_calls"], "timestamp_unix_nano")
		columns["session_telemetry"] = append(columns["session_telemetry"], "captured_at_unix_nano")
	}
	return columns, pk
}

func historicalIndexes(version int) []string {
	indexes := []string{"compressed_source_id_idx", "recorded_streams_bench_run_id_idx", "recorded_streams_session_id_idx"}
	if version >= 4 {
		indexes = append(indexes, "session_telemetry_session_order_idx")
	}
	if version >= 7 {
		indexes = append(indexes, "runtime_events_interval_message_part_idx", "runtime_events_interval_session_idx")
	}
	if version >= 15 {
		indexes = append(indexes, "session_telemetry_runtime_time_idx", "tool_calls_timestamp_unix_nano_idx")
	}
	sort.Strings(indexes)
	return indexes
}

func schemaObjectNames(t *testing.T, raw *sql.DB, objectType string) []string {
	t.Helper()
	rows, err := raw.Query(`SELECT name FROM sqlite_master WHERE type = ? AND name NOT LIKE 'sqlite_%' AND sql IS NOT NULL ORDER BY name`, objectType)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}

func schemaColumns(t *testing.T, raw *sql.DB, table string) ([]string, []string) {
	t.Helper()
	rows, err := raw.Query(`SELECT name, pk FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	primary := map[int]string{}
	for rows.Next() {
		var name string
		var position int
		if err := rows.Scan(&name, &position); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
		if position > 0 {
			primary[position] = name
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	pk := make([]string, len(primary))
	for position, name := range primary {
		pk[position-1] = name
	}
	return columns, pk
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
