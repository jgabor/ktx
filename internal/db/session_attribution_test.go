package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSessionAttributionAuthority(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	for _, session := range []*Session{
		{ID: "identifiedsessaa", Agent: "opencode", AgentAttributionAuthority: AgentAttributionIdentified},
		{ID: "unknownsessionaa", Agent: "opencode"},
	} {
		if err := database.InsertSession(session); err != nil {
			t.Fatal(err)
		}
	}

	identified, err := database.GetSession("identifiedsessaa")
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := database.GetSession("unknownsessionaa")
	if err != nil {
		t.Fatal(err)
	}
	if identified.Agent != unknown.Agent || identified.AgentAttributionAuthority != AgentAttributionIdentified || unknown.AgentAttributionAuthority != AgentAttributionUnknown {
		t.Fatalf("attribution states = %#v, %#v", identified, unknown)
	}

	encoded, err := json.Marshal(identified)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "authority") {
		t.Fatalf("existing session JSON shape changed: %s", encoded)
	}
	if err := database.InsertSession(&Session{ID: "invalidsessionaaa", Agent: "opencode", AgentAttributionAuthority: "label"}); err == nil {
		t.Fatal("invalid attribution authority was accepted")
	}
}

func TestIdentifySessionFirstTrustedIdentityWins(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InsertSession(&Session{ID: "attributionsessaa", Agent: "unknown"}); err != nil {
		t.Fatal(err)
	}

	const attempts = 20
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := database.IdentifySession("attributionsessaa", "opencode"); err != nil {
				t.Errorf("IdentifySession retry: %v", err)
			}
		}()
	}
	wg.Wait()
	if err := database.IdentifySession("attributionsessaa", "codex"); err != nil {
		t.Fatal(err)
	}

	session, err := database.GetSession("attributionsessaa")
	if err != nil {
		t.Fatal(err)
	}
	if session.Agent != "opencode" || session.AgentAttributionAuthority != AgentAttributionIdentified {
		t.Fatalf("session = %#v, want first trusted opencode identity", session)
	}
}

func TestSessionAttributionMigrationIsAtomicAndDoesNotTrustLabels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-11.db")
	raw, err := sql.Open(sqliteDriver, "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY, agent TEXT, started_at TEXT, ended_at TEXT,
		last_seen_at TEXT, tool_call_count INTEGER DEFAULT 0
	);
	INSERT INTO sessions (id, agent, started_at) VALUES ('legacysessionaaa', 'opencode', '2026-08-17T00:00:00Z');
	PRAGMA user_version = 11;`); err != nil {
		t.Fatal(err)
	}

	interrupted := errors.New("interrupted migration")
	err = migrateWith(raw, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`ALTER TABLE sessions ADD COLUMN agent_attribution_authority TEXT NOT NULL DEFAULT 'unknown'`); err != nil {
			return err
		}
		return interrupted
	})
	if !errors.Is(err, interrupted) {
		t.Fatalf("migrateWith error = %v, want interruption", err)
	}
	columns, err := tableColumns(raw, "sessions")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := columns["agent_attribution_authority"]; ok {
		t.Fatal("interrupted migration published a partial attribution column")
	}
	var version int
	var label string
	if err := raw.QueryRow(`SELECT agent FROM sessions WHERE id = 'legacysessionaaa'`).Scan(&label); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if label != "opencode" || version != 11 {
		t.Fatalf("rollback preserved label/version = %q/%d", label, version)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	session, err := database.GetSession("legacysessionaaa")
	if err != nil {
		t.Fatal(err)
	}
	if session.Agent != "opencode" || session.AgentAttributionAuthority != AgentAttributionUnknown {
		t.Fatalf("migrated session = %#v", session)
	}
}

func TestCurrentSchemaMissingAttributionAuthorityFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid-current.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(`ALTER TABLE sessions DROP COLUMN agent_attribution_authority`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), `missing column "agent_attribution_authority"`) {
		t.Fatalf("Open error = %v, want missing authority column", err)
	}
}
