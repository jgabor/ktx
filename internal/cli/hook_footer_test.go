package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/pipeline"
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

func TestRecoveryCueOutput_SizeBoundary(t *testing.T) {
	compressed := "x"
	original := ""
	for n := 1; n < 1000; n++ {
		candidate := strings.Repeat("o", n)
		if len(compressed)+len(buildRecoveryFooter("boundary", n, len(compressed))) == n {
			original = candidate
			break
		}
	}
	if original == "" {
		t.Fatal("failed to construct equal-size boundary")
	}
	if output, ok := recoveryCueOutput("boundary", original, compressed); ok || output != original {
		t.Fatalf("equal-size cue = (%q, %t), want original and rejected", output, ok)
	}
	for {
		original += "o"
		if len(compressed)+len(buildRecoveryFooter("boundary", len(original), len(compressed))) < len(original) {
			break
		}
	}
	footer := buildRecoveryFooter("boundary", len(original), len(compressed))
	if output, ok := recoveryCueOutput("boundary", original, compressed); !ok || output != compressed+footer {
		t.Fatalf("smaller cue = (%q, %t), want decorated output and accepted", output, ok)
	}
}

func TestRecoveryFooter_ProductionBytes(t *testing.T) {
	want := "\n[ktx] output compressed: 4759 bytes (7% reduction) • get full output with `ktx read comparemiddlexxx`\n"
	if got := RecoveryFooter("comparemiddlexxx", 5120, 4759); got != want {
		t.Fatalf("RecoveryFooter bytes = %q, want %q", got, want)
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

func TestHook_ConfiguredEmptyInputCreatesNoArtifact(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["json-compact"]
recover_footer = true
`)
	tc := dbpkg.ToolCall{ID: "emptygate1", Tool: "read", Output: "", Timestamp: "2026-08-23T12:00:00Z"}
	payload, _ := json.Marshal(tc)
	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(bytes.NewReader(payload), d, &stdout, &stderr); err != nil {
		t.Fatalf("hookLogic: %v (stderr=%q)", err, stderr.String())
	}
	var env hookEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Output != "" || env.CompressedID != nil {
		t.Fatalf("envelope = %+v, want empty passthrough without artifact", env)
	}
	if _, err := d.GetCompressedBySource(tc.ID); err == nil {
		t.Fatal("compressed artifact exists for empty passthrough")
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

func TestHook_StructuredFallbackOmitsRecoveryFooter(t *testing.T) {
	tests := []struct {
		name   string
		stages string
		input  string
		want   string
		ndjson bool
	}{
		{
			name:   "complete JSON",
			stages: "head:1",
			input:  "{\n  \"items\": [1, 2, 3, 4],\n  \"message\": \"complete value with padding\"\n}",
			want:   `{"items":[1,2,3,4],"message":"complete value with padding"}`,
		},
		{
			name:   "NDJSON with final newline",
			stages: "truncate:12",
			input:  "{ \"one\" : \"first value with padding\" }\n{ \"two\" : [1, 2, 3, 4] }\n",
			want:   "{\"one\":\"first value with padding\"}\n{\"two\":[1,2,3,4]}\n",
			ndjson: true,
		},
		{
			name:   "NDJSON without final newline",
			stages: "truncate:12",
			input:  "{ \"one\" : \"first value with padding\" }\n{ \"two\" : [1, 2, 3, 4] }",
			want:   "{\"one\":\"first value with padding\"}\n{\"two\":[1,2,3,4]}",
			ndjson: true,
		},
	}
	deliveries := []struct {
		name     string
		idPrefix string
		payload  func(string, string) []byte
	}{
		{
			name:     "flat",
			idPrefix: "guardflat",
			payload: func(id, output string) []byte {
				payload, err := json.Marshal(dbpkg.ToolCall{
					ID: id, Tool: "read", Output: output, Timestamp: "2026-08-06T23:00:00Z",
				})
				if err != nil {
					t.Fatal(err)
				}
				return payload
			},
		},
		{
			name:     "trusted OpenCode",
			idPrefix: "guardopen",
			payload: func(id, output string) []byte {
				payload, err := json.Marshal(map[string]any{
					"runtime": "opencode", "id": id, "timestamp": "2026-08-06T23:00:00Z",
					"input":  map[string]any{"tool": "read", "args": map[string]any{}},
					"output": map[string]any{"output": output},
				})
				if err != nil {
					t.Fatal(err)
				}
				return payload
			},
		},
	}

	for shapeIndex, shape := range tests {
		for _, delivery := range deliveries {
			t.Run(shape.name+"/"+delivery.name, func(t *testing.T) {
				d := hookTestDB(t)
				hookChdirTemp(t)
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				hookWriteUserConfig(t, fmt.Sprintf(`[pipeline.default]
stages = [%q]
recover_footer = true
`, shape.stages))

				id := fmt.Sprintf("%s%04d", delivery.idPrefix, shapeIndex)
				var stdout, stderr bytes.Buffer
				if _, err := hookLogic(bytes.NewReader(delivery.payload(id, shape.input)), d, &stdout, &stderr); err != nil {
					t.Fatalf("hookLogic: %v (stderr=%q)", err, stderr.String())
				}

				var env hookEnvelope
				if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
					t.Fatalf("parse envelope: %v\nstdout=%s", err, stdout.String())
				}
				if env.Output != shape.want {
					t.Errorf("output = %q, want faithful structure %q", env.Output, shape.want)
				}
				if strings.Contains(env.Output, "[ktx]") {
					t.Errorf("recovery footer invalidated repaired structure: %q", env.Output)
				}
				if shape.ndjson {
					for recordNumber, record := range strings.Split(strings.TrimSuffix(env.Output, "\n"), "\n") {
						if !json.Valid([]byte(record)) {
							t.Errorf("NDJSON record %d is invalid: %q", recordNumber+1, record)
						}
					}
				} else if !json.Valid([]byte(env.Output)) {
					t.Errorf("complete JSON output is invalid: %q", env.Output)
				}
				if env.CompressedID == nil {
					t.Fatal("compressed_id is null; want retained faithful replacement")
				}
				compressed, err := d.GetCompressed(*env.CompressedID)
				if err != nil {
					t.Fatalf("GetCompressed: %v", err)
				}
				if compressed.Output != shape.want {
					t.Errorf("persisted output = %q, want %q", compressed.Output, shape.want)
				}
			})
		}
	}
}

func TestHook_TrustedRetryUsesRetainedStructuredFallback(t *testing.T) {
	tests := []struct {
		name         string
		firstStage   string
		input        string
		want         string
		ndjson       bool
		finalNewline bool
	}{
		{
			name:       "complete JSON",
			firstStage: "head:1",
			input:      "{\n  \"items\": [1, 2, 3, 4],\n  \"message\": \"complete value with padding\"\n}",
			want:       `{"items":[1,2,3,4],"message":"complete value with padding"}`,
		},
		{
			name:         "NDJSON with final newline",
			firstStage:   "truncate:12",
			input:        "{ \"one\" : \"first value with padding\" }\n{ \"two\" : [1, 2, 3, 4] }\n",
			want:         "{\"one\":\"first value with padding\"}\n{\"two\":[1,2,3,4]}\n",
			ndjson:       true,
			finalNewline: true,
		},
		{
			name:       "NDJSON without final newline",
			firstStage: "truncate:12",
			input:      "{ \"one\" : \"first value with padding\" }\n{ \"two\" : [1, 2, 3, 4] }",
			want:       "{\"one\":\"first value with padding\"}\n{\"two\":[1,2,3,4]}",
			ndjson:     true,
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := hookTestDB(t)
			hookChdirTemp(t)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			hookWriteUserConfig(t, fmt.Sprintf(`[pipeline.default]
stages = [%q]
recover_footer = true
`, tc.firstStage))

			callID := fmt.Sprintf("json-retry-%02d", i)
			payload := codexPayload(t, callID, "Read", tc.input)
			first := runCodexFooterHook(t, d, payload)
			assertCodexBlocked(t, first)
			assertStructuredFooterDelivery(t, first.Reason, tc.want, tc.ndjson, tc.finalNewline)

			sourceID := mustCodexID(t, "codex-test-session", callID)
			artifact, err := d.GetCompressedBySource(sourceID)
			if err != nil {
				t.Fatalf("GetCompressedBySource: %v", err)
			}
			if artifact.Output != tc.want {
				t.Fatalf("persisted output = %q, want %q", artifact.Output, tc.want)
			}
			fallback, recorded := pipeline.StructuredFallbackFromRecord(artifact.Pipeline)
			if !recorded || !fallback {
				t.Fatalf("persisted structured fallback = (%t, %t), want (true, true); pipeline=%q", fallback, recorded, artifact.Pipeline)
			}

			// The exact retry's current candidate is ordinary json-compact output.
			// The selected first artifact still owns footer suppression.
			hookWriteUserConfig(t, `[pipeline.default]
stages = ["json-compact"]
recover_footer = true
			`)
			retry := runCodexFooterHook(t, d, payload)
			assertCodexBlocked(t, retry)
			assertStructuredFooterDelivery(t, retry.Reason, tc.want, tc.ndjson, tc.finalNewline)
			if retry.Reason != first.Reason {
				t.Errorf("retry output = %q, want initial authoritative output %q", retry.Reason, first.Reason)
			}

			var count int
			if err := d.DB().QueryRow(`SELECT count(*) FROM compressed WHERE source_id = ?`, sourceID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Errorf("compressed rows = %d, want 1", count)
			}
		})
	}
}

func TestHook_TrustedRetryUsesRetainedOrdinaryFooterPolicy(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["json-compact"]
recover_footer = true
`)

	original := "{\n" + strings.Repeat(" ", 600) + `"items": [1, 2, 3, 4], "message": "ordinary compact output"` + "\n}"
	payload := codexPayload(t, "ordinary-retry", "Read", original)
	first := runCodexFooterHook(t, d, payload)
	assertCodexBlocked(t, first)
	if !strings.Contains(first.Reason, "[ktx]") {
		t.Fatalf("initial ordinary delivery lacks recovery footer: %q", first.Reason)
	}

	sourceID := mustCodexID(t, "codex-test-session", "ordinary-retry")
	artifact, err := d.GetCompressedBySource(sourceID)
	if err != nil {
		t.Fatalf("GetCompressedBySource: %v", err)
	}
	if !json.Valid([]byte(artifact.Output)) {
		t.Fatalf("persisted ordinary artifact is invalid JSON: %q", artifact.Output)
	}
	fallback, recorded := pipeline.StructuredFallbackFromRecord(artifact.Pipeline)
	if !recorded || fallback {
		t.Fatalf("persisted structured fallback = (%t, %t), want (false, true); pipeline=%q", fallback, recorded, artifact.Pipeline)
	}
	if first.Reason != artifact.Output+buildRecoveryFooter(sourceID, len(original), len(artifact.Output)) {
		t.Fatalf("initial output did not apply ordinary artifact footer policy: %q", first.Reason)
	}

	// This retry produces a repaired candidate, but the ordinary retained
	// artifact remains authoritative and keeps its footer.
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:1"]
recover_footer = true
	`)
	retry := runCodexFooterHook(t, d, payload)
	assertCodexBlocked(t, retry)
	if retry.Reason != first.Reason {
		t.Errorf("retry output = %q, want retained ordinary delivery %q", retry.Reason, first.Reason)
	}
}

func TestHook_TrustedRetryLegacyArtifactUsesConservativeFooterPolicy(t *testing.T) {
	d := hookTestDB(t)
	hookChdirTemp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	hookWriteUserConfig(t, `[pipeline.default]
recover_footer = true
`)

	original := "{\n" + strings.Repeat(" ", 600) + `"items": [1, 2, 3, 4], "message": "legacy retained artifact"` + "\n}"
	payload := codexPayload(t, "legacy-retry", "Read", original)
	first := runCodexFooterHook(t, d, payload)
	if first.Decision != "" || first.Reason != "" {
		t.Fatalf("initial passthrough response = %+v, want empty Codex response", first)
	}

	sourceID := mustCodexID(t, "codex-test-session", "legacy-retry")
	legacy := &dbpkg.Compressed{
		ID:          "legacyartifactxx",
		SourceID:    sourceID,
		Output:      "legacy summary\n",
		ContentHash: dbpkg.ContentHash("legacy summary\n"),
		Pipeline:    `{"profile":"default","route":"default_fallback","stages":["head:1"]}`,
		Timestamp:   "2026-08-01T00:00:00Z",
	}
	if err := d.InsertCompressed(legacy); err != nil {
		t.Fatalf("seed legacy artifact: %v", err)
	}
	readBack, err := d.GetCompressed(legacy.ID)
	if err != nil {
		t.Fatalf("read legacy artifact: %v", err)
	}
	if readBack.Output != legacy.Output || readBack.Pipeline != legacy.Pipeline {
		t.Fatalf("legacy artifact changed on read: %+v", readBack)
	}
	if fallback, recorded := pipeline.StructuredFallbackFromRecord(readBack.Pipeline); fallback || recorded {
		t.Fatalf("legacy provenance = (%t, %t), want not recorded", fallback, recorded)
	}

	// The current candidate is repaired JSON. Missing legacy provenance is not
	// reconstructed from either candidate or content, so ordinary conservative
	// footer behavior remains in force for the retained artifact.
	hookWriteUserConfig(t, `[pipeline.default]
stages = ["head:1"]
recover_footer = true
	`)
	retry := runCodexFooterHook(t, d, payload)
	assertCodexBlocked(t, retry)
	want := legacy.Output + buildRecoveryFooter(sourceID, len(original), len(legacy.Output))
	if retry.Reason != want {
		t.Errorf("legacy retry output = %q, want conservative delivery %q", retry.Reason, want)
	}
}

func runCodexFooterHook(t *testing.T, d *dbpkg.DB, payload string) codexHookOutput {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if _, err := hookLogic(strings.NewReader(payload), d, &stdout, &stderr); err != nil {
		t.Fatalf("hookLogic: %v (stderr=%q)", err, stderr.String())
	}
	var out codexHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("parse Codex hook output: %v\nstdout=%s", err, stdout.String())
	}
	return out
}

func assertCodexBlocked(t *testing.T, out codexHookOutput) {
	t.Helper()
	if out.Decision != "block" {
		t.Fatalf("Codex decision = %q, want block", out.Decision)
	}
}

func assertStructuredFooterDelivery(t *testing.T, got, want string, ndjson, finalNewline bool) {
	t.Helper()
	if got != want {
		t.Errorf("delivered output = %q, want %q", got, want)
	}
	if strings.Contains(got, "[ktx]") {
		t.Errorf("recovery footer invalidated repaired structure: %q", got)
	}
	if !ndjson {
		if !json.Valid([]byte(got)) {
			t.Errorf("complete JSON output is invalid: %q", got)
		}
		return
	}
	if strings.HasSuffix(got, "\n") != finalNewline {
		t.Errorf("final newline = %t, want %t", strings.HasSuffix(got, "\n"), finalNewline)
	}
	for recordNumber, record := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if !json.Valid([]byte(record)) {
			t.Errorf("NDJSON record %d is invalid: %q", recordNumber+1, record)
		}
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
