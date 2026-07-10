package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/config"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/pipeline"
)

func hookTestDB(t *testing.T) *dbpkg.DB {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	path := os.Getenv("KTX_DB")
	if path == "" {
		path = t.TempDir() + "/ktx.db"
		t.Setenv("KTX_DB", path)
	}
	d, err := dbpkg.Open(path)
	if err != nil {
		t.Fatalf("dbpkg.Open(%q): %v", path, err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func hookChdirTemp(t *testing.T) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

func hookWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// hookWriteUserConfig writes content to the user-level config path
// ($XDG_CONFIG_HOME/ktx/ktx.toml). Requires XDG_CONFIG_HOME to be set
// (tests set it via t.Setenv before calling this helper).
func hookWriteUserConfig(t *testing.T, content string) {
	t.Helper()
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		t.Fatal("XDG_CONFIG_HOME not set")
	}
	hookWriteFile(t, filepath.Join(xdg, "ktx", "ktx.toml"), content)
}

func TestHook_Identity(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	payload := `{"id":"hooktest01","tool":"bash","input":"echo hello","output":"hello\nline 2\nline 3\nline 4\nline 5\n","content_hash":"","timestamp":"2026-07-01T12:00:00Z"}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	var env hookEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("parse envelope: %v\nstdout=%s", err, stdout.String())
	}
	if env.Output != "hello\nline 2\nline 3\nline 4\nline 5\n" {
		t.Errorf("output = %q, want original (passthrough)", env.Output)
	}
	if env.CompressedID != nil {
		t.Errorf("compressed_id = %v, want null (no pipeline)", env.CompressedID)
	}
	if env.SourceID != "hooktest01" {
		t.Errorf("source_id = %q, want hooktest01", env.SourceID)
	}

	source, err := d.GetToolCall("hooktest01")
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if source.Output != "hello\nline 2\nline 3\nline 4\nline 5\n" {
		t.Errorf("persisted output = %q, want original", source.Output)
	}
}

func TestHook_WithPipeline(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// Footer off: short fixtures would trip the footer anti-worsening gate.
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:3"]
recover_footer = false
`)

	payload := `{"id":"hookpipe01","tool":"bash","input":"echo hello","output":"line 1\nline 2\nline 3\nline 4\nline 5\n","content_hash":"","timestamp":"2026-07-01T12:00:00Z"}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	var env hookEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("parse envelope: %v\nstdout=%s", err, stdout.String())
	}
	if !strings.HasPrefix(env.Output, "line 1\nline 2\nline 3") {
		t.Errorf("output = %q, want first 3 lines", env.Output)
	}
	if strings.Contains(env.Output, "line 4") {
		t.Errorf("output should NOT contain line 4: %q", env.Output)
	}
	if env.CompressedID == nil {
		t.Fatal("compressed_id is null; want a compressed row id")
	}

	source, _ := d.GetToolCall("hookpipe01")
	if source.Output != "line 1\nline 2\nline 3\nline 4\nline 5\n" {
		t.Errorf("persisted original = %q, want full 5 lines", source.Output)
	}

}

func TestHook_FailureGate(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:3"]
`)

	payload := `{"id":"hookerr001","tool":"bash","input":"false","output":"Error: failed","content_hash":"","timestamp":"2026-07-01T12:00:00Z","is_error":true}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	if !strings.Contains(stderr.String(), "failure") {
		t.Errorf("stderr should contain failure warning: %q", stderr.String())
	}

	var env hookEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("parse envelope: %v\nstdout=%s", err, stdout.String())
	}
	if env.Output != "Error: failed" {
		t.Errorf("output = %q, want original (failure gate skips compression)", env.Output)
	}
	if env.CompressedID != nil {
		t.Errorf("compressed_id = %v, want null (failure gate, no compressed row)", env.CompressedID)
	}
}

func TestHook_MalformedJSON(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader("not json"), d, &stdout, &stderr)
	if err == nil {
		t.Fatal("hookLogic succeeded; want usage error")
	}
	ktxErr, ok := err.(*Error)
	if !ok || ktxErr.Code != CodeUsage {
		t.Fatalf("error = %v, want usage error", err)
	}
}

func TestHook_MissingFields(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	for _, tc := range []struct {
		name    string
		payload string
		want    string
	}{
		{"missing id", `{"tool":"bash","output":"hi","timestamp":"2026-07-01T12:00:00Z"}`, "id"},
		{"missing tool", `{"id":"abc","output":"hi","timestamp":"2026-07-01T12:00:00Z"}`, "tool"},
		{"missing timestamp", `{"id":"abc","tool":"bash","output":"hi"}`, "timestamp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			_, err := hookLogic(strings.NewReader(tc.payload), d, &stdout, &stderr)
			if err == nil {
				t.Fatal("hookLogic succeeded; want usage error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestHook_Reversibility(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// Footer off: short fixtures would trip the footer anti-worsening gate.
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:2"]
recover_footer = false
`)

	payload := `{"id":"hookrev001","tool":"bash","input":"echo","output":"line 1\nline 2\nline 3\nline 4\n","content_hash":"","timestamp":"2026-07-01T12:00:00Z"}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	var env hookEnvelope
	json.Unmarshal(stdout.Bytes(), &env)
	if env.CompressedID == nil {
		t.Fatal("compressed_id is null")
	}

	source, _ := d.GetToolCall("hookrev001")
	if source.Output != "line 1\nline 2\nline 3\nline 4\n" {
		t.Errorf("original = %q, want full 4 lines", source.Output)
	}
}

func TestHook_NoInput(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(""), d, &stdout, &stderr)
	if err == nil {
		t.Fatal("hookLogic succeeded; want usage error for empty stdin")
	}
}

func TestCompressPipeline_Timeout(t *testing.T) {
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["slow"]
`)

	source := &dbpkg.ToolCall{
		ID:        "timeout01",
		Tool:      "bash",
		Output:    "line 1\nline 2\nline 3\n",
		Timestamp: "2026-07-01T12:00:00Z",
	}

	reg := pipeline.NewRegistry()
	reg.Register("slow", pipeline.StageFunc(func(input []byte) ([]byte, error) {
		time.Sleep(500 * time.Millisecond)
		return input, nil
	}))

	var stderr bytes.Buffer
	out, _, transformed, err := compressPipeline(source, reg, &stderr, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("compressPipeline: %v", err)
	}
	if transformed {
		t.Error("transformed = true; want false (timeout returns source unchanged)")
	}
	if out != source.Output {
		t.Errorf("output = %q; want %q (source unchanged on timeout)", out, source.Output)
	}
	if !strings.Contains(stderr.String(), "timeout") {
		t.Errorf("stderr should contain timeout warning: %q", stderr.String())
	}
}

