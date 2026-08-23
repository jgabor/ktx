package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/binarycap"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

const otherRuntimeDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestCommandDatabaseAccessDeclarations(t *testing.T) {
	for _, command := range []string{"get", "read", "list", "stats", "decompress"} {
		if got := dispatchRegistry[command].opts.DatabaseAccess; got != DatabaseReadOnly {
			t.Errorf("%s database access = %d, want read-only", command, got)
		}
	}
	for _, command := range []string{"write", "compress", "hook"} {
		if got := dispatchRegistry[command].opts.DatabaseAccess; got != DatabaseReadWrite {
			t.Errorf("%s database access = %d, want read-write", command, got)
		}
	}
	for _, command := range []string{"bench", "telemetry", "doctor", "db", "binary-capabilities"} {
		if got := dispatchRegistry[command].opts.DatabaseAccess; got != DatabaseNone {
			t.Errorf("%s database access = %d, want command-owned or none", command, got)
		}
	}
}

func TestRuntimeProofGlobalFlag(t *testing.T) {
	parsed, rest, err := parseGlobalFlags([]string{"--expect-runtime-sha256", testBinaryDigest, "--version"})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ExpectedRuntimeSHA256 != testBinaryDigest || len(rest) != 0 || !parsed.showVersion {
		t.Fatalf("parsed=%#v rest=%#v", parsed, rest)
	}
	for _, value := range []string{"bad", strings.Repeat("a", 63), strings.Repeat("A", 64)} {
		if _, _, err := parseGlobalFlags([]string{"--expect-runtime-sha256", value, "list"}); err == nil {
			t.Fatalf("accepted malformed digest %q", value)
		}
	}
}

func TestAmbientQueryDoesNotCreateDatabase(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("KTX_DB", "")
	originalIdentity := currentBinaryCapabilities
	currentBinaryCapabilities = func() (binarycap.Identity, error) {
		return binarycap.Identity{ExecutablePath: "/verified/ktx", ExecutableSHA256: testBinaryDigest}, nil
	}
	t.Cleanup(func() { currentBinaryCapabilities = originalIdentity })

	for _, args := range [][]string{
		{"list"},
		{"stats", "summary", "--window", "30m"},
		{"stats", "breakdown", "--window", "30m", "--format", "json"},
		{"bench", "report"},
		{"get", "aaaaaaaaaaaaaaaa"},
		{"read", "aaaaaaaaaaaaaaaa"},
		{"decompress", "aaaaaaaaaaaaaaaa"},
		{"telemetry", "list", "--session", "aaaaaaaaaaaaaaaa"},
	} {
		var stdout, stderr bytes.Buffer
		code := runWith(append([]string{"--expect-runtime-sha256", testBinaryDigest}, args...), &stdout, &stderr)
		if code != ExitOK && code != ExitNotFound {
			t.Fatalf("%v exit = %d; stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
		path := filepath.Join(dataHome, "ktx", "ktx.db")
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%v created ambient database: %v", args, err)
		}
	}
}

func TestQueryCommandsPreservePersistentDatabase(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "ktx.db")
	database, err := dbpkg.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := "aaaaaaaaaaaaaaaa"
	callID := "bbbbbbbbbbbbbbbb"
	compressedID := "cccccccccccccccc"
	if err := database.InsertSession(&dbpkg.Session{ID: sessionID, Agent: "test", StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if err := database.PersistToolCall(&dbpkg.ToolCall{ID: callID, SessionID: sessionID, Tool: "read", Input: "{}", Output: "original", Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if err := database.InsertCompressed(&dbpkg.Compressed{ID: compressedID, SourceID: callID, Output: "short", ContentHash: dbpkg.ContentHash("short"), Pipeline: "[]", Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KTX_DB", path)
	before := directorySnapshot(t, directory)

	for _, args := range [][]string{
		{"list"},
		{"stats", "summary", "--window", "30m"},
		{"stats", "breakdown", "--window", "30m", "--format", "json"},
		{"get", callID},
		{"read", callID},
		{"decompress", compressedID},
		{"bench", "report"},
		{"telemetry", "list", "--session", sessionID},
	} {
		var stdout, stderr bytes.Buffer
		if code := runWith(args, &stdout, &stderr); code != ExitOK {
			t.Fatalf("%v exit = %d; stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
		if after := directorySnapshot(t, directory); !reflect.DeepEqual(after, before) {
			t.Fatalf("%v changed persistent database:\nbefore=%#v\nafter=%#v", args, before, after)
		}
	}
}
