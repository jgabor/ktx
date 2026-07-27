package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/jgabor/ktx/internal/binarycap"
	dbpkg "github.com/jgabor/ktx/internal/db"
	_ "modernc.org/sqlite"
)

func TestOpenBackedCommandsShareSchemaIncompatibilityContract(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema-9.db")
	seedSchemaVersion(t, path, dbpkg.MaxSupportedSchema()+1)
	t.Setenv("KTX_DB", path)
	t.Setenv("KTX_BENCH_SESSION_ID", "aaaaaaaaaaaaaaaa")
	t.Setenv("KTX_BENCH_EVENT_INTERVAL_ID", "bbbbbbbbbbbbbbbb")
	t.Setenv("KTX_BENCH_EVENT_CONTRACT", "opencode-event-v2")
	t.Setenv("KTX_BENCH_COLLECTOR_MODE", "required")

	canonicalDB, err := dbpkg.CanonicalPath(path)
	if err != nil {
		t.Fatal(err)
	}
	identity := binarycap.Identity{
		Schema:            binarycap.Schema,
		ExecutablePath:    "/tmp/ktx-schema-test",
		ExecutableSHA256:  testBinaryDigest,
		BuildVersion:      "schema-test",
		MaxDatabaseSchema: dbpkg.MaxSupportedSchema(),
	}
	originalIdentity := currentBinaryCapabilities
	currentBinaryCapabilities = func() (binarycap.Identity, error) { return identity, nil }
	t.Cleanup(func() { currentBinaryCapabilities = originalIdentity })

	before := directorySnapshot(t, dir)
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "dispatcher", args: []string{"list"}},
		{name: "run", args: []string{"run", "unused-agent"}},
		{name: "telemetry collector", args: []string{"telemetry", "collector-open"}},
		{name: "bench", args: []string{"bench", "report"}},
		{name: "collector-enabled agent", args: []string{"bench", "agent", "-m", "openai/gpt-5.6-luna", "prompt"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runWith(test.args, &stdout, &stderr); code != ExitSchemaIncompatible {
				t.Fatalf("exit = %d, want %d; stdout=%q stderr=%q", code, ExitSchemaIncompatible, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			var envelope struct {
				Error map[string]any `json:"error"`
			}
			if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
				t.Fatalf("decode error: %v\n%s", err, stderr.String())
			}
			want := map[string]any{
				"code":              "schema-incompatible",
				"actual_schema":     float64(dbpkg.MaxSupportedSchema() + 1),
				"maximum_schema":    float64(dbpkg.MaxSupportedSchema()),
				"database_path":     canonicalDB,
				"executable_path":   identity.ExecutablePath,
				"executable_sha256": identity.ExecutableSHA256,
				"build_version":     identity.BuildVersion,
			}
			for key, value := range want {
				if !reflect.DeepEqual(envelope.Error[key], value) {
					t.Errorf("error[%q] = %#v, want %#v; envelope=%#v", key, envelope.Error[key], value, envelope.Error)
				}
			}
			if envelope.Error["message"] == nil || envelope.Error["correction"] == nil {
				t.Fatalf("missing guidance: %#v", envelope.Error)
			}
		})
	}
	var textOut, textErr bytes.Buffer
	if code := runWith([]string{"list", "--format", "text"}, &textOut, &textErr); code != ExitSchemaIncompatible {
		t.Fatalf("text exit = %d, want %d; stderr=%q", code, ExitSchemaIncompatible, textErr.String())
	}
	if textOut.Len() != 0 || !bytes.Contains(textErr.Bytes(), []byte("ktx: schema-incompatible:")) || !bytes.Contains(textErr.Bytes(), []byte("do not lower PRAGMA user_version")) {
		t.Fatalf("text output stdout=%q stderr=%q", textOut.String(), textErr.String())
	}
	if after := directorySnapshot(t, dir); !reflect.DeepEqual(after, before) {
		t.Fatalf("schema rejection changed storage:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestRuntimeProofPrecedesSchemaInspection(t *testing.T) {
	dataHome := t.TempDir()
	dbDir := filepath.Join(dataHome, "ktx")
	if err := os.Mkdir(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	seedSchemaVersion(t, filepath.Join(dbDir, "ktx.db"), dbpkg.MaxSupportedSchema()+1)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("KTX_DB", "")
	before := directorySnapshot(t, dbDir)
	for _, test := range []struct {
		name string
		args []string
		code int
	}{
		{name: "missing", args: []string{"list"}, code: ExitConflict},
		{name: "mismatch", args: []string{"--expect-runtime-sha256", otherRuntimeDigest, "list"}, code: ExitConflict},
		{name: "malformed", args: []string{"--expect-runtime-sha256", "bad", "list"}, code: ExitUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runWith(test.args, &stdout, &stderr); code != test.code {
				t.Fatalf("exit = %d, want %d; stderr=%q", code, test.code, stderr.String())
			}
			if stdout.Len() != 0 || !bytes.Contains(stderr.Bytes(), []byte(`"code":"`)) {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
	if after := directorySnapshot(t, dbDir); !reflect.DeepEqual(after, before) {
		t.Fatalf("ambient target changed before proof:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func seedSchemaVersion(t *testing.T, path string, version int) {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("PRAGMA user_version = " + strconv.Itoa(version)); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

type snapshotEntry struct {
	Name string
	Mode os.FileMode
	Data []byte
}

func directorySnapshot(t *testing.T, dir string) []snapshotEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]snapshotEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		item := snapshotEntry{Name: entry.Name(), Mode: info.Mode()}
		if !entry.IsDir() {
			item.Data, err = os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
		}
		result = append(result, item)
	}
	return result
}
