package experiment

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/db"
	"golang.org/x/sys/unix"
)

// Watches only disposable targets, and observes opens as well as reads: a
// read-only SQLite open is still an escape even if no bytes are changed.
func watchArchiveTarget(t *testing.T, target string) func() {
	t.Helper()
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	if _, err := unix.InotifyAddWatch(fd, target, unix.IN_OPEN|unix.IN_ACCESS|unix.IN_MODIFY); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		buf := make([]byte, 4096)
		n, err := unix.Read(fd, buf)
		if n > 0 || !errors.Is(err, unix.EAGAIN) {
			t.Errorf("external target accessed: events=%d err=%v", n, err)
		}
	}
}

func TestArchiveConfinement(t *testing.T) {
	for _, field := range []string{"stream", "database", "artifact", "input", "config", "pricing", "workspace"} {
		for _, escape := range []string{"parent", "absolute", "symlink"} {
			t.Run(field+"/"+escape, func(t *testing.T) {
				dir := produceEntryArchive(t, "prompt-cache-token-hit-rate", true, true)
				data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
				if err != nil {
					t.Fatal(err)
				}
				var manifest Manifest
				if err := json.Unmarshal(data, &manifest); err != nil {
					t.Fatal(err)
				}
				attempts, err := LoadAttempts(dir)
				if err != nil {
					t.Fatal(err)
				}
				relative := attempts[0].Artifacts[field]
				switch field {
				case "artifact":
					relative = attempts[0].Artifacts["stream"]
				case "input":
					relative = manifest.Inputs[0].Path
				case "config":
					relative = manifest.Pipelines[Baseline].Path
				case "pricing":
					relative = manifest.Pricing.Path
				}
				// Keep unrelated targets outside the experiment discovery root:
				// directories inside that root are independently inspected by list.
				target := filepath.Join(filepath.Dir(filepath.Dir(dir)), "outside-"+field)
				if field == "workspace" {
					if err := copyTree(filepath.Join(dir, relative), target, nil); err != nil {
						t.Fatal(err)
					}
				} else if err := copyTreeFile(filepath.Join(dir, relative), target); err != nil {
					t.Fatal(err)
				}
				path, err := filepath.Rel(dir, target)
				if err != nil {
					t.Fatal(err)
				}
				if escape == "absolute" {
					path = target
				}
				if escape == "symlink" {
					path = "escape"
					if err := os.Symlink(target, filepath.Join(dir, path)); err != nil {
						t.Fatal(err)
					}
				}
				switch field {
				case "input":
					manifest.Inputs[0].Path = path
				case "config":
					pipeline := manifest.Pipelines[Baseline]
					pipeline.Path = path
					manifest.Pipelines[Baseline] = pipeline
				case "pricing":
					manifest.Pricing.Path = path
				default:
					attempts[0].Artifacts[field] = path
				}
				if field == "artifact" {
					hash, err := fileSHA256(target)
					if err != nil {
						t.Fatal(err)
					}
					attempts[0].ContentHashes[field] = hash
				}
				if err := WriteJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
					t.Fatal(err)
				}
				writeAttemptRecords(t, dir, attempts...)
				assertUnopened := watchArchiveTarget(t, target)
				for _, verb := range []string{"summarize", "validate"} {
					var err error
					if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
						_, err = entryCLI(t, dir, verb)
					} else if verb == "summarize" {
						_, err = RegenerateSummary(dir)
					} else {
						_, err = ValidateExperiment(dir)
					}
					if err == nil {
						t.Errorf("%s accepted %s escape", verb, escape)
					}
					assertUnopened()
				}
			})
		}
	}
}

func archiveAction(t *testing.T, dir, verb string) error {
	t.Helper()
	if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
		_, err := entryCLI(t, dir, verb)
		return err
	}
	if verb == "summarize" {
		_, err := RegenerateSummary(dir)
		return err
	}
	_, err := ValidateExperiment(dir)
	return err
}

