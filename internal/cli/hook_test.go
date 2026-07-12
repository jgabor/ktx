package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/config"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/pipeline"
)

var idPattern = regexp.MustCompile(`^[a-z]{16}$`)

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
		{"missing tool", `{"id":"abc","output":"hi","timestamp":"2026-07-01T12:00:00Z"}`, "tool"},
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

func TestHook_GeneratesIDWhenEmpty(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	payload := `{"id":"","tool":"bash","input":"echo","output":"hello\n","content_hash":"","timestamp":"2026-07-01T12:00:00Z"}`

	var stdout, stderr bytes.Buffer
	result, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}
	if result != nil {
		t.Errorf("result = %v, want nil", result)
	}

	var env hookEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("parse envelope: %v\nstdout=%s", err, stdout.String())
	}
	if !idPattern.MatchString(env.SourceID) {
		t.Errorf("source_id = %q, want 16-char lowercase alpha id", env.SourceID)
	}
	if env.SourceID == "" {
		t.Fatal("source_id is empty; want generated id")
	}

	persisted, err := d.GetToolCall(env.SourceID)
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if persisted.Output != "hello\n" {
		t.Errorf("persisted output = %q, want %q", persisted.Output, "hello\n")
	}
}

func TestHook_GeneratesTimestampWhenEmpty(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	payload := `{"id":"hookts01","tool":"bash","input":"echo","output":"hello\n","content_hash":"","timestamp":""}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	var env hookEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("parse envelope: %v\nstdout=%s", err, stdout.String())
	}
	if env.SourceID != "hookts01" {
		t.Errorf("source_id = %q, want hookts01 (caller-supplied)", env.SourceID)
	}

	persisted, err := d.GetToolCall("hookts01")
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if persisted.Timestamp == "" {
		t.Fatal("persisted timestamp is empty; want generated RFC3339Nano timestamp")
	}
	if _, err := time.Parse(time.RFC3339Nano, persisted.Timestamp); err != nil {
		t.Errorf("persisted timestamp = %q, not RFC3339Nano: %v", persisted.Timestamp, err)
	}
}

func TestHook_PopulatedFieldsUseCallerValues(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	payload := `{"id":"callerID1","tool":"bash","input":"echo","output":"hello\n","content_hash":"","timestamp":"2026-07-01T12:00:00Z"}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	var env hookEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("parse envelope: %v\nstdout=%s", err, stdout.String())
	}
	if env.SourceID != "callerID1" {
		t.Errorf("source_id = %q, want callerID1 (caller-supplied)", env.SourceID)
	}

	persisted, err := d.GetToolCall("callerID1")
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if persisted.Timestamp != "2026-07-01T12:00:00Z" {
		t.Errorf("persisted timestamp = %q, want caller-supplied 2026-07-01T12:00:00Z", persisted.Timestamp)
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
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
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
timeout = "10ms"
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
	out, _, _, transformed, err := compressPipeline(source, reg, &stderr, true)
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
timeout = "5s"
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
	out, pj, _, transformed, err := compressPipeline(source, reg, &stderr, true)
	if err != nil {
		t.Fatalf("compressPipeline: %v", err)
	}
	if !transformed {
		t.Error("transformed = false; want true (pipeline should have run)")
	}
	if out != "line 1\nline 2\nline 3\n" {
		t.Errorf("output = %q; want 3 lines", out)
	}
	if strings.Contains(pj, `"stages":[]`) {
		t.Error("pipelineJSON has empty stages; want non-empty (stage ran)")
	}
	if strings.Contains(stderr.String(), "timeout") {
		t.Errorf("stderr should NOT contain timeout: %q", stderr.String())
	}
}

// The next four tests cover per-profile resolution of the pipeline
// timeout and recover_footer: a selected profile's own value is used,
// and when the profile omits the field the default profile's value is
// used. Each test routes a bash tool call to a named profile so the
// router selects it inside compressPipeline / hookLogic.

