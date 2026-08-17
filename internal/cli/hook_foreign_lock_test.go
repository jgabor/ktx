package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

func TestHookForeignLockStageHelper(t *testing.T) {
	if os.Getenv("KTX_HOOK_FOREIGN_LOCK_STAGE") != "1" {
		return
	}

	ready := os.Getenv("KTX_HOOK_FOREIGN_LOCK_READY")
	release := os.Getenv("KTX_HOOK_FOREIGN_LOCK_RELEASE")
	if ready == "" || release == "" {
		t.Fatal("foreign-lock stage paths are required")
	}
	if err := os.WriteFile(ready, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitForHookForeignLockFile(t, release, 5*time.Second)
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(os.Stdout, "compressed\n"); err != nil {
		t.Fatal(err)
	}
}

func TestHookForeignLockBeforeSourceLeavesEventUnaccepted(t *testing.T) {
	d, path := hookForeignLockDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	const (
		sessionID = "foreign-lock-source-session"
		sourceID  = "foreignlocksrcaa"
		original  = "original tool output"
	)
	if err := d.InsertSession(&dbpkg.Session{
		ID: sessionID, Agent: "test", StartedAt: "2026-08-09T00:00:00Z", LastSeenAt: "2026-08-09T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	lock := holdHookForeignWriteLock(t, path)
	payload := fmt.Sprintf(`{"id":%q,"session_id":%q,"tool":"bash","input":"true","output":%q,"timestamp":"2026-08-09T00:00:00Z"}`,
		sourceID, sessionID, original)
	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err == nil {
		t.Fatal("hook succeeded while the foreign write lock was held")
	}
	assertHookForeignLockRendered(t, path, dbpkg.OperationSource, err)
	if stdout.Len() != 0 {
		t.Fatalf("hook wrote an envelope before source acceptance: %q", stdout.String())
	}

	releaseHookForeignWriteLock(t, lock)
	if _, err := d.GetToolCall(sourceID); !errors.Is(err, dbpkg.ErrNotFound) {
		t.Fatalf("source after unaccepted hook = %v, want not found", err)
	}
	if _, err := d.GetCompressedBySource(sourceID); !errors.Is(err, dbpkg.ErrNotFound) {
		t.Fatalf("artifact after unaccepted hook = %v, want not found", err)
	}
	if _, err := d.GetCompressionDisposition(sourceID); !errors.Is(err, dbpkg.ErrNotFound) {
		t.Fatalf("disposition after unaccepted hook = %v, want not found", err)
	}

	stdout.Reset()
	stderr.Reset()
	if _, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr); err != nil {
		t.Fatalf("retry after foreign lock release: %v", err)
	}
	var envelope hookEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode retry envelope: %v\nstdout=%s", err, stdout.String())
	}
	if envelope.SourceID != sourceID || envelope.Output != original {
		t.Errorf("retry envelope = %+v, want source %q and original output", envelope, sourceID)
	}
}

func TestHookForeignLockAfterSourceKeepsSourceAndRetryConverges(t *testing.T) {
	d, path := hookForeignLockDB(t)
	hookChdirTemp(t)
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	ready := filepath.Join(t.TempDir(), "stage-ready")
	release := filepath.Join(t.TempDir(), "stage-release")
	t.Setenv("KTX_HOOK_FOREIGN_LOCK_STAGE", "1")
	t.Setenv("KTX_HOOK_FOREIGN_LOCK_READY", ready)
	t.Setenv("KTX_HOOK_FOREIGN_LOCK_RELEASE", release)
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	hookWriteUserConfig(t, fmt.Sprintf(`[pipeline.default]
stages = ["foreign-lock-stage:-test.run=^TestHookForeignLockStageHelper$"]
timeout = "5s"

[[plugin]]
name = "foreign-lock-stage"
command = %q
`, os.Args[0]))

	const (
		runtimeSessionID = "foreign-lock-artifact-runtime"
		callID           = "foreign-lock-artifact-call"
	)
	original := strings.Repeat("original tool output\n", 20)
	payload := openCodeHookPayload(t, runtimeSessionID, callID, "bash", `{"command":"true"}`, original)
	sourceID := mustOpenCodeID(t, runtimeSessionID, callID)

	var stdout, stderr bytes.Buffer
	var observations []string
	done := make(chan error, 1)
	go func() {
		_, err := hookLogicObserved(strings.NewReader(payload), d, &stdout, &stderr, func(kind, phase string) {
			observations = append(observations, kind+":"+phase)
		})
		done <- err
	}()
	waitForHookForeignLockFile(t, ready, 5*time.Second)
	if source, err := d.GetToolCall(sourceID); err != nil || source.Output != original {
		t.Fatalf("source before artifact lock = %+v, %v", source, err)
	}

	lock := holdHookForeignWriteLock(t, path)
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("hook succeeded while the artifact write was foreign-locked")
		}
		assertHookForeignLockRendered(t, path, dbpkg.OperationArtifact, err)
	case <-time.After(5 * time.Second):
		t.Fatal("hook did not return after artifact lock exhaustion")
	}
	if stdout.Len() != 0 {
		t.Fatalf("hook wrote an envelope after artifact failure: %q", stdout.String())
	}
	assertHookObservations(t, observations, dbpkg.OperationSource)

	releaseHookForeignWriteLock(t, lock)
	if _, err := d.GetToolCall(sourceID); err != nil {
		t.Fatalf("accepted source was lost after artifact failure: %v", err)
	}
	if _, err := d.GetCompressedBySource(sourceID); !errors.Is(err, dbpkg.ErrNotFound) {
		t.Fatalf("artifact after failed artifact phase = %v, want not found", err)
	}
	if _, err := d.GetCompressionDisposition(sourceID); !errors.Is(err, dbpkg.ErrNotFound) {
		t.Fatalf("disposition after failed artifact phase = %v, want not found", err)
	}

	stdout.Reset()
	stderr.Reset()
	if _, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr); err != nil {
		t.Fatalf("retry after artifact lock release: %v", err)
	}
	var envelope hookEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode retry envelope: %v\nstdout=%s", err, stdout.String())
	}
	if envelope.SourceID != sourceID || envelope.CompressedID == nil || !strings.HasPrefix(envelope.Output, "compressed\n") || len(envelope.Output) >= len(original) {
		t.Errorf("retry envelope = %+v, want retained source and compressed output", envelope)
	}
	assertHookEvidenceCounts(t, d, sourceID, 1, 1, 1)
}

