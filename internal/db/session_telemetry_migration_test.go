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

func TestSessionTelemetrySchemaV5FreshAndHistorical(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		d, err := OpenMemory()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		assertSessionTelemetryV5Shape(t, d.db)
	})

	for version := 0; version <= 3; version++ {
		t.Run(fmt.Sprintf("version-%d", version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "historical.db")
			raw := openRawTelemetryFixture(t, path)
			if _, err := raw.Exec(`DROP TABLE session_telemetry`); err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(`PRAGMA user_version = ` + fmt.Sprint(version)); err != nil {
				t.Fatal(err)
			}
			raw.Close()
			d, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { d.Close() })
			assertSessionTelemetryV5Shape(t, d.db)
			if count := tableRowCount(t, d.db, "sessions"); count != 2 {
				t.Fatalf("sessions = %d, want 2", count)
			}
		})
	}

	t.Run("empty-version-4", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty-v4.db")
		raw := openRawTelemetryFixture(t, path)
		replaceTelemetryWithV4(t, raw)
		raw.Close()
		d, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		assertSessionTelemetryV5Shape(t, d.db)
	})
}

func TestSessionTelemetryV4MigrationPreservesAllEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "populated-v4.db")
	raw := openRawTelemetryFixture(t, path)
	replaceTelemetryWithV4(t, raw)
	if _, err := raw.Exec(`INSERT INTO session_telemetry VALUES
		('aaaaaaaaaaaaaaaa','native-session','message','part',7,'stop',1.25,10,20,30,40,50,'provider','model','custom/runtime','v 1','captured')`); err != nil {
		t.Fatal(err)
	}
	beforeTelemetry := queryStrings(t, raw, `SELECT * FROM session_telemetry`)
	beforeUnrelated := snapshotUnrelatedEvidence(t, raw)
	raw.Close()

	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	assertSessionTelemetryV5Shape(t, d.db)
	if got := queryStrings(t, d.db, `SELECT * FROM session_telemetry`); !reflect.DeepEqual(got, beforeTelemetry) {
		t.Fatalf("telemetry changed:\n got %#v\nwant %#v", got, beforeTelemetry)
	}
	if got := snapshotUnrelatedEvidence(t, d.db); !reflect.DeepEqual(got, beforeUnrelated) {
		t.Fatalf("unrelated evidence changed:\n got %#v\nwant %#v", got, beforeUnrelated)
	}
}

func TestSessionTelemetryV5MigrationRollsBack(t *testing.T) {
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
			raw := openRawTelemetryFixture(t, path)
			replaceTelemetryWithV4(t, raw)
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

func openRawTelemetryFixture(t *testing.T, path string) *sql.DB {
	t.Helper()
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"} {
		if err := d.InsertSession(&Session{ID: id, Agent: "opencode", StartedAt: "start", LastSeenAt: "seen"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.db.Exec(`INSERT INTO tool_calls (id,session_id,tool,input,output,content_hash,timestamp,event_type,tokens_in,tokens_out,cost,latency_ms,model,provider,exit_code,is_error,runtime)
		VALUES ('cccccccccccccccc','aaaaaaaaaaaaaaaa','bash','in','out','hash','time','tool_call',1,2,3.5,4,'m','p',0,0,'r')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`INSERT INTO compressed VALUES ('dddddddddddddddd','cccccccccccccccc','small','hash2','["normalize"]','time2')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`INSERT INTO bench_runs VALUES ('eeeeeeeeeeeeeeee','agent','model','task',1,2,3,4,'method',5,6,7,8,9)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`INSERT INTO recorded_streams VALUES ('ffffffffffffffff','aaaaaaaaaaaaaaaa','eeeeeeeeeeeeeeee','stream','hash3','format','time3')`); err != nil {
		t.Fatal(err)
	}
	raw := d.db
	d.db = nil
	raw.SetMaxOpenConns(1)
	return raw
}

func replaceTelemetryWithV4(t *testing.T, raw *sql.DB) {
	t.Helper()
	if _, err := raw.Exec(`DROP TABLE session_telemetry; ` + legacySessionTelemetryTable + `;
		CREATE INDEX session_telemetry_session_order_idx ON session_telemetry(session_id, ordinal);
		PRAGMA user_version = 4`); err != nil {
		t.Fatal(err)
	}
}

func assertSessionTelemetryV5Shape(t *testing.T, db *sql.DB) {
	t.Helper()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 5 {
		t.Fatalf("user_version = %d, %v; want 5", version, err)
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
}

func snapshotUnrelatedEvidence(t *testing.T, db *sql.DB) [][]string {
	t.Helper()
	var snapshot [][]string
	for _, query := range []string{
		`SELECT * FROM sessions ORDER BY id`, `SELECT * FROM tool_calls ORDER BY id`,
		`SELECT * FROM compressed ORDER BY id`, `SELECT * FROM bench_runs ORDER BY id`,
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