func TestArchiveConfinementFixedFilesAndSidecars(t *testing.T) {
	for _, field := range []string{"manifest.json", "attempts.jsonl", "summary.json", "-wal", "-shm", "-journal", "stream-parent", "workspace-child"} {
		t.Run(field, func(t *testing.T) {
			dir := produceEntryArchive(t, "prompt-cache-token-hit-rate", true, true)
			attempts, err := LoadAttempts(dir)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, field)
			target := filepath.Join(filepath.Dir(filepath.Dir(dir)), "outside")
			switch {
			case strings.HasPrefix(field, "-"):
				path = filepath.Join(dir, attempts[0].Artifacts["database"]) + field
				if err := os.WriteFile(target, []byte("unrelated sidecar"), 0o600); err != nil {
					t.Fatal(err)
				}
			case field == "stream-parent":
				path = filepath.Dir(filepath.Join(dir, attempts[0].Artifacts["stream"]))
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
			case field == "workspace-child":
				workspace := attempts[0].Artifacts["workspace"]
				path = filepath.Join(dir, workspace, "nested")
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "file"), []byte("owned"), 0o600); err != nil {
					t.Fatal(err)
				}
				// The producer hashes the link itself. A later directory swap is
				// covered separately by the concurrent traversal test.
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			assertUnopened := watchArchiveTarget(t, target)
			for _, verb := range []string{"summarize", "validate"} {
				err := archiveAction(t, dir, verb)
				// A workspace link is data, not a request for its target. The
				// changed hash removes attempt credit without following it.
				if field != "workspace-child" && err == nil {
					t.Errorf("%s accepted escaping %s", verb, field)
				}
				assertUnopened()
			}
		})
	}
}

func TestArchiveConfinementUnsupportedTypes(t *testing.T) {
	for _, field := range []string{"manifest.json", "attempts.jsonl", "summary.json", "stream", "workspace-child", "-wal"} {
		t.Run(field, func(t *testing.T) {
			dir := produceEntryArchive(t, "prompt-cache-token-hit-rate", true, true)
			attempts, err := LoadAttempts(dir)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, field)
			switch field {
			case "stream":
				path = filepath.Join(dir, attempts[0].Artifacts["stream"])
			case "workspace-child":
				path = filepath.Join(dir, attempts[0].Artifacts["workspace"], "fifo")
			case "-wal":
				path = filepath.Join(dir, attempts[0].Artifacts["database"]) + field
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			for _, verb := range []string{"summarize", "validate"} {
				done := make(chan error, 1)
				go func() {
					if verb == "summarize" {
						_, err := RegenerateSummary(dir)
						done <- err
					} else {
						_, err := ValidateExperiment(dir)
						done <- err
					}
				}()
				select {
				case err := <-done:
					if err == nil || !strings.Contains(err.Error(), "unsupported archive file type") {
						t.Fatalf("%s: %v", verb, err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("archive inspection blocked on FIFO")
				}
			}
		})
	}
}

func TestArchiveConfinementSymlinkRace(t *testing.T) {
	for _, field := range []string{"stream", "workspace"} {
		t.Run(field, func(t *testing.T) {
			dir := produceEntryArchive(t, "prompt-cache-token-hit-rate", true, true)
			attempts, err := LoadAttempts(dir)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, attempts[0].Artifacts[field])
			outside := filepath.Join(filepath.Dir(filepath.Dir(dir)), "outside")
			if field == "workspace" {
				if err := copyTree(path, outside, nil); err != nil {
					t.Fatal(err)
				}
			} else if err := copyTreeFile(path, outside); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path, path+".owned"); err != nil {
				t.Fatal(err)
			}
			assertUnopened := watchArchiveTarget(t, outside)
			stop := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					for _, target := range []string{outside, filepath.Base(path) + ".owned"} {
						select {
						case <-stop:
							return
						default:
						}
						if err := os.Symlink(target, path+".swap"); err != nil {
							t.Error(err)
							return
						}
						if err := os.Rename(path+".swap", path); err != nil {
							t.Error(err)
							return
						}
					}
				}
			}()
			for range 32 {
				_, _ = RegenerateSummary(dir)
				_, _ = ValidateExperiment(dir)
			}
			close(stop)
			wg.Wait()
			assertUnopened()
		})
	}
}

