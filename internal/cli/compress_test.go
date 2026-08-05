// Table-driven tests for `ktx compress` and `ktx decompress`. The
// pair's primary acceptance contract is the round-trip:
// decompress(compress(source)) returns the source's original output
// bytes verbatim, regardless of how many lossy stages ran between
// them. Coverage:
//
//   - passthrough pipeline (no ktx.toml): compress → decompress round-trip
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
	"slices"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/config"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/pipeline"
	"github.com/jgabor/ktx/internal/pipeline/stages"
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

func seedSourceWithFailure(t *testing.T, d *dbpkg.DB, toolCallID, sessionID, output string, isError *bool, exitCode *int64) {
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
		IsError:     isError,
		ExitCode:    exitCode,
	}); err != nil {
		t.Fatalf("PersistToolCall: %v", err)
	}
}

func ptrBool(v bool) *bool { return &v }

func ptrInt64(v int64) *int64 { return &v }

// noConfigEnv chdirs to a fresh tempdir and points XDG_CONFIG_HOME at
// another fresh tempdir, so config.Load() resolves to the passthrough
// default (no ktx.toml anywhere). Returns a restore func.
func noConfigEnv(t *testing.T) func() {
	t.Helper()
	restore := chdir(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return restore
}

// writeUserConfig writes a ktx.toml containing the named pipeline
// stages into the user-level config path
// ($XDG_CONFIG_HOME/ktx/ktx.toml). Requires XDG_CONFIG_HOME to be set.
func writeUserConfig(t *testing.T, stages string) {
	t.Helper()
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		t.Fatal("XDG_CONFIG_HOME not set")
	}
	path := filepath.Join(xdg, "ktx", "ktx.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
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

// TestRunCompressWith_IdentityRoundTrip covers the canonical round-trip
// acceptance path: a ktx with NO ktx.toml runs the passthrough pipeline,
// compress(source) persists a compressed row whose output equals the
// source, and decompress(compressedID) returns the source bytes verbatim.
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

	// Verify the persisted compressed row records what the contract
	// mandates: output equals source.output (passthrough pipeline),
	// content_hash matches db.ContentHash(output), pipeline is "[]",
	// source_id references the source tool_call.
	comp, err := d.GetCompressed(compressedID)
	if err != nil {
		t.Fatalf("GetCompressed(%q): %v", compressedID, err)
	}
	if comp.SourceID != "abcdefghijklmnop" {
		t.Errorf("SourceID = %q, want abcdefghijklmnop", comp.SourceID)
	}
	if comp.Output != sourceOutput {
		t.Errorf("Output = %q, want %q (passthrough pipeline should preserve source)", comp.Output, sourceOutput)
	}
	if comp.ContentHash != dbpkg.ContentHash(sourceOutput) {
		t.Errorf("ContentHash = %q, want %q", comp.ContentHash, dbpkg.ContentHash(sourceOutput))
	}
	if comp.Pipeline != `{"profile":"default","route":"default_fallback","stages":[]}` {
		t.Errorf("Pipeline = %q, want empty default profile (no stages ran)", comp.Pipeline)
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

// TestRunCompressWith_PipelineModifiesOutput covers the case where lossy
// stages run: compress(source).output differs from source.output, but
// decompress(compressedID) STILL returns the source bytes (reversibility
// is by id, not per-stage inverse).
func TestRunCompressWith_PipelineModifiesOutput(t *testing.T) {
	t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
	d := compressTestDB(t)
	restore := chdir(t, t.TempDir())
	defer restore()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// strip-ansi removes the escape codes; head:1 keeps only the
	// first line. Together they should make the compressed output
	// strictly shorter than the source.
	writeUserConfig(t, `["strip-ansi", "head:1"]`)

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
	// pipeline records what ran (a record of what stages ran).
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
// was deleted, violating the append-only contract — exit 2 as well
// because the typed ErrSourceNotFound wraps ErrNotFound).
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

	t.Run("missing source (append-only violation) → exit 2", func(t *testing.T) {
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
		// Manually delete the source row to violate the append-only
		// contract. The FK on compressed.source_id is ON DELETE NO
		// ACTION by default in SQLite, so we use a raw delete with FKs
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

// TestCompressLogic_SourceFailureGate covers a failure route that selects a
// lossy profile. The gate still writes a passthrough row, warns, prints an ID,
// and leaves the original recoverable verbatim.
func TestCompressLogic_SourceFailureGate(t *testing.T) {
	t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
	d := compressTestDB(t)
	restore := chdir(t, t.TempDir())
	defer restore()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:5"]

[pipeline.failure-lossy]
stages = ["head:1"]

[[route]]
exit_status = "success"
profile = "default"

[[route]]
exit_status = "failure"
profile = "failure-lossy"
`)

	const sourceOutput = "failed tool output\nline two\n"
	seedSourceWithFailure(t, d, "abcdefghijklmnop", "zyxwvutsrqponmlk", sourceOutput, ptrBool(true), nil)

	var stdout, stderr bytes.Buffer
	_, err := compressLogic([]string{"abcdefghijklmnop"}, nil, d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("compressLogic: %v", err)
	}
	if !strings.Contains(stderr.String(), sourceFailureWarning) {
		t.Errorf("stderr missing source-failure warning: %q", stderr.String())
	}
	compressedID := decodeID(t, &stdout)

	comp, err := d.GetCompressed(compressedID)
	if err != nil {
		t.Fatalf("GetCompressed: %v", err)
	}
	if comp.Output != sourceOutput {
		t.Errorf("Output = %q, want passthrough %q", comp.Output, sourceOutput)
	}
	if comp.Pipeline != `{"profile":"failure-lossy","route":"matched","stages":[]}` {
		t.Errorf("Pipeline = %q, want matched failure profile with no stages run", comp.Pipeline)
	}

	var dstdout, dstderr bytes.Buffer
	code := runDecompressWith([]string{compressedID}, Options{Format: FormatJSON}, &dstdout, &dstderr)
	if code != ExitOK {
		t.Fatalf("decompress exit = %d (stderr=%q)", code, dstderr.String())
	}
	if dstdout.String() != sourceOutput {
		t.Errorf("decompress = %q, want source verbatim", dstdout.String())
	}
}

// TestCompressLogic_SourceFailureGate_NoFire covers ordinary configured
// handling for explicit success and unknown, including exit-code-only unknown.
func TestCompressLogic_SourceFailureGate_NoFire(t *testing.T) {
	cases := []struct {
		name        string
		isError     *bool
		exitCode    *int64
		routes      string
		wantProfile string
		wantRoute   string
	}{
		{
			name:        "explicit success uses success route",
			isError:     ptrBool(false),
			routes:      "[[route]]\nexit_status = \"success\"\nprofile = \"outcome\"\n",
			wantProfile: "outcome",
			wantRoute:   "matched",
		},
		{
			name:        "unknown rejects success route and uses default",
			routes:      "[[route]]\nexit_status = \"success\"\nprofile = \"outcome\"\n",
			wantProfile: "default",
			wantRoute:   "default_fallback",
		},
		{
			name:     "exit-code-only unknown skips success and uses any route",
			exitCode: ptrInt64(1),
			routes: "[[route]]\nexit_status = \"success\"\nprofile = \"default\"\n\n" +
				"[[route]]\nexit_status = \"any\"\nprofile = \"outcome\"\n",
			wantProfile: "outcome",
			wantRoute:   "matched",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
			d := compressTestDB(t)
			restore := chdir(t, t.TempDir())
			defer restore()
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:2"]

[pipeline.outcome]
stages = ["head:1"]
`+tc.routes)

			const sourceOutput = "line one\nline two\nline three\n"
			seedSourceWithFailure(t, d, "abcdefghijklmnop", "zyxwvutsrqponmlk", sourceOutput, tc.isError, tc.exitCode)

			var stdout, stderr bytes.Buffer
			_, err := compressLogic([]string{"abcdefghijklmnop"}, nil, d, &stdout, &stderr)
			if err != nil {
				t.Fatalf("compressLogic: %v", err)
			}
			if strings.Contains(stderr.String(), sourceFailureWarning) {
				t.Errorf("stderr must not contain source-failure warning: %q", stderr.String())
			}
			compressedID := decodeID(t, &stdout)
			comp, err := d.GetCompressed(compressedID)
			if err != nil {
				t.Fatalf("GetCompressed: %v", err)
			}
			wantOutput := "line one\n"
			if tc.wantProfile == "default" {
				wantOutput = "line one\nline two\n"
			}
			if comp.Output != wantOutput {
				t.Errorf("Output = %q, want %q", comp.Output, wantOutput)
			}
			wantSelection := `"profile":"` + tc.wantProfile + `","route":"` + tc.wantRoute + `"`
			if !strings.Contains(comp.Pipeline, wantSelection) {
				t.Errorf("Pipeline = %q, want selection %s", comp.Pipeline, wantSelection)
			}
		})
	}
}

// TestCompressLogic_GateDistinctFromStageError covers the gate/passthrough
// distinction: nil is_error with a stage that errors exercises stage-error
// passthrough, not the failure gate.
func TestCompressLogic_GateDistinctFromStageError(t *testing.T) {
	t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
	d := compressTestDB(t)
	restore := chdir(t, t.TempDir())
	defer restore()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeUserConfig(t, `["head:0", "head:1"]`)

	const sourceOutput = "line one\nline two\n"
	seedSource(t, d, "abcdefghijklmnop", "zyxwvutsrqponmlk", sourceOutput)

	reg := pipeline.NewRegistry()
	stages.RegisterBuiltins(reg)

	var stdout, stderr bytes.Buffer
	_, err := compressLogic([]string{"abcdefghijklmnop"}, reg, d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("compressLogic: %v", err)
	}
	if strings.Contains(stderr.String(), sourceFailureWarning) {
		t.Errorf("failure gate must not fire when is_error is nil: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "head:0") {
		t.Errorf("stderr missing stage-error warning for head:0: %q", stderr.String())
	}
	compressedID := decodeID(t, &stdout)
	comp, err := d.GetCompressed(compressedID)
	if err != nil {
		t.Fatalf("GetCompressed: %v", err)
	}
	want := "line one\n"
	if comp.Output != want {
		t.Errorf("Output = %q, want %q (head:1 ran after head:0 passthrough)", comp.Output, want)
	}
	if !strings.Contains(comp.Pipeline, "head:1") {
		t.Errorf("Pipeline = %q, want head:1 recorded", comp.Pipeline)
	}
}

// seedSourceWithTool inserts a session + tool_call row with a specified
// tool name and input, so route rules matching on tool or command-prefix
// can be exercised. Returns nothing — the test supplies the id.
func seedSourceWithTool(t *testing.T, d *dbpkg.DB, toolCallID, sessionID, tool, input, output string) {
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
		Tool:        tool,
		Input:       input,
		Output:      output,
		ContentHash: dbpkg.ContentHash(output),
		Timestamp:   "2026-06-24T12:00:00Z",
		EventType:   "tool_call",
	}); err != nil {
		t.Fatalf("PersistToolCall: %v", err)
	}
}

// TestCompressLogic_RoutedProfileAppliesRoutedStages covers the core
// routing contract: a tool call matching a route rule gets the routed
// profile's stages, not the default profile's stages.
func TestCompressLogic_RoutedProfileAppliesRoutedStages(t *testing.T) {
	t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
	d := compressTestDB(t)
	restore := chdir(t, t.TempDir())
	defer restore()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:1"]

[pipeline.git-status]
stages = ["head:2"]

[[route]]
tool = "bash"
command_prefix = "git status"
profile = "git-status"
`)

	const sourceOutput = "line one\nline two\nline three\n"
	seedSourceWithTool(t, d, "abcdefghijklmnop", "zyxwvutsrqponmlk", "bash", `{"command":"git status"}`, sourceOutput)

	var stdout, stderr bytes.Buffer
	_, err := compressLogic([]string{"abcdefghijklmnop"}, nil, d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("compressLogic: %v", err)
	}
	compressedID := decodeID(t, &stdout)

	comp, err := d.GetCompressed(compressedID)
	if err != nil {
		t.Fatalf("GetCompressed: %v", err)
	}
	if comp.Output != "line one\nline two\n" {
		t.Errorf("Output = %q, want 2 lines (head:2 from routed git-status profile, not head:1 from default)", comp.Output)
	}
	if !strings.Contains(comp.Pipeline, `"profile":"git-status"`) {
		t.Errorf("Pipeline = %q, want profile git-status", comp.Pipeline)
	}
	if !strings.Contains(comp.Pipeline, "head:2") {
		t.Errorf("Pipeline = %q, want head:2 stage recorded", comp.Pipeline)
	}
}

// TestCompressLogic_NoRouteMatchUsesDefaultProfile covers the fallback
// contract: a tool call matching no route rule gets the default
// profile's stages.
func TestCompressLogic_NoRouteMatchUsesDefaultProfile(t *testing.T) {
	t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
	d := compressTestDB(t)
	restore := chdir(t, t.TempDir())
	defer restore()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:1"]

[pipeline.git-status]
stages = ["head:2"]

[[route]]
tool = "bash"
command_prefix = "git status"
profile = "git-status"
`)

	const sourceOutput = "line one\nline two\nline three\n"
	seedSourceWithTool(t, d, "abcdefghijklmnop", "zyxwvutsrqponmlk", "bash", `{"command":"ls -la"}`, sourceOutput)

	var stdout, stderr bytes.Buffer
	_, err := compressLogic([]string{"abcdefghijklmnop"}, nil, d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("compressLogic: %v", err)
	}
	compressedID := decodeID(t, &stdout)

	comp, err := d.GetCompressed(compressedID)
	if err != nil {
		t.Fatalf("GetCompressed: %v", err)
	}
	if comp.Output != "line one\n" {
		t.Errorf("Output = %q, want 1 line (head:1 from default profile, no route matched)", comp.Output)
	}
	if !strings.Contains(comp.Pipeline, `"profile":"default"`) {
		t.Errorf("Pipeline = %q, want profile default (no route matched)", comp.Pipeline)
	}
	if !strings.Contains(comp.Pipeline, "head:1") {
		t.Errorf("Pipeline = %q, want head:1 stage recorded", comp.Pipeline)
	}
}

