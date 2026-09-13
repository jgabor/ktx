package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDoctorStorageStatOnly(t *testing.T) {
	for _, state := range []string{"observed", "absent", "unavailable", "directory"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "not-sqlite.db")
			for i, suffix := range []string{"", "-wal", "-shm"} {
				name := path + suffix
				var err error
				switch state {
				case "observed":
					// Arbitrary bytes, including an empty main file: no SQLite
					// connection or aggregate query is needed to observe sizes.
					err = os.WriteFile(name, bytes.Repeat([]byte("x"), i*19), 0o600)
				case "unavailable":
					// A symlink loop gives a deterministic stat error even as root.
					err = os.Symlink(name, name)
				case "directory":
					err = os.Mkdir(name, 0o700)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			before := doctorSnapshot(t, root)
			checks := doctorStorageChecks(path)
			if len(checks) != 4 {
				t.Fatalf("checks=%+v", checks)
			}
			for i, name := range []string{"database_file_size", "database_wal_file_size", "database_shm_file_size"} {
				want := state + ":"
				if state == "directory" {
					want = "unavailable:"
				}
				check := checks[i]
				if check.Name != name || check.Status != doctorStatusOK || !strings.HasPrefix(check.Detail, want) {
					t.Fatalf("check=%+v, want %s %s", check, name, want)
				}
				if state == "observed" && !strings.Contains(check.Detail, fmt.Sprintf("file size %d bytes", i*19)) {
					t.Fatalf("wrong size: %+v", check)
				}
				if state != "observed" && strings.Contains(check.Detail, "0 bytes") {
					t.Fatalf("unknown reported as zero: %+v", check)
				}
			}
			if !reflect.DeepEqual(before, doctorSnapshot(t, root)) {
				t.Fatal("storage observation changed files or directories")
			}
		})
	}
}

func TestDoctorStorageFormatsAndReadOnly(t *testing.T) {
	for _, format := range []string{FormatJSON, FormatText} {
		for _, present := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/present=%t", format, present), func(t *testing.T) {
				root, _ := doctorConfigRoot(t)
				path := filepath.Join(root, "data", "ktx", "ktx.db")
				wantCode := ExitInternal
				state := "absent:"
				if present {
					seedDoctorDatabase(t, path, nil)
					wantCode, state = ExitOK, "observed:"
				}
				before := doctorSnapshot(t, root)
				var out, stderr bytes.Buffer
				if code := runDoctorWith(nil, Options{Format: format}, &out, &stderr); code != wantCode || stderr.Len() != 0 {
					t.Fatalf("code=%d want=%d stderr=%s", code, wantCode, &stderr)
				}
				if format == FormatJSON {
					report := decodeDoctorReport(t, out.Bytes())
					if report.Path != path {
						t.Fatalf("path=%q want=%q", report.Path, path)
					}
					assertDoctorCheck(t, report, "database_file_size", doctorStatusOK, state)
					assertDoctorCheck(t, report, "database_storage_scope", doctorStatusOK, "ktx db report")
				}
				for _, text := range []string{"database_file_size", "database_wal_file_size", "database_shm_file_size", state, "ktx db report", "logical payload bytes", "not an atomic SQLite snapshot"} {
					if !strings.Contains(out.String(), text) {
						t.Fatalf("missing %q: %s", text, &out)
					}
				}
				if !reflect.DeepEqual(before, doctorSnapshot(t, root)) {
					t.Fatal("doctor changed files or directories")
				}
				// The suggested command is registered and valid in both formats;
				// help validates syntax without executing its aggregate queries.
				out.Reset()
				stderr.Reset()
				if code := runWith([]string{"db", "report", "--help", "--format", format}, &out, &stderr); code != ExitOK {
					t.Fatalf("report pointer: code=%d stderr=%s", code, &stderr)
				}
			})
		}
	}
}
