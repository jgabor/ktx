package db

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSQLiteProcessHelper(t *testing.T) {
	if os.Getenv("KTX_SQLITE_HELPER") != "1" {
		return
	}
	path := os.Getenv("KTX_SQLITE_PATH")
	switch os.Getenv("KTX_SQLITE_ROLE") {
	case "hold":
		d, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		tx, err := d.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := os.WriteFile(os.Getenv("KTX_SQLITE_READY"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		waitForFile(t, os.Getenv("KTX_SQLITE_RELEASE"), 10*time.Second)
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	case "open-write":
		if err := os.WriteFile(os.Getenv("KTX_SQLITE_READY"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		waitForFile(t, os.Getenv("KTX_SQLITE_START"), 10*time.Second)
		d, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		if err := d.InsertSession(&Session{ID: os.Getenv("KTX_SQLITE_ID"), Agent: "test"}); err != nil {
			t.Fatal(err)
		}
	case "hold-exclusive":
		database, err := sql.Open(sqliteDriver, "file:"+path+"?_pragma=busy_timeout(1)")
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		database.SetMaxOpenConns(1)
		if _, err := database.Exec(`BEGIN EXCLUSIVE`); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("KTX_SQLITE_READY"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		waitForFile(t, os.Getenv("KTX_SQLITE_RELEASE"), 10*time.Second)
		if _, err := database.Exec(`COMMIT`); err != nil {
			t.Fatal(err)
		}
	case "hold-writer":
		d, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		err = d.withWriteTx(context.Background(), func(tx *sql.Tx) error {
			if err := os.WriteFile(os.Getenv("KTX_SQLITE_READY"), []byte("ready"), 0o600); err != nil {
				return err
			}
			waitForFile(t, os.Getenv("KTX_SQLITE_RELEASE"), 10*time.Second)
			_, err := tx.Exec(`INSERT INTO sessions (id, agent) VALUES (?, ?)`, os.Getenv("KTX_SQLITE_ID"), "test")
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	case "hold-reader":
		d, err := OpenReadOnly(path)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		tx, err := d.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var count int
		if err := tx.QueryRow(`SELECT count(*) FROM sessions`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("KTX_SQLITE_READY"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		waitForFile(t, os.Getenv("KTX_SQLITE_RELEASE"), 10*time.Second)
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown helper role %q", os.Getenv("KTX_SQLITE_ROLE"))
	}
}

func TestSupportedReaderAndWriterOwnershipAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reader-writer.db")
	initialized, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := initialized.InsertSession(&Session{ID: "readerseedaaaaaa", Agent: "test"}); err != nil {
		initialized.Close()
		t.Fatal(err)
	}
	if err := initialized.Close(); err != nil {
		t.Fatal(err)
	}

	readerReady := path + ".reader-ready"
	readerRelease := path + ".reader-release"
	reader, readerOutput := startDBHelper(t, path, "hold-reader", map[string]string{
		"KTX_SQLITE_READY":   readerReady,
		"KTX_SQLITE_RELEASE": readerRelease,
	})
	readerWaited := false
	t.Cleanup(func() {
		if readerWaited {
			return
		}
		_ = os.WriteFile(readerRelease, nil, 0o600)
		_ = reader.Process.Kill()
		_ = reader.Wait()
	})
	waitForFile(t, readerReady, 5*time.Second)
	assertWriterLockBusy(t, path)

	writerReady := path + ".writer-ready"
	writerStart := path + ".writer-start"
	writer, writerOutput := startDBHelper(t, path, "open-write", map[string]string{
		"KTX_SQLITE_ID":    "readerwriteraaaa",
		"KTX_SQLITE_READY": writerReady,
		"KTX_SQLITE_START": writerStart,
	})
	writerWaited := false
	t.Cleanup(func() {
		if writerWaited {
			return
		}
		_ = os.WriteFile(writerStart, nil, 0o600)
		_ = writer.Process.Kill()
		_ = writer.Wait()
	})
	waitForFile(t, writerReady, 5*time.Second)
	if err := os.WriteFile(writerStart, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	assertWriterLockBusy(t, path)
	if err := os.WriteFile(readerRelease, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reader.Wait(); err != nil {
		t.Fatalf("reader: %v\n%s", err, readerOutput.String())
	}
	readerWaited = true
	if err := writer.Wait(); err != nil {
		t.Fatalf("writer: %v\n%s", err, writerOutput.String())
	}
	writerWaited = true

	assertSessionCount(t, path, 2)
}

func TestOpenQueryKeepsValidatedSchemaStableUntilClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "query-schema.db")
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	reader, err := OpenQuery(path)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	err = writer.withWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`PRAGMA user_version = ` + strconv.Itoa(schemaVersion+1))
		return err
	})
	cancel()
	if err == nil {
		reader.Close()
		t.Fatal("newer schema became visible through an open query handle")
	}
	if !IsDatabaseUnavailable(err) {
		reader.Close()
		t.Fatalf("schema transition error = %v, want database unavailable", err)
	}
	if version, err := reader.SchemaVersion(); err != nil || version != schemaVersion {
		reader.Close()
		t.Fatalf("open query schema = %d, %v; want %d", version, err, schemaVersion)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}

	if err := writer.withWriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`PRAGMA user_version = ` + strconv.Itoa(schemaVersion+1))
		return err
	}); err != nil {
		t.Fatalf("schema transition after query close: %v", err)
	}
	if database, err := OpenQuery(path); err == nil {
		database.Close()
		t.Fatal("OpenQuery accepted newer schema")
	} else {
		var target *SchemaTooNewError
		if !errors.As(err, &target) {
			t.Fatalf("OpenQuery newer schema error = %v", err)
		}
	}
}

func TestSupportedCompatibleMigrationAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stale-coordination.db")
	initialized, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initialized.db.Exec(`
		DROP TABLE compression_dispositions;
		CREATE TABLE migration_coordination_marker (value TEXT NOT NULL);
		INSERT INTO migration_coordination_marker VALUES ('preserved');
		PRAGMA user_version = ` + strconv.Itoa(schemaVersion-1)); err != nil {
		initialized.Close()
		t.Fatal(err)
	}
	if err := initialized.Close(); err != nil {
		t.Fatal(err)
	}

	start := path + ".start"
	const workers = 4
	type child struct {
		cmd    *exec.Cmd
		output *bytes.Buffer
		ready  string
	}
	children := make([]child, 0, workers)
	for index := 0; index < workers; index++ {
		ready := path + ".ready-" + strconv.Itoa(index)
		cmd, output := startDBHelper(t, path, "open-write", map[string]string{
			"KTX_SQLITE_START": start,
			"KTX_SQLITE_ID":    strings.Repeat(string(rune('a'+index)), idLength),
			"KTX_SQLITE_READY": ready,
		})
		children = append(children, child{cmd: cmd, output: output, ready: ready})
	}
	for _, child := range children {
		waitForFile(t, child.ready, 5*time.Second)
	}
	if err := os.WriteFile(start, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for index, child := range children {
		if err := child.cmd.Wait(); err != nil {
			t.Errorf("worker %d: %v\n%s", index, err, child.output.String())
		}
	}
	if t.Failed() {
		return
	}

	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var version, calls, dispositionTable int
	if err := database.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'compression_dispositions'`).Scan(&dispositionTable); err != nil {
		t.Fatal(err)
	}
	var marker, integrity string
	if err := database.db.QueryRow(`SELECT value FROM migration_coordination_marker`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || calls != workers || dispositionTable != 1 || marker != "preserved" || integrity != "ok" {
		t.Fatalf("migration post-state version=%d sessions=%d disposition_table=%d marker=%q integrity=%q", version, calls, dispositionTable, marker, integrity)
	}
}

func TestNewerSchemaCoordinationPreservesStorage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "newer-coordination.db")
	initialized, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initialized.db.Exec(`CREATE TABLE newer_coordination_marker (value TEXT NOT NULL); INSERT INTO newer_coordination_marker VALUES ('preserved'); PRAGMA user_version = ` + strconv.Itoa(schemaVersion+1)); err != nil {
		initialized.Close()
		t.Fatal(err)
	}
	if err := initialized.Close(); err != nil {
		t.Fatal(err)
	}
	before := snapshotStorage(t, dir)
	for name, open := range map[string]func(string) (*DB, error){
		"write-capable": Open,
		"read-only":     OpenQuery,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := open(path)
			var schemaErr *SchemaTooNewError
			if !errors.As(err, &schemaErr) || schemaErr.Actual != schemaVersion+1 || schemaErr.Maximum != schemaVersion {
				t.Fatalf("newer schema error = %v", err)
			}
		})
	}
	if after := snapshotStorage(t, dir); !reflect.DeepEqual(after, before) {
		t.Fatalf("newer schema open changed storage:\n before=%#v\n after=%#v", before, after)
	}
}

func TestForeignSQLiteLocksRemainOutsideSupportedOwnership(t *testing.T) {
	t.Run("foreign writer blocks supported reader", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "foreign-writer.db")
		initialized, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := initialized.Close(); err != nil {
			t.Fatal(err)
		}
		ready := path + ".ready"
		release := path + ".release"
		holder, output := startDBHelper(t, path, "hold-exclusive", map[string]string{
			"KTX_SQLITE_READY":   ready,
			"KTX_SQLITE_RELEASE": release,
		})
		t.Cleanup(func() {
			_ = os.WriteFile(release, nil, 0o600)
			_ = holder.Process.Kill()
			_ = holder.Wait()
		})
		waitForFile(t, ready, 5*time.Second)
		if database, err := OpenReadOnly(path); err == nil || !IsSQLiteLockError(err) {
			if database != nil {
				_ = database.Close()
			}
			t.Fatalf("foreign writer result = %v, want SQLite lock\n%s", err, output.String())
		}
		if err := os.WriteFile(release, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := holder.Wait(); err != nil {
			t.Fatalf("foreign writer: %v\n%s", err, output.String())
		}
	})

	t.Run("foreign reader blocks supported writer", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "foreign-reader.db")
		initialized, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := initialized.InsertSession(&Session{ID: "foreignreadseedx", Agent: "test"}); err != nil {
			initialized.Close()
			t.Fatal(err)
		}
		if err := initialized.Close(); err != nil {
			t.Fatal(err)
		}
		reader, readTx := beginSharedRead(t, path, 10)
		defer reader.Close()
		defer readTx.Rollback()

		raw, err := sql.Open(sqliteDriver, "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(10)&_txlock=immediate")
		if err != nil {
			t.Fatal(err)
		}
		database, err := OpenWithoutMigration(raw)
		if err != nil {
			raw.Close()
			t.Fatal(err)
		}
		defer database.Close()
		database.writer, err = newWriterCoordinator(path)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err = database.withWriteTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO sessions (id, agent) VALUES ('foreignreaderwrt', 'test')`)
			return err
		})
		if !IsSQLiteLockError(err) {
			t.Fatalf("foreign reader result = %v, want SQLite lock", err)
		}
	})
}