// TestCompressPipeline_PerProfileTimeout_ProfileOwnValue covers a
// selected profile with its own timeout: the resolved deadline is that
// profile's timeout (5ms), which fires against a 200ms slow stage. The
// default profile sets no timeout (effective 2s), which would not fire —
// so a fired timeout proves the routed profile's own value was used.
func TestCompressPipeline_PerProfileTimeout_ProfileOwnValue(t *testing.T) {
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["slow"]

[pipeline.fast]
stages = ["slow"]
timeout = "5ms"

[[route]]
tool = "bash"
profile = "fast"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if got := config.ResolveTimeout(cfg, "fast"); got != 5*time.Millisecond {
		t.Fatalf("ResolveTimeout(fast) = %v, want 5ms (the fast profile's own timeout)", got)
	}
	// default sets no timeout; its effective deadline is the 2s const,
	// which would NOT fire against the 200ms slow stage below.
	if got := config.ResolveTimeout(cfg, "default"); got != config.DefaultPipelineTimeout {
		t.Fatalf("ResolveTimeout(default) = %v, want %v (default leaves timeout unset)", got, config.DefaultPipelineTimeout)
	}

	source := &dbpkg.ToolCall{
		ID:        "pp-timeout-own",
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
	out, _, profileName, transformed, err := compressPipeline(source, reg, &stderr, true)
	if err != nil {
		t.Fatalf("compressPipeline: %v", err)
	}
	if profileName != "fast" {
		t.Errorf("profileName = %q, want fast (routed by tool match)", profileName)
	}
	if transformed {
		t.Error("transformed = true; want false (the fast profile's 5ms timeout fires before the 200ms stage)")
	}
	if out != source.Output {
		t.Errorf("output = %q; want %q (source unchanged on timeout)", out, source.Output)
	}
	if !strings.Contains(stderr.String(), "timeout") {
		t.Errorf("stderr should contain the timeout warning: %q", stderr.String())
	}
}

// TestCompressPipeline_PerProfileTimeout_InheritsDefault covers a selected
// profile WITHOUT its own timeout: the resolved deadline is the default
// profile's timeout (5ms), which fires against a slow stage on the routed
// profile. The routed profile sets no timeout, so the fired 5ms deadline
// must have come from the default profile.
func TestCompressPipeline_PerProfileTimeout_InheritsDefault(t *testing.T) {
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["slow"]
timeout = "5ms"

[pipeline.inherit]
stages = ["slow"]

[[route]]
tool = "bash"
profile = "inherit"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	// inherit sets no timeout → resolves to the default profile's 5ms.
	if got := config.ResolveTimeout(cfg, "inherit"); got != 5*time.Millisecond {
		t.Fatalf("ResolveTimeout(inherit) = %v, want 5ms (inherited from the default profile)", got)
	}

	source := &dbpkg.ToolCall{
		ID:        "pp-timeout-inherit",
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
	out, _, profileName, transformed, err := compressPipeline(source, reg, &stderr, true)
	if err != nil {
		t.Fatalf("compressPipeline: %v", err)
	}
	if profileName != "inherit" {
		t.Errorf("profileName = %q, want inherit (routed by tool match)", profileName)
	}
	if transformed {
		t.Error("transformed = true; want false (the default profile's 5ms timeout fires before the 200ms stage)")
	}
	if out != source.Output {
		t.Errorf("output = %q; want %q (source unchanged on timeout)", out, source.Output)
	}
	if !strings.Contains(stderr.String(), "timeout") {
		t.Errorf("stderr should contain the timeout warning: %q", stderr.String())
	}
}

// TestHook_PerProfileRecoverFooter_ProfileOwnValue covers a selected
// profile with its own recover_footer setting: that profile's value
// overrides the default's. The default profile enables the footer, but a
// routed profile with recover_footer = false suppresses it (the
// agent-visible output is the compressed content with no recovery cue).
func TestHook_PerProfileRecoverFooter_ProfileOwnValue(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:3"]
recover_footer = true

[pipeline.silent]
stages = ["head:3"]
recover_footer = false

[[route]]
tool = "bash"
profile = "silent"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if got := config.ResolveRecoverFooter(cfg, "silent"); got != false {
		t.Fatalf("ResolveRecoverFooter(silent) = %v, want false (the silent profile's own value)", got)
	}
	if got := config.ResolveRecoverFooter(cfg, "default"); got != true {
		t.Fatalf("ResolveRecoverFooter(default) = %v, want true (the default profile enables the footer)", got)
	}

	var sb strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&sb, "line %d content here for padding\n", i)
	}
	payload := codexPayload(t, "pp-foot-own", "bash", sb.String())

	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr); err != nil {
		t.Fatalf("hookLogic: %v (stderr=%q)", err, stderr.String())
	}

	var out codexHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("parse codex output: %v\nstdout=%s", err, stdout.String())
	}
	if out.Decision != "block" {
		t.Errorf("decision = %q, want block (the pipeline transformed the output)", out.Decision)
	}
	if !strings.HasPrefix(out.Reason, "line 0") {
		t.Errorf("reason = %q, want the compressed content starting with line 0", out.Reason)
	}
	footerMarker := "\n[ktx]"
	if strings.Contains(out.Reason, footerMarker) {
		t.Errorf("reason should NOT contain a recovery footer (the silent profile sets recover_footer = false): %q", out.Reason)
	}
}