func TestArchiveConfinementWALAndColdJournal(t *testing.T) {
	dir := produceEntryArchive(t, "prompt-cache-token-hit-rate", true, true)
	attempts, err := LoadAttempts(dir)
	if err != nil {
		t.Fatal(err)
	}
	name := attempts[0].Artifacts["database"]
	database, err := db.Open(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	const endedAt = "2026-09-06T00:00:00Z"
	if _, err := database.DB().Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; UPDATE sessions SET ended_at=?; UPDATE runtime_event_intervals SET closed_at=?", endedAt, endedAt); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, name) + "-wal")
	if err != nil || info.Size() == 0 {
		t.Fatalf("fixture has no retained WAL: %v", err)
	}
	attempts[0].ContentHashes["database"], err = fileSHA256(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name)+"-journal", make([]byte, 1024), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(name), filepath.Join(dir, name)+".alias"); err != nil {
		t.Fatal(err)
	}
	name += ".alias"
	attempts[0].Artifacts["database"] = name
	writeAttemptRecords(t, dir, attempts...)
	a, err := openArchive(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	copy, cleanup, err := a.openDatabase(name)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer copy.Close()
	var got string
	if err := copy.DB().QueryRow("SELECT ended_at FROM sessions").Scan(&got); err != nil || got != endedAt {
		t.Fatalf("committed WAL lost: got %q err %v", got, err)
	}
	for _, verb := range []string{"summarize", "validate"} {
		if err := archiveAction(t, dir, verb); err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
	}
	report, err := ValidateExperiment(dir)
	if err != nil || report["valid"] != true {
		t.Fatalf("WAL control: checks=%v err=%v", report["attempt_checks"], err)
	}
}

func TestArchiveConfinementNonzeroJournal(t *testing.T) {
	dir := produceEntryArchive(t, "prompt-cache-token-hit-rate", true, true)
	attempts, err := LoadAttempts(dir)
	if err != nil {
		t.Fatal(err)
	}
	name := attempts[0].Artifacts["database"]
	if err := os.WriteFile(filepath.Join(dir, name)+"-journal", []byte{0xd9, 0xd5, 0x05, 0xf9, 0x20, 0xa1, 0x63, 0xd7}, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"summarize", "validate"} {
		if err := archiveAction(t, dir, verb); err == nil {
			t.Fatalf("%s accepted rollback recovery", verb)
		}
	}
}

func TestArchiveConfinementDatabaseOwnership(t *testing.T) {
	dir := produceEntryArchive(t, "prompt-cache-token-hit-rate", true, true)
	attempts, err := LoadAttempts(dir)
	if err != nil {
		t.Fatal(err)
	}
	hashes := map[string]string{}
	for _, attempt := range attempts {
		path := filepath.Join(dir, attempt.Artifacts["database"])
		hashes[path], err = fileSHA256(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(t.TempDir(), "database-access.jsonl")
	t.Setenv("KTX_VERIFY_DB_AUDIT", log)
	for _, verb := range []string{"summarize", "validate"} {
		if err := archiveAction(t, dir, verb); err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	opened := false
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var record struct {
			Operation, Path string
			Forbidden       bool
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record.Forbidden || !strings.HasPrefix(filepath.Base(filepath.Dir(record.Path)), "ktx-archive-db-") {
			t.Fatalf("SQLite received a non-private target: %+v", record)
		}
		opened = opened || record.Operation == "open_read_only"
		if _, err := os.Stat(record.Path); !os.IsNotExist(err) {
			t.Fatalf("temporary database not cleaned up: %v", err)
		}
	}
	if !opened {
		t.Fatal("no SQLite query open observed")
	}
	for path, expected := range hashes {
		got, err := fileSHA256(path)
		if err != nil || got != expected {
			t.Fatalf("source database changed: %s: %v", path, err)
		}
	}
}

func TestArchiveConfinementSocket(t *testing.T) {
	dir := produceEntryArchive(t, "exact-match", false, false)
	path := filepath.Join(dir, "summary.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	parent, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: fmt.Sprintf("/proc/self/fd/%d/summary.json", parent.Fd())}); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"summarize", "validate"} {
		var err error
		if verb == "summarize" {
			_, err = RegenerateSummary(dir)
		} else {
			_, err = ValidateExperiment(dir)
		}
		if err == nil || !strings.Contains(err.Error(), "unsupported archive file type") {
			t.Fatalf("%s: %v", verb, err)
		}
	}
}

func TestArchiveConfinementValidLinks(t *testing.T) {
	dir := produceEntryArchive(t, "prompt-cache-token-hit-rate", true, true)
	data, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := LoadAttempts(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"pricing.json", attempts[0].Artifacts["stream"], attempts[0].Artifacts["database"]} {
		path := filepath.Join(dir, relative)
		if err := os.Rename(path, path+".owned"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Base(path)+".owned", path); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := RegenerateSummary(dir); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil || string(data) != string(after) {
		t.Fatalf("valid archive bytes changed: %v", err)
	}
	report, err := ValidateExperiment(dir)
	if err != nil || report["valid"] != true {
		t.Fatalf("valid owned links: %v %v", report, err)
	}
	if os.Getenv("KTX_ENTRY_CONTRACT_BINARY") != "" {
		for _, verb := range []string{"summarize", "validate"} {
			out, err := entryCLI(t, dir, verb)
			if err != nil || strings.Contains(string(out), `"valid": false`) {
				t.Fatalf("%s: %s %v", verb, out, err)
			}
		}
	}
}
