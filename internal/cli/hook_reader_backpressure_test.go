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

func TestBackpressuredReadReleasesDatabaseBeforeExactOutputCompletes(t *testing.T) {
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
	readerRecord, err := database.GetToolCall(readerSourceID)
	if err != nil {
		t.Fatal(err)
	}
	expectedOutput, err := json.Marshal(readerRecord)
	if err != nil {
		t.Fatal(err)
	}
	expectedOutput = append(expectedOutput, '\n')

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

	type firstByteResult struct {
		value byte
		err   error
	}
	firstByte := make(chan firstByteResult, 1)
	go func() {
		var first [1]byte
		_, err := io.ReadFull(readerStdout, first[:])
		firstByte <- firstByteResult{value: first[0], err: err}
	}()
	var first byte
	select {
	case result := <-firstByte:
		if result.err != nil {
			t.Fatalf("read first supported-reader output byte: %v", result.err)
		}
		first = result.value
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
	if err != nil {
		t.Fatalf("hook while read output remained blocked: %v; stdout=%q stderr=%q", err, hookStdout.String(), hookStderr.String())
	}
	if elapsed > 3*time.Second {
		t.Fatalf("hook exceeded startup bound: elapsed=%s", elapsed)
	}
	if hookStderr.Len() != 0 {
		t.Fatalf("hook stderr = %q, want empty", hookStderr.String())
	}
	var envelope hookEnvelope
	if err := json.Unmarshal(hookStdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode hook envelope: %v\nstdout=%s", err, hookStdout.String())
	}
	if envelope.SourceID != blockedSourceID || envelope.Output != original {
		t.Errorf("hook envelope = %+v, want source %q and original output", envelope, blockedSourceID)
	}
	assertHookEvidenceCounts(t, database, blockedSourceID, 1, 0, 1)

	actualOutput := bytes.NewBuffer([]byte{first})
	if _, err := io.Copy(actualOutput, readerStdout); err != nil {
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
	if !bytes.Equal(actualOutput.Bytes(), expectedOutput) {
		t.Fatalf("read output differs: got %d bytes, want %d", actualOutput.Len(), len(expectedOutput))
	}
}

func TestTerminatedReadLeavesNoDatabaseOwnership(t *testing.T) {
	const (
		readerSourceID  = "killedreadsrcaaa"
		readerSessionID = "killedreadsessaa"
		hookSourceID    = "afterkillsourcea"
	)
	path := filepath.Join(t.TempDir(), "reader-termination.db")
	database, err := dbpkg.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InsertSession(&dbpkg.Session{ID: readerSessionID, Agent: "test"}); err != nil {
		t.Fatal(err)
	}
	largeOutput := strings.Repeat("x", 2*1024*1024)
	if err := database.PersistToolCall(&dbpkg.ToolCall{
		ID: readerSourceID, SessionID: readerSessionID, Tool: "bash", Input: "true",
		Output: largeOutput, ContentHash: dbpkg.ContentHash(largeOutput), Timestamp: "2026-08-11T00:00:00Z",
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
	var first [1]byte
	if _, err := io.ReadFull(readerStdout, first[:]); err != nil {
		t.Fatalf("read first byte: %v", err)
	}
	if err := reader.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Wait(); err == nil {
		t.Fatal("terminated reader exited successfully")
	}

	payload := []byte(`{"id":"` + hookSourceID + `","tool":"bash","input":"true","output":"after termination","timestamp":"2026-08-11T00:00:01Z"}`)
	hook := hookReaderBackpressureCommand("hook", path, configHome, runtimeDir, "")
	hook.Stdin = bytes.NewReader(payload)
	var hookStdout, hookStderr bytes.Buffer
	hook.Stdout, hook.Stderr = &hookStdout, &hookStderr
	if err := hook.Run(); err != nil {
		t.Fatalf("hook after reader termination: %v; stdout=%q stderr=%q", err, hookStdout.String(), hookStderr.String())
	}
	assertHookEvidenceCounts(t, database, hookSourceID, 1, 0, 1)
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