func TestSupportedWriterOwnershipAcrossPathAliases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ownership.db")
	initialized, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := initialized.Close(); err != nil {
		t.Fatal(err)
	}

	alias := filepath.Join(t.TempDir(), "ownership-alias.db")
	if err := os.Symlink(path, alias); err != nil {
		t.Skipf("create database path alias: %v", err)
	}
	holderReady := path + ".holder-ready"
	holderRelease := path + ".holder-release"
	holder, holderOutput := startDBHelper(t, path, "hold-writer", map[string]string{
		"KTX_SQLITE_ID":      "ownershipholder",
		"KTX_SQLITE_READY":   holderReady,
		"KTX_SQLITE_RELEASE": holderRelease,
	})
	holderWaited := false
	t.Cleanup(func() {
		if holderWaited {
			return
		}
		_ = os.WriteFile(holderRelease, nil, 0o600)
		_ = holder.Process.Kill()
		_ = holder.Wait()
	})
	waitForFile(t, holderReady, 5*time.Second)
	assertWriterLockBusy(t, alias)

	contenderReady := path + ".contender-ready"
	contenderStart := path + ".contender-start"
	contender, contenderOutput := startDBHelper(t, alias, "open-write", map[string]string{
		"KTX_SQLITE_ID":    "ownershiprunner",
		"KTX_SQLITE_READY": contenderReady,
		"KTX_SQLITE_START": contenderStart,
	})
	contenderWaited := false
	t.Cleanup(func() {
		if contenderWaited {
			return
		}
		_ = os.WriteFile(contenderStart, nil, 0o600)
		_ = contender.Process.Kill()
		_ = contender.Wait()
	})
	waitForFile(t, contenderReady, 5*time.Second)
	if err := os.WriteFile(contenderStart, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	assertWriterLockBusy(t, alias)
	if err := os.WriteFile(holderRelease, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := holder.Wait(); err != nil {
		t.Fatalf("owner: %v\n%s", err, holderOutput.String())
	}
	holderWaited = true
	if err := contender.Wait(); err != nil {
		t.Fatalf("contender: %v\n%s", err, contenderOutput.String())
	}
	contenderWaited = true

	assertSessionCount(t, path, 2)
}

func TestSupportedWriterOwnershipRecoversAfterProcessTermination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ownership-recovery.db")
	initialized, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := initialized.Close(); err != nil {
		t.Fatal(err)
	}

	alias := filepath.Join(t.TempDir(), "ownership-recovery-alias.db")
	if err := os.Symlink(path, alias); err != nil {
		t.Skipf("create database path alias: %v", err)
	}
	ready := path + ".ready"
	release := path + ".release"
	holder, output := startDBHelper(t, path, "hold-writer", map[string]string{
		"KTX_SQLITE_ID":      "ownershipkilled",
		"KTX_SQLITE_READY":   ready,
		"KTX_SQLITE_RELEASE": release,
	})
	waited := false
	t.Cleanup(func() {
		if waited {
			return
		}
		_ = os.WriteFile(release, nil, 0o600)
		_ = holder.Process.Kill()
		_ = holder.Wait()
	})
	waitForFile(t, ready, 5*time.Second)
	assertWriterLockBusy(t, alias)
	if err := holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := holder.Wait(); err == nil {
		t.Fatalf("terminated owner exited successfully\n%s", output.String())
	}
	waited = true
	assertWriterLockAvailable(t, alias)

	recovery, err := Open(alias)
	if err != nil {
		t.Fatalf("open after terminated owner: %v", err)
	}
	defer recovery.Close()
	if err := recovery.InsertSession(&Session{ID: "ownershiprecover", Agent: "test"}); err != nil {
		t.Fatalf("write after terminated owner: %v", err)
	}
	assertSessionCount(t, path, 1)
}

