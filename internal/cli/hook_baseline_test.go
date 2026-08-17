package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

var baselineBlockerCommands = map[string]struct{}{
	"write": {}, "compress": {}, "hook": {}, "run": {},
	"get": {}, "read": {}, "list": {}, "stats": {}, "decompress": {},
	"bench": {}, "telemetry record": {}, "telemetry list": {}, "doctor": {},
	"db migrate": {}, "db report": {},
}

func TestCommandBaselineInventories(t *testing.T) {
	producerCases := []struct {
		surface, evidence, want string
	}{
		{"ktx write", "source", "write"},
		{"ktx hook", "source", "hook"},
		{"ktx hook", "artifact", "hook"},
		{"ktx run", "source", "run"},
		{"ktx compress", "artifact", "compress"},
		{"component benchmark", "source", "componentbench"},
		{"component benchmark", "artifact", "componentbench"},
		{"load benchmark fixture", "artifact", "loadbench-fixture"},
	}
	for _, tc := range producerCases {
		if got := baselineProducerLabel(tc.surface, tc.evidence, "payload says hook"); got != tc.want {
			t.Errorf("%s %s producer = %q, want %q", tc.surface, tc.evidence, got, tc.want)
		}
	}
	if got := baselineProducerLabel("unknown surface", "source", "hook"); got != "unknown" {
		t.Errorf("unknown surface producer = %q, want unknown", got)
	}

	for _, command := range []string{"write", "compress", "hook"} {
		if dispatchRegistry[command].opts.DatabaseAccess != DatabaseReadWrite {
			t.Errorf("%s no longer acquires write coordination", command)
		}
	}
	for _, command := range []string{"get", "read", "list", "stats", "decompress"} {
		if dispatchRegistry[command].opts.DatabaseAccess != DatabaseReadOnly {
			t.Errorf("%s no longer acquires read coordination", command)
		}
	}

	for command := range baselineBlockerCommands {
		if got := baselineBlockerLabel(command, "supported"); got != command {
			t.Errorf("supported %q = %q", command, got)
		}
		for _, identity := range []string{"foreign", "unavailable", "ambiguous"} {
			if got := baselineBlockerLabel(command, identity); got != "unknown" {
				t.Errorf("%s %q = %q, want unknown", identity, command, got)
			}
		}
	}
	if got := baselineBlockerLabel("unsupported", "supported"); got != "unknown" {
		t.Errorf("unsupported command = %q, want unknown", got)
	}
}

func baselineProducerLabel(surface, evidence, _ string) string {
	switch surface + ":" + evidence {
	case "ktx write:source":
		return "write"
	case "ktx hook:source", "ktx hook:artifact":
		return "hook"
	case "ktx run:source":
		return "run"
	case "ktx compress:artifact":
		return "compress"
	case "component benchmark:source", "component benchmark:artifact":
		return "componentbench"
	case "load benchmark fixture:artifact":
		return "loadbench-fixture"
	default:
		return "unknown"
	}
}

func baselineBlockerLabel(command, identity string) string {
	if identity != "supported" {
		return "unknown"
	}
	if _, ok := baselineBlockerCommands[command]; !ok {
		return "unknown"
	}
	return command
}

func TestHookBaselineStageHelper(t *testing.T) {
	if os.Getenv("KTX_HOOK_BASELINE_FAIL_STAGE") != "1" {
		return
	}
	fmt.Fprintln(os.Stderr, "baseline stage failed")
	os.Exit(1)
}