func TestHookForeignLockDuringDispositionKeepsAcceptedEvidence(t *testing.T) {
	d, path := hookForeignLockDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, "[pipeline.default]\nstages = [\"head:1\"]\n")
	const (
		runtimeSessionID = "foreign-lock-disposition-session"
		callID           = "foreign-lock-disposition-call"
	)
	original := strings.Repeat("line one\nline two\n", 20)
	payload := openCodeHookPayload(t, runtimeSessionID, callID, "bash", `{"command":"true"}`, original)
	sourceID := mustOpenCodeID(t, runtimeSessionID, callID)

	var stdout, stderr bytes.Buffer
	var observations []string
	artifactAccepted := make(chan struct{})
	continueHook := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(continueHook)
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, err := hookLogicObserved(strings.NewReader(payload), d, &stdout, &stderr, func(kind, phase string) {
			observations = append(observations, kind+":"+phase)
			if kind == hookAcceptanceCommit && phase == dbpkg.OperationArtifact {
				close(artifactAccepted)
				<-continueHook
			}
		})
		done <- err
	}()
	select {
	case <-artifactAccepted:
	case err := <-done:
		t.Fatalf("hook stopped before artifact acceptance: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("hook did not reach artifact acceptance")
	}
	lock := holdHookForeignWriteLock(t, path)
	close(continueHook)
	released = true
	var err error
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("hook succeeded while the disposition write was foreign-locked")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hook did not return after disposition lock exhaustion")
	}
	assertHookForeignLockRendered(t, path, dbpkg.OperationDisposition, err)
	if stdout.Len() != 0 {
		t.Fatalf("hook wrote an envelope instead of preserving the adapter's original-output fallback: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("hook wrote unexpected stage diagnostics: %q", stderr.String())
	}
	assertHookObservations(t, observations, dbpkg.OperationSource, dbpkg.OperationArtifact)

	releaseHookForeignWriteLock(t, lock)
	source, err := d.GetToolCall(sourceID)
	if err != nil {
		t.Fatalf("accepted source was lost after disposition failure: %v", err)
	}
	if source.Output != original {
		t.Fatalf("retained source output = %q, want original-output fallback", source.Output)
	}
	artifact, err := d.GetCompressedBySource(sourceID)
	if err != nil {
		t.Fatalf("accepted artifact was lost after disposition failure: %v", err)
	}
	if artifact.Output != "line one\n" {
		t.Fatalf("retained artifact output = %q, want transformed output", artifact.Output)
	}
	if _, err := d.GetCompressionDisposition(sourceID); !errors.Is(err, dbpkg.ErrNotFound) {
		t.Fatalf("disposition after failed phase = %v, want not found", err)
	}

	retryStdout, retryStderr, retryObservations, err := runObservedHook(t, d, payload)
	if err != nil || retryStderr != "" {
		t.Fatalf("retry hook = %v, stderr=%q", err, retryStderr)
	}
	var envelope hookEnvelope
	if err := json.Unmarshal([]byte(retryStdout), &envelope); err != nil {
		t.Fatalf("decode retry envelope: %v\nstdout=%s", err, retryStdout)
	}
	if envelope.SourceID != sourceID || envelope.CompressedID == nil || *envelope.CompressedID != artifact.ID || envelope.Output != artifact.Output {
		t.Errorf("retry envelope = %+v, want retained source and artifact", envelope)
	}
	assertHookObservations(t, retryObservations, dbpkg.OperationSource, dbpkg.OperationArtifact, dbpkg.OperationDisposition)
	disposition, err := d.GetCompressionDisposition(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if disposition.Disposition != dbpkg.CompressionDispositionArtifactPersisted || disposition.ArtifactID != artifact.ID {
		t.Errorf("retried disposition = %+v", disposition)
	}
	assertHookEvidenceCounts(t, d, sourceID, 1, 1, 1)
}

func hookForeignLockDB(t *testing.T) (*dbpkg.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ktx.db")
	t.Setenv("KTX_DB", path)
	d := hookTestDB(t)
	if _, err := d.DB().Exec(`PRAGMA busy_timeout = 1`); err != nil {
		t.Fatal(err)
	}
	return d, path
}

func holdHookForeignWriteLock(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(1)")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	if _, err := database.Exec(`BEGIN IMMEDIATE`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`ROLLBACK`)
		_ = database.Close()
	})
	return database
}

