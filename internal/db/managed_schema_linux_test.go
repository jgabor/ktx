package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"
)

// Only a test-owned path can install a rendezvous. The production driver and
// coordinator are otherwise unchanged, including their admission ordering.
var schemaOpenRendezvous sync.Map

func init() {
	sqlite.RegisterConnectionHook(func(_ sqlite.ExecQuerierContext, dsn string) error {
		u, err := url.Parse(dsn)
		if err == nil && u.Query().Get("mode") == "rw" {
			if fn, ok := schemaOpenRendezvous.Load(u.Path); ok {
				return fn.(func() error)()
			}
		}
		return nil
	})
}

func requireNewerSchema(t *testing.T, err error) {
	t.Helper()
	var newer *SchemaTooNewError
	if !errors.As(err, &newer) || newer.Actual != schemaVersion+1 || newer.Maximum != schemaVersion {
		t.Fatalf("error = %v, want typed newer schema %d/%d", err, schemaVersion+1, schemaVersion)
	}
}

func TestManagedSchemaWriterRechecksAfterLease(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "writer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	blocker, err := newWriterCoordinator(d.writer.path)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := blocker.acquire(ctx, false, coordinationExclusive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	queued := make(chan struct{})
	d.writer.gateAcquired = func() { close(queued) }
	result := make(chan error, 1)
	go func() { result <- d.InsertSession(&Session{ID: "abcdefghijklmnop"}) }()
	select {
	case <-queued:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// The writer has entered admission, but the upgrader still owns the file
	// lease. This distinguishes an in-lease recheck from a pre-lock check.
	if _, err := d.db.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	lease = nil
	requireNewerSchema(t, <-result)
	if n := tableRowCount(t, d.db, "sessions"); n != 0 {
		t.Fatalf("stale writer committed %d sessions", n)
	}
}

func TestManagedSchemaQueryRechecksReopenedLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "query.db")
	// This historical schema migrates in one exclusive window.
	raw := openHistoricalSchema(t, path, 14)
	defer raw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	upgrader, err := newWriterCoordinator(path)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestManagedSchemaUpgradeProcess$")
	childRoot := t.TempDir()
	cmd.Env = append(os.Environ(), "KTX_SCHEMA_UPGRADE_TEST_PATH="+path,
		"HOME="+childRoot+"/home", "XDG_DATA_HOME="+childRoot+"/data",
		"XDG_CONFIG_HOME="+childRoot+"/config", "XDG_CACHE_HOME="+childRoot+"/cache",
		"XDG_STATE_HOME="+childRoot+"/state")
	cmd.Stderr = os.Stderr
	ready, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	schemaOpenRendezvous.Store(path, func() error {
		if err := cmd.Start(); err != nil {
			return err
		}
		var signal [1]byte
		_, err := ready.Read(signal[:])
		return err
	})
	defer schemaOpenRendezvous.Delete(path)
	// The upgrader queues while readiness owns its exclusive lease. Writer
	// preference guarantees it publishes before the query's fresh shared lease.
	query, err := OpenQuery(path)
	if query != nil {
		query.Close()
	}
	if upgradeErr := cmd.Wait(); upgradeErr != nil {
		t.Fatal(upgradeErr)
	}
	requireNewerSchema(t, err)
	if query != nil {
		t.Fatal("incompatible query handle returned")
	}
	// Failed reopening must release its lease and pool; raw inspection remains
	// deliberately capable of inspecting a newer schema.
	lease, err := upgrader.acquire(ctx, false, coordinationExclusive)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	inspection, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Close()
	version, err := inspection.SchemaVersion()
	if err != nil || version != schemaVersion+1 {
		t.Fatalf("inspection version=%d err=%v", version, err)
	}
}