// TestHook_PerProfileRecoverFooter_InheritsDefault covers a selected
// profile WITHOUT its own recover_footer setting: the resolved value is
// the default profile's. The default profile enables the footer, and a
// routed profile that sets no recover_footer inherits that (the
// agent-visible output carries the recovery cue).
func TestHook_PerProfileRecoverFooter_InheritsDefault(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:3"]
recover_footer = true

[pipeline.inherit]
stages = ["head:3"]

[[route]]
tool = "bash"
profile = "inherit"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	// inherit sets no recover_footer → resolves to the default profile's true.
	if got := config.ResolveRecoverFooter(cfg, "inherit"); got != true {
		t.Fatalf("ResolveRecoverFooter(inherit) = %v, want true (inherited from the default profile)", got)
	}

	var sb strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&sb, "line %d content here for padding\n", i)
	}
	payload := codexPayload(t, "pp-foot-inherit", "bash", sb.String())

	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr); err != nil {
		t.Fatalf("hookLogic: %v (stderr=%q)", err, stderr.String())
	}

	var out codexHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("parse codex output: %v\nstdout=%s", err, stdout.String())
	}
	if out.Decision != "block" {
		t.Errorf("decision = %q, want block (the pipeline transformed the output)", out.Decision)
	}
	footerMarker := "\n[ktx]"
	if !strings.Contains(out.Reason, footerMarker) {
		t.Errorf("reason should contain the recovery footer (the inherit profile inherits the default's recover_footer = true): %q", out.Reason)
	}
	if idx := strings.Index(out.Reason, footerMarker); idx <= 0 || !strings.HasPrefix(out.Reason, "line 0") {
		t.Errorf("footer must follow the compressed content, not precede it: %q", out.Reason)
	}
}

