package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

type databaseUnavailableLogRecord struct {
	Timestamp              string `json:"timestamp"`
	Level                  string `json:"level"`
	Event                  string `json:"event"`
	Command                string `json:"ktx_command"`
	ExecutablePath         string `json:"executable_path"`
	ExecutableSHA256       string `json:"executable_sha256"`
	DatabasePath           string `json:"database_path"`
	FailureClass           string `json:"failure_class"`
	OperationPhase         string `json:"operation_phase"`
	ElapsedWaitNanoseconds int64  `json:"elapsed_wait_nanoseconds"`
}

func TestDatabaseUnavailableLoggingExactlyOnce(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	logPath := filepath.Join(root, "events.jsonl")
	hookWriteUserConfig(t, "[logs]\nlevel = \"error\"\npath = \""+logPath+"\"\n")
	databasePath := filepath.Join(root, "database.db")
	details := &dbpkg.DatabaseUnavailable{Class: dbpkg.UnavailableClassCoordinatorTimeout, Phase: dbpkg.OperationQuery, ElapsedWait: 2*time.Second + 2*time.Millisecond}
	failure := databaseUnavailableError(databasePath, "get", details)
	if typed, ok := failure.(*Error); !ok || typed.Code != CodeDatabaseUnavailable {
		t.Fatalf("failure = %#v", failure)
	}
	record := readDatabaseUnavailableLog(t, logPath)
	if record.Timestamp == "" || record.Level != "error" || record.Event != "database-unavailable" || record.Command != "get" ||
		record.ExecutablePath == "" || len(record.ExecutableSHA256) != 64 || record.DatabasePath != databasePath ||
		record.FailureClass != dbpkg.UnavailableClassCoordinatorTimeout || record.OperationPhase != dbpkg.OperationQuery ||
		record.ElapsedWaitNanoseconds != details.ElapsedWait.Nanoseconds() {
		t.Fatalf("record = %#v", record)
	}
}

func TestDatabaseUnavailableLoggingIsolation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	databasePath := filepath.Join(root, "database.db")
	details := &dbpkg.DatabaseUnavailable{Class: dbpkg.UnavailableClassSQLiteLock, Phase: dbpkg.OperationSource, ElapsedWait: time.Millisecond}

	render := func() string {
		var stderr bytes.Buffer
		printError(&stderr, databaseUnavailableError(databasePath, "hook", details), FormatJSON)
		return stderr.String()
	}

	withoutLogging := render()
	if entries, _ := filepath.Glob(filepath.Join(root, "state", "**")); len(entries) != 0 {
		t.Fatalf("disabled logging created state entries: %v", entries)
	}

	hookWriteUserConfig(t, "[logs]\nlevel = \"off\"\n")
	if got := render(); got != withoutLogging {
		t.Fatalf("off logging changed stderr:\n%s\nwant:\n%s", got, withoutLogging)
	}

	hookWriteUserConfig(t, "[logs]\nlevel = \"error\"\npath = \""+filepath.Join(root, "missing", "events.log")+"\"\n")
	if err := os.WriteFile(filepath.Join(root, "missing"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := render(); got != withoutLogging {
		t.Fatalf("failed sink changed stderr:\n%s\nwant:\n%s", got, withoutLogging)
	}

	hookWriteUserConfig(t, "[logs]\nlevel = \"invalid\"\n")
	if got := render(); got != withoutLogging {
		t.Fatalf("invalid logging changed stderr:\n%s\nwant:\n%s", got, withoutLogging)
	}

	logPath := filepath.Join(root, "other-failure.log")
	hookWriteUserConfig(t, "[logs]\nlevel = \"error\"\npath = \""+logPath+"\"\n")
	if classified := classifyDatabaseLockError(databasePath, "hook", errors.New("secret arbitrary failure"), dbpkg.OperationOpen, time.Now()); classified != nil {
		t.Fatalf("other failure classified: %v", classified)
	}
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("other failure created log: %v", err)
	}
}

func readDatabaseUnavailableLog(t *testing.T, path string) databaseUnavailableLogRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		t.Fatalf("missing record: %v", scanner.Err())
	}
	line := append([]byte(nil), scanner.Bytes()...)
	if scanner.Scan() {
		t.Fatal("more than one record")
	}
	var record databaseUnavailableLogRecord
	if err := json.Unmarshal(line, &record); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"payload", "SELECT", "database is locked", "secret arbitrary failure"} {
		if strings.Contains(string(line), forbidden) {
			t.Errorf("record contains %q: %s", forbidden, line)
		}
	}
	return record
}
