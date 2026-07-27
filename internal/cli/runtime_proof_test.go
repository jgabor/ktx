package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/binarycap"
)

const otherRuntimeDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestRuntimeProofGlobalFlag(t *testing.T) {
	parsed, rest, err := parseGlobalFlags([]string{"--expect-runtime-sha256", testBinaryDigest, "list"})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ExpectedRuntimeSHA256 != testBinaryDigest || len(rest) != 1 || rest[0] != "list" {
		t.Fatalf("parsed = %#v, rest = %#v", parsed, rest)
	}
	for _, value := range []string{"ABC", strings.Repeat("a", 63), strings.Repeat("A", 64)} {
		if _, _, err := parseGlobalFlags([]string{"--expect-runtime-sha256", value, "list"}); err == nil {
			t.Fatalf("accepted malformed digest %q", value)
		}
	}
}

func TestAmbientOpenRequiresExactRuntimeProofBeforeDatabaseAccess(t *testing.T) {
	dataHome := t.TempDir()
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("KTX_DB", "")
	t.Setenv("KTX_VERIFY_DB_AUDIT", auditPath)
	t.Setenv("KTX_VERIFY_FORBIDDEN_DB_PATHS", "[]")

	original := currentBinaryCapabilities
	currentBinaryCapabilities = func() (binarycap.Identity, error) {
		return binarycap.Identity{ExecutablePath: "/tmp/verified-ktx", ExecutableSHA256: testBinaryDigest, BuildVersion: "test", MaxDatabaseSchema: 8}, nil
	}
	t.Cleanup(func() { currentBinaryCapabilities = original })

	for _, tc := range []struct {
		name  string
		args  []string
		code  int
		class string
	}{
		{name: "missing", args: []string{"list"}, code: ExitConflict, class: "runtime-proof-required"},
		{name: "mismatch", args: []string{"--expect-runtime-sha256", otherRuntimeDigest, "list"}, code: ExitConflict, class: "runtime-proof-mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(auditPath)
			var stdout, stderr bytes.Buffer
			if code := runWith(tc.args, &stdout, &stderr); code != tc.code {
				t.Fatalf("exit = %d, want %d; stderr=%s", code, tc.code, stderr.String())
			}
			var envelope struct {
				Error map[string]any `json:"error"`
			}
			if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error["failure_class"] != tc.class || envelope.Error["correction"] == nil {
				t.Fatalf("error = %#v", envelope.Error)
			}
			if data, err := os.ReadFile(auditPath); err != nil || strings.Contains(string(data), `"operation":"open"`) || strings.Contains(string(data), `"operation":"inspect"`) {
				t.Fatalf("audit = %q, err=%v", data, err)
			}
			if _, err := os.Stat(filepath.Join(dataHome, "ktx")); !os.IsNotExist(err) {
				t.Fatalf("database directory changed: %v", err)
			}
		})
	}
}

func TestExplicitEnvironmentTargetDoesNotRequireRuntimeProof(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned", "ktx.db")
	t.Setenv("KTX_DB", path)
	var stdout, stderr bytes.Buffer
	if code := runWith([]string{"list"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit = %d; stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("explicit database was not created: %v", err)
	}
}

func TestValidAmbientRuntimeProofAllowsOpen(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("KTX_DB", "")
	original := currentBinaryCapabilities
	currentBinaryCapabilities = func() (binarycap.Identity, error) {
		return binarycap.Identity{ExecutablePath: "/tmp/verified-ktx", ExecutableSHA256: testBinaryDigest, BuildVersion: "test", MaxDatabaseSchema: 8}, nil
	}
	t.Cleanup(func() { currentBinaryCapabilities = original })

	var stdout, stderr bytes.Buffer
	args := []string{"--expect-runtime-sha256", testBinaryDigest, "list"}
	if code := runWith(args, &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit = %d; stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dataHome, "ktx", "ktx.db")); err != nil {
		t.Fatalf("verified ambient database was not created: %v", err)
	}
}

func TestRuntimeProofIsHarmlessForDatabaseFreeCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runWith([]string{"--expect-runtime-sha256", testBinaryDigest, "--version"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit = %d; stderr=%s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("version output is empty")
	}
}