func releaseHookForeignWriteLock(t *testing.T, database *sql.DB) {
	t.Helper()
	if _, err := database.Exec(`ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertHookForeignLockRendered(t *testing.T, path, operationPhase string, err error) {
	t.Helper()
	if !dbpkg.IsSQLiteLockError(err) {
		t.Fatalf("error = %v, want wrapped SQLite lock error", err)
	}
	started := time.Now().Add(-time.Millisecond)
	classified := classifyDatabaseLockError(path, "hook", dbpkg.AttributeDatabaseUnavailable(operationPhase, started, err), operationPhase, started)
	if classified == nil {
		t.Fatalf("classifyDatabaseLockError(%v) = nil", err)
	}
	if exitCodeOf(classified) != ExitInternal {
		t.Errorf("exit code = %d, want %d", exitCodeOf(classified), ExitInternal)
	}

	canonicalPath, pathErr := dbpkg.CanonicalPath(path)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	for _, format := range []string{FormatJSON, FormatText} {
		var rendered bytes.Buffer
		printError(&rendered, classified, format)
		assertNoHookLockDriverWording(t, rendered.String())
		if format == FormatText {
			if !strings.Contains(rendered.String(), "ktx: database-unavailable:") || !strings.Contains(rendered.String(), canonicalPath) {
				t.Errorf("text database-unavailable result = %q", rendered.String())
			}
			continue
		}
		var envelope struct {
			Error struct {
				Code                   string `json:"code"`
				DatabasePath           string `json:"database_path"`
				ExecutablePath         string `json:"executable_path"`
				ExecutableSHA256       string `json:"executable_sha256"`
				FailureClass           string `json:"failure_class"`
				OperationPhase         string `json:"operation_phase"`
				ElapsedWaitNanoseconds int64  `json:"elapsed_wait_nanoseconds"`
				Correction             string `json:"correction"`
			} `json:"error"`
		}
		if decodeErr := json.Unmarshal(rendered.Bytes(), &envelope); decodeErr != nil {
			t.Fatalf("decode database-unavailable result: %v\n%s", decodeErr, rendered.String())
		}
		if envelope.Error.Code != string(CodeDatabaseUnavailable) || envelope.Error.DatabasePath != canonicalPath ||
			envelope.Error.ExecutablePath == "" || len(envelope.Error.ExecutableSHA256) != 64 ||
			envelope.Error.FailureClass != dbpkg.UnavailableClassSQLiteLock || envelope.Error.OperationPhase != operationPhase ||
			envelope.Error.ElapsedWaitNanoseconds <= 0 || envelope.Error.Correction == "" {
			t.Errorf("database-unavailable result = %+v", envelope.Error)
		}
	}
}

func assertNoHookLockDriverWording(t *testing.T, output string) {
	t.Helper()
	for _, forbidden := range []string{"database is locked", "sqlite_busy", "sqlite_locked"} {
		if strings.Contains(strings.ToLower(output), forbidden) {
			t.Errorf("rendered lock result exposes %q: %q", forbidden, output)
		}
	}
}

func assertHookEvidenceCounts(t *testing.T, d *dbpkg.DB, sourceID string, wantSources, wantArtifacts, wantDispositions int) {
	t.Helper()
	for _, check := range []struct {
		table string
		query string
		want  int
	}{
		{table: "tool_calls", query: `SELECT count(*) FROM tool_calls WHERE id = ?`, want: wantSources},
		{table: "compressed", query: `SELECT count(*) FROM compressed WHERE source_id = ?`, want: wantArtifacts},
		{table: "compression_dispositions", query: `SELECT count(*) FROM compression_dispositions WHERE source_id = ?`, want: wantDispositions},
	} {
		var got int
		if err := d.DB().QueryRow(check.query, sourceID).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", check.table, err)
		}
		if got != check.want {
			t.Errorf("%s rows for %q = %d, want %d", check.table, sourceID, got, check.want)
		}
	}
}

func waitForHookForeignLockFile(t *testing.T, path string, timeout time.Duration) {
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