func assertWriterLockBusy(t *testing.T, path string) {
	t.Helper()
	coordinator, err := newWriterCoordinator(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(coordinator.path, os.O_RDWR, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	err = tryLockWriterFile(file, coordinationExclusive)
	if !errors.Is(err, errWriterLockBusy) {
		if err == nil {
			_ = unlockWriterFile(file)
		}
		t.Fatalf("writer lock through %q = %v, want busy", path, err)
	}
}

func assertWriterLockAvailable(t *testing.T, path string) {
	t.Helper()
	coordinator, err := newWriterCoordinator(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(coordinator.path, os.O_RDWR, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := tryLockWriterFile(file, coordinationExclusive); err != nil {
		t.Fatalf("writer lock through %q: %v", path, err)
	}
	if err := unlockWriterFile(file); err != nil {
		t.Fatal(err)
	}
}

func assertSessionCount(t *testing.T, path string, want int) {
	t.Helper()
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var got int
	if err := database.db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("sessions = %d, want %d", got, want)
	}
}

func TestWriterCoordinatorAcquisitionHonorsContext(t *testing.T) {
	t.Run("file lease", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "coordinator-deadline.db")
		initialized, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := initialized.Close(); err != nil {
			t.Fatal(err)
		}

		holder, err := newWriterCoordinator(path)
		if err != nil {
			t.Fatal(err)
		}
		lease, err := holder.acquire(context.Background(), false, coordinationShared)
		if err != nil {
			t.Fatal(err)
		}

		waiter, err := newWriterCoordinator(path)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, err = waiter.acquire(ctx, false, coordinationExclusive)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || !IsDatabaseUnavailable(err) {
			t.Fatalf("coordinator wait error = %v, want classified deadline", err)
		}

		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
		retryCtx, retryCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		retry, err := waiter.acquire(retryCtx, false, coordinationExclusive)
		retryCancel()
		if err != nil {
			t.Fatalf("coordinator retry after release: %v", err)
		}
		if err := retry.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("process-local queue", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "coordinator-queue.db")
		coordinator, err := newWriterCoordinator(path)
		if err != nil {
			t.Fatal(err)
		}
		entered := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- coordinator.withLock(context.Background(), true, func() error {
				close(entered)
				<-release
				return nil
			})
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("first process-local coordinator did not acquire")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		err = coordinator.withLock(ctx, true, func() error {
			return errors.New("unexpected acquisition")
		})
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || !IsDatabaseUnavailable(err) {
			t.Fatalf("process-local coordinator wait error = %v, want classified deadline", err)
		}

		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		retryCtx, retryCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		err = coordinator.withLock(retryCtx, true, func() error { return nil })
		retryCancel()
		if err != nil {
			t.Fatalf("process-local coordinator retry after release: %v", err)
		}
	})
}

func TestOpenRetriesStartupLock(t *testing.T) {
	for name, open := range map[string]func(string) (interface{ Close() error }, error){
		"read-write": func(path string) (interface{ Close() error }, error) { return Open(path) },
		"read-only":  func(path string) (interface{ Close() error }, error) { return OpenReadOnly(path) },
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "startup-retry.db")
			initialized, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := initialized.Close(); err != nil {
				t.Fatal(err)
			}

			ready := path + ".ready"
			release := path + ".release"
			cmd, output := startDBHelper(t, path, "hold-exclusive", map[string]string{
				"KTX_SQLITE_READY":   ready,
				"KTX_SQLITE_RELEASE": release,
			})
			t.Cleanup(func() {
				_ = os.WriteFile(release, nil, 0o600)
				_ = cmd.Wait()
			})
			waitForFile(t, ready, 5*time.Second)

			started := time.Now()
			opened := make(chan error, 1)
			go func() {
				database, err := open(path)
				if err == nil {
					err = database.Close()
				}
				opened <- err
			}()
			// This deliberately outlives the configured one-second busy wait.
			time.Sleep(time.Duration(sqliteBusyTimeoutMS)*time.Millisecond + 100*time.Millisecond)
			if err := os.WriteFile(release, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := <-opened; err != nil {
				t.Fatalf("Open after startup lock release: %v\nhelper output: %s", err, output.String())
			}
			if elapsed := time.Since(started); elapsed > startupRetryDeadline+250*time.Millisecond {
				t.Fatalf("startup retry exceeded bound: elapsed %s, bound %s", elapsed, startupRetryDeadline+250*time.Millisecond)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("exclusive lock holder: %v\n%s", err, output.String())
			}
		})
	}
}

func TestOpenStartupLockFailureIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup-bounded.db")
	initialized, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := initialized.Close(); err != nil {
		t.Fatal(err)
	}

	ready := path + ".ready"
	release := path + ".release"
	cmd, _ := startDBHelper(t, path, "hold-exclusive", map[string]string{
		"KTX_SQLITE_READY":   ready,
		"KTX_SQLITE_RELEASE": release,
	})
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0o600)
		_ = cmd.Wait()
	})
	waitForFile(t, ready, 5*time.Second)

	started := time.Now()
	_, err = Open(path)
	elapsed := time.Since(started)
	if err == nil || !isSQLiteLockError(err) {
		t.Fatalf("held startup lock error = %v, want SQLite BUSY/LOCKED", err)
	}
	if elapsed < time.Duration(sqliteBusyTimeoutMS)*time.Millisecond {
		t.Fatalf("startup retry ended before one busy wait: elapsed %s", elapsed)
	}
	if bound := startupRetryDeadline + 250*time.Millisecond; elapsed > bound {
		t.Fatalf("startup retry exceeded bound: elapsed %s, bound %s", elapsed, bound)
	}
}

func TestInitialSchemaVersionReadRetriesOnlyLockErrors(t *testing.T) {
	t.Run("retries lock", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "schema-version-lock.db")
		initialized, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := initialized.Close(); err != nil {
			t.Fatal(err)
		}

		ready := path + ".ready"
		release := path + ".release"
		cmd, output := startDBHelper(t, path, "hold-exclusive", map[string]string{
			"KTX_SQLITE_READY":   ready,
			"KTX_SQLITE_RELEASE": release,
		})
		t.Cleanup(func() {
			_ = os.WriteFile(release, nil, 0o600)
			_ = cmd.Wait()
		})
		waitForFile(t, ready, 5*time.Second)

		contender, err := sql.Open(sqliteDriver, "file:"+path+"?_pragma=busy_timeout(5)")
		if err != nil {
			t.Fatal(err)
		}
		defer contender.Close()
		go func() {
			time.Sleep(25 * time.Millisecond)
			_ = os.WriteFile(release, nil, 0o600)
		}()
		policy := sqliteLockRetryPolicy{
			deadline:    100 * time.Millisecond,
			maxAttempts: 8,
			retryDelay:  func(int) time.Duration { return 2 * time.Millisecond },
		}
		version, err := readSchemaVersionWithRetry(contender, policy)
		if err != nil {
			t.Fatalf("schema version after lock release: %v\nhelper output: %s", err, output.String())
		}
		if version != schemaVersion {
			t.Fatalf("user_version = %d, want %d", version, schemaVersion)
		}
	})

	t.Run("returns other errors immediately", func(t *testing.T) {
		want := errors.New("not a lock")
		calls := 0
		err := retrySQLiteLock(sqliteLockRetryPolicy{
			deadline:    time.Second,
			maxAttempts: 8,
			retryDelay:  func(int) time.Duration { return time.Second },
		}, func() error {
			calls++
			return want
		})
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want original %v", err, want)
		}
		if calls != 1 {
			t.Fatalf("attempts = %d, want 1 for non-lock error", calls)
		}
	})
}

