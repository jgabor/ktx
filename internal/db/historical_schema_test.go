package db

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// historicalFixtureFS contains immutable databases captured from the production
// schema-version history. The manifest binds each fixture to its source revision,
// bytes, and complete physical-schema metadata without requiring Git at test time.
//
//go:embed testdata/historical/*
var historicalFixtureFS embed.FS

type historicalFixtureManifest struct {
	SchemaVersion int                      `json:"schema_version"`
	Fixtures      []historicalFixtureEntry `json:"fixtures"`
}

type historicalFixtureEntry struct {
	Version        int    `json:"version"`
	File           string `json:"file"`
	SourceRevision string `json:"source_revision"`
	DatabaseSHA256 string `json:"database_sha256"`
	MetadataSHA256 string `json:"metadata_sha256"`
}

func TestHistoricalSchemaPhysicalShapeMatrix(t *testing.T) {
	manifest := loadHistoricalFixtureManifest(t)
	if manifest.SchemaVersion != 1 || len(manifest.Fixtures) != schemaVersion {
		t.Fatalf("historical fixture manifest: schema=%d fixtures=%d, want schema=1 fixtures=%d", manifest.SchemaVersion, len(manifest.Fixtures), schemaVersion)
	}

	for version, fixture := range manifest.Fixtures {
		if fixture.Version != version {
			t.Fatalf("historical fixture %d records version %d", version, fixture.Version)
		}
		t.Run(fmt.Sprintf("schema-%d", version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "historical.db")
			raw := openHistoricalSchema(t, path, version)
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

func TestHistoricalSchemaFingerprintRejectsPhysicalMutation(t *testing.T) {
	const version = 15
	manifest := loadHistoricalFixtureManifest(t)
	raw := openHistoricalSchema(t, filepath.Join(t.TempDir(), "historical.db"), version)
	defer raw.Close()

	if _, err := raw.Exec(`DROP INDEX tool_calls_timestamp_unix_nano_idx`); err != nil {
		t.Fatal(err)
	}
	if err := verifyHistoricalSchemaFingerprint(raw, manifest.Fixtures[version]); err == nil {
		t.Fatal("historical fingerprint accepted a missing production index")
	}
}

func openHistoricalSchema(t *testing.T, path string, version int) *sql.DB {
	t.Helper()
	manifest := loadHistoricalFixtureManifest(t)
	if version < 0 || version >= len(manifest.Fixtures) {
		t.Fatalf("historical schema version %d is outside 0-%d", version, len(manifest.Fixtures)-1)
	}
	fixture := manifest.Fixtures[version]
	compressed, err := historicalFixtureFS.ReadFile("testdata/historical/" + fixture.File)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		_ = reader.Close()
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != fixture.DatabaseSHA256 {
		t.Fatalf("historical schema %d database digest = %s, want %s", version, got, fixture.DatabaseSHA256)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open(sqliteDriver, "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	if err := verifyHistoricalSchemaFingerprint(raw, fixture); err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	return raw
}

func loadHistoricalFixtureManifest(t *testing.T) historicalFixtureManifest {
	t.Helper()
	data, err := historicalFixtureFS.ReadFile("testdata/historical/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest historicalFixtureManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func verifyHistoricalSchemaFingerprint(database *sql.DB, fixture historicalFixtureEntry) error {
	digest, err := physicalSchemaFingerprint(database)
	if err != nil {
		return fmt.Errorf("historical schema %d metadata: %w", fixture.Version, err)
	}
	if digest != fixture.MetadataSHA256 {
		return fmt.Errorf("historical schema %d physical fingerprint = %s, want %s from production revision %s", fixture.Version, digest, fixture.MetadataSHA256, fixture.SourceRevision)
	}
	return nil
}

func physicalSchemaFingerprint(database *sql.DB) (string, error) {
	var snapshot strings.Builder
	var version int
	if err := database.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return "", err
	}
	appendSchemaMetadata(&snapshot, "user_version", version)

	rows, err := database.Query(`SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' AND sql IS NOT NULL ORDER BY type,name`)
	if err != nil {
		return "", err
	}
	var tables []string
	for rows.Next() {
		var objectType, name, table, statement string
		if err := rows.Scan(&objectType, &name, &table, &statement); err != nil {
			_ = rows.Close()
			return "", err
		}
		appendSchemaMetadata(&snapshot, "sqlite_schema", objectType, name, table, strings.Join(strings.Fields(statement), " "))
		if objectType == "table" {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", err
	}
	if err := rows.Close(); err != nil {
		return "", err
	}

	for _, table := range tables {
		if err := appendTableMetadata(&snapshot, database, table); err != nil {
			return "", fmt.Errorf("%s: %w", table, err)
		}
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(snapshot.String()))), nil
}

func appendTableMetadata(snapshot *strings.Builder, database *sql.DB, table string) error {
	rows, err := database.Query(`SELECT cid,name,type,"notnull",dflt_value,pk FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		appendSchemaMetadata(snapshot, "table_info", table, cid, name, columnType, notNull, nullableString(defaultValue), primaryKey)
	}
	if err := closeSchemaRows(rows); err != nil {
		return err
	}

	rows, err = database.Query(`SELECT id,seq,"table","from","to",on_update,on_delete,match FROM pragma_foreign_key_list(?) ORDER BY id,seq`, table)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, sequence int
		var targetTable, from, onUpdate, onDelete, match string
		var to sql.NullString
		if err := rows.Scan(&id, &sequence, &targetTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			_ = rows.Close()
			return err
		}
		appendSchemaMetadata(snapshot, "foreign_key", table, id, sequence, targetTable, from, nullableString(to), onUpdate, onDelete, match)
	}
	if err := closeSchemaRows(rows); err != nil {
		return err
	}

	rows, err = database.Query(`SELECT seq,name,"unique",origin,partial FROM pragma_index_list(?) ORDER BY seq,name`, table)
	if err != nil {
		return err
	}
	type indexMetadata struct {
		sequence int
		name     string
		unique   int
		origin   string
		partial  int
	}
	var indexes []indexMetadata
	for rows.Next() {
		var index indexMetadata
		if err := rows.Scan(&index.sequence, &index.name, &index.unique, &index.origin, &index.partial); err != nil {
			_ = rows.Close()
			return err
		}
		indexes = append(indexes, index)
		appendSchemaMetadata(snapshot, "index_list", table, index.sequence, index.name, index.unique, index.origin, index.partial)
	}
	if err := closeSchemaRows(rows); err != nil {
		return err
	}
	for _, index := range indexes {
		rows, err = database.Query(`SELECT seqno,cid,name,"desc",coll,"key" FROM pragma_index_xinfo(?) ORDER BY seqno`, index.name)
		if err != nil {
			return err
		}
		for rows.Next() {
			var sequence, cid, descending, key int
			var name, collation sql.NullString
			if err := rows.Scan(&sequence, &cid, &name, &descending, &collation, &key); err != nil {
				_ = rows.Close()
				return err
			}
			appendSchemaMetadata(snapshot, "index_xinfo", table, index.name, sequence, cid, nullableString(name), descending, nullableString(collation), key)
		}
		if err := closeSchemaRows(rows); err != nil {
			return err
		}
	}
	return nil
}

func closeSchemaRows(rows *sql.Rows) error {
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	return rows.Close()
}

func nullableString(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

func appendSchemaMetadata(snapshot *strings.Builder, kind string, values ...any) {
	data, err := json.Marshal(values)
	if err != nil {
		panic(err)
	}
	snapshot.WriteString(kind)
	snapshot.WriteByte(':')
	snapshot.Write(data)
	snapshot.WriteByte('\n')
}
