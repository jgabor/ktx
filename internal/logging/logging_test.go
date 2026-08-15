package logging

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type testEvent struct {
	level Level
	pid   int
	seq   int
	fill  string
}

type testRecord struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Event     string `json:"event"`
	PID       int    `json:"pid"`
	Sequence  int    `json:"sequence"`
	Fill      string `json:"fill,omitempty"`
}

func (e testEvent) logLevel() Level { return e.level }
func (e testEvent) logRecord(now time.Time) any {
	return testRecord{
		Timestamp: now.Format(time.RFC3339Nano),
		Level:     e.level.String(),
		Event:     "test_event",
		PID:       e.pid,
		Sequence:  e.seq,
		Fill:      e.fill,
	}
}

func TestResolvePathAndFiltering(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)

	if got := New(Error, "", "").path; got != filepath.Join(root, "ktx", "ktx.log") {
		t.Fatalf("default path = %q", got)
	}
	if got := New(Error, "logs/events.jsonl", "").path; got != filepath.Join(root, "logs", "events.jsonl") {
		t.Fatalf("relative path = %q", got)
	}
	absolute := filepath.Join(t.TempDir(), "explicit.log")
	if got := New(Error, absolute, "").path; got != absolute {
		t.Fatalf("absolute path = %q", got)
	}
	if sink := New(Off, "../../escape.log", ""); !sink.disabled || sink.path != "" {
		t.Fatalf("off sink resolved path: %#v", sink)
	}
	if sink := New(Error, "../../escape.log", ""); !sink.disabled {
		t.Fatal("escaping relative path enabled sink")
	}

	path := filepath.Join(root, "threshold.log")
	sink := New(Warn, path, "")
	sink.Write(testEvent{level: Info})
	sink.Write(testEvent{level: Warn, seq: 1})
	sink.Write(testEvent{level: Error, seq: 2})
	records := readRecords(t, path)
	if len(records) != 2 || records[0].Level != "warn" || records[1].Level != "error" {
		t.Fatalf("records = %#v", records)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode: info=%v err=%v", info, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	sink.Write(testEvent{level: Error, seq: 3})
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("existing destination mode: info=%v err=%v", info, err)
	}
}

func TestRejectedEventsDoNoFilesystemWork(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "new", "events.log")
	sink := New(Error, path, "")
	sink.Write(testEvent{level: Warn})
	sink.Write(testEvent{level: Error, fill: strings.Repeat("x", maxRecordBytes)})
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("destination directory exists or stat failed: %v", err)
	}
}

func TestRejectsUnsafeDestinations(t *testing.T) {
	root := t.TempDir()
	database := filepath.Join(root, "ktx.db")
	if err := os.WriteFile(database, []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"database path": database,
		"directory":     filepath.Join(root, "directory"),
		"symlink":       filepath.Join(root, "alias.log"),
		"hard link":     filepath.Join(root, "hard.log"),
	} {
		t.Run(name, func(t *testing.T) {
			switch name {
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(database, path); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			case "hard link":
				if err := os.Link(database, path); err != nil {
					t.Skipf("hard link unavailable: %v", err)
				}
			}
			before, err := os.ReadFile(database)
			if err != nil {
				t.Fatal(err)
			}
			New(Error, path, database).Write(testEvent{level: Error})
			after, err := os.ReadFile(database)
			if err != nil || string(after) != string(before) {
				t.Fatalf("database changed: before=%q after=%q err=%v", before, after, err)
			}
		})
	}
}

func TestContentionDropsWithinBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	lock, err := openRegular(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := acquire(lock, maxLockWait); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release(lock) }()

	wait, accepted := New(Error, path, "").write(testEvent{level: Error})
	if accepted {
		t.Fatal("contended event was written")
	}
	if wait > maxLockWait {
		t.Fatalf("coordination wait = %s, want <= %s", wait, maxLockWait)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("active log exists or stat failed: %v", err)
	}
}