func TestCompressPipeline_TimeoutNotHit(t *testing.T) {
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["fast"]
`)

	source := &dbpkg.ToolCall{
		ID:        "timeout02",
		Tool:      "bash",
		Output:    "line 1\nline 2\nline 3\nline 4\nline 5\n",
		Timestamp: "2026-07-01T12:00:00Z",
	}

	reg := pipeline.NewRegistry()
	reg.Register("fast", pipeline.StageFunc(func(input []byte) ([]byte, error) {
		return []byte("line 1\nline 2\nline 3\n"), nil
	}))

	var stderr bytes.Buffer
	out, pj, transformed, err := compressPipeline(source, reg, &stderr, 5*time.Second)
	if err != nil {
		t.Fatalf("compressPipeline: %v", err)
	}
	if !transformed {
		t.Error("transformed = false; want true (pipeline should have run)")
	}
	if out != "line 1\nline 2\nline 3\n" {
		t.Errorf("output = %q; want 3 lines", out)
	}
	if pj == "[]" {
		t.Error("pipelineJSON = []; want non-empty (stage ran)")
	}
	if strings.Contains(stderr.String(), "timeout") {
		t.Errorf("stderr should NOT contain timeout: %q", stderr.String())
	}
}

// TestHook_ConfigurableTimeout covers: a ktx.toml with
// [pipeline.default] timeout = "5ms" makes the config surface a 5ms
// PipelineTimeout, which compressPipeline honours (a slow stage racing
// a 5ms timer fires the CONFIGURED timeout, not the legacy 2s default).
// hookLogic forwards cfg.PipelineTimeout to compressPipeline, so proving
// config→timeout→fire exercises the configurable-timeout contract
// end-to-end minus the envelope write.
func TestHook_ConfigurableTimeout(t *testing.T) {
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["slow"]
timeout = "5ms"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.PipelineTimeout != 5*time.Millisecond {
		t.Fatalf("PipelineTimeout = %v, want 5ms (configured, not the 2s default)", cfg.PipelineTimeout)
	}

	source := &dbpkg.ToolCall{
		ID:        "hookcfg01",
		Tool:      "bash",
		Output:    "line 1\nline 2\nline 3\n",
		Timestamp: "2026-07-01T12:00:00Z",
	}
	reg := pipeline.NewRegistry()
	reg.Register("slow", pipeline.StageFunc(func(input []byte) ([]byte, error) {
		time.Sleep(200 * time.Millisecond)
		return []byte("shrunk"), nil
	}))

	var stderr bytes.Buffer
	out, _, transformed, err := compressPipeline(source, reg, &stderr, cfg.PipelineTimeout)
	if err != nil {
		t.Fatalf("compressPipeline: %v", err)
	}
	if transformed {
		t.Error("transformed = true; want false (configured 5ms timeout fires before the 200ms stage)")
	}
	if out != source.Output {
		t.Errorf("output = %q; want %q (source unchanged on configured-timeout fire)", out, source.Output)
	}
	if !strings.Contains(stderr.String(), "timeout") {
		t.Errorf("stderr should contain timeout warning: %q", stderr.String())
	}
}

// TestHook_RealSessionEndToEnd covers the real-session path through the
// actual code the OpenCode plugin's tool.execute.after hook calls
// (hookLogic → DB → decompress). A live interactive OpenCode agent is
// not available in this build environment, but hookLogic IS the plugin's
// entry point, so this exercises the identical in-flight compression
// path a real session would take. Replay-ratio coverage lives in
// bench_test.go (TestRunBenchReplay_RealWorldPipeline).
func TestHook_RealSessionEndToEnd(t *testing.T) {
	// The real-world pipeline: strip ANSI, drop
	// debug noise, keep head+tail (errors at the bottom survive),
	// line-safe token truncation. recover_footer off so the envelope
	// output stays content-only for ratio/decompress assertions.
	const pipeline = `[pipeline.default]
stages = ["strip-ansi", "grep-v:^debug:", "keep-ends:50:20", "token-budget:2000"]
recover_footer = false
`
	// buildVerboseOutput constructs a build-log-style output: ANSI
	// codes, 100 debug lines (stripped by grep-v), compile lines, and a
	// fatal error at the bottom. Well over the 2000-token budget so
	// every stage does real work.
	buildVerboseOutput := func() string {
		var sb strings.Builder
		sb.WriteString("\x1b[31merror\x1b[0m: build started\n")
		for i := 0; i < 100; i++ {
			sb.WriteString("debug: tracing step ")
			sb.WriteString(itoa(i))
			sb.WriteByte('\n')
		}
		for i := 0; i < 40; i++ {
			sb.WriteString("compile: unit ")
			sb.WriteString(itoa(i))
			sb.WriteString(" ok\n")
		}
		sb.WriteString("\x1b[31mfatal\x1b[0m: linker error at end\n")
		return sb.String()
	}

	t.Run("verbose output → agent sees compressed; original in DB; decompress verbatim", func(t *testing.T) {
		d := hookTestDB(t)
		hookChdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		hookWriteUserConfig(t, pipeline)

		original := buildVerboseOutput()
		tc := dbpkg.ToolCall{
			ID:        "e2eac1",
			Tool:      "bash",
			Input:     "build",
			Output:    original,
			Timestamp: "2026-07-01T12:00:00Z",
		}
		payload, err := json.Marshal(tc)
		if err != nil {
			t.Fatalf("marshal tool_call: %v", err)
		}

		var stdout, stderr bytes.Buffer
		if _, err := hookLogic(strings.NewReader(string(payload)), d, &stdout, &stderr); err != nil {
			t.Fatalf("hookLogic: %v (stderr=%q)", err, stderr.String())
		}

		var env hookEnvelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("parse envelope: %v\nstdout=%s", err, stdout.String())
		}

		// The agent saw compressed output (fewer bytes than original).
		if len(env.Output) >= len(original) {
			t.Errorf("compressed output (%d) not smaller than original (%d); the pipeline should shrink a verbose build log",
				len(env.Output), len(original))
		}
		// A compressed row was created (the pipeline helped).
		if env.CompressedID == nil {
			t.Fatal("compressed_id = null; want a compressed row id (pipeline transformed)")
		}
		// No ANSI codes leaked into the agent's context.
		if strings.Contains(env.Output, "\x1b[") {
			t.Errorf("compressed output still contains ANSI escape codes (strip-ansi should have removed them)")
		}
		// No debug noise leaked (grep-v).
		if strings.Contains(env.Output, "debug:") {
			t.Errorf("compressed output still contains debug: lines (grep-v should have stripped them)")
		}
		// The bottom-of-log error survived (keep-ends preserves the tail).
		if !strings.Contains(env.Output, "linker error") {
			t.Errorf("compressed output lost the bottom-of-log error (keep-ends should preserve the tail)")
		}

		// The original is in the DB verbatim (reversibility).
		comp, err := d.GetCompressed(*env.CompressedID)
		if err != nil {
			t.Fatalf("GetCompressed(%q): %v", *env.CompressedID, err)
		}
		sourceOutput, err := d.GetSourceOutput(comp.SourceID)
		if err != nil {
			t.Fatalf("GetSourceOutput(%q): %v", comp.SourceID, err)
		}
		if sourceOutput != original {
			t.Errorf("decompress(source) = %q, want original verbatim (reversibility)", sourceOutput)
		}
		// The persisted source equals the input (append-only).
		source, err := d.GetToolCall("e2eac1")
		if err != nil {
			t.Fatalf("GetToolCall: %v", err)
		}
		if source.Output != original {
			t.Errorf("persisted source = %q, want original", source.Output)
		}
	})

	t.Run("short input → anti-worsening gate returns original, compressed_id=null", func(t *testing.T) {
		d := hookTestDB(t)
		hookChdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		hookWriteUserConfig(t, pipeline)

		// A short input: the pipeline cannot shrink it (strip-ansi is a
		// no-op, grep-v keeps all, keep-ends keeps all, token-budget is
		// under budget). The anti-worsening gate fires (result not
		// smaller than input) → no compressed row.
		short := "all good: one line, no noise\n"
		tc := dbpkg.ToolCall{
			ID:        "e2eac3",
			Tool:      "bash",
			Input:     "echo",
			Output:    short,
			Timestamp: "2026-07-01T12:00:00Z",
		}
		payload, _ := json.Marshal(tc)

		var stdout, stderr bytes.Buffer
		if _, err := hookLogic(strings.NewReader(string(payload)), d, &stdout, &stderr); err != nil {
			t.Fatalf("hookLogic: %v (stderr=%q)", err, stderr.String())
		}
		var env hookEnvelope
		json.Unmarshal(stdout.Bytes(), &env)
		if env.Output != short {
			t.Errorf("output = %q, want %q (anti-worsening gate returns original on short input)", env.Output, short)
		}
		if env.CompressedID != nil {
			t.Errorf("compressed_id = %v, want null (gate fired; no compressed row)", env.CompressedID)
		}
	})

	t.Run("is_error=true → failure gate returns original unchanged", func(t *testing.T) {
		d := hookTestDB(t)
		hookChdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		hookWriteUserConfig(t, pipeline)

		errOutput := "\x1b[31mError: build failed with 47 errors\x1b[0m\nsee log for details\n"
		isErr := true
		tc := dbpkg.ToolCall{
			ID:        "e2eac4",
			Tool:      "bash",
			Input:     "make",
			Output:    errOutput,
			Timestamp: "2026-07-01T12:00:00Z",
			IsError:   &isErr,
		}
		payload, _ := json.Marshal(tc)

		var stdout, stderr bytes.Buffer
		if _, err := hookLogic(strings.NewReader(string(payload)), d, &stdout, &stderr); err != nil {
			t.Fatalf("hookLogic: %v (stderr=%q)", err, stderr.String())
		}
		var env hookEnvelope
		json.Unmarshal(stdout.Bytes(), &env)
		// The agent sees the ORIGINAL error output unchanged
		// (the failure gate skips compression so the full error reaches the
		// agent). Note: the original includes the ANSI codes — they are
		// NOT stripped because compression did not run.
		if env.Output != errOutput {
			t.Errorf("output = %q, want %q (failure gate: original error unchanged)", env.Output, errOutput)
		}
		if env.CompressedID != nil {
			t.Errorf("compressed_id = %v, want null (failure gate; no compression attempted)", env.CompressedID)
		}
		if !strings.Contains(stderr.String(), "failure") {
			t.Errorf("stderr should note the failure-skipped warning: %q", stderr.String())
		}
	})
}

func TestHook_DefaultTimeoutWhenOmitted(t *testing.T) {
	t.Run("ktx.toml without timeout → 2s default", func(t *testing.T) {
		hookChdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:3"]
`)

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("config.Load: %v", err)
		}
		if cfg.PipelineTimeout != config.DefaultPipelineTimeout {
			t.Errorf("PipelineTimeout = %v, want %v (default 2s when omitted)", cfg.PipelineTimeout, config.DefaultPipelineTimeout)
		}
		if config.DefaultPipelineTimeout != 2*time.Second {
			t.Errorf("DefaultPipelineTimeout = %v, want 2s", config.DefaultPipelineTimeout)
		}
	})

	t.Run("no ktx.toml → 2s default (passthrough path)", func(t *testing.T) {
		hookChdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("config.Load: %v", err)
		}
		if cfg.PipelineTimeout != config.DefaultPipelineTimeout {
			t.Errorf("PipelineTimeout = %v, want %v (default 2s on passthrough path)", cfg.PipelineTimeout, config.DefaultPipelineTimeout)
		}
	})
}
