// Table-driven tests for `ktx compress` and `ktx decompress` (M4 plan
// Task 6 / D14). The pair's primary acceptance contract is the
// round-trip: decompress(compress(source)) returns the source's
// original output bytes verbatim, regardless of how many lossy stages
// ran between them. Coverage:
//
//   - identity pipeline (no ktx.toml): compress → decompress round-trip
//   - parameterised pipeline (strip-ansi + head:N): compress differs
//     from source; decompress still returns the source bytes verbatim
//   - source not found → exit 2 (not-found envelope on stderr)
//   - compressed not found → exit 2 (not-found envelope on stderr)
//   - missing id → exit 1 (usage)
//   - extra positional arg → exit 1 (usage)
//   - --help → exit 0 with help on stdout
//   - compressed row records source_id, content_hash, pipeline JSON
//   - decompress emits raw bytes (no JSON wrapping)
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// compressTestDB opens a per-test KTX_DB database. Mirrors
// readTestDB / writeTestDB; the project's test harness sets KTX_DB
// to a tempdir for hermetic per-test isolation.
func compressTestDB(t *testing.T) *dbpkg.DB {
	t.Helper()
	path := os.Getenv("KTX_DB")
	if path == "" {
		t.Fatal("compressTestDB requires KTX_DB to be set")
	}
	d, err := dbpkg.Open(path)
	if err != nil {
		t.Fatalf("dbpkg.Open(%q): %v", path, err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// seedSource tool_call inserts a session + tool_call row so `ktx
// compress <id>` has something to read. Returns the tool_call id.
// outputBytes is the persisted Output (so tests can vary it without
// a long struct literal).
func seedSource(t *testing.T, d *dbpkg.DB, toolCallID, sessionID, output string) {
	t.Helper()
	if err := d.InsertSession(&dbpkg.Session{
		ID:         sessionID,
		Agent:      "test-agent",
		StartedAt:  "2026-06-24T12:00:00Z",
		LastSeenAt: "2026-06-24T12:00:00Z",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	if err := d.PersistToolCall(&dbpkg.ToolCall{
		ID:          toolCallID,
		SessionID:   sessionID,
		Tool:        "bash",
		Input:       "ls",
		Output:      output,
		ContentHash: dbpkg.ContentHash(output),
		Timestamp:   "2026-06-24T12:00:00Z",
		EventType:   "tool_call",
	}); err != nil {
		t.Fatalf("PersistToolCall: %v", err)
	}
}

// noConfigEnv chdirs to a fresh tempdir and points XDG_CONFIG_HOME at
// another fresh tempdir, so config.Load() resolves to the D14
// identity default (no ktx.toml anywhere). Returns a restore func.
// The chdir is essential because Load() looks for ktx.toml in the
// cwd before checking XDG.
func noConfigEnv(t *testing.T) func() {
	t.Helper()
	restore := chdir(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return restore
}

// writeProjectConfig writes a ktx.toml containing the named pipeline
// stages into the cwd (the test's chdirTemp target). Returns the
// path. The file is small so it is written inline via the standard
// library.
func writeProjectConfig(t *testing.T, stages string) {
	t.Helper()
	path := filepath.Join(".", "ktx.toml")
	if err := os.WriteFile(path, []byte("[pipeline.default]\nstages = "+stages+"\n"), 0o600); err != nil {
		t.Fatalf("write ktx.toml: %v", err)
	}
}

// decodeID reads a JSON-encoded id string from stdout (the shape
// `ktx compress` prints: a single JSON string). Returns the id so
// tests can pass it to a subsequent decompress / GetCompressed call.
func decodeID(t *testing.T, stdout *bytes.Buffer) string {
	t.Helper()
	var id string
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &id); err != nil {
		t.Fatalf("stdout is not a JSON string: %v\nstdout=%q", err, stdout.String())
	}
	if id == "" {
		t.Fatalf("decoded id is empty (stdout=%q)", stdout.String())
	}
	return id
}

// TestRunCompressWith_IdentityRoundTrip covers the canonical M4
// acceptance path: a ktx with NO ktx.toml runs the D14 identity
// pipeline, compress(source) persists a compressed row whose
// output equals the source, and decompress(compressedID) returns
// the source bytes verbatim.
func TestRunCompressWith_IdentityRoundTrip(t *testing.T) {
	t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
	d := compressTestDB(t)
	restore := noConfigEnv(t)
	defer restore()

	const sourceOutput = "hello\nworld\n"
	seedSource(t, d, "abcdefghijklmnop", "zyxwvutsrqponmlk", sourceOutput)

	// compress <source-id>
	var stdout, stderr bytes.Buffer
	code := runCompressWith([]string{"abcdefghijklmnop"}, Options{Format: FormatJSON}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("compress exit = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
	}
	compressedID := decodeID(t, &stdout)

	// Verify the persisted compressed row records what D14 mandates:
	// output equals source.output (identity pipeline), content_hash
	// matches db.ContentHash(output), pipeline is "[]", source_id
	// references the source tool_call.
	comp, err := d.GetCompressed(compressedID)
	if err != nil {
		t.Fatalf("GetCompressed(%q): %v", compressedID, err)
	}
	if comp.SourceID != "abcdefghijklmnop" {
		t.Errorf("SourceID = %q, want abcdefghijklmnop", comp.SourceID)
	}
	if comp.Output != sourceOutput {
		t.Errorf("Output = %q, want %q (identity pipeline should preserve source)", comp.Output, sourceOutput)
	}
	if comp.ContentHash != dbpkg.ContentHash(sourceOutput) {
		t.Errorf("ContentHash = %q, want %q", comp.ContentHash, dbpkg.ContentHash(sourceOutput))
	}
	if comp.Pipeline != "[]" {
		t.Errorf("Pipeline = %q, want \"[]\" (no stages ran)", comp.Pipeline)
	}

	// decompress <compressed-id> → source bytes verbatim.
	var dstdout, dstderr bytes.Buffer
	code = runDecompressWith([]string{compressedID}, Options{Format: FormatJSON}, &dstdout, &dstderr)
	if code != ExitOK {
		t.Fatalf("decompress exit = %d, want %d (stderr=%q)", code, ExitOK, dstderr.String())
	}
	if dstdout.String() != sourceOutput {
		t.Errorf("decompress output = %q, want %q (verbatim source)", dstdout.String(), sourceOutput)
	}
}

// TestRunCompressWith_PipelineModifiesOutput covers the D14 case
// where lossy stages run: compress(source).output differs from
// source.output, but decompress(compressedID) STILL returns the
// source bytes (reversibility is by id, not per-stage inverse).
func TestRunCompressWith_PipelineModifiesOutput(t *testing.T) {
	t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
	d := compressTestDB(t)
	restore := chdir(t, t.TempDir())
	defer restore()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// strip-ansi removes the escape codes; head:1 keeps only the
	// first line. Together they should make the compressed output
	// strictly shorter than the source.
	writeProjectConfig(t, `["strip-ansi", "head:1"]`)

	const sourceOutput = "\x1b[31mred line\x1b[0m\nsecond line\nthird line\n"
	seedSource(t, d, "abcdefghijklmnop", "zyxwvutsrqponmlk", sourceOutput)

	var stdout, stderr bytes.Buffer
	code := runCompressWith([]string{"abcdefghijklmnop"}, Options{Format: FormatJSON}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("compress exit = %d, want %d (stderr=%q)", code, ExitOK, stderr.String())
	}
	compressedID := decodeID(t, &stdout)
	if compressedID == "abcdefghijklmnop" {
		t.Fatalf("compressed id should differ from source id (got %q)", compressedID)
	}

	comp, err := d.GetCompressed(compressedID)
	if err != nil {
		t.Fatalf("GetCompressed(%q): %v", compressedID, err)
	}
	if comp.Output == sourceOutput {
		t.Errorf("Output unchanged; pipeline should have reduced it (output=%q)", comp.Output)
	}
	if comp.ContentHash != dbpkg.ContentHash(comp.Output) {
		t.Errorf("ContentHash = %q, want hash of compressed output %q", comp.ContentHash, comp.Output)
	}
	// pipeline records what ran (D14 "a record of what stages ran").
	if !strings.Contains(comp.Pipeline, "strip-ansi") || !strings.Contains(comp.Pipeline, "head:1") {
		t.Errorf("Pipeline = %q, expected both stage names", comp.Pipeline)
	}
	// Reversibility by id: decompress returns the SOURCE verbatim.
	var dstdout, dstderr bytes.Buffer
	code = runDecompressWith([]string{compressedID}, Options{Format: FormatJSON}, &dstdout, &dstderr)
	if code != ExitOK {
		t.Fatalf("decompress exit = %d, want %d (stderr=%q)", code, ExitOK, dstderr.String())
	}
	if dstdout.String() != sourceOutput {
		t.Errorf("decompress output = %q, want source verbatim %q", dstdout.String(), sourceOutput)
	}
}

// TestRunCompressWith_SourceNotFound covers the not-found path: a
// source id that does not exist in tool_calls maps to exit 2 with
// the canonical not-found envelope on stderr.
func TestRunCompressWith_SourceNotFound(t *testing.T) {
	t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
	compressTestDB(t)
	restore := noConfigEnv(t)
	defer restore()

	var stdout, stderr bytes.Buffer
	code := runCompressWith([]string{"doesnotexist12345"}, Options{Format: FormatJSON}, &stdout, &stderr)
	if code != ExitNotFound {
		t.Fatalf("exit = %d, want %d (stderr=%q)", code, ExitNotFound, stderr.String())
	}
	if !strings.Contains(stderr.String(), `"code":"not-found"`) {
		t.Errorf("stderr missing not-found envelope: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on not-found, got %q", stdout.String())
	}
}

// TestRunDecompressWith_NotFound covers both not-found cases for
// `ktx decompress`: a missing compressed id (exit 2) and a missing
// source (the compressed row's source_id points at a tool_call that
// was deleted, violating D7's append-only — exit 2 as well because
// the typed ErrSourceNotFound wraps ErrNotFound).
func TestRunDecompressWith_NotFound(t *testing.T) {
	t.Run("missing compressed id → exit 2", func(t *testing.T) {
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		compressTestDB(t)

		var stdout, stderr bytes.Buffer
		code := runDecompressWith([]string{"missingcompressed1"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitNotFound {
			t.Fatalf("exit = %d, want %d (stderr=%q)", code, ExitNotFound, stderr.String())
		}
		if !strings.Contains(stderr.String(), `"code":"not-found"`) {
			t.Errorf("stderr missing not-found envelope: %q", stderr.String())
		}
	})

	t.Run("missing source (D7 violation) → exit 2", func(t *testing.T) {
		// Synthesize a dangling compressed row by inserting one
		// whose source_id has no tool_call row. db.InsertCompressed
		// pre-checks the FK and would reject, so we use a raw
		// INSERT via OpenWithoutMigration is overkill — the FK
		// pre-check fires inside InsertCompressed. Instead, we
		// insert a real source, then delete the tool_call row by
		// raw SQL via the db handle (the test exercises the
		// decompress error path, not the InsertCompressed path).
		t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
		d := compressTestDB(t)
		seedSource(t, d, "abcdefghijklmnop", "zyxwvutsrqponmlk", "doomed\n")
		c := &dbpkg.Compressed{
			ID:          "compressedid0001",
			SourceID:    "abcdefghijklmnop",
			Output:      "doomed\n",
			ContentHash: dbpkg.ContentHash("doomed\n"),
			Pipeline:    "[]",
			Timestamp:   "2026-06-24T12:00:00Z",
		}
		if err := d.InsertCompressed(c); err != nil {
			t.Fatalf("InsertCompressed: %v", err)
		}
		// Manually delete the source row to violate D7. The FK
		// on compressed.source_id is ON DELETE NO ACTION by
		// default in SQLite, so we use a raw delete with FKs
		// temporarily disabled (PRAGMA foreign_keys=OFF scoped
		// to this connection).
		_, err := d.DB().Exec("PRAGMA foreign_keys=OFF; DELETE FROM tool_calls WHERE id = ?; PRAGMA foreign_keys=ON;", "abcdefghijklmnop")
		if err != nil {
			t.Fatalf("raw delete: %v", err)
		}

		var stdout, stderr bytes.Buffer
		code := runDecompressWith([]string{"compressedid0001"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitNotFound {
			t.Fatalf("exit = %d, want %d (stderr=%q)", code, ExitNotFound, stderr.String())
		}
		if !strings.Contains(stderr.String(), `"code":"not-found"`) {
			t.Errorf("stderr missing not-found envelope: %q", stderr.String())
		}
	})
}

// TestRunCompressWith_UsageErrors covers the argument-validation
// surface: missing id and an extra positional arg both map to exit
// 1 with a usage envelope.
func TestRunCompressWith_UsageErrors(t *testing.T) {
	t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
	compressTestDB(t)
	restore := noConfigEnv(t)
	defer restore()

	t.Run("missing id → exit 1 usage", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runCompressWith(nil, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "missing required argument") {
			t.Errorf("stderr missing missing-arg message: %q", stderr.String())
		}
	})

	t.Run("extra positional → exit 1 usage", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runCompressWith([]string{"abcdefghijklmnop", "extra"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
	})
}

// TestRunDecompressWith_UsageErrors mirrors the compress usage-error
// surface for `ktx decompress`.
func TestRunDecompressWith_UsageErrors(t *testing.T) {
	t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
	compressTestDB(t)

	t.Run("missing id → exit 1 usage", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runDecompressWith(nil, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "missing required argument") {
			t.Errorf("stderr missing missing-arg message: %q", stderr.String())
		}
	})

	t.Run("extra positional → exit 1 usage", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runDecompressWith([]string{"compressedid0001", "extra"}, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("exit = %d, want %d (stderr=%q)", code, ExitUsage, stderr.String())
		}
	})
}

// TestRunCompressWith_Help covers the --help / -h surface: both
// exit 0 with the help block on stdout (no stderr noise).
func TestRunCompressWith_Help(t *testing.T) {
	t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
	compressTestDB(t)

	for _, args := range [][]string{{"--help"}, {"-h"}} {
		var stdout, stderr bytes.Buffer
		code := runCompressWith(args, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("args=%v exit = %d, want %d (stderr=%q)", args, code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx compress") {
			t.Errorf("args=%v stdout missing ktx compress header: %q", args, stdout.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("args=%v stderr should be empty, got %q", args, stderr.String())
		}
	}
}

// TestRunDecompressWith_Help mirrors for decompress.
func TestRunDecompressWith_Help(t *testing.T) {
	t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
	compressTestDB(t)

	for _, args := range [][]string{{"--help"}, {"-h"}} {
		var stdout, stderr bytes.Buffer
		code := runDecompressWith(args, Options{Format: FormatJSON}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("args=%v exit = %d, want %d (stderr=%q)", args, code, ExitOK, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx decompress") {
			t.Errorf("args=%v stdout missing ktx decompress header: %q", args, stdout.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("args=%v stderr should be empty, got %q", args, stderr.String())
		}
	}
}