// TestHook_ConfigurableTimeout covers: a ktx.toml with
// [pipeline.default] timeout = "5ms" makes ResolveTimeout return 5ms for
// the default profile, which compressPipeline honours when on the
// critical path (a slow stage racing a 5ms timer fires the CONFIGURED
// timeout, not the 2s const default). compressPipeline resolves the
// per-profile deadline internally, so proving config→timeout→fire
// exercises the configurable-timeout contract end-to-end minus the
// envelope write.
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
	if got := config.ResolveTimeout(cfg, "default"); got != 5*time.Millisecond {
		t.Fatalf("ResolveTimeout(default) = %v, want 5ms (configured, not the 2s default)", got)
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
	out, _, _, transformed, err := compressPipeline(source, reg, &stderr, true)
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
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
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
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
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

func TestHook_OpenCode_RawCallbackExtracted(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// runtime="opencode" with raw callback input (tool + args object)
	// and raw callback output (object with output field).
	payload := `{"runtime":"opencode","id":"ocext001","timestamp":"2026-07-11T12:00:00Z","input":{"tool":"bash","args":{"command":"echo hello"}},"output":{"output":"hello\nline 2\nline 3\nline 4\nline 5\n"}}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	var env hookEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("parse envelope: %v\nstdout=%s", err, stdout.String())
	}
	if env.SourceID != "ocext001" {
		t.Errorf("source_id = %q, want ocext001", env.SourceID)
	}

	persisted, err := d.GetToolCall("ocext001")
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if persisted.Tool != "bash" {
		t.Errorf("persisted tool = %q, want bash", persisted.Tool)
	}
	if !strings.Contains(persisted.Input, "echo hello") {
		t.Errorf("persisted input = %q, want stringified args containing echo hello", persisted.Input)
	}
	if persisted.Output != "hello\nline 2\nline 3\nline 4\nline 5\n" {
		t.Errorf("persisted output = %q, want extracted output field", persisted.Output)
	}
	if persisted.Runtime == nil || *persisted.Runtime != "opencode" {
		t.Errorf("persisted runtime = %v, want &opencode", persisted.Runtime)
	}
}

func TestHook_OpenCode_BareStringOutput(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// runtime="opencode" with output as a bare string (not an object).
	payload := `{"runtime":"opencode","id":"ocext002","timestamp":"2026-07-11T12:00:00Z","input":{"tool":"bash","args":{"command":"echo hi"}},"output":"bare string output"}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	persisted, err := d.GetToolCall("ocext002")
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if persisted.Output != "bare string output" {
		t.Errorf("persisted output = %q, want bare string output", persisted.Output)
	}
}

func TestHook_OpenCode_ObjectWithoutOutputField(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// runtime="opencode" with output as an object without an output
	// field. The persisted output is the stringified object (fallback).
	payload := `{"runtime":"opencode","id":"ocext003","timestamp":"2026-07-11T12:00:00Z","input":{"tool":"bash","args":{}},"output":{"foo":"bar"}}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	persisted, err := d.GetToolCall("ocext003")
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if !strings.Contains(persisted.Output, "foo") || !strings.Contains(persisted.Output, "bar") {
		t.Errorf("persisted output = %q, want stringified object containing foo and bar", persisted.Output)
	}
}

func TestHook_OpenCode_NoToolName(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// runtime="opencode" with input that has no tool name. The
	// persisted tool is "unknown" (fallback).
	payload := `{"runtime":"opencode","id":"ocext004","timestamp":"2026-07-11T12:00:00Z","input":{"args":{"command":"echo"}},"output":"hello"}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	persisted, err := d.GetToolCall("ocext004")
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if persisted.Tool != "unknown" {
		t.Errorf("persisted tool = %q, want unknown (fallback)", persisted.Tool)
	}
}

func TestHook_NoRuntime_FlatDeserialisation(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// No runtime field: flat tool_call shape, backward compatible.
	payload := `{"id":"ocext005","tool":"bash","input":"echo hello","output":"hello\n","content_hash":"","timestamp":"2026-07-11T12:00:00Z"}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	persisted, err := d.GetToolCall("ocext005")
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if persisted.Tool != "bash" {
		t.Errorf("persisted tool = %q, want bash (flat deserialisation)", persisted.Tool)
	}
	if persisted.Input != "echo hello" {
		t.Errorf("persisted input = %q, want echo hello (flat string, not stringified args)", persisted.Input)
	}
	if persisted.Output != "hello\n" {
		t.Errorf("persisted output = %q, want hello\\n", persisted.Output)
	}
	if persisted.Runtime != nil {
		t.Errorf("persisted runtime = %v, want nil (no runtime in flat payload)", persisted.Runtime)
	}
}

func TestHook_UnrecognisedRuntime_FlatDeserialisation(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Unrecognised runtime value: falls back to flat tool_call
	// deserialisation (backward compatible).
	payload := `{"runtime":"cli-tool","id":"ocext006","tool":"bash","input":"echo hello","output":"hello\n","timestamp":"2026-07-11T12:00:00Z"}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	persisted, err := d.GetToolCall("ocext006")
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if persisted.Tool != "bash" {
		t.Errorf("persisted tool = %q, want bash (flat deserialisation)", persisted.Tool)
	}
	if persisted.Input != "echo hello" {
		t.Errorf("persisted input = %q, want echo hello (flat string, not stringified args)", persisted.Input)
	}
	if persisted.Output != "hello\n" {
		t.Errorf("persisted output = %q, want hello\\n", persisted.Output)
	}
	if persisted.Runtime == nil || *persisted.Runtime != "cli-tool" {
		t.Errorf("persisted runtime = %v, want &cli-tool (preserved from flat payload)", persisted.Runtime)
	}
}

func TestHook_Codex_ObjectWithAggregatedOutput(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Codex PostToolUse payload with object tool_response containing
	// aggregated_output. Extracts correctly, runtime="codex".
	payload := `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"ls -la"},"tool_response":{"aggregated_output":"file1\nfile2","exit_code":0},"tool_use_id":"call_abc123","session_id":"some-codex-uuid","cwd":"/path/to/project","model":"gpt-5"}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	var out codexHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("parse codex output: %v\nstdout=%s", err, stdout.String())
	}
	if out.Decision != "" || out.Reason != "" {
		t.Errorf("codex output = {decision:%q reason:%q}, want {} (passthrough, no pipeline)", out.Decision, out.Reason)
	}

	persisted, err := d.GetToolCall("call_abc123")
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if persisted.Tool != "Bash" {
		t.Errorf("persisted tool = %q, want Bash", persisted.Tool)
	}
	if !strings.Contains(persisted.Input, "ls -la") {
		t.Errorf("persisted input = %q, want stringified tool_input containing ls -la", persisted.Input)
	}
	if persisted.Output != "file1\nfile2" {
		t.Errorf("persisted output = %q, want file1\\nfile2 (aggregated_output)", persisted.Output)
	}
	if persisted.Runtime == nil || *persisted.Runtime != "codex" {
		t.Errorf("persisted runtime = %v, want &codex", persisted.Runtime)
	}
	if persisted.SessionID == "some-codex-uuid" {
		t.Errorf("persisted session_id = %q, should not use Codex session_id (ppid resolution instead)", persisted.SessionID)
	}
}

func TestHook_Codex_BareStringOutput(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Codex PostToolUse with bare string tool_response. Output
	// is the bare string.
	payload := `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"echo hi"},"tool_response":"bare string output","tool_use_id":"codex002"}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	persisted, err := d.GetToolCall("codex002")
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if persisted.Output != "bare string output" {
		t.Errorf("persisted output = %q, want bare string output", persisted.Output)
	}
}