func TestOpenCurrentSchemaDoesNotAcquireWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "current.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	assertRollbackJournal(t, d.db)
	d.Close()

	ready := path + ".ready"
	release := path + ".release"
	cmd, output := startDBHelper(t, path, "hold", map[string]string{
		"KTX_SQLITE_READY":   ready,
		"KTX_SQLITE_RELEASE": release,
	})
	waitForFile(t, ready, 5*time.Second)

	opened := make(chan error, 1)
	go func() {
		db, err := Open(path)
		if err == nil {
			err = db.Close()
		}
		opened <- err
	}()
	select {
	case err := <-opened:
		if err != nil {
			t.Fatalf("Open current schema while another writer holds the database: %v", err)
		}
	case <-time.After(time.Second):
		_ = os.WriteFile(release, nil, 0o600)
		_ = cmd.Wait()
		t.Fatalf("Open current schema waited for a write lock; helper output: %s", output.String())
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("lock holder: %v\n%s", err, output.String())
	}
}

func TestConcurrentStaleOpenersAndWritesAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stale.db")
	start := path + ".start"
	const workers = 6
	type child struct {
		cmd    *exec.Cmd
		output *bytes.Buffer
		ready  string
	}
	children := make([]child, 0, workers)
	for i := 0; i < workers; i++ {
		id := strings.Repeat(string(rune('a'+i)), 16)
		ready := path + ".ready-" + string(rune('a'+i))
		cmd, output := startDBHelper(t, path, "open-write", map[string]string{
			"KTX_SQLITE_START": start,
			"KTX_SQLITE_ID":    id,
			"KTX_SQLITE_READY": ready,
		})
		children = append(children, child{cmd, output, ready})
	}
	for _, child := range children {
		waitForFile(t, child.ready, 5*time.Second)
	}
	if err := os.WriteFile(start, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for i, child := range children {
		if err := child.cmd.Wait(); err != nil {
			t.Errorf("worker %d: %v\n%s", i, err, child.output.String())
		}
	}
	if t.Failed() {
		return
	}

	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	assertRollbackJournal(t, d.db)
	var version, count int
	if err := d.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Errorf("user_version = %d, want %d", version, schemaVersion)
	}
	if err := d.db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != workers {
		t.Errorf("sessions = %d, want %d", count, workers)
	}
}