func TestManagedSchemaUpgradeProcess(t *testing.T) {
	path := os.Getenv("KTX_SCHEMA_UPGRADE_TEST_PATH")
	if path == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	upgrader, err := newWriterCoordinator(path)
	if err != nil {
		t.Fatal(err)
	}
	upgrader.gateAcquired = func() { _, _ = os.Stdout.Write([]byte("Q")) }
	err = upgrader.withLock(ctx, false, func() error {
		raw, err := sql.Open(sqliteDriver, (&url.URL{Scheme: "file", Path: path}).String()+"?mode=rw")
		if err != nil {
			return err
		}
		defer raw.Close()
		var version int
		if err := raw.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
		if version != schemaVersion {
			return fmt.Errorf("readiness published %d, want %d", version, schemaVersion)
		}
		_, err = raw.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion+1))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestManagedSchemaTransactionBoundary(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "boundary.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, version := range []int{schemaVersion - 1, schemaVersion + 1} {
		if _, err := d.db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
			t.Fatal(err)
		}
		called := false
		err := d.withWriteTx(context.Background(), func(*sql.Tx) error { called = true; return nil })
		if version > schemaVersion {
			requireNewerSchema(t, err)
			if called {
				t.Fatal("stale writer callback entered")
			}
		} else if err != nil || !called {
			t.Fatalf("partial readiness write: version=%d callback=%v err=%v", version, called, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = d.withWriteTx(ctx, func(*sql.Tx) error { t.Error("canceled callback entered"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write: %v", err)
	}
}

func TestManagedSchemaQueryHoldsCompatibleLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compatible.db")
	raw := openHistoricalSchema(t, path, 14)
	raw.Close()
	query, err := OpenQuery(path)
	if err != nil {
		t.Fatal(err)
	}
	defer query.Close()
	version, err := query.SchemaVersion()
	if err != nil || version != schemaVersion {
		t.Fatalf("prepared version=%d err=%v", version, err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := tryLockWriterFile(file, coordinationExclusive); !errors.Is(err, errWriterLockBusy) {
		t.Fatalf("query does not hold shared lease: %v", err)
	}
	if err := query.InsertSession(&Session{ID: "abcdefghijklmnop"}); err == nil {
		t.Fatal("read-only query accepted mutation")
	}
	if n := tableRowCount(t, query.db, "sessions"); n != 0 {
		t.Fatalf("query committed %d sessions", n)
	}
}

func TestManagedSchemaCheckHonorsContext(t *testing.T) {
	d, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// Occupy the sole connection: an unbounded QueryRow would deadlock here.
	conn, err := d.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := readCompatibleSchemaVersion(ctx, d.db); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("schema check deadline: %v", err)
	}
}

func TestManagedSchemaExcludesCallerOwnedHandles(t *testing.T) {
	for _, name := range []string{"memory", "raw"} {
		t.Run(name, func(t *testing.T) {
			d, err := OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if name == "raw" {
				d, err = OpenWithoutMigration(d.db)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := d.db.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion+1)); err != nil {
				t.Fatal(err)
			}
			if err := d.InsertSession(&Session{ID: "abcdefghijklmnop"}); err != nil {
				t.Fatalf("caller-owned write contract changed: %v", err)
			}
		})
	}
}

func TestManagedSchemaWriteCategories(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "categories.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	const session = "abcdefghijklmnop"
	const source = "bcdefghijklmnopq"
	const artifact = "cdefghijklmnopqr"
	const collection = "defghijklmnopqrs"
	const stamp = "2026-09-06T12:00:00Z"
	if err := d.InsertSession(&Session{ID: session}); err != nil {
		t.Fatal(err)
	}
	if err := d.PersistToolCall(&ToolCall{ID: source, SessionID: session, Output: "kept", Timestamp: stamp}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	// Exercise every ordinary persistence method that reaches the shared
	// transaction boundary, including updates, retry paths and multi-table writes.
	for _, test := range []struct {
		name  string
		write func() error
	}{
		{"insert session", func() error { return d.InsertSession(&Session{ID: artifact}) }},
		{"identify session", func() error { return d.IdentifySession(session, "opencode") }},
		{"close session", func() error { return d.CloseSession(session, stamp) }},
		{"source", func() error { return d.PersistToolCall(&ToolCall{ID: artifact, SessionID: session, Timestamp: stamp}) }},
		{"source retry", func() error {
			_, err := d.PersistToolCallOnce(&ToolCall{ID: source, SessionID: session, Output: "kept", Timestamp: stamp})
			return err
		}},
		{"artifact", func() error { return d.InsertCompressed(&Compressed{ID: artifact, SourceID: source, Timestamp: stamp}) }},
		{"artifact retry", func() error {
			_, _, err := d.InsertCompressedOnce(&Compressed{ID: artifact, SourceID: source, Timestamp: stamp})
			return err
		}},
		{"disposition", func() error {
			return d.RecordCompressionDisposition(&CompressionDisposition{SourceID: source, Disposition: CompressionDispositionIntentionalPassthrough, ObservedAt: stamp})
		}},
		{"benchmark", func() error { return d.InsertBenchRun(&BenchRun{ID: artifact}) }},
		{"recording", func() error { return d.InsertRecordedStream(&RecordedStream{ID: artifact, SessionID: session}) }},
		{"telemetry", func() error {
			_, err := d.PersistSessionTelemetry(session, stamp, telemetryObservation("ses", "msg", "part", 1, 0, 0))
			return err
		}},
		{"collection", func() error { _, err := d.OpenCollectionInterval([]string{"run"}, stamp); return err }},
		{"named collection", func() error { _, err := d.EnsureCollectionInterval(collection, []string{"run"}, stamp); return err }},
		{"ingress", func() error {
			return d.RetainIngressObservation(IngressObservation{IntervalID: collection, Ingress: "run", InstanceID: session, State: "started", ObservedAt: stamp, Authority: IngressProcessAuthority})
		}},
		{"runtime source", func() error {
			return d.RetainRuntimeSourceObservation(RuntimeSourceObservation{CollectionID: collection, SessionID: session, SourceID: source, ObservedAt: stamp}, "kept")
		}},
		{"recovery cue", func() error {
			return d.AcknowledgeRecoveryCue(RecoveryCueDelivery{CollectionID: collection, SourceID: source, ArtifactID: artifact, SessionID: session, RuntimeSessionID: "ses", RuntimeCallID: "call", DeliveredAt: stamp, Authority: RecoveryDeliveryAuthority})
		}},
		{"recovery attempt", func() error {
			return d.RetainRecoveryAttempt(RecoveryAttempt{AttemptSourceID: artifact, CollectionID: collection, CueSourceID: source, AttemptedAt: stamp, ResultState: "failed", Authority: RecoveryAttemptAuthority})
		}},
		{"event interval", func() error {
			_, err := d.CreateBenchmarkEventInterval("attempt", "opencode", "1.18.5", "test", stamp)
			return err
		}},
		{"event", func() error {
			_, _, err := d.PersistRuntimeEvent(RuntimeEventInput{IntervalID: collection, SessionID: session, EpochID: "epoch", Kind: "collector_open", EventType: "collector_open", CapturedAt: stamp})
			return err
		}},
		{"seal interval", func() error {
			return d.SealRuntimeEventInterval(collection, session, stamp, RuntimeIncomplete, "stopped")
		}},
		{"hook window", func() error {
			return d.WithHookWriter(func(*HookWriter) error { t.Error("stale hook callback entered"); return nil })
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var before int
			if err := d.db.QueryRow("SELECT total_changes()").Scan(&before); err != nil {
				t.Fatal(err)
			}
			requireNewerSchema(t, test.write())
			var after int
			if err := d.db.QueryRow("SELECT total_changes()").Scan(&after); err != nil || after != before {
				t.Fatalf("changes before=%d after=%d err=%v", before, after, err)
			}
		})
	}
}
