package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

func TestDoctorAndBenchExportClassifyContentionOnce(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "doctor", args: []string{"doctor"}},
		{name: "bench export", args: []string{"bench", "export", "aaaaaaaaaaaaaaaa"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "inspection.db")
			database, err := dbpkg.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}

			ready, release := path+".ready", path+".release"
			holder, _ := startHookStartupHelper(t, "hold-exclusive", path, ready, release)
			if err := holder.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = os.WriteFile(release, nil, 0o600)
				_ = holder.Wait()
			})
			waitForHookStartupFile(t, ready, 5*time.Second)
			t.Setenv("KTX_DB", path)

			var stdout, stderr bytes.Buffer
			if code := runWith(test.args, &stdout, &stderr); code != ExitInternal {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout=%q, want empty", stdout.String())
			}
			var envelope struct {
				Error map[string]any `json:"error"`
			}
			if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
				t.Fatalf("expected one JSON envelope: %v\n%s", err, stderr.String())
			}
			if envelope.Error["code"] != string(CodeDatabaseUnavailable) || envelope.Error["failure_class"] != dbpkg.UnavailableClassSQLiteLock {
				t.Fatalf("error=%#v", envelope.Error)
			}
			lower := strings.ToLower(stderr.String())
			for _, forbidden := range []string{"sqlite_busy", "sqlite_locked", "database is locked"} {
				if strings.Contains(lower, forbidden) {
					t.Fatalf("raw lock error leaked: %s", stderr.String())
				}
			}
			if err := os.WriteFile(release, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := holder.Wait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
