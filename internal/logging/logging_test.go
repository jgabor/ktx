package logging

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testEvent(databasePath string) DatabaseUnavailable {
	return DatabaseUnavailable{
		Command: "hook", ExecutablePath: "/tmp/ktx", ExecutableSHA256: strings.Repeat("a", 64),
		DatabasePath: databasePath, FailureClass: "sqlite-lock", OperationPhase: "open",
		ElapsedWaitNanoseconds: 12,
	}
}

func TestDefaultPath(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	got, err := DefaultPath()
	if err != nil || got != filepath.Join(state, "ktx", "ktx.log") {
		t.Fatalf("DefaultPath() = %q, %v", got, err)
	}
}

func TestPrivateLogDACL(t *testing.T) {
	const sid = "S-1-5-21-1000"
	if got, want := privateLogDACL(sid), "D:P(A;;FA;;;"+sid+")"; got != want {
		t.Fatalf("privateLogDACL() = %q, want %q", got, want)
	}
}

func TestAppendDatabaseUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "events.jsonl")
	AppendDatabaseUnavailable(Config{Level: Error, Path: path}, "", testEvent("/tmp/ktx.db"))

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		t.Fatal("missing record")
	}
	var record map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	want := []string{"timestamp", "level", "event", "ktx_command", "executable_path", "executable_sha256", "database_path", "failure_class", "operation_phase", "elapsed_wait_nanoseconds"}
	if len(record) != len(want) || scanner.Scan() {
		t.Fatalf("record fields=%v or multiple records", record)
	}
	for _, field := range want {
		if _, ok := record[field]; !ok {
			t.Errorf("missing field %q", field)
		}
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode: info=%v err=%v", info, err)
	}
}

func TestOffDoesNoFilesystemWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "events.jsonl")
	AppendDatabaseUnavailable(Config{Level: Off, Path: path}, "", testEvent("/tmp/ktx.db"))
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("destination directory exists or stat failed: %v", err)
	}
}

func TestRejectsUnsafeTargets(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "ktx.db")
	if err := os.WriteFile(databasePath, []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	AppendDatabaseUnavailable(Config{Level: Error, Path: databasePath}, databasePath, testEvent(databasePath))
	if got, _ := os.ReadFile(databasePath); string(got) != "database" {
		t.Fatalf("database changed: %q", got)
	}

	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	AppendDatabaseUnavailable(Config{Level: Error, Path: directory}, databasePath, testEvent(databasePath))
}
