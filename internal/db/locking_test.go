package db

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
	default:
		t.Fatalf("unknown helper role %q", os.Getenv("KTX_SQLITE_ROLE"))
	}
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
