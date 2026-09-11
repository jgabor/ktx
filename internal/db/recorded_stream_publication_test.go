package db

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestPublishRecordedStreamActualConnectionLimit(t *testing.T) {
	for _, size := range []int{0, 3800, 4096, 4097, 8 * 1024 * 1024} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			d, err := OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			session, bench := seedSessionAndBenchRun(t, d)
			conn, err := d.DB().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := sqlite.Limit(conn, sqlite3.SQLITE_LIMIT_LENGTH, 4096); err != nil {
				t.Fatal(err)
			}
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			text := strings.Repeat("x", size)
			rs := &RecordedStream{ID: mustNewID(t), SessionID: session, BenchRunID: bench, ContentHash: ContentHash(text), StreamFormat: "opencode-jsonl-1", CapturedAt: "2026-09-07T00:00:00Z"}
			loads := 0
			err = d.PublishRecordedStream(rs, int64(size), func() (string, error) { loads++; return text, nil })
			if (err == nil) != (size < 4096) {
				t.Fatalf("size %d: %v", size, err)
			}
			if (loads == 1) != (size <= 4096) {
				t.Fatalf("size %d: loads %d", size, loads)
			}
			if size < 4096 {
				got, err := d.GetRecordedStreamByID(rs.ID)
				if err != nil || got.StreamText != text || got.ContentHash != rs.ContentHash {
					t.Fatalf("publication mismatch: %#v %v", got, err)
				}
			}
			conn, err = d.DB().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			limit, err := sqlite.Limit(conn, sqlite3.SQLITE_LIMIT_LENGTH, -1)
			_ = conn.Close()
			if err != nil || limit != 4096 {
				t.Fatalf("changed limit: %d %v", limit, err)
			}
		})
	}
}

func TestPublishRecordedStreamReplacementAndSchema(t *testing.T) {
	for _, mode := range []string{"replacement", "newer-schema"} {
		t.Run(mode, func(t *testing.T) {
			d, err := Open(filepath.Join(t.TempDir(), "owned.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			session, bench := seedSessionAndBenchRun(t, d)
			conn, err := d.DB().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			_, err = sqlite.Limit(conn, sqlite3.SQLITE_LIMIT_LENGTH, 4096)
			_ = conn.Close()
			if err != nil {
				t.Fatal(err)
			}
			d.DB().SetMaxIdleConns(0)
			if mode == "newer-schema" {
				if _, err := d.DB().Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion+1)); err != nil {
					t.Fatal(err)
				}
			}
			text := strings.Repeat("x", 8192)
			rs := &RecordedStream{ID: mustNewID(t), SessionID: session, BenchRunID: bench, ContentHash: ContentHash(text), StreamFormat: "opencode-jsonl-1", CapturedAt: "2026-09-07T00:00:00Z"}
			loaded := false
			err = d.PublishRecordedStream(rs, int64(len(text)), func() (string, error) { loaded = true; return text, nil })
			if (err == nil) != (mode == "replacement") || loaded != (mode == "replacement") {
				t.Fatalf("publication: loaded %v error %v", loaded, err)
			}
		})
	}
}
