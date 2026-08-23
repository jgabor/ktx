package reuse

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRuntimeControlProbeReportsOnlySelectedOfflineMode(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	probe := filepath.Join(root, "tools", "bench", "evidence", "opencode-runtime-cache-controls", "probe.sh")
	bin := t.TempDir()
	docker := filepath.Join(bin, "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", probe, "--preflight")
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "OPENCODE_API_KEY=dummy")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("preflight: %v: %s", err, out)
	}
	if !bytes.Contains(out, []byte("network=none model_calls=0")) {
		t.Fatalf("preflight report = %q", out)
	}
	cmd = exec.Command("bash", probe, "--live-changed")
	cmd.Env = append(os.Environ(), "OPENCODE_API_KEY=dummy")
	out, err = cmd.CombinedOutput()
	if err == nil || len(out) != 0 {
		t.Fatalf("unguarded live mode error = %v, output = %q", err, out)
	}
}

type cacheInput struct {
	CacheRead  *int64 `json:"cache_read"`
	CacheWrite *int64 `json:"cache_write"`
	Eligible   *bool  `json:"eligible"`
}

type measurements struct {
	CacheRead     *int64   `json:"cache_read"`
	CacheWrite    *int64   `json:"cache_write"`
	UncachedInput *int64   `json:"uncached_input"`
	Output        *int64   `json:"output"`
	Cost          *float64 `json:"cost_usd"`
}

type runtimeControlEvidence struct {
	SchemaVersion int    `json:"schema_version"`
	Status        string `json:"status"`
	Source        struct {
		Runtime        string `json:"runtime"`
		RuntimeVersion string `json:"runtime_version"`
		Model          string `json:"model"`
	} `json:"source"`
	Bounds struct {
		Invocations struct{ Actual, Maximum int }     `json:"opencode_invocations"`
		Attempts    struct{ Actual, Maximum int }     `json:"invocation_attempts"`
		Retries     struct{ Actual, Maximum int }     `json:"intentional_retries"`
		Spend       struct{ Actual, Maximum float64 } `json:"spend_usd"`
		Wall        struct{ Actual, Maximum int }     `json:"wall_time_seconds"`
		ModelSteps  int                               `json:"model_steps"`
	} `json:"bounds"`
	Observations []struct {
		Ordinal            int          `json:"ordinal"`
		RequestedTurn      int          `json:"requested_turn"`
		ObservedModelSteps int          `json:"observed_model_steps"`
		SessionState       string       `json:"session_state"`
		SessionSHA256      string       `json:"session_sha256"`
		PromptSHA256       string       `json:"prompt_sha256"`
		Outcomes           []string     `json:"cache_outcomes"`
		Input              cacheInput   `json:"classification_input"`
		Measurements       measurements `json:"measurements"`
		FieldCompleteness  struct {
			CacheRead     string `json:"cache_read"`
			CacheWrite    string `json:"cache_write"`
			UncachedInput string `json:"uncached_input"`
			Output        string `json:"output"`
			Cost          string `json:"cost"`
		} `json:"field_completeness"`
	} `json:"observations"`
	Controls []struct {
		Dimension   string            `json:"dimension"`
		Accepted    []json.RawMessage `json:"accepted_cases"`
		Unavailable []json.RawMessage `json:"unavailable_cases"`
	} `json:"controls"`
	ClassificationCases []struct {
		Input    cacheInput `json:"input"`
		Expected string     `json:"expected"`
	} `json:"classification_cases"`
	UnavailableCapabilities []string                        `json:"unavailable_capabilities"`
	TrackedArtifacts        []struct{ Path, SHA256 string } `json:"tracked_artifacts"`
	IgnoredRoot             string                          `json:"ignored_artifact_root"`
	IgnoredArtifacts        []struct{ Path, SHA256 string } `json:"ignored_artifacts"`
	Redaction               struct {
		Result                   string `json:"result"`
		AuthMode                 string `json:"auth_mode"`
		CredentialValuesRetained bool   `json:"credential_values_retained"`
		RawKeysRetained          bool   `json:"raw_keys_or_namespaces_retained"`
	} `json:"redaction"`
}

type sourceExcerpt struct {
	Ordinal       int        `json:"observation_ordinal"`
	EventType     string     `json:"event_type"`
	Input         cacheInput `json:"-"`
	Eligibility   *bool      `json:"eligibility"`
	CacheRead     *int64     `json:"cache_read"`
	CacheWrite    *int64     `json:"cache_write"`
	UncachedInput *int64     `json:"uncached_input"`
	Output        *int64     `json:"output"`
	Cost          *float64   `json:"cost_usd"`
	SessionSHA256 *string    `json:"session_sha256"`
	PromptSHA256  string     `json:"prompt_sha256"`
}

