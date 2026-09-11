package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func spoolFixture(t *testing.T, text string) *recordingSpool {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, err := newRecordingSpool()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	if _, err := io.Copy(s, strings.NewReader(text)); err != nil {
		t.Fatal(err)
	}
	if err := s.complete("abcdefghijklmnop"); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRecordingInterpretationEquivalence(t *testing.T) {
	step := `{"type":"step_finish","part":{"model":" ","provider":"offline","cost":0,"tokens":{"input":11,"output":7,"reasoning":0,"cache":{"read":0,"write":0}}}}` + "\r\n"
	for _, text := range []string{
		"", "plain\r\n\t", "no final newline", step + step,
		step + `{"type":"text","content":" flat ","part":{"text":"nested"}}`,
		step + `{"type":"text","part":{"text":"","content":"4"}}`,
		step + "{invalid}\n\n",
	} {
		s := spoolFixture(t, text)
		got, answer, err := interpretRecording(s)
		want := extractStreamMetrics([]byte(text))
		want.modelSteps = nil
		if err != nil || !reflect.DeepEqual(got, want) || answer != extractAgentOutput([]byte(text)) {
			t.Fatalf("interpret mismatch: %#v %#v %q %v", got, want, answer, err)
		}
	}
}

func TestRecordingAnswerScannerStopsAtExistingBoundary(t *testing.T) {
	for _, size := range []int{4*1024*1024 - 1, 4 * 1024 * 1024, 4*1024*1024 + 1} {
		text := "prior\n" + strings.Repeat("x", size) + "\nafter\n"
		s := spoolFixture(t, text)
		_, answer, err := interpretRecording(s)
		want := "prior"
		if size < 4*1024*1024 {
			want = "after"
		}
		if err != nil || answer != want {
			t.Fatalf("size %d: %q %v", size, answer, err)
		}
	}
}

func TestRecordingDiagnosticsByteExact(t *testing.T) {
	text := " \r\n\x00" + strings.Repeat("x", 1024*1024) + "\t"
	s := spoolFixture(t, text)
	var got bytes.Buffer
	if err := s.copyDiagnostics(&got); err != nil || got.String() != text {
		t.Fatalf("diagnostic mismatch: %v", err)
	}
	if err := s.copyDiagnostics(failedRecordingWriter{}); err == nil {
		t.Fatal("delivery failure ignored")
	}
	if _, err := s.root.Stat("stream"); err != nil {
		t.Fatal("last copy removed", err)
	}
}

type failedRecordingWriter struct{}

func (failedRecordingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type faultyRecordingFile struct {
	io.Writer
	sync  func() error
	close func() error
}

func (f faultyRecordingFile) Sync() error  { return f.sync() }
func (f faultyRecordingFile) Close() error { return f.close() }

type shortRecordingWriter struct{}

func (shortRecordingWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

type quotaRecordingWriter struct{}

func (quotaRecordingWriter) Write([]byte) (int, error) { return 0, syscall.ENOSPC }

func TestRecordingCaptureFaults(t *testing.T) {
	for _, mode := range []string{"short-write", "quota", "sync", "close"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			s, err := newRecordingSpool()
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			original := s.file
			fault := faultyRecordingFile{Writer: original, sync: original.Sync, close: original.Close}
			switch mode {
			case "short-write":
				fault.Writer = shortRecordingWriter{}
			case "quota":
				fault.Writer = quotaRecordingWriter{}
			case "sync":
				fault.sync = func() error { return syscall.ENOSPC }
			case "close":
				fault.close = func() error { return errors.Join(original.Close(), syscall.EIO) }
			}
			s.file = fault
			if n, err := s.Write([]byte("normal forwarding")); n != 17 || err != nil {
				t.Fatal("optional failure escaped capture")
			}
			if err := s.complete("abcdefghijklmnop"); err == nil {
				t.Fatal("capture fault completed")
			}
			if _, err := s.root.Stat("stream"); err != nil {
				t.Fatal("last copy removed", err)
			}
			if _, err := s.root.Stat("completion.json"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("fault supplied completion metadata")
			}
		})
	}
}

func TestRecordingCaptureAndReadFailuresRetainBytes(t *testing.T) {
	s := spoolFixture(t, "complete\n")
	if err := s.root.Remove("stream"); err != nil {
		t.Fatal(err)
	}
	if err := s.root.Mkdir("stream", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := interpretRecording(s); err == nil {
		t.Fatal("actual read error accepted")
	}
	if err := s.root.Remove("stream"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := interpretRecording(s); err == nil {
		t.Fatal("reopen failure accepted")
	}

	t.Setenv("XDG_DATA_HOME", t.TempDir())
	partial, err := newRecordingSpool()
	if err != nil {
		t.Fatal(err)
	}
	defer partial.close()
	_, _ = partial.Write([]byte("prior"))
	_ = partial.file.Close()
	if n, err := partial.Write([]byte("forwarded")); n != 9 || err != nil {
		t.Fatal("optional capture interrupted forwarding")
	}
	if err := partial.complete("abcdefghijklmnop"); err == nil {
		t.Fatal("failed capture completed")
	}
	if _, err := partial.root.Stat("stream"); err != nil {
		t.Fatal("partial copy lost", err)
	}
}

func TestRecordingOptionalCreateFailurePreservesProducer(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	blocked := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := runCmd
	t.Cleanup(func() { runCmd = old })
	runCmd = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		// Session readiness has completed. Fail only optional capture creation.
		t.Setenv("XDG_DATA_HOME", blocked)
		return exec.CommandContext(ctx, "sh", "-c", "printf 'ordinary output'; exit 23")
	}
	var stdout, stderr, receipt bytes.Buffer
	code := executeRun("offline", nil, runOptions{Format: FormatJSON, Quiet: true, RecordStream: true, completionReceipt: &receipt}, &stdout, &stderr)
	if code != 23 || stdout.String() != "ordinary output\n" || receipt.Len() != 16 || !strings.Contains(stderr.String(), "record stream unavailable") {
		t.Fatalf("optional capture changed producer: code %d stdout %q receipt %q stderr %s", code, stdout.String(), receipt.String(), stderr.String())
	}
}

func TestRecordingPublicationOwnership(t *testing.T) {
	for _, mode := range []string{"success", "over-limit", "insert-failure", "commit-failure", "cleanup-failure", "corrupt-row", "wrong-format", "wrong-id", "wrong-hash", "missing-receipt", "uncertain-committed", "readback-failure"} {
		t.Run(mode, func(t *testing.T) {
			s := spoolFixture(t, strings.Repeat("x", 8192))
			d, err := dbpkg.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if err := d.InsertSession(&dbpkg.Session{ID: s.row.SessionID, Agent: "offline", StartedAt: "2026-09-07T00:00:00Z"}); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "over-limit":
				conn, err := d.DB().Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				_, err = sqlite.Limit(conn, sqlite3.SQLITE_LIMIT_LENGTH, 4096)
				_ = conn.Close()
				if err != nil {
					t.Fatal(err)
				}
			case "insert-failure":
				_, err = d.DB().Exec(`CREATE TRIGGER fail_recording BEFORE INSERT ON recorded_streams BEGIN SELECT RAISE(ABORT, 'offline failure'); END`)
			case "commit-failure":
				_, err = d.DB().Exec(`CREATE TABLE recording_commit_fault (session_id TEXT REFERENCES sessions(id) DEFERRABLE INITIALLY DEFERRED);
				CREATE TRIGGER fail_recording_commit AFTER INSERT ON recorded_streams BEGIN INSERT INTO recording_commit_fault VALUES ('missing'); END`)
			case "cleanup-failure":
				if os.Geteuid() == 0 {
					t.Skip("root bypasses the filesystem permission fault")
				}
				err = os.Chmod(s.path, 0o500)
				t.Cleanup(func() { _ = os.Chmod(s.path, 0o700) })
			case "corrupt-row":
				_, err = d.DB().Exec(`CREATE TRIGGER corrupt_recording AFTER INSERT ON recorded_streams BEGIN UPDATE recorded_streams SET stream_text=replace(stream_text,'x','y') WHERE id=NEW.id; END`)
			case "wrong-format", "wrong-id", "wrong-hash":
				column := map[string]string{"wrong-format": "stream_format", "wrong-id": "id", "wrong-hash": "content_hash"}[mode]
				_, err = d.DB().Exec(`CREATE TRIGGER corrupt_recording AFTER INSERT ON recorded_streams BEGIN UPDATE recorded_streams SET ` + column + `='wrong' WHERE id=NEW.id; END`)
			case "missing-receipt":
				s.row.SessionID = ""
			case "uncertain-committed":
				row := s.row
				row.StreamText = strings.Repeat("x", 8192)
				err = d.InsertRecordedStream(&row)
			case "readback-failure":
				err = d.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			err = s.publish(d, "")
			if mode == "cleanup-failure" && (err == nil || !strings.Contains(err.Error(), "committed recording verified")) {
				t.Fatalf("cleanup not distinguished from publication: %v", err)
			}
			if (err == nil) != (mode == "success" || mode == "uncertain-committed") {
				t.Fatalf("publish: %v", err)
			}
			_, statErr := s.root.Stat("stream")
			if mode == "success" || mode == "uncertain-committed" {
				if !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("redundant copy remains: %v", statErr)
				}
			} else if statErr != nil {
				t.Fatalf("last copy lost: %v", statErr)
			}
		})
	}
}
