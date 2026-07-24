package cacheevidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

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
	} `json:"bounds"`
	Observations []struct {
		Ordinal            int      `json:"ordinal"`
		RequestedTurn      int      `json:"requested_turn"`
		ObservedModelSteps int      `json:"observed_model_steps"`
		SessionState       string   `json:"session_state"`
		SessionSHA256      string   `json:"session_sha256"`
		PromptSHA256       string   `json:"prompt_sha256"`
		Outcomes           []string `json:"cache_outcomes"`
		FieldCompleteness  struct {
			CacheRead     string `json:"cache_read"`
			CacheWrite    string `json:"cache_write"`
			UncachedInput string `json:"uncached_input"`
			Output        string `json:"output"`
			Cost          string `json:"cost"`
		} `json:"field_completeness"`
	} `json:"observations"`
	Controls []struct {
		Dimension string `json:"dimension"`
		Accepted  []struct {
			Case string `json:"case"`
		} `json:"accepted_cases"`
		Unavailable []struct {
			Case string `json:"case"`
		} `json:"unavailable_cases"`
	} `json:"controls"`
	ClassificationCases []struct {
		Input struct {
			CacheRead  *int64 `json:"cache_read"`
			CacheWrite *int64 `json:"cache_write"`
			Eligible   *bool  `json:"eligible"`
		} `json:"input"`
		Expected string `json:"expected"`
	} `json:"classification_cases"`
	IgnoredRoot string                          `json:"ignored_artifact_root"`
	Artifacts   []struct{ Path, SHA256 string } `json:"artifacts"`
	Redaction   struct {
		Result                   string `json:"result"`
		AuthMode                 string `json:"auth_mode"`
		CredentialValuesRetained bool   `json:"credential_values_retained"`
		RawKeysRetained          bool   `json:"raw_keys_or_namespaces_retained"`
	} `json:"redaction"`
}

