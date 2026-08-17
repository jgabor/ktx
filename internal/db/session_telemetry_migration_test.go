package db

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const legacySessionTelemetryTable = `CREATE TABLE session_telemetry (
	session_id TEXT NOT NULL REFERENCES sessions(id), source_session_id TEXT NOT NULL,
	message_id TEXT NOT NULL, part_id TEXT NOT NULL, ordinal INTEGER NOT NULL,
	reason TEXT NOT NULL, cost REAL NOT NULL, tokens_input INTEGER NOT NULL,
	tokens_output INTEGER NOT NULL, tokens_reasoning INTEGER NOT NULL,
	cache_read INTEGER NOT NULL, cache_write INTEGER NOT NULL, provider TEXT, model TEXT,
	runtime TEXT NOT NULL, runtime_version TEXT NOT NULL, captured_at TEXT NOT NULL,
	PRIMARY KEY (source_session_id, message_id, part_id))`

const telemetryLegacyColumns = `session_id,source_session_id,message_id,part_id,ordinal,reason,cost,tokens_input,tokens_output,tokens_reasoning,cache_read,cache_write,provider,model,runtime,runtime_version,captured_at`

const historicalSessionTelemetryV5Table = `CREATE TABLE session_telemetry (
	session_id TEXT NOT NULL REFERENCES sessions(id), source_session_id TEXT NOT NULL,
	message_id TEXT NOT NULL, part_id TEXT NOT NULL, ordinal INTEGER NOT NULL,
	reason TEXT NOT NULL, cost REAL, tokens_input INTEGER, tokens_output INTEGER,
	tokens_reasoning INTEGER, cache_read INTEGER, cache_write INTEGER, provider TEXT, model TEXT,
	runtime TEXT NOT NULL, runtime_version TEXT NOT NULL, captured_at TEXT NOT NULL,
	PRIMARY KEY (runtime, source_session_id, message_id, part_id))`

func TestSessionTelemetrySchemaV6FreshAndHistorical(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		assertSessionTelemetryV6Shape(t, d.db)
	})

	for version := 0; version <= 3; version++ {
		t.Run(fmt.Sprintf("version-%d", version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "historical.db")
			raw := openHistoricalTelemetryFixture(t, path, version)
			before := snapshotHistoricalEvidence(t, raw, version)
			raw.Close()
			d, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { d.Close() })
			assertSessionTelemetryV6Shape(t, d.db)
			if count := tableRowCount(t, d.db, "sessions"); count != 2 {
				t.Fatalf("sessions = %d, want 2", count)
			}
			if got := snapshotHistoricalEvidence(t, d.db, version); !reflect.DeepEqual(got, before) {
				t.Fatalf("historical evidence changed:\n got %#v\nwant %#v", got, before)
			}
		})
	}

	t.Run("empty-version-4", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty-v4.db")
		raw := openHistoricalTelemetryFixtureWithoutTelemetryRows(t, path, 4)
		raw.Close()
		d, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		assertSessionTelemetryV6Shape(t, d.db)
	})
}

func TestSessionTelemetryV4MigrationPreservesAllEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "populated-v4.db")
	raw := openHistoricalTelemetryFixture(t, path, 4)
	legacyColumns := `session_id,source_session_id,message_id,part_id,ordinal,reason,cost,tokens_input,tokens_output,tokens_reasoning,cache_read,cache_write,provider,model,runtime,runtime_version,captured_at`
	beforeTelemetry := queryStrings(t, raw, `SELECT `+legacyColumns+` FROM session_telemetry`)
	beforeUnrelated := snapshotUnrelatedEvidence(t, raw)
	raw.Close()

	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	assertSessionTelemetryV6Shape(t, d.db)
	if got := queryStrings(t, d.db, `SELECT `+legacyColumns+` FROM session_telemetry`); !reflect.DeepEqual(got, beforeTelemetry) {
		t.Fatalf("telemetry changed:\n got %#v\nwant %#v", got, beforeTelemetry)
	}
	if got := queryStrings(t, d.db, `SELECT source,cost_state,tokens_input_state,tokens_output_state,tokens_reasoning_state,cache_read_state,cache_write_state FROM session_telemetry`); !reflect.DeepEqual(got, [][]string{{"legacy", "measured", "measured", "measured", "measured", "measured", "measured"}}) {
		t.Fatalf("migrated state/provenance = %#v", got)
	}
	if got := snapshotUnrelatedEvidence(t, d.db); !reflect.DeepEqual(got, beforeUnrelated) {
		t.Fatalf("unrelated evidence changed:\n got %#v\nwant %#v", got, beforeUnrelated)
	}
}

