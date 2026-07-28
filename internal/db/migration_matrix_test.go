package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type migrationMatrixCase struct {
	name   string
	seed   func(*testing.T, string) migrationInvariant
	verify func(*testing.T, *sql.DB, migrationInvariant)
}

type migrationInvariant struct {
	version  int
	evidence [][]string
}

func TestForwardMigrationMatrix(t *testing.T) {
	cases := []migrationMatrixCase{
		{name: "schema-0 legacy benchmark", seed: seedHistoricalMatrix(0), verify: verifyHistoricalMatrix},
		{name: "schema-1 pre-cache benchmark", seed: seedHistoricalMatrix(1), verify: verifyHistoricalMatrix},
		{name: "schema-2 cache columns", seed: seedHistoricalMatrix(2), verify: verifyHistoricalMatrix},
		{name: "schema-3 benchmark cost fields", seed: seedHistoricalMatrix(3), verify: verifyHistoricalMatrix},
		{name: "schema-4 unqualified telemetry key", seed: seedHistoricalMatrix(4), verify: verifyHistoricalMatrix},
		{name: "schema-5 runtime-qualified telemetry", seed: seedHistoricalMatrix(5), verify: verifyHistoricalMatrix},
		{name: "schema-6 telemetry field states", seed: seedHistoricalMatrix(6), verify: verifyHistoricalMatrix},
		{name: "schema-7 runtime event classification", seed: seedVersion7Matrix, verify: verifyVersion7Matrix},
		{name: "recorded stream required benchmark link", seed: seedRecordedStreamsNotNullMatrix, verify: verifyRecordedStreamsNotNullMatrix},
		{name: "partial additive tool-call fields", seed: seedPartialToolCallsMatrix, verify: verifyPartialToolCallsMatrix},
		{name: "partial additive benchmark fields", seed: seedPartialBenchRunsMatrix, verify: verifyPartialBenchRunsMatrix},
		{name: "partial additive compressed fields", seed: seedPartialCompressedMatrix, verify: verifyPartialCompressedMatrix},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "historical.db")
			invariant := test.seed(t, path)
			database, err := Open(path)
			if err != nil {
				t.Fatalf("migrate schema %d: %v", invariant.version, err)
			}
			defer database.Close()
			var version int
			if err := database.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
				t.Fatalf("user_version = %d, %v; want %d", version, err, schemaVersion)
			}
			test.verify(t, database.db, invariant)
			var marker string
			if err := database.db.QueryRow(`SELECT value FROM migration_matrix_marker`).Scan(&marker); err != nil || marker != "preserved" {
				t.Fatalf("migration marker = %q, %v", marker, err)
			}
		})
	}
}

func TestCompressionDispositionMigrationDoesNotBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-8.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sourceID := seedSessionAndCalls(t, database, []callFixture{{id: "migrationdisposx", output: "original"}})[0]
	seedCompressedWithID(t, database, "migrationartifx", sourceID, "short", "2026-07-21T12:00:00Z")
	if _, err := database.db.Exec(`DROP TABLE IF EXISTS compression_dispositions; PRAGMA user_version = 8`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if MaxSupportedSchema() != 9 {
		t.Fatalf("MaxSupportedSchema = %d, want 9", MaxSupportedSchema())
	}
	var rows int
	if err := database.db.QueryRow(`SELECT count(*) FROM compression_dispositions`).Scan(&rows); err != nil {
		t.Fatalf("query compression_dispositions: %v", err)
	}
	if rows != 0 {
		t.Fatalf("migrated disposition rows = %d, want 0 (no historical inference)", rows)
	}
}

func seedHistoricalMatrix(version int) func(*testing.T, string) migrationInvariant {
	return func(t *testing.T, path string) migrationInvariant {
		raw := openHistoricalTelemetryFixture(t, path, version)
		addMigrationMarker(t, raw)
		invariant := migrationInvariant{version: version, evidence: snapshotHistoricalEvidence(t, raw, version)}
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
		return invariant
	}
}

func verifyHistoricalMatrix(t *testing.T, database *sql.DB, invariant migrationInvariant) {
	t.Helper()
	if got := snapshotHistoricalEvidence(t, database, invariant.version); !reflect.DeepEqual(got, invariant.evidence) {
		t.Fatalf("historical evidence changed:\n got %#v\nwant %#v", got, invariant.evidence)
	}
	if invariant.version >= 4 {
		assertSessionTelemetryV6Shape(t, database)
	}
}