func TestWriteTransactionRetriesBusyBySQLiteCode(t *testing.T) {
	t.Run("begin", testWriteRetryAtBegin)
	t.Run("commit", testWriteRetryAtCommit)
}

func TestWriteTimeoutCoversRetryBudget(t *testing.T) {
	retryBudget := time.Duration(writeMaxAttempts*sqliteBusyTimeoutMS) * time.Millisecond
	for attempt := range writeMaxAttempts {
		delay := time.Duration(attempt+1) * 10 * time.Millisecond
		retryBudget += min(delay, 100*time.Millisecond)
	}
	if writeTimeout <= retryBudget {
		t.Fatalf("write timeout %s must exceed retry budget %s", writeTimeout, retryBudget)
	}
}

func TestWriteTransactionFailureIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bounded.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.InsertSession(&Session{ID: "boundedreaderrow", Agent: "test"}); err != nil {
		t.Fatal(err)
	}
	assertRollbackJournal(t, d.db)

	readDB, readTx := beginSharedRead(t, path, sqliteBusyTimeoutMS)
	defer readDB.Close()
	defer readTx.Rollback()

	started := time.Now()
	err = d.InsertSession(&Session{ID: "boundedwriterrow", Agent: "test"})
	elapsed := time.Since(started)
	if err == nil || !isSQLiteLockError(err) {
		t.Fatalf("held reader error = %v, want SQLite BUSY/LOCKED", err)
	}
	bound := writeTimeout + time.Duration(sqliteBusyTimeoutMS)*time.Millisecond + 250*time.Millisecond
	if elapsed > bound {
		t.Fatalf("write exceeded bound: elapsed %s, bound %s", elapsed, bound)
	}
	if elapsed < 500*time.Millisecond {
		t.Fatalf("write failed before useful retries: elapsed %s", elapsed)
	}
}

