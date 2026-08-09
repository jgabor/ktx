package db

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestDatabaseAccessAuditRecordsBeforeAccess(t *testing.T) {
	root := t.TempDir()
	audit := filepath.Join(root, "audit.jsonl")
	forbidden := filepath.Join(root, "forbidden", "ktx.db")
	allowed := filepath.Join(root, "allowed", "ktx.db")
	paths, err := json.Marshal([]string{forbidden})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(databaseAccessAuditEnv, audit)
	t.Setenv(forbiddenDatabasePathsEnv, string(paths))

	if database, err := Open(forbidden); err == nil {
		database.Close()
		t.Fatal("forbidden open succeeded")
	}
	if _, err := os.Stat(filepath.Dir(forbidden)); !os.IsNotExist(err) {
		t.Fatalf("forbidden parent was touched before rejection: %v", err)
	}
	if database, err := Open(allowed); err != nil {
		t.Fatal(err)
	} else if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	records := readDatabaseAccessRecords(t, audit)
	if len(records) != 2 {
		t.Fatalf("records = %#v, want two", records)
	}
	if records[0].Operation != "open" || !records[0].Forbidden || records[0].Path != lexicalPath(forbidden) {
		t.Fatalf("forbidden record = %#v", records[0])
	}
	if records[1].Operation != "open" || records[1].Forbidden || records[1].Path != lexicalPath(allowed) {
		t.Fatalf("allowed record = %#v", records[1])
	}
}

func TestDatabaseAccessAuditConcurrentWriters(t *testing.T) {
	audit := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(databaseAccessAuditEnv, audit)
	t.Setenv(forbiddenDatabasePathsEnv, "[]")
	const writers = 32
	var group sync.WaitGroup
	for i := 0; i < writers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := AuditPathInspection(filepath.Join(t.TempDir(), "ktx.db")); err != nil {
				t.Errorf("audit: %v", err)
			}
		}()
	}
	group.Wait()
	if got := len(readDatabaseAccessRecords(t, audit)); got != writers {
		t.Fatalf("records = %d, want %d", got, writers)
	}
}

func TestDatabaseAccessAuditRejectsDatabaseOutsideVerificationRoot(t *testing.T) {
	root := t.TempDir()
	audit := filepath.Join(root, "audit.jsonl")
	outside := filepath.Join(t.TempDir(), "outside", "ktx.db")
	allowed := filepath.Join(root, "allowed", "ktx.db")
	t.Setenv(verificationRootEnv, root)
	t.Setenv(databaseAccessAuditEnv, audit)
	t.Setenv(forbiddenDatabasePathsEnv, "[]")

	if database, err := Open(outside); err == nil {
		database.Close()
		t.Fatal("outside-root open succeeded")
	}
	if _, err := os.Stat(filepath.Dir(outside)); !os.IsNotExist(err) {
		t.Fatalf("outside-root parent was touched before rejection: %v", err)
	}
	if database, err := Open(allowed); err != nil {
		t.Fatal(err)
	} else if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	records := readDatabaseAccessRecords(t, audit)
	if len(records) != 2 {
		t.Fatalf("records = %#v, want two", records)
	}
	if !records[0].Forbidden || records[0].Path != lexicalPath(outside) {
		t.Fatalf("outside-root record = %#v", records[0])
	}
	if records[1].Forbidden || records[1].Path != lexicalPath(allowed) {
		t.Fatalf("allowed record = %#v", records[1])
	}
}

func readDatabaseAccessRecords(t *testing.T, path string) []databaseAccessRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var records []databaseAccessRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record databaseAccessRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("decode audit line %q: %v", scanner.Text(), err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}