func seedVersion7Matrix(t *testing.T, path string) migrationInvariant {
	raw := openHistoricalTelemetryFixture(t, path, 6)
	if _, err := raw.Exec(`CREATE TABLE runtime_event_intervals (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL UNIQUE REFERENCES sessions(id), attempt_id TEXT NOT NULL UNIQUE,
			runtime TEXT NOT NULL, runtime_version TEXT NOT NULL, source TEXT NOT NULL CHECK (source = 'event_hook'),
			contract_version TEXT NOT NULL, opened_at TEXT NOT NULL, closed_at TEXT,
			close_state TEXT NOT NULL CHECK (close_state IN ('open','complete','incomplete','contradictory')),
			first_ordinal INTEGER, last_ordinal INTEGER, event_count INTEGER NOT NULL DEFAULT 0,
			collector_epoch_count INTEGER NOT NULL DEFAULT 0, failure_reason TEXT);
		CREATE TABLE runtime_events (
			interval_id TEXT NOT NULL REFERENCES runtime_event_intervals(id), ordinal INTEGER NOT NULL,
			epoch_id TEXT NOT NULL, epoch_ordinal INTEGER NOT NULL, kind TEXT NOT NULL, event_type TEXT NOT NULL,
			source_session_id TEXT, message_id TEXT, part_id TEXT, captured_at TEXT NOT NULL,
			PRIMARY KEY (interval_id, ordinal), UNIQUE (interval_id, epoch_id, epoch_ordinal));
		INSERT INTO runtime_event_intervals VALUES
			('kkkkkkkkkkkkkkkk','aaaaaaaaaaaaaaaa','attempt','opencode','1','event_hook','contract','open','closed','complete',1,1,1,1,NULL);
		INSERT INTO runtime_events VALUES
			('kkkkkkkkkkkkkkkk',1,'epoch',1,'model_step','message.part.updated','runtime-session','message','part','captured');
		PRAGMA user_version = 7`); err != nil {
		t.Fatal(err)
	}
	return closeMatrixFixture(t, raw, 7, queryStrings(t, raw, `SELECT interval_id,ordinal,kind,event_type,source_session_id,message_id,part_id,captured_at FROM runtime_events`))
}

func seedRecordedStreamsNotNullMatrix(t *testing.T, path string) migrationInvariant {
	raw := openHistoricalTelemetryFixture(t, path, 3)
	if _, err := raw.Exec(`ALTER TABLE recorded_streams RENAME TO recorded_streams_nullable;
		CREATE TABLE recorded_streams (
			id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id),
			bench_run_id TEXT NOT NULL REFERENCES bench_runs(id), stream_text TEXT,
			content_hash TEXT, stream_format TEXT, captured_at TEXT);
		INSERT INTO recorded_streams SELECT * FROM recorded_streams_nullable WHERE bench_run_id IS NOT NULL;
		DROP TABLE recorded_streams_nullable`); err != nil {
		t.Fatal(err)
	}
	evidence := queryStrings(t, raw, `SELECT id,session_id,bench_run_id,stream_text,content_hash,stream_format,captured_at FROM recorded_streams`)
	return closeMatrixFixture(t, raw, 3, evidence)
}

func verifyRecordedStreamsNotNullMatrix(t *testing.T, database *sql.DB, invariant migrationInvariant) {
	t.Helper()
	got := queryStrings(t, database, `SELECT id,session_id,bench_run_id,stream_text,content_hash,stream_format,captured_at FROM recorded_streams`)
	if !reflect.DeepEqual(got, invariant.evidence) {
		t.Fatalf("recorded stream changed: got %#v want %#v", got, invariant.evidence)
	}
	var notNull int
	if err := database.QueryRow(`SELECT "notnull" FROM pragma_table_info('recorded_streams') WHERE name='bench_run_id'`).Scan(&notNull); err != nil || notNull != 0 {
		t.Fatalf("bench_run_id notnull = %d, %v; want nullable", notNull, err)
	}
	var linked int
	if err := database.QueryRow(`SELECT count(*) FROM recorded_streams r
		JOIN sessions s ON s.id=r.session_id JOIN bench_runs b ON b.id=r.bench_run_id`).Scan(&linked); err != nil || linked != 1 {
		t.Fatalf("linked recorded streams = %d, %v; want 1", linked, err)
	}
}