func TestOpenCodeRuntimeControlEvidence(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	validatePreflight(t, filepath.Join(root, "tools", "bench", "evidence", "opencode-runtime-cache-controls-preflight.json"), 4, 300)
	validatePreflight(t, filepath.Join(root, "tools", "bench", "evidence", "opencode-runtime-cache-controls-preflight-2.json"), 2, 120)

	var evidence runtimeControlEvidence
	readJSON(t, filepath.Join(root, "tools", "bench", "evidence", "opencode-runtime-cache-controls.json"), &evidence)
	if evidence.SchemaVersion != 1 || evidence.Status != "runtime_controls_complete" || evidence.Source.Runtime != "opencode" || evidence.Source.RuntimeVersion != "1.18.4" || evidence.Source.Model != "opencode/big-pickle" {
		t.Fatalf("invalid source or status: %#v", evidence.Source)
	}
	if evidence.Bounds.Invocations.Actual != 6 || evidence.Bounds.Invocations.Maximum != 6 || evidence.Bounds.Attempts.Actual != 6 || evidence.Bounds.Attempts.Maximum != 6 || evidence.Bounds.Retries.Actual != 0 || evidence.Bounds.Retries.Maximum != 0 || evidence.Bounds.Spend.Actual != 0 || evidence.Bounds.Spend.Maximum != 0 || evidence.Bounds.Wall.Actual != 33 || evidence.Bounds.Wall.Maximum != 420 || evidence.Bounds.ModelSteps != 3 {
		t.Fatalf("incorrect cumulative accounting: %#v", evidence.Bounds)
	}

	excerpts := readSourceExcerpts(t, filepath.Join(root, "tools", "bench", "evidence", "opencode-runtime-cache-controls", "source-events.jsonl"))
	if len(excerpts) != len(evidence.Observations) || len(evidence.Observations) != evidence.Bounds.Invocations.Actual {
		t.Fatalf("excerpts=%d observations=%d invocations=%d", len(excerpts), len(evidence.Observations), evidence.Bounds.Invocations.Actual)
	}
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for i, observation := range evidence.Observations {
		excerpt := excerpts[i]
		if observation.Ordinal != i+1 || excerpt.Ordinal != observation.Ordinal || observation.RequestedTurn < 0 || !hex64.MatchString(observation.PromptSHA256) || excerpt.PromptSHA256 != observation.PromptSHA256 {
			t.Fatalf("incomplete observation %d", i+1)
		}
		if observation.SessionState == "measured" && (!hex64.MatchString(observation.SessionSHA256) || excerpt.SessionSHA256 == nil || *excerpt.SessionSHA256 != observation.SessionSHA256) {
			t.Errorf("observation %d lacks matching sanitized session hash", i+1)
		}
		if observation.ObservedModelSteps == 1 && excerpt.EventType != "step_finish" || observation.ObservedModelSteps == 0 && excerpt.EventType != "no_model_step" {
			t.Errorf("observation %d event=%q model_steps=%d", i+1, excerpt.EventType, observation.ObservedModelSteps)
		}
		excerpt.Input = cacheInput{CacheRead: excerpt.CacheRead, CacheWrite: excerpt.CacheWrite, Eligible: excerpt.Eligibility}
		if !sameCacheInput(observation.Input, excerpt.Input) {
			t.Errorf("observation %d classification input does not match retained source excerpt", i+1)
		}
		if len(observation.Outcomes) != 1 || classifyRuntimeCache(observation.Input) != observation.Outcomes[0] {
			t.Errorf("observation %d outcome=%v does not derive from %#v", i+1, observation.Outcomes, observation.Input)
		}
		if observation.ObservedModelSteps == 1 && (observation.Measurements.CacheRead == nil || *observation.Measurements.CacheRead != *excerpt.CacheRead || observation.Measurements.CacheWrite == nil || *observation.Measurements.CacheWrite != *excerpt.CacheWrite || observation.Measurements.UncachedInput == nil || *observation.Measurements.UncachedInput != *excerpt.UncachedInput || observation.Measurements.Output == nil || *observation.Measurements.Output != *excerpt.Output || observation.Measurements.Cost == nil || *observation.Measurements.Cost != *excerpt.Cost) {
			t.Errorf("observation %d measurements do not match retained source excerpt", i+1)
		}
	}

	validateControlsAndOfflineClasses(t, evidence)
	validateUnavailableCapabilities(t, evidence.UnavailableCapabilities)
	validateTrackedArtifacts(t, root, evidence)
	validatePromptHashes(t, evidence)
	validateAdditionalAccounting(t, root)
	if evidence.Redaction.Result != "pass" || evidence.Redaction.AuthMode != "read_only" || evidence.Redaction.CredentialValuesRetained || evidence.Redaction.RawKeysRetained {
		t.Errorf("redaction failure: %#v", evidence.Redaction)
	}
}