func TestConcurrentProcessesPreserveBoundedJSONL(t *testing.T) {
	if os.Getenv("KTX_LOG_HELPER") == "1" {
		runLoggingHelper()
		return
	}
	root := t.TempDir()
	path := filepath.Join(root, "events.log")
	const processes, perProcess = 8, 100
	commands := make([]*exec.Cmd, processes)
	outputs := make([]bytes.Buffer, processes)
	for process := range processes {
		report := filepath.Join(root, fmt.Sprintf("wait-%d", process))
		cmd := exec.Command(os.Args[0], "-test.run=^TestConcurrentProcessesPreserveBoundedJSONL$")
		cmd.Env = append(os.Environ(),
			"KTX_LOG_HELPER=1",
			"KTX_LOG_PATH="+path,
			"KTX_LOG_PROCESS="+strconv.Itoa(process),
			"KTX_LOG_COUNT="+strconv.Itoa(perProcess),
			"KTX_LOG_WAIT_REPORT="+report,
		)
		cmd.Stdout = &outputs[process]
		cmd.Stderr = &outputs[process]
		commands[process] = cmd
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for process, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper: %v\n%s", err, outputs[process].Bytes())
		}
	}

	seen := make(map[string]bool, processes*perProcess)
	totalBytes := int64(0)
	for _, candidate := range []string{path + ".1", path} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatalf("stat %s: %v", candidate, err)
		}
		totalBytes += info.Size()
		for _, record := range readRecords(t, candidate) {
			key := fmt.Sprintf("%d/%d", record.PID, record.Sequence)
			if seen[key] {
				t.Fatalf("duplicate record %s", key)
			}
			seen[key] = true
		}
	}
	if len(seen) != processes*perProcess {
		t.Fatalf("complete records = %d, want %d", len(seen), processes*perProcess)
	}
	if totalBytes > 2*maxFileBytes {
		t.Fatalf("active + previous = %d, want <= %d", totalBytes, 2*maxFileBytes)
	}
	var observedMaxWait time.Duration
	for process := range processes {
		data, err := os.ReadFile(filepath.Join(root, fmt.Sprintf("wait-%d", process)))
		if err != nil {
			t.Fatal(err)
		}
		wait, err := time.ParseDuration(string(data))
		if err != nil || wait > maxLockWait {
			t.Fatalf("process %d max coordination wait = %s, err=%v", process, data, err)
		}
		if wait > observedMaxWait {
			observedMaxWait = wait
		}
	}
	t.Logf("preserved %d records across rotation, active+previous=%d bytes, max coordination wait=%s", len(seen), totalBytes, observedMaxWait)
}

func runLoggingHelper() {
	process, _ := strconv.Atoi(os.Getenv("KTX_LOG_PROCESS"))
	count, _ := strconv.Atoi(os.Getenv("KTX_LOG_COUNT"))
	sink := New(Debug, os.Getenv("KTX_LOG_PATH"), "")
	fill := strings.Repeat("x", 8<<10)
	var maxWait time.Duration
	for sequence := range count {
		wait, ok := sink.write(testEvent{level: Info, pid: process, seq: sequence, fill: fill})
		if !ok {
			os.Exit(2)
		}
		if wait > maxWait {
			maxWait = wait
		}
	}
	if err := os.WriteFile(os.Getenv("KTX_LOG_WAIT_REPORT"), []byte(maxWait.String()), 0o600); err != nil {
		os.Exit(3)
	}
}

func readRecords(t *testing.T, path string) []testRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var records []testRecord
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, maxRecordBytes), maxRecordBytes)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(line)+1 > maxRecordBytes {
			t.Fatalf("record size = %d", len(line)+1)
		}
		var record testRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("invalid JSONL record: %v", err)
		}
		if record.Timestamp == "" || record.Level == "" || record.Event != "test_event" {
			t.Fatalf("incomplete record: %#v", record)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}
