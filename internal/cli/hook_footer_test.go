package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

func TestBuildRecoveryFooter_ContainsSourceID(t *testing.T) {
	footer := buildRecoveryFooter("mysource", 50000, 2000)
	if !strings.Contains(footer, "mysource") {
		t.Errorf("footer does not contain source_id verbatim: %q", footer)
	}
}

func TestBuildRecoveryFooter_CompressedSizeAndRatio(t *testing.T) {
	footer := buildRecoveryFooter("srcid", 50000, 2000)
	if !strings.Contains(footer, "compressed: 2000 bytes") {
		t.Errorf("footer missing compressed size: %q", footer)
	}
	if !strings.Contains(footer, "96%") {
		t.Errorf("footer missing reduction ratio 96%%: %q", footer)
	}
	if strings.Contains(footer, "50000") {
		t.Errorf("footer should not surface the original byte count: %q", footer)
	}
}

func TestBuildRecoveryFooter_ContainsRecoveryCommand(t *testing.T) {
	footer := buildRecoveryFooter("recoveryid", 1000, 500)
	if !strings.Contains(footer, "ktx read recoveryid") {
		t.Errorf("footer missing recovery command: %q", footer)
	}
}

func TestHook_FooterGate_Suppresses(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["strip-ansi"]
recover_footer = true
`)

	original := "\x1b[31mError\x1b[0m: build started\nsee log for details\ncheck config\nrun tests\n"
	tc := dbpkg.ToolCall{
		ID:        "footsup01",
		Tool:      "bash",
		Input:     "make",
		Output:    original,
		Timestamp: "2026-07-01T12:00:00Z",
	}
	payload, _ := json.Marshal(tc)

	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(string(payload)), d, &stdout, &stderr); err != nil {
		t.Fatalf("hookLogic: %v (stderr=%q)", err, stderr.String())
	}

	var env hookEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("parse envelope: %v\nstdout=%s", err, stdout.String())
	}

	if env.Output != original {
		t.Errorf("output = %q, want original (footer gate suppressed)", env.Output)
	}
	if env.CompressedID != nil {
		t.Errorf("compressed_id = %v, want null (suppressed; no compressed row)", env.CompressedID)
	}
	if strings.Contains(env.Output, "[ktx]") {
		t.Errorf("output should NOT contain footer (suppressed): %q", env.Output)
	}
}

func TestHook_FooterGate_Keeps(t *testing.T) {
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
	original := sb.String()

	tc := dbpkg.ToolCall{
		ID:        "footkeep01",
		Tool:      "bash",
		Input:     "build",
		Output:    original,
		Timestamp: "2026-07-01T12:00:00Z",
	}
	payload, _ := json.Marshal(tc)

	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(string(payload)), d, &stdout, &stderr); err != nil {
		t.Fatalf("hookLogic: %v (stderr=%q)", err, stderr.String())
	}

	var env hookEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("parse envelope: %v\nstdout=%s", err, stdout.String())
	}

	if env.CompressedID == nil {
		t.Fatal("compressed_id is null; want a compressed row (footer gate kept)")
	}
	if !strings.Contains(env.Output, "[ktx]") {
		t.Errorf("envelope output should contain the footer: %q", env.Output)
	}
	if !strings.HasPrefix(env.Output, "line 0") {
		t.Errorf("envelope output should start with compressed content: %q", env.Output)
	}

	comp, err := d.GetCompressed(*env.CompressedID)
	if err != nil {
		t.Fatalf("GetCompressed: %v", err)
	}
	if strings.Contains(comp.Output, "[ktx]") {
		t.Errorf("DB row should NOT contain the footer: %q", comp.Output)
	}

	if !strings.HasPrefix(env.Output, comp.Output) {
		t.Errorf("envelope output should start with the DB row content")
	}

	source, _ := d.GetToolCall("footkeep01")
	if source.Output != original {
		t.Errorf("persisted original does not match input (reversibility)")
	}
}

func TestHook_FooterDisabled(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:3"]
recover_footer = false
`)

	var sb strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&sb, "line %d content here for padding\n", i)
	}
	original := sb.String()

	tc := dbpkg.ToolCall{
		ID:        "footdis01",
		Tool:      "bash",
		Input:     "build",
		Output:    original,
		Timestamp: "2026-07-01T12:00:00Z",
	}
	payload, _ := json.Marshal(tc)

	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(string(payload)), d, &stdout, &stderr); err != nil {
		t.Fatalf("hookLogic: %v (stderr=%q)", err, stderr.String())
	}

	var env hookEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("parse envelope: %v\nstdout=%s", err, stdout.String())
	}

	if env.CompressedID == nil {
		t.Fatal("compressed_id is null; want a compressed row (pipeline transformed)")
	}
	if strings.Contains(env.Output, "[ktx]") {
		t.Errorf("output should NOT contain footer (recover_footer disabled): %q", env.Output)
	}
	if !strings.HasPrefix(env.Output, "line 0") {
		t.Errorf("output should start with compressed content: %q", env.Output)
	}

	comp, _ := d.GetCompressed(*env.CompressedID)
	if comp.Output != env.Output {
		t.Errorf("envelope output should equal DB row (no footer when disabled)")
	}
}