func TestHookPhaseBaseline(t *testing.T) {
	t.Run("fresh passthrough", func(t *testing.T) {
		d := hookTestDB(t)
		hookChdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		payload := openCodeHookPayload(t, "baseline-pass-session", "baseline-pass-call", "bash", `{"command":"true"}`, "original")
		stdout, stderr, observations, err := runObservedHook(t, d, payload)
		if err != nil || stderr != "" {
			t.Fatalf("hook = %v, stderr=%q", err, stderr)
		}
		assertHookEnvelopeOutput(t, stdout, "original", false)
		assertHookObservations(t, observations, dbpkg.OperationSource, dbpkg.OperationDisposition)
		assertHookEvidenceCounts(t, d, mustOpenCodeID(t, "baseline-pass-session", "baseline-pass-call"), 1, 0, 1)
	})

	t.Run("fresh transformed", func(t *testing.T) {
		d := hookTestDB(t)
		hookChdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		hookWriteUserConfig(t, "[pipeline.default]\nstages = [\"head:1\"]\n")
		original := strings.Repeat("line one\nline two\n", 20)
		payload := openCodeHookPayload(t, "baseline-transform-session", "baseline-transform-call", "bash", `{"command":"true"}`, original)
		stdout, stderr, observations, err := runObservedHook(t, d, payload)
		if err != nil || stderr != "" {
			t.Fatalf("hook = %v, stderr=%q", err, stderr)
		}
		assertHookEnvelopeOutput(t, stdout, "line one\n", true)
		assertHookObservations(t, observations, dbpkg.OperationSource, dbpkg.OperationArtifact, dbpkg.OperationDisposition)
		assertHookEvidenceCounts(t, d, mustOpenCodeID(t, "baseline-transform-session", "baseline-transform-call"), 1, 1, 1)
	})

	t.Run("exact retry", func(t *testing.T) {
		d := hookTestDB(t)
		hookChdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		hookWriteUserConfig(t, "[pipeline.default]\nstages = [\"head:1\"]\n")
		original := strings.Repeat("first\nsecond\n", 20)
		payload := openCodeHookPayload(t, "baseline-retry-session", "baseline-retry-call", "bash", `{"command":"true"}`, original)
		if _, _, _, err := runObservedHook(t, d, payload); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, observations, err := runObservedHook(t, d, payload)
		if err != nil || stderr != "" {
			t.Fatalf("retry = %v, stderr=%q", err, stderr)
		}
		assertHookEnvelopeOutput(t, stdout, "first\n", true)
		assertHookObservations(t, observations, dbpkg.OperationSource, dbpkg.OperationArtifact, dbpkg.OperationDisposition)
		assertHookEvidenceCounts(t, d, mustOpenCodeID(t, "baseline-retry-session", "baseline-retry-call"), 1, 1, 1)
	})

	t.Run("conflicting retry", func(t *testing.T) {
		d := hookTestDB(t)
		hookChdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		first := openCodeHookPayload(t, "baseline-conflict-session", "baseline-conflict-call", "bash", `{"command":"true"}`, "first")
		if _, _, _, err := runObservedHook(t, d, first); err != nil {
			t.Fatal(err)
		}
		changed := openCodeHookPayload(t, "baseline-conflict-session", "baseline-conflict-call", "bash", `{"command":"true"}`, "changed")
		stdout, _, observations, err := runObservedHook(t, d, changed)
		var cliErr *Error
		if !errors.As(err, &cliErr) || cliErr.Code != CodeConflict {
			t.Fatalf("conflicting retry = %v, want conflict", err)
		}
		if stdout != "" || len(observations) != 0 {
			t.Fatalf("conflicting retry stdout=%q observations=%v", stdout, observations)
		}
		assertHookEvidenceCounts(t, d, mustOpenCodeID(t, "baseline-conflict-session", "baseline-conflict-call"), 1, 0, 1)
	})

	t.Run("malformed config", func(t *testing.T) {
		d := hookTestDB(t)
		hookChdirTemp(t)
		configHome := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", configHome)
		configDir := filepath.Join(configHome, "ktx")
		if err := os.MkdirAll(configDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "ktx.toml"), []byte("malformed = ["), 0o600); err != nil {
			t.Fatal(err)
		}
		payload := openCodeHookPayload(t, "baseline-config-session", "baseline-config-call", "bash", `{"command":"true"}`, "original")
		stdout, _, observations, err := runObservedHook(t, d, payload)
		var cliErr *Error
		if !errors.As(err, &cliErr) || cliErr.Code != CodeUsage {
			t.Fatalf("malformed config = %v, want usage error", err)
		}
		if stdout != "" {
			t.Fatalf("malformed config stdout=%q", stdout)
		}
		assertHookObservations(t, observations, dbpkg.OperationSource, dbpkg.OperationDisposition)
		assertHookEvidenceCounts(t, d, mustOpenCodeID(t, "baseline-config-session", "baseline-config-call"), 1, 0, 1)
	})

	t.Run("pipeline failure", func(t *testing.T) {
		d := hookTestDB(t)
		hookChdirTemp(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("KTX_HOOK_BASELINE_FAIL_STAGE", "1")
		hookWriteUserConfig(t, fmt.Sprintf(`[pipeline.default]
stages = ["baseline-failure:-test.run=^TestHookBaselineStageHelper$"]

[[plugin]]
name = "baseline-failure"
command = %q
`, os.Args[0]))
		payload := openCodeHookPayload(t, "baseline-pipeline-session", "baseline-pipeline-call", "bash", `{"command":"true"}`, "original")
		stdout, stderr, observations, err := runObservedHook(t, d, payload)
		if err != nil {
			t.Fatal(err)
		}
		assertHookEnvelopeOutput(t, stdout, "original", false)
		if !strings.Contains(stderr, "stage") {
			t.Fatalf("pipeline failure stderr=%q", stderr)
		}
		assertHookObservations(t, observations, dbpkg.OperationSource, dbpkg.OperationDisposition)
		assertHookEvidenceCounts(t, d, mustOpenCodeID(t, "baseline-pipeline-session", "baseline-pipeline-call"), 1, 0, 1)
	})
}

func runObservedHook(t *testing.T, database *dbpkg.DB, payload string) (stdout, stderr string, observations []string, err error) {
	t.Helper()
	var out, warnings bytes.Buffer
	_, err = hookLogicObserved(strings.NewReader(payload), database, &out, &warnings, func(kind, phase string) {
		observations = append(observations, kind+":"+phase)
	})
	return out.String(), warnings.String(), observations, err
}

func assertHookEnvelopeOutput(t *testing.T, encoded, output string, compressed bool) {
	t.Helper()
	var envelope hookEnvelope
	if err := json.Unmarshal([]byte(encoded), &envelope); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, encoded)
	}
	if envelope.Output != output || (envelope.CompressedID != nil) != compressed {
		t.Errorf("envelope = %+v, want output %q compressed=%t", envelope, output, compressed)
	}
}

func assertHookObservations(t *testing.T, got []string, phases ...string) {
	t.Helper()
	want := make([]string, 0, len(phases)*2)
	for _, phase := range phases {
		want = append(want, hookExclusiveAcquisition+":"+phase, hookAcceptanceCommit+":"+phase)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hook observations = %v, want %v", got, want)
	}
}