func TestOpenCodeRuntimeControlEvidence(t *testing.T) {
	root := filepath.Join("..", "..")
	preflightRaw, err := os.ReadFile(filepath.Join(root, "tools", "bench", "evidence", "opencode-runtime-cache-controls-preflight.json"))
	if err != nil {
		t.Fatal(err)
	}
	var preflight struct {
		Status          string `json:"status"`
		NetworkActivity string `json:"network_activity"`
		ModelCalls      int    `json:"model_calls"`
		Bounds          struct {
			Invocations struct{ Maximum int }     `json:"opencode_invocations"`
			Attempts    struct{ Maximum int }     `json:"invocation_attempts"`
			Retries     struct{ Maximum int }     `json:"intentional_retries"`
			Spend       struct{ Maximum float64 } `json:"spend_usd"`
			Wall        struct{ Maximum int }     `json:"wall_time_seconds"`
		} `json:"bounds"`
	}
	if err := json.Unmarshal(preflightRaw, &preflight); err != nil {
		t.Fatal(err)
	}
	if preflight.Status != "ready" || preflight.NetworkActivity != "none" || preflight.ModelCalls != 0 || preflight.Bounds.Invocations.Maximum != 4 || preflight.Bounds.Attempts.Maximum != 4 || preflight.Bounds.Retries.Maximum != 0 || preflight.Bounds.Spend.Maximum != 0 || preflight.Bounds.Wall.Maximum != 300 {
		t.Fatalf("invalid no-call preflight: %#v", preflight)
	}

	raw, err := os.ReadFile(filepath.Join(root, "tools", "bench", "evidence", "opencode-runtime-cache-controls.json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence runtimeControlEvidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.SchemaVersion != 1 || evidence.Status == "" || evidence.Source.Runtime != "opencode" || evidence.Source.RuntimeVersion != "1.18.4" || evidence.Source.Model != "opencode/big-pickle" {
		t.Fatalf("invalid source: %#v", evidence.Source)
	}
	if evidence.Bounds.Invocations.Actual > 4 || evidence.Bounds.Invocations.Maximum != 4 || evidence.Bounds.Attempts.Actual > 4 || evidence.Bounds.Attempts.Maximum != 4 || evidence.Bounds.Retries.Actual != 0 || evidence.Bounds.Retries.Maximum != 0 || evidence.Bounds.Spend.Actual != 0 || evidence.Bounds.Spend.Maximum != 0 || evidence.Bounds.Wall.Actual > 300 || evidence.Bounds.Wall.Maximum != 300 {
		t.Fatalf("bounds exceeded: %#v", evidence.Bounds)
	}
	if len(evidence.Observations) != evidence.Bounds.Invocations.Actual {
		t.Fatalf("observations=%d invocations=%d", len(evidence.Observations), evidence.Bounds.Invocations.Actual)
	}
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for i, observation := range evidence.Observations {
		if observation.Ordinal != i+1 || observation.RequestedTurn < 0 || observation.ObservedModelSteps < 0 || !hex64.MatchString(observation.PromptSHA256) || len(observation.Outcomes) == 0 {
			t.Errorf("incomplete observation %d: %#v", i+1, observation)
		}
		if observation.SessionState == "measured" && !hex64.MatchString(observation.SessionSHA256) {
			t.Errorf("observation %d lacks hashed measured session", i+1)
		}
		if observation.SessionState != "measured" && observation.SessionState != "unknown" {
			t.Errorf("observation %d session state=%q", i+1, observation.SessionState)
		}
		for _, state := range []string{observation.FieldCompleteness.CacheRead, observation.FieldCompleteness.CacheWrite, observation.FieldCompleteness.UncachedInput, observation.FieldCompleteness.Output, observation.FieldCompleteness.Cost} {
			if state != "measured" && state != "unknown" && state != "malformed" {
				t.Errorf("invalid field state %q", state)
			}
		}
	}
	wantDimensions := map[string]bool{"runtime_session_key": false, "submitted_user_prefix": false, "provider_visible_prefix": false, "request_count": false, "cache_classification": false}
	for _, control := range evidence.Controls {
		if _, ok := wantDimensions[control.Dimension]; !ok {
			t.Errorf("unexpected dimension %q", control.Dimension)
			continue
		}
		if len(control.Accepted) == 0 || len(control.Unavailable) == 0 {
			t.Errorf("dimension %q lacks accepted or unavailable opposite", control.Dimension)
		}
		wantDimensions[control.Dimension] = true
	}
	for dimension, seen := range wantDimensions {
		if !seen {
			t.Errorf("missing dimension %q", dimension)
		}
	}
	wantClasses := map[string]bool{"cache_reuse": false, "cache_fill": false, "no_reported_reuse": false, "ineligible": false, "unknown": false}
	for _, c := range evidence.ClassificationCases {
		if got := classifyRuntimeCache(c.Input.CacheRead, c.Input.CacheWrite, c.Input.Eligible); got != c.Expected {
			t.Errorf("classification=%q want %q for %#v", got, c.Expected, c.Input)
		}
		if _, ok := wantClasses[c.Expected]; ok {
			wantClasses[c.Expected] = true
		}
	}
	for class, seen := range wantClasses {
		if !seen {
			t.Errorf("missing classification case %q", class)
		}
	}
	if evidence.Redaction.Result != "pass" || evidence.Redaction.AuthMode != "read_only" || evidence.Redaction.CredentialValuesRetained || evidence.Redaction.RawKeysRetained {
		t.Errorf("redaction failure: %#v", evidence.Redaction)
	}
	for _, artifact := range evidence.Artifacts {
		decoded, err := hex.DecodeString(artifact.SHA256)
		if err != nil || len(decoded) != sha256.Size || artifact.Path == "" {
			t.Errorf("invalid artifact: %#v", artifact)
		}
	}
	artifactRoot := filepath.Join(root, filepath.FromSlash(evidence.IgnoredRoot))
	if _, err := os.Stat(artifactRoot); err == nil {
		for _, artifact := range evidence.Artifacts {
			content, err := os.ReadFile(filepath.Join(artifactRoot, filepath.FromSlash(artifact.Path)))
			if err != nil {
				t.Fatalf("read retained artifact %s: %v", artifact.Path, err)
			}
			got := sha256.Sum256(content)
			if hex.EncodeToString(got[:]) != artifact.SHA256 {
				t.Errorf("retained artifact %s hash mismatch", artifact.Path)
			}
		}
		secret := regexp.MustCompile(`(?i)Authorization:|Bearer\s+[A-Za-z0-9._-]{8,}|"(?:access|refresh)"\s*:\s*"|sk-[A-Za-z0-9]|OPENCODE_API_KEY=`)
		err := filepath.WalkDir(filepath.Join(artifactRoot, "raw"), func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if secret.Match(content) {
				t.Errorf("sensitive credential pattern found in %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func classifyRuntimeCache(read, write *int64, eligible *bool) string {
	if eligible != nil && !*eligible {
		return "ineligible"
	}
	if read != nil && *read > 0 {
		return "cache_reuse"
	}
	if write != nil && *write > 0 {
		return "cache_fill"
	}
	if read != nil && write != nil && *read == 0 && *write == 0 {
		return "no_reported_reuse"
	}
	return "unknown"
}