func TestHook_Codex_ObjectWithoutRecognisedField(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Codex PostToolUse with object tool_response without
	// aggregated_output, output, or text. Stringified fallback.
	payload := `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{},"tool_response":{"foo":"bar"},"tool_use_id":"codex003"}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	persisted, err := d.GetToolCall("codex003")
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if !strings.Contains(persisted.Output, "foo") || !strings.Contains(persisted.Output, "bar") {
		t.Errorf("persisted output = %q, want stringified object containing foo and bar", persisted.Output)
	}
}

func TestHook_Codex_NoToolName(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Codex PostToolUse missing tool_name. Tool="unknown".
	payload := `{"hook_event_name":"PostToolUse","tool_input":{"command":"echo"},"tool_response":"hello","tool_use_id":"codex004"}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	persisted, err := d.GetToolCall("codex004")
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if persisted.Tool != "unknown" {
		t.Errorf("persisted tool = %q, want unknown (fallback)", persisted.Tool)
	}
}

func TestHook_Codex_WithoutToolUseID_GeneratesID(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Codex PostToolUse without tool_use_id. ID is generated
	// (16-char lowercase alpha).
	payload := `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"echo"},"tool_response":"hello"}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	var out codexHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("parse codex output: %v\nstdout=%s", err, stdout.String())
	}
	if out.Decision != "" || out.Reason != "" {
		t.Errorf("codex output = {decision:%q reason:%q}, want {} (passthrough)", out.Decision, out.Reason)
	}
	_, ids, err := d.SampleIDs(1)
	if err != nil {
		t.Fatalf("SampleIDs: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("SampleIDs returned %d tool_call ids, want 1 (fresh DB)", len(ids))
	}
	if !idPattern.MatchString(ids[0]) {
		t.Errorf("generated source_id = %q, want 16-char lowercase alpha", ids[0])
	}
}

func TestHook_Codex_BackwardCompat_OpenCode(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// OpenCode payload (runtime="opencode") uses OpenCode path
	// (backward compat).
	payload := `{"runtime":"opencode","id":"occompat01","timestamp":"2026-07-11T12:00:00Z","input":{"tool":"bash","args":{"command":"echo hi"}},"output":{"output":"result"}}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	persisted, err := d.GetToolCall("occompat01")
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if persisted.Tool != "bash" {
		t.Errorf("persisted tool = %q, want bash (OpenCode path)", persisted.Tool)
	}
	if persisted.Output != "result" {
		t.Errorf("persisted output = %q, want result (OpenCode extraction)", persisted.Output)
	}
	if persisted.Runtime == nil || *persisted.Runtime != "opencode" {
		t.Errorf("persisted runtime = %v, want &opencode", persisted.Runtime)
	}
}

func TestHook_Codex_BackwardCompat_Flat(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Flat tool_call payload (no hook_event_name, no runtime)
	// uses flat deserialisation (backward compat).
	payload := `{"id":"flatcompat01","tool":"bash","input":"echo hello","output":"hello\n","content_hash":"","timestamp":"2026-07-11T12:00:00Z"}`

	var stdout, stderr bytes.Buffer
	_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
	if err != nil {
		t.Fatalf("hookLogic: %v", err)
	}

	persisted, err := d.GetToolCall("flatcompat01")
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if persisted.Tool != "bash" {
		t.Errorf("persisted tool = %q, want bash (flat deserialisation)", persisted.Tool)
	}
	if persisted.Input != "echo hello" {
		t.Errorf("persisted input = %q, want echo hello (flat string)", persisted.Input)
	}
	if persisted.Output != "hello\n" {
		t.Errorf("persisted output = %q, want hello\\n", persisted.Output)
	}
	if persisted.Runtime != nil {
		t.Errorf("persisted runtime = %v, want nil (no runtime in flat payload)", persisted.Runtime)
	}
}