func TestSessionTelemetryV6MigrationRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runtime string
		version string
		inject  bool
		orphan  bool
	}{
		{name: "blank-runtime", runtime: " ", version: "1"},
		{name: "blank-runtime-version", runtime: "opencode", version: "\t"},
		{name: "copy-failure", runtime: "opencode", version: "1", orphan: true},
		{name: "injected-copy-failure", runtime: "opencode", version: "1", inject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rollback.db")
			raw := openHistoricalTelemetryFixtureWithoutTelemetryRows(t, path, 4)
			owner := "aaaaaaaaaaaaaaaa"
			if tc.orphan {
				owner = "zzzzzzzzzzzzzzzz"
				if _, err := raw.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := raw.Exec(`INSERT INTO session_telemetry VALUES
				('aaaaaaaaaaaaaaaa','native-session','message','part',7,'stop',1.25,10,20,30,40,50,NULL,NULL,?,?, 'captured')`, tc.runtime, tc.version); err != nil {
				t.Fatal(err)
			}
			if tc.orphan {
				if _, err := raw.Exec(`UPDATE session_telemetry SET session_id = ?; PRAGMA foreign_keys = ON`, owner); err != nil {
					t.Fatal(err)
				}
			}
			beforeSQL := tableSQL(t, raw, "session_telemetry")
			beforeTelemetry := queryStrings(t, raw, `SELECT * FROM session_telemetry`)
			beforeUnrelated := snapshotUnrelatedEvidence(t, raw)
			var err error
			if tc.inject {
				err = migrateWith(raw, func(tx *sql.Tx) error {
					if err := migrateTx(tx); err != nil {
						return err
					}
					return errors.New("injected migration failure")
				})
			} else {
				err = migrate(raw)
			}
			if err == nil {
				t.Fatal("migration succeeded")
			}
			if got := tableSQL(t, raw, "session_telemetry"); got != beforeSQL {
				t.Fatalf("table shape changed:\n got %s\nwant %s", got, beforeSQL)
			}
			if got := queryStrings(t, raw, `SELECT * FROM session_telemetry`); !reflect.DeepEqual(got, beforeTelemetry) {
				t.Fatalf("telemetry changed: got %#v want %#v", got, beforeTelemetry)
			}
			if got := snapshotUnrelatedEvidence(t, raw); !reflect.DeepEqual(got, beforeUnrelated) {
				t.Fatalf("unrelated evidence changed: got %#v want %#v", got, beforeUnrelated)
			}
			var version int
			if err := raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 4 {
				t.Fatalf("user_version = %d, %v; want 4", version, err)
			}
			raw.Close()
		})
	}
}

func openHistoricalTelemetryFixture(t *testing.T, path string, version int) *sql.DB {
	return openHistoricalTelemetryFixtureWithRows(t, path, version, true)
}

func openHistoricalTelemetryFixtureWithoutTelemetryRows(t *testing.T, path string, version int) *sql.DB {
	return openHistoricalTelemetryFixtureWithRows(t, path, version, false)
}