func validateUnavailableCapabilities(t *testing.T, got []string) {
	t.Helper()
	want := map[string]bool{
		"provider_visible_prefix": false, "explicit_breakpoint": false, "provider_cache_key": false,
		"positive_cache_fill": false, "provider_native_fill_write_fields": false,
		"native_provider_request_identity": false, "native_provider_request_count": false, "cache_isolation": false,
	}
	for _, capability := range got {
		if _, ok := want[capability]; !ok {
			t.Errorf("unexpected unavailable capability %q", capability)
		} else {
			want[capability] = true
		}
	}
	for capability, seen := range want {
		if !seen {
			t.Errorf("capability %q was not kept unavailable", capability)
		}
	}
}

func TestOpenCodeRuntimeControlIgnoredRawHashes(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var evidence runtimeControlEvidence
	readJSON(t, filepath.Join(root, "tools", "bench", "evidence", "opencode-runtime-cache-controls.json"), &evidence)
	artifactRoot := filepath.Join(root, filepath.FromSlash(evidence.IgnoredRoot))
	if _, err := os.Stat(artifactRoot); os.IsNotExist(err) {
		t.Skip("ignored raw artifacts are optional")
	} else if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range evidence.IgnoredArtifacts {
		checkHash(t, filepath.Join(artifactRoot, filepath.FromSlash(artifact.Path)), artifact.SHA256)
	}
}

func validatePreflight(t *testing.T, path string, invocations, wall int) {
	t.Helper()
	var preflight struct {
		Status          string `json:"status"`
		NetworkActivity string `json:"network_activity"`
		ModelCalls      int    `json:"model_calls"`
		Bounds          struct {
			Invocations struct{ Maximum int }     `json:"opencode_invocations"`
			Retries     struct{ Maximum int }     `json:"intentional_retries"`
			Spend       struct{ Maximum float64 } `json:"spend_usd"`
			Wall        struct{ Maximum int }     `json:"wall_time_seconds"`
		} `json:"bounds"`
	}
	readJSON(t, path, &preflight)
	if preflight.Status != "ready" || preflight.NetworkActivity != "none" || preflight.ModelCalls != 0 || preflight.Bounds.Invocations.Maximum != invocations || preflight.Bounds.Retries.Maximum != 0 || preflight.Bounds.Spend.Maximum != 0 || preflight.Bounds.Wall.Maximum != wall {
		t.Fatalf("invalid no-call preflight %s: %#v", path, preflight)
	}
}

func readSourceExcerpts(t *testing.T, path string) []sourceExcerpt {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var excerpts []sourceExcerpt
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var excerpt sourceExcerpt
		if err := json.Unmarshal(scanner.Bytes(), &excerpt); err != nil {
			t.Fatal(err)
		}
		excerpts = append(excerpts, excerpt)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return excerpts
}

func validateControlsAndOfflineClasses(t *testing.T, evidence runtimeControlEvidence) {
	t.Helper()
	wantDimensions := map[string]bool{"runtime_session_key": false, "submitted_user_prefix": false, "provider_visible_prefix": false, "request_count": false, "cache_classification": false}
	for _, control := range evidence.Controls {
		if _, ok := wantDimensions[control.Dimension]; !ok || len(control.Accepted) == 0 || len(control.Unavailable) == 0 {
			t.Errorf("invalid control dimension %q", control.Dimension)
		} else {
			wantDimensions[control.Dimension] = true
		}
	}
	for dimension, seen := range wantDimensions {
		if !seen {
			t.Errorf("missing dimension %q", dimension)
		}
	}
	wantClasses := map[string]bool{"cache_reuse": false, "cache_fill": false, "no_reported_reuse": false, "ineligible": false, "unknown": false}
	for _, c := range evidence.ClassificationCases {
		if got := classifyRuntimeCache(c.Input); got != c.Expected {
			t.Errorf("classification=%q want %q", got, c.Expected)
		}
		wantClasses[c.Expected] = true
	}
	for class, seen := range wantClasses {
		if !seen {
			t.Errorf("missing classification fixture %q", class)
		}
	}
}