// TestCompressLogic_PipelineRecordsSelectedProfile covers the
// auditability contract: a compressed row created from a routed
// profile, when inspected through the existing compressed-row access
// path (GetCompressed), shows which profile was selected in the
// pipeline evidence.
func TestCompressLogic_PipelineRecordsSelectedProfile(t *testing.T) {
	t.Setenv("KTX_DB", t.TempDir()+"/ktx.db")
	d := compressTestDB(t)
	restore := chdir(t, t.TempDir())
	defer restore()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:1"]

[pipeline.logs]
stages = ["head:2"]

[[route]]
tool = "bash"
command_prefix = "tail"
profile = "logs"
`)

	const sourceOutput = "line one\nline two\nline three\n"
	seedSourceWithTool(t, d, "abcdefghijklmnop", "zyxwvutsrqponmlk", "bash", `{"command":"tail -f /var/log/syslog"}`, sourceOutput)

	var stdout, stderr bytes.Buffer
	_, err := compressLogic([]string{"abcdefghijklmnop"}, nil, d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("compressLogic: %v", err)
	}
	compressedID := decodeID(t, &stdout)

	comp, err := d.GetCompressed(compressedID)
	if err != nil {
		t.Fatalf("GetCompressed: %v", err)
	}

	var rec struct {
		Profile string   `json:"profile"`
		Stages  []string `json:"stages"`
	}
	if err := json.Unmarshal([]byte(comp.Pipeline), &rec); err != nil {
		t.Fatalf("Pipeline JSON is not a valid object: %v (value=%q)", err, comp.Pipeline)
	}
	if rec.Profile != "logs" {
		t.Errorf("Pipeline profile = %q, want \"logs\" (routed profile)", rec.Profile)
	}
	if len(rec.Stages) != 1 || rec.Stages[0] != "head:2" {
		t.Errorf("Pipeline stages = %v, want [\"head:2\"] (stages from logs profile)", rec.Stages)
	}
}

// TestStagesForProfile covers the defense-in-depth fallback: when the
// profile name is not in the Profiles map (e.g. no ktx.toml and the
// router returned "default" against an empty map), the function
// falls back to PipelineDefault.
func TestStagesForProfile(t *testing.T) {
	cfg := &config.Config{
		PipelineDefault: []string{"strip-ansi"},
		Profiles: map[string]config.Profile{
			"default":    {Stages: []string{"strip-ansi"}},
			"git-status": {Stages: []string{"head:5"}},
		},
	}

	if got := stagesForProfile(cfg, "git-status"); !slices.Equal(got, []string{"head:5"}) {
		t.Errorf("stagesForProfile(git-status) = %v, want [head:5]", got)
	}
	if got := stagesForProfile(cfg, "default"); !slices.Equal(got, []string{"strip-ansi"}) {
		t.Errorf("stagesForProfile(default) = %v, want [strip-ansi]", got)
	}
	if got := stagesForProfile(cfg, "nonexistent"); !slices.Equal(got, []string{"strip-ansi"}) {
		t.Errorf("stagesForProfile(nonexistent) = %v, want [strip-ansi] (PipelineDefault fallback)", got)
	}
	if got := stagesForProfile(nil, "default"); got != nil {
		t.Errorf("stagesForProfile(nil, default) = %v, want nil", got)
	}
}

// TestCompressPipeline_UnknownProfileFallsBackToDefault covers the
// full pipeline path when no ktx.toml is present: the router returns
// "default" against an empty Profiles map, stagesForProfile falls back
// to PipelineDefault (nil), and the pipeline runs passthrough with the
// profile recorded as "default".
func TestCompressPipeline_UnknownProfileFallsBackToDefault(t *testing.T) {
	restore := noConfigEnv(t)
	defer restore()

	source := &dbpkg.ToolCall{
		ID:        "fallback01",
		Tool:      "bash",
		Output:    "line one\nline two\n",
		Timestamp: "2026-06-24T12:00:00Z",
	}

	var stderr bytes.Buffer
	out, pj, _, transformed, err := compressPipeline(source, nil, &stderr, false)
	if err != nil {
		t.Fatalf("compressPipeline: %v", err)
	}
	if transformed {
		t.Error("transformed = true; want false (passthrough, no stages)")
	}
	if out != source.Output {
		t.Errorf("output = %q; want %q (source unchanged)", out, source.Output)
	}
	if !strings.Contains(pj, `"profile":"default"`) {
		t.Errorf("pipelineJSON = %q; want profile \"default\" (fallback)", pj)
	}
	if !strings.Contains(pj, `"stages":[]`) {
		t.Errorf("pipelineJSON = %q; want empty stages (passthrough)", pj)
	}
}