func openHistoricalTelemetryFixtureWithRows(t *testing.T, path string, version int, populateTelemetry bool) *sql.DB {
	t.Helper()
	raw, err := sql.Open(sqliteDriver, "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)

	benchExtra := ""
	benchInsertColumns := "id,agent,model,task,score,tokens_in,tokens_out,cost,methodology_version,timestamp"
	benchInsertValues := "'eeeeeeeeeeeeeeee','bench-agent','bench-model','bench-task',1.25,211,322,4.75,'method-v',1700000001"
	if version >= 2 {
		benchExtra = ", cache_read INTEGER, cache_write INTEGER"
		benchInsertColumns += ",cache_read,cache_write"
		benchInsertValues += ",433,544"
	}
	if version >= 3 {
		benchExtra += ", reasoning INTEGER, estimated_api_cost REAL"
		benchInsertColumns += ",reasoning,estimated_api_cost"
		benchInsertValues += ",655,6.875"
	}
	statements := []string{
		`CREATE TABLE sessions (
			id TEXT PRIMARY KEY, agent TEXT, started_at TEXT, ended_at TEXT,
			last_seen_at TEXT, tool_call_count INTEGER DEFAULT 0)`,
		`CREATE TABLE tool_calls (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), tool TEXT,
			input TEXT, output TEXT, content_hash TEXT, timestamp TEXT,
			event_type TEXT DEFAULT 'tool_call', tokens_in INTEGER, tokens_out INTEGER,
			cost REAL, latency_ms INTEGER, model TEXT, provider TEXT,
			exit_code INTEGER, is_error INTEGER, runtime TEXT)`,
		`CREATE TABLE bench_runs (
			id TEXT PRIMARY KEY, agent TEXT, model TEXT, task TEXT, score REAL,
			tokens_in INTEGER, tokens_out INTEGER, cost REAL,
			methodology_version TEXT, timestamp INTEGER` + benchExtra + `)`,
		`CREATE TABLE compressed (
			id TEXT PRIMARY KEY, source_id TEXT NOT NULL REFERENCES tool_calls(id),
			output TEXT, content_hash TEXT, pipeline TEXT, timestamp TEXT)`,
		`CREATE TABLE recorded_streams (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id),
			bench_run_id TEXT REFERENCES bench_runs(id), stream_text TEXT,
			content_hash TEXT, stream_format TEXT, captured_at TEXT)`,
	}
	if version == 4 {
		statements = append(statements, legacySessionTelemetryTable,
			`CREATE INDEX session_telemetry_session_order_idx ON session_telemetry(session_id, ordinal)`)
	}
	if version == 5 {
		statements = append(statements, historicalSessionTelemetryV5Table,
			`CREATE INDEX session_telemetry_session_order_idx ON session_telemetry(session_id, ordinal, runtime, source_session_id, message_id, part_id)`)
	}
	if version == 6 {
		statements = append(statements, createSessionTelemetryTable, createSessionTelemetryOrderIndex)
	}
	for _, statement := range statements {
		if _, err := raw.Exec(statement); err != nil {
			raw.Close()
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec(`
		INSERT INTO sessions VALUES
			('aaaaaaaaaaaaaaaa','session-agent','2026-01-02T03:04:05Z','2026-01-02T04:05:06Z','2026-01-02T05:06:07Z',17),
			('bbbbbbbbbbbbbbbb',NULL,NULL,NULL,NULL,NULL);
		INSERT INTO tool_calls (id,session_id,tool,input,output,content_hash,timestamp,event_type,tokens_in,tokens_out,cost,latency_ms,model,provider,exit_code,is_error,runtime)
			VALUES
			('cccccccccccccccc','aaaaaaaaaaaaaaaa','shell-tool','input-text','output-text','tool-hash','2026-02-03T04:05:06Z','tool-result',101,202,3.125,404,'tool-model','tool-provider',23,1,'tool-runtime'),
			('hhhhhhhhhhhhhhhh','bbbbbbbbbbbbbbbb',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL);
		INSERT INTO bench_runs (` + benchInsertColumns + `) VALUES
			(` + benchInsertValues + `),
			('gggggggggggggggg',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL` + historicalBenchNullTail(version) + `);
		INSERT INTO compressed VALUES
			('dddddddddddddddd','cccccccccccccccc','compressed-output','compressed-hash','["normalize","head"]','2026-03-04T05:06:07Z'),
			('iiiiiiiiiiiiiiii','hhhhhhhhhhhhhhhh',NULL,NULL,NULL,NULL);
		INSERT INTO recorded_streams VALUES
			('ffffffffffffffff','aaaaaaaaaaaaaaaa','eeeeeeeeeeeeeeee','stream-text','stream-hash','runtime-jsonl-v1','2026-04-05T06:07:08Z'),
			('jjjjjjjjjjjjjjjj','bbbbbbbbbbbbbbbb',NULL,NULL,NULL,NULL,NULL);
		PRAGMA user_version = ` + fmt.Sprint(version)); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if version >= 4 && populateTelemetry {
		if _, err := raw.Exec(`INSERT INTO session_telemetry (` + telemetryLegacyColumns + `) VALUES
			('aaaaaaaaaaaaaaaa','native-session','message','part',7,'stop',1.25,10,20,30,40,50,'provider','model','opencode','1','captured')`); err != nil {
			raw.Close()
			t.Fatal(err)
		}
	}
	return raw
}

func historicalBenchNullTail(version int) string {
	if version >= 3 {
		return ",NULL,NULL,NULL,NULL"
	}
	if version >= 2 {
		return ",NULL,NULL"
	}
	return ""
}

func snapshotHistoricalEvidence(t *testing.T, db *sql.DB, version int) [][]string {
	t.Helper()
	benchColumns := []string{"id", "agent", "model", "task", "score", "tokens_in", "tokens_out", "cost", "methodology_version", "timestamp"}
	if version >= 2 {
		benchColumns = append(benchColumns, "cache_read", "cache_write")
	}
	if version >= 3 {
		benchColumns = append(benchColumns, "reasoning", "estimated_api_cost")
	}
	tables := []struct {
		name    string
		columns []string
	}{
		{name: "sessions", columns: []string{"id", "agent", "started_at", "ended_at", "last_seen_at", "tool_call_count"}},
		{name: "tool_calls", columns: []string{"id", "session_id", "tool", "input", "output", "content_hash", "timestamp", "event_type", "tokens_in", "tokens_out", "cost", "latency_ms", "model", "provider", "exit_code", "is_error", "runtime"}},
		{name: "bench_runs", columns: benchColumns},
		{name: "compressed", columns: []string{"id", "source_id", "output", "content_hash", "pipeline", "timestamp"}},
		{name: "recorded_streams", columns: []string{"id", "session_id", "bench_run_id", "stream_text", "content_hash", "stream_format", "captured_at"}},
	}
	var snapshot [][]string
	for _, table := range tables {
		fields := make([]string, 0, len(table.columns)*2)
		for _, column := range table.columns {
			fields = append(fields, "typeof("+column+")", "quote("+column+")")
		}
		query := "SELECT " + strings.Join(fields, ",") + " FROM " + table.name + " ORDER BY id"
		snapshot = append(snapshot, queryStrings(t, db, query)...)
	}
	if version >= 4 {
		snapshot = append(snapshot, queryStrings(t, db, `SELECT `+telemetryLegacyColumns+` FROM session_telemetry ORDER BY runtime,source_session_id,message_id,part_id`)...)
	}
	return snapshot
}

func assertSessionTelemetryV6Shape(t *testing.T, db *sql.DB) {
	t.Helper()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("user_version = %d, %v; want %d", version, err, schemaVersion)
	}
	rows, err := db.Query(`SELECT name, pk FROM pragma_table_info('session_telemetry') WHERE pk > 0 ORDER BY pk`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var primaryKey []string
	for rows.Next() {
		var name string
		var position int
		if err := rows.Scan(&name, &position); err != nil {
			t.Fatal(err)
		}
		primaryKey = append(primaryKey, name)
	}
	if want := []string{"runtime", "source_session_id", "message_id", "part_id"}; !reflect.DeepEqual(primaryKey, want) {
		t.Fatalf("primary key = %v, want %v", primaryKey, want)
	}
	var indexSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='session_telemetry_session_order_idx'`).Scan(&indexSQL); err != nil {
		t.Fatal(err)
	}
	wantColumns := "(session_id, ordinal, runtime, source_session_id, message_id, part_id)"
	if !strings.Contains(indexSQL, wantColumns) {
		t.Fatalf("ordering index = %q, want columns %s", indexSQL, wantColumns)
	}
	columns, err := tableColumns(db, "session_telemetry")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"source", "cost_state", "tokens_input_state", "tokens_output_state", "tokens_reasoning_state", "cache_read_state", "cache_write_state"} {
		if _, ok := columns[name]; !ok {
			t.Errorf("missing column %q", name)
		}
	}
}

func snapshotUnrelatedEvidence(t *testing.T, db *sql.DB) [][]string {
	t.Helper()
	var snapshot [][]string
	for _, query := range []string{
		`SELECT id,agent,started_at,ended_at,last_seen_at,tool_call_count FROM sessions ORDER BY id`, `SELECT id,session_id,tool,input,output,content_hash,timestamp,event_type,tokens_in,tokens_out,cost,latency_ms,model,provider,exit_code,is_error,runtime FROM tool_calls ORDER BY id`,
		`SELECT id,source_id,output,content_hash,pipeline,timestamp FROM compressed ORDER BY id`, `SELECT * FROM bench_runs ORDER BY id`,
		`SELECT * FROM recorded_streams ORDER BY id`,
	} {
		snapshot = append(snapshot, queryStrings(t, db, query)...)
	}
	return snapshot
}

func queryStrings(t *testing.T, db *sql.DB, query string) [][]string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]string
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		row := make([]string, len(values))
		for i, value := range values {
			row[i] = fmt.Sprintf("%v", value)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func tableSQL(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	var sqlText string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&sqlText); err != nil {
		t.Fatal(err)
	}
	return sqlText
}