func validateTrackedArtifacts(t *testing.T, root string, evidence runtimeControlEvidence) {
	t.Helper()
	secret := regexp.MustCompile(`(?i)Authorization:|Bearer\s+[A-Za-z0-9._-]{8,}|"(?:access|refresh)"\s*:\s*"|sk-[A-Za-z0-9]|OPENCODE_API_KEY=|ses_[A-Za-z0-9]|msg_[A-Za-z0-9]|prt_[A-Za-z0-9]`)
	for _, artifact := range evidence.TrackedArtifacts {
		path := filepath.Join(root, filepath.FromSlash(artifact.Path))
		checkHash(t, path, artifact.SHA256)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if secret.Match(content) {
			t.Errorf("sensitive or raw identifier pattern in %s", artifact.Path)
		}
	}
	probe, err := os.ReadFile(filepath.Join(root, "tools", "bench", "evidence", "opencode-runtime-cache-controls", "probe.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(probe, []byte("-e OPENCODE_API_KEY")) || bytes.Contains(probe, []byte("$OPENCODE_API_KEY")) || bytes.Contains(probe, []byte("OPENCODE_API_KEY=")) {
		t.Error("probe must pass auth without reading or assigning its value")
	}
}

func validatePromptHashes(t *testing.T, evidence runtimeControlEvidence) {
	t.Helper()
	common := make([]string, 700)
	for i := range common {
		common[i] = fmt.Sprintf("synthetic-cache-control-token-%04d", i)
	}
	prefix := strings.Join(common, " ") + "\nReturn exactly ACK-"
	want := map[string]bool{}
	for _, suffix := range []string{"A.\n", "B.\n"} {
		hash := sha256.Sum256([]byte(prefix + suffix))
		want[hex.EncodeToString(hash[:])] = false
	}
	for _, observation := range evidence.Observations {
		if _, ok := want[observation.PromptSHA256]; ok {
			want[observation.PromptSHA256] = true
		}
	}
	for hash, seen := range want {
		if !seen {
			t.Errorf("generated prompt hash not retained: %s", hash)
		}
	}
}

func validateAdditionalAccounting(t *testing.T, root string) {
	t.Helper()
	var accounting struct {
		Bounds struct {
			Invocations struct{ Actual, Maximum int } `json:"opencode_invocations"`
			Steps       struct{ Actual, Maximum int } `json:"successful_model_steps"`
			Retries     struct{ Actual, Maximum int } `json:"intentional_retries"`
			Spend       struct{ Actual, Maximum int } `json:"spend_usd"`
			Wall        struct{ Actual, Maximum int } `json:"wall_time_seconds"`
		} `json:"bounds"`
		NativeCount string `json:"native_provider_request_count"`
	}
	readJSON(t, filepath.Join(root, "tools", "bench", "evidence", "opencode-runtime-cache-controls", "new-probe-accounting.json"), &accounting)
	if accounting.Bounds.Invocations.Actual != 2 || accounting.Bounds.Invocations.Maximum != 2 || accounting.Bounds.Steps.Actual != 1 || accounting.Bounds.Steps.Maximum != 2 || accounting.Bounds.Retries.Actual != 0 || accounting.Bounds.Retries.Maximum != 0 || accounting.Bounds.Spend.Actual != 0 || accounting.Bounds.Spend.Maximum != 0 || accounting.Bounds.Wall.Actual != 12 || accounting.Bounds.Wall.Maximum != 120 || accounting.NativeCount != "unavailable" {
		t.Fatalf("incorrect additional probe accounting: %#v", accounting)
	}
}

func readJSON(t *testing.T, path string, target any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatal(err)
	}
}

func checkHash(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := sha256.Sum256(content)
	if hex.EncodeToString(got[:]) != want {
		t.Errorf("hash mismatch for %s", path)
	}
}

func sameCacheInput(a, b cacheInput) bool {
	return equalInt64(a.CacheRead, b.CacheRead) && equalInt64(a.CacheWrite, b.CacheWrite) && equalBool(a.Eligible, b.Eligible)
}

func equalInt64(a, b *int64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func equalBool(a, b *bool) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func classifyRuntimeCache(input cacheInput) string {
	if input.Eligible != nil && !*input.Eligible {
		return "ineligible"
	}
	if input.CacheRead != nil && *input.CacheRead > 0 {
		return "cache_reuse"
	}
	if input.CacheWrite != nil && *input.CacheWrite > 0 {
		return "cache_fill"
	}
	if input.CacheRead != nil && input.CacheWrite != nil && *input.CacheRead == 0 && *input.CacheWrite == 0 {
		return "no_reported_reuse"
	}
	return "unknown"
}