// codexPayload builds a Codex PostToolUse JSON payload string from a
// tool_use_id, tool_name, and the tool_response text (carried as
// aggregated_output). When toolUseID is empty, the field is omitted so
// the hook generates an id.
func codexPayload(t *testing.T, toolUseID, toolName, output string) string {
	t.Helper()
	m := map[string]any{
		"hook_event_name": "PostToolUse",
		"tool_name":       toolName,
		"tool_input":      map[string]any{"command": "echo"},
		"tool_response":   map[string]any{"aggregated_output": output},
	}
	if toolUseID != "" {
		m["tool_use_id"] = toolUseID
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal codex payload: %v", err)
	}
	return string(b)
}

func TestHook_CodexOutput_Transforms(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:3"]
`)

	var sb strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&sb, "line %d content here for padding\n", i)
	}
	original := sb.String()
	payload := codexPayload(t, "codxform01", "Bash", original)

	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr); err != nil {
		t.Fatalf("hookLogic: %v (stderr=%q)", err, stderr.String())
	}

	var out codexHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("parse codex output: %v\nstdout=%s", err, stdout.String())
	}
	if out.Decision != "block" {
		t.Errorf("decision = %q, want block", out.Decision)
	}
	if !strings.HasPrefix(out.Reason, "line 0") {
		t.Errorf("reason = %q, want compressed content starting with line 0", out.Reason)
	}
	if strings.Contains(out.Reason, "line 50") {
		t.Errorf("reason should be truncated (head:3); still contains line 50: %q", out.Reason)
	}
	if strings.Contains(out.Reason, "[ktx]") {
		t.Errorf("reason should NOT contain footer (footer off by default): %q", out.Reason)
	}
	if out.Reason == original {
		t.Error("reason equals original; want compressed (transformed)")
	}

	persisted, err := d.GetToolCall("codxform01")
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if persisted.Output != original {
		t.Errorf("persisted original does not match input (reversibility)")
	}

	var comp string
	if err := d.DB().QueryRow("SELECT output FROM compressed WHERE source_id = ?", "codxform01").Scan(&comp); err != nil {
		t.Fatalf("query compressed row: %v", err)
	}
	if out.Reason != comp {
		t.Errorf("reason should equal the stored compressed row content (footer off): %q vs %q", out.Reason, comp)
	}
}

func TestHook_CodexOutput_Passthrough_Empty(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	payload := codexPayload(t, "codxpass01", "Bash", "some tool result text\n")

	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr); err != nil {
		t.Fatalf("hookLogic: %v (stderr=%q)", err, stderr.String())
	}

	if got := strings.TrimSpace(stdout.String()); got != "{}" {
		t.Errorf("stdout = %q, want {} (passthrough, no pipeline)", got)
	}

	var out codexHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("parse codex output: %v\nstdout=%s", err, stdout.String())
	}
	if out.Decision != "" || out.Reason != "" {
		t.Errorf("codex output = {decision:%q reason:%q}, want both empty", out.Decision, out.Reason)
	}

	persisted, err := d.GetToolCall("codxpass01")
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if persisted.Output != "some tool result text\n" {
		t.Errorf("persisted original = %q, want the tool result unchanged", persisted.Output)
	}
}

func TestHook_CodexOutput_AntiWorsening_Suppressed(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["strip-ansi"]
recover_footer = true
`)

	original := "\x1b[31mError\x1b[0m: build started\nsee log for details\ncheck config\nrun tests\n"
	payload := codexPayload(t, "codxsup01", "Bash", original)

	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr); err != nil {
		t.Fatalf("hookLogic: %v (stderr=%q)", err, stderr.String())
	}

	if got := strings.TrimSpace(stdout.String()); got != "{}" {
		var out codexHookOutput
		_ = json.Unmarshal(stdout.Bytes(), &out)
		t.Errorf("stdout = %q (decision=%q reason present=%v), want {} (anti-worsening gate suppressed)", got, out.Decision, out.Reason != "")
	}

	persisted, err := d.GetToolCall("codxsup01")
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	if persisted.Output != original {
		t.Errorf("persisted original = %q, want unchanged (suppressed transform)", persisted.Output)
	}
}

func TestHook_CodexOutput_OpenCode_KeepsEnvelope(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	payload := `{"runtime":"opencode","id":"ocenv01","timestamp":"2026-07-11T12:00:00Z","input":{"tool":"bash","args":{"command":"echo hi"}},"output":{"output":"open code result"}}`

	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr); err != nil {
		t.Fatalf("hookLogic: %v (stderr=%q)", err, stderr.String())
	}

	var env hookEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("parse envelope: %v\nstdout=%s", err, stdout.String())
	}
	if env.Output != "open code result" {
		t.Errorf("envelope output = %q, want open code result (passthrough)", env.Output)
	}
	if env.CompressedID != nil {
		t.Errorf("compressed_id = %v, want null (no pipeline)", env.CompressedID)
	}
	if env.SourceID != "ocenv01" {
		t.Errorf("source_id = %q, want ocenv01", env.SourceID)
	}

	var spoof codexHookOutput
	_ = json.Unmarshal(stdout.Bytes(), &spoof)
	if spoof.Decision != "" || spoof.Reason != "" {
		t.Errorf("OpenCode payload must NOT emit codex decision/reason; got decision=%q reason=%q", spoof.Decision, spoof.Reason)
	}
}

