package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

func TestHookReaderBackpressureProcessHelper(t *testing.T) {
	if os.Getenv("KTX_HOOK_READER_BACKPRESSURE_HELPER") != "1" {
		return
	}

	var code int
	switch os.Getenv("KTX_HOOK_READER_BACKPRESSURE_ROLE") {
	case "read":
		code = Run([]string{"read", os.Getenv("KTX_HOOK_READER_BACKPRESSURE_SOURCE")})
	case "hook":
		code = Run([]string{"hook"})
	default:
		t.Fatalf("unknown helper role %q", os.Getenv("KTX_HOOK_READER_BACKPRESSURE_ROLE"))
	}
	os.Exit(code)
}

func TestHookBlockedByBackpressuredSupportedReaderIsBoundedAndRecovers(t *testing.T) {
	const (
		readerSourceID  = "stalledreadsrcaa"
		blockedSourceID = "blockedhooksrcaa"
		readerSessionID = "stalledreadsessa"
		original        = "original hook output"
	)

	path := filepath.Join(t.TempDir(), "reader-backpressure.db")
	database, err := dbpkg.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InsertSession(&dbpkg.Session{ID: readerSessionID, Agent: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := database.PersistToolCall(&dbpkg.ToolCall{
		ID:          readerSourceID,
		SessionID:   readerSessionID,
		Tool:        "bash",
		Input:       "true",
		Output:      strings.Repeat("x", 2*1024*1024),
		ContentHash: dbpkg.ContentHash(strings.Repeat("x", 2*1024*1024)),
		Timestamp:   "2026-08-09T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	configHome := t.TempDir()
	runtimeDir := t.TempDir()
	reader := hookReaderBackpressureCommand("read", path, configHome, runtimeDir, readerSourceID)
	readerStdout, err := reader.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var readerStderr bytes.Buffer
	reader.Stderr = &readerStderr
	if err := reader.Start(); err != nil {
		t.Fatal(err)
	}
	readerDone := make(chan error, 1)
	t.Cleanup(func() {
		_ = readerStdout.Close()
		_ = reader.Process.Kill()
		select {
		case <-readerDone:
		default:
		}
	})

	firstByte := make(chan error, 1)
	go func() {
		var first [1]byte
		_, err := io.ReadFull(readerStdout, first[:])
		firstByte <- err
	}()
	select {
	case err := <-firstByte:
		if err != nil {
			t.Fatalf("read first supported-reader output byte: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("supported reader did not begin output")
	}
	go func() { readerDone <- reader.Wait() }()
	select {
	case err := <-readerDone:
		t.Fatalf("2 MiB supported reader did not remain backpressured: %v; stderr=%s", err, readerStderr.String())
	case <-time.After(50 * time.Millisecond):
	}

	payload := []byte(`{"id":"` + blockedSourceID + `","tool":"bash","input":"true","output":"` + original + `","timestamp":"2026-08-09T00:00:01Z"}`)
	hookContext, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	hook := exec.CommandContext(hookContext, os.Args[0], "-test.run=^TestHookReaderBackpressureProcessHelper$")
	hook.Env = hookReaderBackpressureEnv("hook", path, configHome, runtimeDir, "")
	hook.Stdin = bytes.NewReader(payload)
	var hookStdout, hookStderr bytes.Buffer
	hook.Stdout, hook.Stderr = &hookStdout, &hookStderr
	started := time.Now()
	err = hook.Run()
	elapsed := time.Since(started)
	if errors.Is(hookContext.Err(), context.DeadlineExceeded) {
		t.Fatalf("hook exceeded bounded startup wait: elapsed=%s stdout=%q stderr=%q", elapsed, hookStdout.String(), hookStderr.String())
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != ExitInternal {
		t.Fatalf("blocked hook result = %v, want exit %d; stdout=%q stderr=%q", err, ExitInternal, hookStdout.String(), hookStderr.String())
	}
	if elapsed > 3*time.Second {
		t.Fatalf("blocked hook exceeded startup bound: elapsed=%s", elapsed)
	}
	t.Logf("blocked hook returned database-unavailable after %s", elapsed)
	if hookStdout.Len() != 0 {
		t.Fatalf("blocked hook stdout = %q, want empty", hookStdout.String())
	}
	assertBackpressuredHookUnavailable(t, path, hookStderr.Bytes())
	assertHookEvidenceCounts(t, database, blockedSourceID, 0, 0, 0)

	if _, err := io.Copy(io.Discard, readerStdout); err != nil {
		t.Fatalf("release supported reader: %v", err)
	}
	select {
	case err := <-readerDone:
		if err != nil {
			t.Fatalf("supported reader after release: %v; stderr=%s", err, readerStderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("supported reader did not release coordination ownership")
	}

	retry := hookReaderBackpressureCommand("hook", path, configHome, runtimeDir, "")
	retry.Stdin = bytes.NewReader(payload)
	var retryStdout, retryStderr bytes.Buffer
	retry.Stdout, retry.Stderr = &retryStdout, &retryStderr
	retryStarted := time.Now()
	if err := retry.Run(); err != nil {
		t.Fatalf("hook retry after reader release: %v; stdout=%q stderr=%q", err, retryStdout.String(), retryStderr.String())
	}
	t.Logf("hook retry persisted normally after reader release in %s", time.Since(retryStarted))
	var envelope hookEnvelope
	if err := json.Unmarshal(retryStdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode retry envelope: %v\nstdout=%s", err, retryStdout.String())
	}
	if envelope.SourceID != blockedSourceID || envelope.Output != original {
		t.Errorf("retry envelope = %+v, want source %q and original output", envelope, blockedSourceID)
	}
	assertHookEvidenceCounts(t, database, blockedSourceID, 1, 0, 1)
}

func hookReaderBackpressureCommand(role, path, configHome, runtimeDir, sourceID string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHookReaderBackpressureProcessHelper$")
	cmd.Env = hookReaderBackpressureEnv(role, path, configHome, runtimeDir, sourceID)
	return cmd
}

func hookReaderBackpressureEnv(role, path, configHome, runtimeDir, sourceID string) []string {
	return append(os.Environ(),
		"KTX_HOOK_READER_BACKPRESSURE_HELPER=1",
		"KTX_HOOK_READER_BACKPRESSURE_ROLE="+role,
		"KTX_HOOK_READER_BACKPRESSURE_SOURCE="+sourceID,
		"KTX_DB="+path,
		"XDG_CONFIG_HOME="+configHome,
		"XDG_RUNTIME_DIR="+runtimeDir,
	)
}

func assertBackpressuredHookUnavailable(t *testing.T, path string, stderr []byte) {
	t.Helper()
	var result struct {
		Error struct {
			Code         string `json:"code"`
			DatabasePath string `json:"database_path"`
			Correction   string `json:"correction"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stderr, &result); err != nil {
		t.Fatalf("decode database-unavailable result: %v\nstderr=%s", err, stderr)
	}
	canonicalPath, err := dbpkg.CanonicalPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error.Code != string(CodeDatabaseUnavailable) || result.Error.DatabasePath != canonicalPath || result.Error.Correction == "" {
		t.Errorf("database-unavailable result = %+v, want code, canonical path, and correction", result.Error)
	}
	assertNoHookLockDriverWording(t, string(stderr))
}
