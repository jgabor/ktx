package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/usage"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type repeatedRecording struct {
	line   string
	offset int
}

func TestRecordingLargeRecordBoundaries(t *testing.T) {
	if os.Getenv("KTX_TEST_RECORDING_LARGE") != "1" {
		t.Skip("opt-in after checking available RAM and temporary capacity")
	}
	var initial syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &initial); err != nil {
		t.Fatal(err)
	}
	t.Logf("initial process RSS high-water bytes=%d", initial.Maxrss*1024)
	for _, extra := range []int64{0, 1} {
		for _, suffix := range []string{"", "\n" + `{"type":"step_finish","part":{"tokens":{"input":42}}}` + "\n"} {
			t.Run(strconv.FormatInt(extra, 10)+"-suffix"+strconv.Itoa(len(suffix)), func(t *testing.T) {
				t.Setenv("XDG_DATA_HOME", t.TempDir())
				s, err := newRecordingSpool()
				if err != nil {
					t.Fatal(err)
				}
				defer s.close()
				header := `{"type":"step_finish","part":{"tokens":{"input":10},"padding":"`
				footer := `"}}`
				input := io.MultiReader(strings.NewReader("prior\n"+header), io.LimitReader(&repeatedRecording{line: "a"}, int64(usage.MaxRecordSize)+extra-int64(len(header)+len(footer))), strings.NewReader(footer+suffix))
				if _, err := io.Copy(s, input); err != nil {
					t.Fatal(err)
				}
				if err := s.complete("abcdefghijklmnop"); err != nil {
					t.Fatal(err)
				}
				sm, answer, err := interpretRecording(s)
				if err != nil || answer != "prior" || len(sm.modelSteps) != 0 {
					t.Fatalf("interpretation: %q %v", answer, err)
				}
				var ru syscall.Rusage
				if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
					t.Fatal(err)
				}
				if ru.Maxrss*1024 > 128*1024*1024+16*int64(usage.MaxRecordSize) {
					t.Fatalf("large-record interpretation RSS budget: got %d bytes", ru.Maxrss*1024)
				}
				t.Logf("record bytes=%d, peak process RSS bytes=%d", int64(usage.MaxRecordSize)+extra, ru.Maxrss*1024)
				want := int64(0)
				if extra == 0 {
					want += 10
				}
				if suffix != "" {
					want += 42
				}
				if want == 0 {
					if sm.hasData {
						t.Fatal("oversized record accepted")
					}
				} else if sm.tokensIn == nil || *sm.tokensIn != want {
					t.Fatalf("usage %v want %d", sm.tokensIn, want)
				}
			})
			runtime.GC()
		}
	}
}

func (r *repeatedRecording) Read(p []byte) (int, error) {
	for n := range p {
		p[n] = r.line[r.offset]
		r.offset = (r.offset + 1) % len(r.line)
	}
	return len(p), nil
}

