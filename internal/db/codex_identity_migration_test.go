package db

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/identity"
)

func TestMigrateCodexIdentitiesUpdatesEvidenceAndAllReferences(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	sessionID := mustNewID(t)
	sourceID := "exec-d0a88822-65de-4d16-8c37-df6a0aae9a48"
	mustSeedIdentityMigration(t, d, sessionID, sourceID, " Codex ")
	if _, err := d.db.Exec("CREATE TABLE extra_evidence (id TEXT PRIMARY KEY, source TEXT NOT NULL REFERENCES tool_calls(id), note TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec("INSERT INTO extra_evidence VALUES ('evidence', ?, 'unchanged')", sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec("INSERT INTO compressed (id, source_id, output, content_hash, pipeline, timestamp) VALUES (?, ?, ?, ?, ?, ?)", mustNewID(t), sourceID, "short", ContentHash("short"), "[]", "2026-07-12T10:01:00Z"); err != nil {
		t.Fatal(err)
	}

	if err := migrate(d.db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	want, err := identity.Derive(" Codex ", sessionID, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	var id, tool, input, output, runtime string
	if err := d.db.QueryRow("SELECT id, tool, input, output, runtime FROM tool_calls").Scan(&id, &tool, &input, &output, &runtime); err != nil {
		t.Fatal(err)
	}
	if id != want || tool != "bash" || input != "pwd" || output != "/tmp\n" || runtime != " Codex " {
		t.Fatalf("migrated row = (%q, %q, %q, %q, %q), want identity replacement only", id, tool, input, output, runtime)
	}
	for _, query := range []string{"SELECT source_id FROM compressed", "SELECT source FROM extra_evidence"} {
		var got string
		if err := d.db.QueryRow(query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", query, got, want)
		}
	}
	assertForeignKeysValid(t, d)

	if err := migrate(d.db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var eligible int
	if err := d.db.QueryRow("SELECT COUNT(*) FROM tool_calls WHERE runtime IS NOT NULL AND id NOT GLOB '[a-z][a-z][a-z][a-z][a-z][a-z][a-z][a-z][a-z][a-z][a-z][a-z][a-z][a-z][a-z][a-z]'").Scan(&eligible); err != nil {
		t.Fatal(err)
	}
	if eligible != 0 {
		t.Fatalf("eligible external identities after second migrate = %d, want 0", eligible)
	}
}

func TestMigrateCodexIdentitiesRejectsAmbiguousTupleAtomically(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	validSession := mustNewID(t)
	mustSeedIdentityMigration(t, d, validSession, "exec-valid", "codex")
	if _, err := d.db.Exec("INSERT INTO sessions (id, agent) VALUES ('', 'codex')"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec("INSERT INTO tool_calls (id, session_id, runtime) VALUES ('exec-ambiguous', '', 'codex')"); err != nil {
		t.Fatal(err)
	}

	err = migrate(d.db)
	if err == nil || !strings.Contains(err.Error(), "tuple field is empty") {
		t.Fatalf("migrate error = %v, want actionable ambiguous-tuple diagnostic", err)
	}
	for _, id := range []string{"exec-valid", "exec-ambiguous"} {
		var count int
		if err := d.db.QueryRow("SELECT COUNT(*) FROM tool_calls WHERE id = ?", id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("source row %q count = %d after rollback, want 1", id, count)
		}
	}
}

func TestMigrateCodexIdentitiesRejectsCollisionAtomically(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	sessionID := mustNewID(t)
	sourceID := "exec-collision"
	mustSeedIdentityMigration(t, d, sessionID, sourceID, "codex")
	target, err := identity.Derive("codex", sessionID, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec("INSERT INTO tool_calls (id, session_id, runtime) VALUES (?, ?, 'opencode')", target, sessionID); err != nil {
		t.Fatal(err)
	}

	err = migrate(d.db)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("migrate error = %v, want collision diagnostic", err)
	}
	var count int
	if err := d.db.QueryRow("SELECT COUNT(*) FROM tool_calls WHERE id IN (?, ?)", sourceID, target).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("rows after collision rollback = %d, want 2", count)
	}
}

func TestMigrateCodexIdentitiesRejectsCompetingRows(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	sessionID := mustNewID(t)
	mustSeedIdentityMigration(t, d, sessionID, "exec-same", "codex")
	if _, err := d.db.Exec("INSERT INTO tool_calls (id, session_id, runtime) VALUES (' exec-same ', ?, 'codex')", sessionID); err != nil {
		t.Fatal(err)
	}
	err = migrate(d.db)
	if err == nil || !strings.Contains(err.Error(), "both map to") {
		t.Fatalf("migrate error = %v, want competing-row diagnostic", err)
	}
	var count int
	if err := d.db.QueryRow("SELECT COUNT(*) FROM tool_calls WHERE id IN ('exec-same', ' exec-same ')").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("source rows after rollback = %d, want 2", count)
	}
}

func TestOpenCodexIdentityMigrationIsIdempotent(t *testing.T) {
	path := t.TempDir() + "/codex-identities.db"
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := mustNewID(t)
	sourceID := "exec-reopen"
	mustSeedIdentityMigration(t, d, sessionID, sourceID, "codex")
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	first, err := Open(path)
	if err != nil {
		t.Fatalf("first migrated open: %v", err)
	}
	want, err := identity.Derive("codex", sessionID, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	if err := first.db.QueryRow("SELECT id FROM tool_calls").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("migrated ID = %q, want %q", got, want)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("second migrated open: %v", err)
	}
	defer second.Close()
	var count int
	if err := second.db.QueryRow("SELECT COUNT(*) FROM tool_calls WHERE id = ?", want).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("canonical rows after reopen = %d, want 1", count)
	}
	assertForeignKeysValid(t, second)
}

func mustSeedIdentityMigration(t *testing.T, d *DB, sessionID, sourceID, runtime string) {
	t.Helper()
	if _, err := d.db.Exec("INSERT INTO sessions (id, agent, started_at) VALUES (?, 'codex', '2026-07-12T10:00:00Z')", sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec("INSERT INTO tool_calls (id, session_id, tool, input, output, content_hash, timestamp, runtime) VALUES (?, ?, 'bash', 'pwd', '/tmp\n', ?, '2026-07-12T10:00:01Z', ?)", sourceID, sessionID, ContentHash("/tmp\n"), runtime); err != nil {
		t.Fatal(err)
	}
}

func assertForeignKeysValid(t *testing.T, d *DB) {
	t.Helper()
	var table string
	err := d.db.QueryRow("SELECT \"table\" FROM pragma_foreign_key_check LIMIT 1").Scan(&table)
	if err == nil {
		t.Fatalf("foreign-key violation in %q", table)
	}
	if err != sql.ErrNoRows {
		t.Fatalf("foreign-key check: %v", err)
	}
}