func verifyVersion7Matrix(t *testing.T, database *sql.DB, invariant migrationInvariant) {
	t.Helper()
	got := queryStrings(t, database, `SELECT interval_id,ordinal,kind,event_type,source_session_id,message_id,part_id,captured_at FROM runtime_events`)
	if !reflect.DeepEqual(got, invariant.evidence) {
		t.Fatalf("runtime events changed: got %#v want %#v", got, invariant.evidence)
	}
	if classification := queryStrings(t, database, `SELECT model_step_classification FROM runtime_events`); !reflect.DeepEqual(classification, [][]string{{"accepted"}}) {
		t.Fatalf("runtime event classification = %#v", classification)
	}
}

func seedPartialToolCallsMatrix(t *testing.T, path string) migrationInvariant {
	raw := openHistoricalTelemetryFixture(t, path, 6)
	if _, err := raw.Exec(`ALTER TABLE tool_calls DROP COLUMN tokens_in;
		ALTER TABLE tool_calls DROP COLUMN exit_code;
		ALTER TABLE tool_calls DROP COLUMN runtime;
		PRAGMA user_version = 7`); err != nil {
		t.Fatal(err)
	}
	return closeMatrixFixture(t, raw, 7, queryStrings(t, raw, `SELECT id,session_id,tool,input,output,content_hash,timestamp FROM tool_calls`))
}

func verifyPartialToolCallsMatrix(t *testing.T, database *sql.DB, invariant migrationInvariant) {
	t.Helper()
	if got := queryStrings(t, database, `SELECT id,session_id,tool,input,output,content_hash,timestamp FROM tool_calls`); !reflect.DeepEqual(got, invariant.evidence) {
		t.Fatalf("tool-call evidence changed: got %#v want %#v", got, invariant.evidence)
	}
	assertMatrixColumns(t, database, "tool_calls", "tokens_in", "exit_code", "runtime")
}

func seedPartialBenchRunsMatrix(t *testing.T, path string) migrationInvariant {
	raw := openHistoricalTelemetryFixture(t, path, 6)
	if _, err := raw.Exec(`ALTER TABLE bench_runs DROP COLUMN cache_read;
		ALTER TABLE bench_runs DROP COLUMN reasoning;
		PRAGMA user_version = 7`); err != nil {
		t.Fatal(err)
	}
	return closeMatrixFixture(t, raw, 7, queryStrings(t, raw, `SELECT id,agent,model,task,score,tokens_in,tokens_out,cost,methodology_version,timestamp FROM bench_runs`))
}

func verifyPartialBenchRunsMatrix(t *testing.T, database *sql.DB, invariant migrationInvariant) {
	t.Helper()
	if got := queryStrings(t, database, `SELECT id,agent,model,task,score,tokens_in,tokens_out,cost,methodology_version,timestamp FROM bench_runs`); !reflect.DeepEqual(got, invariant.evidence) {
		t.Fatalf("benchmark evidence changed: got %#v want %#v", got, invariant.evidence)
	}
	assertMatrixColumns(t, database, "bench_runs", "cache_read", "reasoning")
}

func seedPartialCompressedMatrix(t *testing.T, path string) migrationInvariant {
	raw := openHistoricalTelemetryFixture(t, path, 6)
	if _, err := raw.Exec(`ALTER TABLE compressed DROP COLUMN output;
		ALTER TABLE compressed DROP COLUMN pipeline;
		PRAGMA user_version = 7`); err != nil {
		t.Fatal(err)
	}
	return closeMatrixFixture(t, raw, 7, queryStrings(t, raw, `SELECT id,source_id,content_hash,timestamp FROM compressed`))
}

func verifyPartialCompressedMatrix(t *testing.T, database *sql.DB, invariant migrationInvariant) {
	t.Helper()
	if got := queryStrings(t, database, `SELECT id,source_id,content_hash,timestamp FROM compressed`); !reflect.DeepEqual(got, invariant.evidence) {
		t.Fatalf("compressed evidence changed: got %#v want %#v", got, invariant.evidence)
	}
	assertMatrixColumns(t, database, "compressed", "output", "pipeline")
}

