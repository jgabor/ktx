package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenLiteralDatabaseTarget(t *testing.T) {
	for _, name := range []string{"plain.db", "uri?tag.db", "uri#tag.db", "uri%23tag.db", "uri%.db", "uri?mode=memory#%.db"} {
		dir := t.TempDir()
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			// Exercise relative creation without changing the process working directory.
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			relative, err := filepath.Rel(cwd, path)
			if err != nil {
				t.Fatal(err)
			}
			database, err := Open(relative)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			var seq int
			var schema, opened string
			if err := database.db.QueryRow("PRAGMA database_list").Scan(&seq, &schema, &opened); err != nil {
				t.Fatal(err)
			}
			requestedInfo, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			openedInfo, err := os.Stat(opened)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(requestedInfo, openedInfo) || requestedInfo.Size() == 0 {
				t.Fatalf("SQLite opened %q instead of populated target %q", opened, path)
			}
			if _, err := database.db.Exec("CREATE TABLE target_marker (value TEXT); INSERT INTO target_marker VALUES ('intended')"); err != nil {
				t.Fatal(err)
			}
			var foreignKeys int
			if err := database.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
				t.Fatalf("foreign_keys = %d, error = %v", foreignKeys, err)
			}
			if _, err := database.db.Exec("CREATE TABLE target_parent (id INTEGER PRIMARY KEY); CREATE TABLE target_child (parent_id INTEGER REFERENCES target_parent(id))"); err != nil {
				t.Fatal(err)
			}
			if _, err := database.db.Exec("INSERT INTO target_child VALUES (1)"); err == nil {
				t.Fatal("orphan insert succeeded")
			}
			// A coordinator built from SQLite's resolved filename must contend
			// with the coordinator used by Open, not a different sibling file.
			contender, err := newWriterCoordinator(opened)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := database.writer.acquire(context.Background(), false, coordinationExclusive)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			other, lockErr := contender.acquire(ctx, false, coordinationExclusive)
			cancel()
			if other != nil {
				other.Close()
			}
			lease.Close()
			if !errors.Is(lockErr, context.DeadlineExceeded) {
				t.Fatalf("same-file contention error = %v", lockErr)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			for _, open := range []func(string) (*DB, error){OpenExisting, OpenReadOnly} {
				reopened, err := open(path)
				if err != nil {
					t.Fatal(err)
				}
				var marker string
				err = reopened.db.QueryRow("SELECT value FROM target_marker").Scan(&marker)
				reopened.Close()
				if err != nil || marker != "intended" {
					t.Fatalf("reopened marker = %q, error = %v", marker, err)
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Name() != name && entry.Name() != name+"-wal" && entry.Name() != name+"-shm" {
					t.Errorf("unexpected sibling %q", entry.Name())
				}
			}
		})
	}
}