func TestHook_CodexOutput_Footer_Transforms(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:3"]
recover_footer = true
`)

	var sb strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&sb, "line %d content here for padding\n", i)
	}
	payload := codexPayload(t, "codxfoot01", "Bash", sb.String())

	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr); err != nil {
		t.Fatalf("hookLogic: %v (stderr=%q)", err, stderr.String())
	}

	var out codexHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("parse codex output: %v\nstdout=%s", err, stdout.String())
	}
	if out.Decision != "block" {
		t.Errorf("decision = %q, want block", out.Decision)
	}
	if !strings.HasPrefix(out.Reason, "line 0") {
		t.Errorf("reason = %q, want compressed content starting with line 0", out.Reason)
	}
	footerMarker := "\n[ktx]"
	if !strings.Contains(out.Reason, footerMarker) {
		t.Errorf("reason should contain the recovery footer marker %q: %q", footerMarker, out.Reason)
	}
	if idx := strings.Index(out.Reason, footerMarker); idx <= 0 || !strings.HasPrefix(out.Reason, "line 0") {
		t.Errorf("footer must follow the compressed content, not precede it: %q", out.Reason)
	}
}

func TestExtractCodexFields_Direct(t *testing.T) {
	// The extraction logic lives in the hook layer
	// (internal/cli), not in the stream parser. Calling the function
	// directly from a test in package cli confirms the placement.
	t.Run("tool_name and tool_input, aggregated_output response", func(t *testing.T) {
		toolInputRaw := json.RawMessage(`{"command":"echo hi"}`)
		toolResponseRaw := json.RawMessage(`{"aggregated_output":"result"}`)
		tool, input, output := extractCodexFields("Bash", toolInputRaw, toolResponseRaw)
		if tool != "Bash" {
			t.Errorf("tool = %q, want Bash", tool)
		}
		if !strings.Contains(input, "echo hi") {
			t.Errorf("input = %q, want stringified tool_input containing echo hi", input)
		}
		if output != "result" {
			t.Errorf("output = %q, want result", output)
		}
	})

	t.Run("bare string response", func(t *testing.T) {
		toolResponseRaw := json.RawMessage(`"bare"`)
		_, _, output := extractCodexFields("Bash", nil, toolResponseRaw)
		if output != "bare" {
			t.Errorf("output = %q, want bare", output)
		}
	})

	t.Run("object response with output field", func(t *testing.T) {
		toolResponseRaw := json.RawMessage(`{"output":"result"}`)
		_, _, output := extractCodexFields("Bash", nil, toolResponseRaw)
		if output != "result" {
			t.Errorf("output = %q, want result", output)
		}
	})

	t.Run("object response with text field", func(t *testing.T) {
		toolResponseRaw := json.RawMessage(`{"text":"result"}`)
		_, _, output := extractCodexFields("Bash", nil, toolResponseRaw)
		if output != "result" {
			t.Errorf("output = %q, want result", output)
		}
	})

	t.Run("object response without recognised field", func(t *testing.T) {
		toolResponseRaw := json.RawMessage(`{"foo":"bar"}`)
		_, _, output := extractCodexFields("Bash", nil, toolResponseRaw)
		if !strings.Contains(output, "foo") {
			t.Errorf("output = %q, want stringified object (fallback)", output)
		}
	})

	t.Run("empty tool name falls back to unknown", func(t *testing.T) {
		tool, _, _ := extractCodexFields("", nil, nil)
		if tool != "unknown" {
			t.Errorf("tool = %q, want unknown (fallback)", tool)
		}
	})
}

func TestExtractOpenCodeFields_Direct(t *testing.T) {
	// The extraction logic lives in the hook layer (internal/cli),
	// not in the stream parser (internal/parser). Calling the function
	// directly from a test in package cli confirms the placement.
	t.Run("input with tool and args, output object", func(t *testing.T) {
		inputRaw := json.RawMessage(`{"tool":"bash","args":{"command":"echo hi"}}`)
		outputRaw := json.RawMessage(`{"output":"result"}`)
		tool, input, output := extractOpenCodeFields(inputRaw, outputRaw)
		if tool != "bash" {
			t.Errorf("tool = %q, want bash", tool)
		}
		if !strings.Contains(input, "echo hi") {
			t.Errorf("input = %q, want stringified args containing echo hi", input)
		}
		if output != "result" {
			t.Errorf("output = %q, want result", output)
		}
	})

	t.Run("bare string output", func(t *testing.T) {
		outputRaw := json.RawMessage(`"bare"`)
		_, _, output := extractOpenCodeFields(nil, outputRaw)
		if output != "bare" {
			t.Errorf("output = %q, want bare", output)
		}
	})

	t.Run("object output without output field", func(t *testing.T) {
		outputRaw := json.RawMessage(`{"foo":"bar"}`)
		_, _, output := extractOpenCodeFields(nil, outputRaw)
		if !strings.Contains(output, "foo") {
			t.Errorf("output = %q, want stringified object (fallback)", output)
		}
	})

	t.Run("input without tool name", func(t *testing.T) {
		inputRaw := json.RawMessage(`{"args":{"command":"echo"}}`)
		tool, _, _ := extractOpenCodeFields(inputRaw, nil)
		if tool != "unknown" {
			t.Errorf("tool = %q, want unknown (fallback)", tool)
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
		if config.ResolveTimeout(cfg, "default") != config.DefaultPipelineTimeout {
			t.Errorf("PipelineTimeout = %v, want %v (default 2s when omitted)", config.ResolveTimeout(cfg, "default"), config.DefaultPipelineTimeout)
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
		if config.ResolveTimeout(cfg, "default") != config.DefaultPipelineTimeout {
			t.Errorf("PipelineTimeout = %v, want %v (default 2s on passthrough path)", config.ResolveTimeout(cfg, "default"), config.DefaultPipelineTimeout)
		}
	})
}

func TestHook_SessionIDGuard(t *testing.T) {
	t.Run("KTX_SESSION_ID set → noop (nil, nil), no stdin read, no stdout, no db write", func(t *testing.T) {
		t.Setenv("KTX_SESSION_ID", "test-session-id")

		d := hookTestDB(t)
		hookChdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		stdin := &trackingReader{r: strings.NewReader(`{"id":"sess-01","tool":"bash","output":"data"}`)}
		var stdout, stderr bytes.Buffer

		result, err := hookLogic(stdin, d, &stdout, &stderr)
		if err != nil {
			t.Fatalf("hookLogic err = %v, want nil", err)
		}
		if result != nil {
			t.Errorf("result = %v, want nil (no stdout payload)", result)
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout = %q, want empty", stdout.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr = %q, want empty", stderr.String())
		}
		if got := stdin.bytes(); got != 0 {
			t.Errorf("stdin read %d bytes, want 0 (must not read stdin)", got)
		}
		if _, err := d.GetToolCall("sess-01"); !errors.Is(err, dbpkg.ErrNotFound) {
			t.Errorf("GetToolCall(sess-01) err = %v, want %v (no row should be persisted)", err, dbpkg.ErrNotFound)
		}
	})

	t.Run("KTX_SESSION_ID unset → normal processing writes envelope to stdout", func(t *testing.T) {
		d := hookTestDB(t)
		hookChdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		payload := `{"id":"sess-02","tool":"bash","input":"echo hi","output":"hi\n","content_hash":"","timestamp":"2026-07-01T12:00:00Z"}`
		var stdout, stderr bytes.Buffer

		_, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr)
		if err != nil {
			t.Fatalf("hookLogic err = %v, want nil", err)
		}
		if stdout.Len() == 0 {
			t.Fatal("stdout empty, want envelope written (normal processing)")
		}
		var env hookEnvelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("parse envelope: %v\nstdout=%s", err, stdout.String())
		}
		if env.Output != "hi\n" {
			t.Errorf("output = %q, want %q (passthrough)", env.Output, "hi\n")
		}
		if env.SourceID != "sess-02" {
			t.Errorf("source_id = %q, want sess-02", env.SourceID)
		}
		if env.CompressedID != nil {
			t.Errorf("compressed_id = %v, want null (no pipeline)", env.CompressedID)
		}
	})
}

type trackingReader struct {
	r *strings.Reader
	n int64
}

func (tr *trackingReader) Read(p []byte) (int, error) {
	n, err := tr.r.Read(p)
	tr.n += int64(n)
	return n, err
}

func (tr *trackingReader) bytes() int64 {
	return tr.n
}