func TestHook_NoTransform_NoFooter(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
recover_footer = true
`)

	original := "hello\nline 2\nline 3\n"
	tc := dbpkg.ToolCall{
		ID:        "footnotrans01",
		Tool:      "bash",
		Input:     "echo",
		Output:    original,
		Timestamp: "2026-07-01T12:00:00Z",
	}
	payload, _ := json.Marshal(tc)

	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(string(payload)), d, &stdout, &stderr); err != nil {
		t.Fatalf("hookLogic: %v (stderr=%q)", err, stderr.String())
	}

	var env hookEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("parse envelope: %v\nstdout=%s", err, stdout.String())
	}

	if env.Output != original {
		t.Errorf("output = %q, want original (no pipeline -> no transform)", env.Output)
	}
	if env.CompressedID != nil {
		t.Errorf("compressed_id = %v, want null (no transform)", env.CompressedID)
	}
	if strings.Contains(env.Output, "[ktx]") {
		t.Errorf("output should NOT contain footer (no transform): %q", env.Output)
	}
}

// When the footer gate keeps the transform, the envelope output
// is exactly the compressed content followed by the footer — the
// compressed content must appear before the footer.
func TestHook_FooterGate_OrderedContentBeforeFooter(t *testing.T) {
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
	original := sb.String()

	tc := dbpkg.ToolCall{
		ID:        "footorder01",
		Tool:      "bash",
		Input:     "build",
		Output:    original,
		Timestamp: "2026-07-01T12:00:00Z",
	}
	payload, _ := json.Marshal(tc)

	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(string(payload)), d, &stdout, &stderr); err != nil {
		t.Fatalf("hookLogic: %v (stderr=%q)", err, stderr.String())
	}

	var env hookEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("parse envelope: %v\nstdout=%s", err, stdout.String())
	}
	if env.CompressedID == nil {
		t.Fatal("compressed_id is null; want a compressed row (gate kept)")
	}

	comp, err := d.GetCompressed(*env.CompressedID)
	if err != nil {
		t.Fatalf("GetCompressed: %v", err)
	}

	contentIdx := strings.Index(env.Output, comp.Output)
	if contentIdx != 0 {
		t.Errorf("compressed content must be a prefix of envelope output; Index=%d want 0", contentIdx)
	}
	// env.Output = compressedContent + footer, where footer begins with
	// "\n[ktx]". Assert the footer starts exactly where the compressed
	// content ends: the content is a prefix and the bytes right after it
	// are the footer marker.
	footerMarker := "\n[ktx]"
	if !strings.HasPrefix(env.Output, comp.Output) {
		t.Fatalf("envelope output must start with the content-only DB row content: %q", env.Output)
	}
	tail := env.Output[len(comp.Output):]
	if !strings.HasPrefix(tail, footerMarker) {
		t.Errorf("footer must follow the compressed content immediately; tail=%q want prefix %q", tail, footerMarker)
	}
}

// The OpenCode plugin template must set output.output = envelope.output
// verbatim — the footer propagates through envelope.Output without any
// plugin-side footer logic. This guards against accidental footer wiring
// leaking into the template.
func TestPluginTemplate_PropagatesEnvelopeUnchanged(t *testing.T) {
	if !strings.Contains(ktxOpencodePluginTemplate, "output.output = envelope.output") {
		t.Errorf("plugin template must set output.output = envelope.output to propagate the footer-bearing envelope verbatim")
	}
	for _, probe := range []string{"footer", "output compressed:", "get full output with", "buildRecoveryFooter", "recover_footer"} {
		if strings.Contains(ktxOpencodePluginTemplate, probe) {
			t.Errorf("plugin template must not contain footer logic (%q found); the footer lives only in envelope.Output", probe)
		}
	}
}