// Opt-in, one fresh process per phase and size. Fixtures stay small; these
// qualification samples are neither admission limits nor worst-case proofs.
func TestRecordingResourcePhase(t *testing.T) {
	phase := os.Getenv("KTX_TEST_RECORDING_PHASE")
	if phase == "" {
		t.Skip("opt-in isolated resource measurement")
	}
	mb, err := strconv.Atoi(os.Getenv("KTX_TEST_RECORDING_MIB"))
	if err != nil || (mb != 1 && mb != 8 && mb != 16) {
		t.Fatal("use 1, 8 or 16 MiB")
	}
	n := int64(mb) * 1024 * 1024
	line := `{"type":"step_finish","part":{"model":"offline","provider":"offline","cost":0,"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}},"pad":"`
	line += strings.Repeat("x", 256-len(line)-3) + "\"}\n"
	if len(line) != 256 {
		t.Fatal("fixture record size")
	}
	makeInput := func() io.Reader { return io.LimitReader(&repeatedRecording{line: line}, n) }
	var spool *recordingSpool
	var db *dbpkg.DB
	var dbDir string
	var baselineStorage int64
	storage := func() int64 {
		var total int64
		_ = filepath.WalkDir(dbDir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				info, err := entry.Info()
				if err != nil {
					return err
				}
				total += info.Size()
			}
			return nil
		})
		return total
	}
	if phase != "control" && phase != "capture" {
		spool, err = newRecordingSpool()
		if err != nil {
			t.Fatal(err)
		}
		defer spool.close()
		if _, err := io.Copy(spool, makeInput()); err != nil {
			t.Fatal(err)
		}
		if err := spool.complete("abcdefghijklmnop"); err != nil {
			t.Fatal(err)
		}
	}
	const limit = 32 * 1024 * 1024
	if phase == "publication" || phase == "replay" {
		dbDir = t.TempDir()
		db, err = dbpkg.Open(filepath.Join(dbDir, "owned.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := db.InsertSession(&dbpkg.Session{ID: spool.row.SessionID, Agent: "offline", StartedAt: "2026-09-07T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
		conn, err := db.DB().Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_, err = sqlite.Limit(conn, sqlite3.SQLITE_LIMIT_LENGTH, limit)
		_ = conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		baselineStorage = storage()
		if phase == "replay" {
			if err := spool.publish(db, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var peakStorage atomic.Int64
	stopStorage := make(chan struct{})
	storageStopped := make(chan struct{})
	if phase == "publication" {
		go func() {
			defer close(storageStopped)
			ticker := time.NewTicker(100 * time.Microsecond)
			defer ticker.Stop()
			for {
				value := storage() - baselineStorage
				if value > peakStorage.Load() {
					peakStorage.Store(value)
				}
				select {
				case <-stopStorage:
					return
				case <-ticker.C:
				}
			}
		}()
		defer func() { close(stopStorage); <-storageStopped }()
	}
	switch phase {
	case "control":
		for range 2 {
			if _, err := io.CopyBuffer(io.Discard, makeInput(), make([]byte, recordingSlabSize)); err != nil {
				t.Fatal(err)
			}
		}
	case "capture":
		for range 2 {
			s, err := newRecordingSpool()
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			if _, err := io.Copy(s, makeInput()); err != nil {
				t.Fatal(err)
			}
			if err := s.finish(); err != nil {
				t.Fatal(err)
			}
			if s.size != n {
				t.Fatal("capture byte count")
			}
			if err := s.copyDiagnostics(io.Discard); err != nil {
				t.Fatal(err)
			}
		}
	case "interpret":
		sm, _, err := interpretRecording(spool)
		if err != nil || len(sm.modelSteps) != 0 || sm.tokensIn == nil || *sm.tokensIn != n/256 {
			t.Fatalf("interpretation: %#v %v", sm, err)
		}
	case "publication":
		if err := spool.publish(db, ""); err != nil {
			t.Fatal(err)
		}
	case "replay":
		if code := runBenchReplayImpl(db, spool.row.SessionID, io.Discard, io.Discard, FormatJSON); code != 0 {
			t.Fatalf("replay code %d", code)
		}
	default:
		t.Fatal("unknown phase")
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Fatal(err)
	}
	result := map[string]any{"phase": phase, "bytes": n, "record_bytes": 256, "rss_bytes": ru.Maxrss * 1024, "retained_heap_delta": int64(after.HeapAlloc) - int64(before.HeapAlloc), "total_alloc_delta": after.TotalAlloc - before.TotalAlloc}
	if phase == "publication" {
		result["additional_storage_bytes"] = storage() - baselineStorage
		result["sampled_peak_additional_storage_bytes"] = max(peakStorage.Load(), storage()-baselineStorage)
		result["sqlite_limit"] = limit
		if storage()-baselineStorage > 64*1024*1024+4*limit {
			t.Fatal("publication storage budget")
		}
	}
	bound := int64(128*1024*1024) + 16*n
	if phase == "interpret" {
		bound = 128*1024*1024 + 16*256
	}
	if phase == "replay" {
		bound = 256*1024*1024 + 64*n
	}
	if phase != "control" && phase != "capture" && ru.Maxrss*1024 > bound {
		t.Fatal("RSS budget")
	}
	if (phase == "interpret" || phase == "capture") && int64(after.HeapAlloc)-int64(before.HeapAlloc) > 8*1024*1024 {
		t.Fatal("retained heap budget")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(encoded))
}
