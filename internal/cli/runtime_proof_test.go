package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/binarycap"
)

func TestAmbientDatabaseRequiresExactRuntimeProofBeforeInspection(t *testing.T) {
	dataHome := t.TempDir()
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("KTX_DB", "")
	t.Setenv("KTX_VERIFY_DB_AUDIT", auditPath)
	t.Setenv("KTX_VERIFY_FORBIDDEN_DB_PATHS", "[]")

	originalIdentity := currentBinaryCapabilities
	currentBinaryCapabilities = func() (binarycap.Identity, error) {
		return binarycap.Identity{ExecutablePath: "/verified/ktx", ExecutableSHA256: testBinaryDigest}, nil
	}
	t.Cleanup(func() { currentBinaryCapabilities = originalIdentity })

	for _, test := range []struct {
		name, digest, failureClass string
	}{
		{name: "missing", failureClass: "runtime-proof-required"},
		{name: "mismatch", digest: otherRuntimeDigest, failureClass: "runtime-proof-mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_ = os.Remove(auditPath)
			args := []string{"list"}
			if test.digest != "" {
				args = append([]string{"--expect-runtime-sha256", test.digest}, args...)
			}
			var stdout, stderr bytes.Buffer
			if code := runWith(args, &stdout, &stderr); code != ExitConflict {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			var envelope struct {
				Error map[string]any `json:"error"`
			}
			if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error["failure_class"] != test.failureClass || envelope.Error["correction"] == nil {
				t.Fatalf("error=%#v", envelope.Error)
			}
			data, err := os.ReadFile(auditPath)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			for _, forbidden := range []string{`"operation":"inspect"`, `"operation":"open"`, `"operation":"open_read_only"`} {
				if strings.Contains(string(data), forbidden) {
					t.Fatalf("database access preceded proof: %s", data)
				}
			}
			if _, err := os.Stat(filepath.Join(dataHome, "ktx")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("ambient directory changed: %v", err)
			}
		})
	}
}

func TestExplicitDatabaseTargetIsExemptFromRuntimeProof(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned", "ktx.db")
	t.Setenv("KTX_DB", path)
	var stdout, stderr bytes.Buffer
	if code := runWith([]string{"list"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("logical-empty query created explicit database: %v", err)
	}
}

func TestRuntimeProofDoesNotMakeDBFreeCommandsAccessDatabase(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("KTX_DB", "")
	var stdout, stderr bytes.Buffer
	if code := runWith([]string{"--expect-runtime-sha256", testBinaryDigest, "--version"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dataHome, "ktx")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("DB-free command changed ambient database path: %v", err)
	}
}

func TestMatchingRuntimeProofAllowsAmbientCreation(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("KTX_DB", "")
	originalIdentity := currentBinaryCapabilities
	currentBinaryCapabilities = func() (binarycap.Identity, error) {
		return binarycap.Identity{ExecutablePath: "/verified/ktx", ExecutableSHA256: testBinaryDigest}, nil
	}
	t.Cleanup(func() { currentBinaryCapabilities = originalIdentity })
	registerFor(t, "proof-create", RegisterOptions{DatabaseAccess: DatabaseReadWrite}, func(*DispatchContext) (any, error) {
		return nil, nil
	})

	var stdout, stderr bytes.Buffer
	if code := runWith([]string{"--expect-runtime-sha256", testBinaryDigest, "proof-create"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dataHome, "ktx", "ktx.db")); err != nil {
		t.Fatalf("matching proof did not permit ambient creation: %v", err)
	}
}