func assertMatrixColumns(t *testing.T, database *sql.DB, table string, names ...string) {
	t.Helper()
	columns, err := tableColumns(database, table)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if _, exists := columns[name]; !exists {
			t.Errorf("%s.%s was not restored", table, name)
		}
	}
}

func closeMatrixFixture(t *testing.T, raw *sql.DB, version int, evidence [][]string) migrationInvariant {
	t.Helper()
	addMigrationMarker(t, raw)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	return migrationInvariant{version: version, evidence: evidence}
}

func addMigrationMarker(t *testing.T, raw *sql.DB) {
	t.Helper()
	if _, err := raw.Exec(`CREATE TABLE migration_matrix_marker (value TEXT NOT NULL); INSERT INTO migration_matrix_marker VALUES ('preserved')`); err != nil {
		t.Fatal(err)
	}
}

func TestRejectedMigrationInputsRemainUnchanged(t *testing.T) {
	tests := []struct {
		name            string
		seed            func(*testing.T, string)
		wantErr         bool
		wantErrContains string
	}{
		{name: "current schema is byte-idempotent", seed: seedCurrentStorage},
		{name: "newer schema", seed: func(t *testing.T, path string) { seedVersionedStorage(t, path, schemaVersion+1) }, wantErr: true, wantErrContains: fmt.Sprintf("install and verify a runtime supporting schema %d or newer", schemaVersion+1)},
		{name: "partial recorded streams", seed: seedPartialStorage, wantErr: true},
		{name: "malformed SQLite", seed: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("not a sqlite database\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "candidate.db")
			test.seed(t, path)
			before := snapshotStorage(t, dir)
			database, err := Open(path)
			if database != nil {
				if closeErr := database.Close(); err == nil {
					err = closeErr
				}
			}
			if test.wantErr && err == nil {
				t.Fatal("Open succeeded, want rejection")
			}
			if test.wantErrContains != "" && (err == nil || !strings.Contains(err.Error(), test.wantErrContains)) {
				t.Fatalf("Open error = %v, want text %q", err, test.wantErrContains)
			}
			if test.name == "newer schema" {
				var typed *SchemaTooNewError
				if !errors.As(err, &typed) || typed.Actual != schemaVersion+1 || typed.Maximum != schemaVersion {
					t.Fatalf("Open error = %#v, want typed schema %d > %d", err, schemaVersion+1, schemaVersion)
				}
			}
			if !test.wantErr && err != nil {
				t.Fatal(err)
			}
			if after := snapshotStorage(t, dir); !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected or idempotent storage changed:\n before=%#v\n after=%#v", before, after)
			}
		})
	}
}

func TestInjectedMigrationFailureRollsBackStorage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollback.db")
	raw := openHistoricalTelemetryFixture(t, path, 4)
	addMigrationMarker(t, raw)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	before := snapshotStorage(t, dir)
	raw, err := sql.Open(sqliteDriver, "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	err = migrateWith(raw, func(tx *sql.Tx) error {
		if err := migrateTx(tx); err != nil {
			return err
		}
		return errors.New("injected failure after schema and row migration")
	})
	if err == nil {
		t.Fatal("injected migration succeeded")
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if after := snapshotStorage(t, dir); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed migration changed storage:\n before=%#v\n after=%#v", before, after)
	}
}

func seedCurrentStorage(t *testing.T, path string) { seedVersionedStorage(t, path, schemaVersion) }

func seedVersionedStorage(t *testing.T, path string, version int) {
	t.Helper()
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`CREATE TABLE storage_marker (value TEXT); INSERT INTO storage_marker VALUES ('preserved'); PRAGMA user_version = ` + fmt.Sprint(version)); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func seedPartialStorage(t *testing.T, path string) {
	t.Helper()
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`DROP TABLE recorded_streams; CREATE TABLE recorded_streams (id TEXT PRIMARY KEY); PRAGMA user_version = 7`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

type storageEntry struct {
	name string
	mode os.FileMode
	data string
}

func snapshotStorage(t *testing.T, dir string) []storageEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]storageEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		item := storageEntry{name: entry.Name(), mode: info.Mode()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			item.data = string(data)
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].name < result[j].name })
	return result
}
