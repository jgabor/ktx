package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/binarycap"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

func TestDatabaseDeadlinesEmitOneStableAttribution(t *testing.T) {
	originalIdentity := currentBinaryCapabilities
	currentBinaryCapabilities = func() (binarycap.Identity, error) {
		return binarycap.Identity{ExecutablePath: "/verified/ktx", ExecutableSHA256: testBinaryDigest}, nil
	}
	t.Cleanup(func() { currentBinaryCapabilities = originalIdentity })

	path := filepath.Join(t.TempDir(), "ktx.db")
	started := time.Now().Add(-time.Second)
	for _, test := range []struct {
		code, phase string
	}{
		{code: dbpkg.TimeoutRuntimeStartup, phase: dbpkg.OperationOpen},
		{code: dbpkg.TimeoutOperation, phase: dbpkg.OperationSource},
		{code: dbpkg.TimeoutMigration, phase: dbpkg.OperationMigration},
	} {
		t.Run(test.code, func(t *testing.T) {
			raw := dbpkg.AttributeDeadline(test.code, test.phase, started, context.DeadlineExceeded)
			classified := classifyDatabaseError(path, "test", raw, test.phase, started)
			if classified == nil {
				t.Fatal("deadline was not classified")
			}
			var stderr bytes.Buffer
			printError(&stderr, classified, FormatJSON)
			rendered := stderr.String()
			var envelope struct {
				Error map[string]any `json:"error"`
			}
			decoder := json.NewDecoder(&stderr)
			if err := decoder.Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error["code"] != test.code || envelope.Error["operation_phase"] != test.phase {
				t.Fatalf("error=%#v", envelope.Error)
			}
			if envelope.Error["database_path"] == "" || envelope.Error["correction"] == "" || envelope.Error["elapsed_wait_nanoseconds"].(float64) < 0 {
				t.Fatalf("incomplete attribution: %#v", envelope.Error)
			}
			if decoder.More() || strings.TrimSpace(rendered) == "" {
				t.Fatalf("unexpected deadline envelope: %q", rendered)
			}
			for _, forbidden := range []string{"SQLITE_BUSY", "SQLITE_LOCKED", "database is locked"} {
				if strings.Contains(strings.ToUpper(rendered), strings.ToUpper(forbidden)) {
					t.Fatalf("deadline leaked driver wording %q: %s", forbidden, rendered)
				}
			}
		})
	}
}