func TestStatsSummaryRowsReleasesPhaseOneReadBeforePhaseTwo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats-summary-phases.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	assertRollbackJournal(t, d.db)

	from := time.Date(2026, 7, 21, 11, 30, 0, 0, time.UTC)
	to := from.Add(time.Minute)
	seedSessionAndCalls(t, d, []callFixture{{
		id:        "summaryphasetest",
		output:    "output",
		timestamp: from.Format(time.RFC3339Nano),
	}})

	phaseOneClosed := make(chan struct{})
	releasePhaseTwo := make(chan struct{})
	type summaryResult struct {
		rows []StatsSummaryRow
		err  error
	}
	summaryDone := make(chan summaryResult, 1)
	go func() {
		rows, err := statsSummaryRowsFor(d.db, from, to, func() {
			close(phaseOneClosed)
			<-releasePhaseTwo
		})
		summaryDone <- summaryResult{rows: rows, err: err}
	}()

	select {
	case <-phaseOneClosed:
	case <-time.After(time.Second):
		t.Fatal("stats summary did not reach the phase boundary")
	}

	// The source query has closed before this point. In rollback-journal mode a
	// monolithic summary query would still hold its shared read lock here and
	// this independent writer would time out before phase two is released.
	writer, err := sql.Open(sqliteDriver, "file:"+path+"?_pragma=busy_timeout(100)")
	if err != nil {
		close(releasePhaseTwo)
		t.Fatal(err)
	}
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	_, writeErr := writer.ExecContext(ctx,
		`INSERT INTO sessions (id, agent) VALUES (?, ?)`, "summaryphasewrite", "test")
	cancel()

	close(releasePhaseTwo)
	result := <-summaryDone
	if writeErr != nil {
		t.Fatalf("writer between stats summary phases: %v", writeErr)
	}
	if result.err != nil {
		t.Fatalf("StatsSummaryRows: %v", result.err)
	}
	if len(result.rows) != 1 {
		t.Fatalf("summary rows = %d, want 1", len(result.rows))
	}
}

func TestMigrationExecutionOutlivesOrdinaryWriteTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slow-migration.db")
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(" + strconv.Itoa(sqliteBusyTimeoutMS) + ")&_txlock=immediate"
	raw, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	started := time.Now()
	err = migrateWith(raw, func(tx *sql.Tx) error {
		ordinary, cancel := writeContext()
		defer cancel()
		<-ordinary.Done()
		if ordinary.Err() != context.DeadlineExceeded {
			return ordinary.Err()
		}
		return migrateTx(tx)
	})
	if err != nil {
		t.Fatalf("migration after ordinary write deadline: %v", err)
	}
	if elapsed := time.Since(started); elapsed < writeTimeout {
		t.Fatalf("migration elapsed %s, want at least ordinary timeout %s", elapsed, writeTimeout)
	}
	var version int
	if err := raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
}

func TestMigrationLockRetriesRemainAttemptBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bounded-migration.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`PRAGMA user_version = 0`); err != nil {
		t.Fatal(err)
	}
	d.Close()

	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(" + strconv.Itoa(sqliteBusyTimeoutMS) + ")&_txlock=immediate"
	holder, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	lock, err := holder.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()

	contender, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	started := time.Now()
	err = migrate(contender)
	elapsed := time.Since(started)
	if err == nil || !isSQLiteLockError(err) {
		t.Fatalf("held migration lock error = %v, want SQLite BUSY/LOCKED", err)
	}
	bound := time.Duration(writeMaxAttempts*sqliteBusyTimeoutMS)*time.Millisecond + 250*time.Millisecond
	for attempt := range writeMaxAttempts {
		delay := time.Duration(attempt+1) * 10 * time.Millisecond
		bound += min(delay, 100*time.Millisecond)
	}
	if elapsed > bound {
		t.Fatalf("migration lock retries exceeded bound: elapsed %s, bound %s", elapsed, bound)
	}
	if elapsed < 500*time.Millisecond {
		t.Fatalf("migration lock retries ended too early: elapsed %s", elapsed)
	}
}

func testWriteRetryAtBegin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retry.db")
	initialized, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	initialized.Close()

	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(1)&_txlock=immediate"
	holder, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	lock, err := holder.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()

	raw, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	d, err := OpenWithoutMigration(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	result := make(chan error, 1)
	go func() {
		result <- d.InsertSession(&Session{ID: "retryretryretryre", Agent: "test"})
	}()
	time.Sleep(75 * time.Millisecond)
	if err := lock.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("write after BUSY retries: %v", err)
	}
}

func testWriteRetryAtCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retry-commit.db")
	initialized, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := initialized.InsertSession(&Session{ID: "existingreadrowx", Agent: "test"}); err != nil {
		t.Fatal(err)
	}
	initialized.Close()

	reader, readTx := beginSharedRead(t, path, 25)
	defer reader.Close()
	defer readTx.Rollback()

	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(25)&_txlock=immediate"
	raw, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	d, err := OpenWithoutMigration(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := make(chan error, 1)
	secondBody := make(chan struct{})
	allowSecondCommit := make(chan struct{})
	var bodyRuns atomic.Int32
	go func() {
		result <- withWriteTx(ctx, d.db, func(tx *sql.Tx) error {
			run := bodyRuns.Add(1)
			if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (id, agent) VALUES ('commitretrytestx', 'test')`); err != nil {
				return err
			}
			if run == 2 {
				close(secondBody)
				select {
				case <-allowSecondCommit:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})
	}()
	select {
	case <-secondBody:
	case <-ctx.Done():
		t.Fatalf("transaction body did not rerun after commit BUSY; runs=%d", bodyRuns.Load())
	}
	if err := readTx.Commit(); err != nil {
		t.Fatal(err)
	}
	close(allowSecondCommit)
	if err := <-result; err != nil {
		t.Fatalf("write after commit BUSY retries: %v", err)
	}
	if runs := bodyRuns.Load(); runs != 2 {
		t.Fatalf("transaction body runs = %d, want 2", runs)
	}
	var count int
	if err := d.db.QueryRow(`SELECT count(*) FROM sessions WHERE id = 'commitretrytestx'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("committed rows = %d, want 1", count)
	}
}

func beginSharedRead(t *testing.T, path string, busyTimeoutMS int) (*sql.DB, *sql.Tx) {
	t.Helper()
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(" + strconv.Itoa(busyTimeoutMS) + ")"
	database, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	var id string
	if err := tx.QueryRow(`SELECT id FROM sessions LIMIT 1`).Scan(&id); err != nil {
		tx.Rollback()
		database.Close()
		t.Fatal(err)
	}
	return database, tx
}

func startDBHelper(t *testing.T, path, role string, extra map[string]string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSQLiteProcessHelper$")
	cmd.Env = append(os.Environ(),
		"KTX_SQLITE_HELPER=1",
		"KTX_SQLITE_PATH="+path,
		"KTX_SQLITE_ROLE="+role,
	)
	for key, value := range extra {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, &output
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func assertRollbackJournal(t *testing.T, database *sql.DB) {
	t.Helper()
	var mode string
	if err := database.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "delete" {
		t.Fatalf("journal_mode = %q, want delete", mode)
	}
}
