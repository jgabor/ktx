package cacheevidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestTrackedLunaEvidenceIsIndependentlyReviewable(t *testing.T) {
	trackedPath := filepath.Join("..", "..", "tools", "bench", "evidence", "openai-opencode-mediated-luna-probe.json")
	b, err := os.ReadFile(trackedPath)
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		SchemaVersion int `json:"schema_version"`
		Bounds        struct {
			Invocations struct {
				Actual  int `json:"actual"`
				Maximum int `json:"maximum"`
			} `json:"opencode_invocations"`
			Retries struct {
				Actual  int `json:"actual"`
				Maximum int `json:"maximum"`
			} `json:"intentional_retries"`
			WallTime struct {
				Actual                int `json:"actual"`
				Maximum               int `json:"maximum"`
				MeasurementResolution int `json:"measurement_resolution_seconds"`
			} `json:"wall_time_seconds"`
		} `json:"bounds"`
		Invocations []struct {
			Ordinal                  int      `json:"ordinal"`
			ExitStatus               int      `json:"exit_status"`
			ElapsedSeconds           int      `json:"elapsed_seconds"`
			ModelSteps               int      `json:"model_steps"`
			ProviderRequestsObserved int      `json:"provider_requests_observed"`
			Command                  string   `json:"command"`
			FailureClass             string   `json:"failure_class"`
			SanitizedStderrExcerpt   []string `json:"sanitized_stderr_excerpt"`
		} `json:"invocations"`
		Result struct {
			SuccessfulInvocations    int                    `json:"successful_invocations"`
			ModelSteps               int                    `json:"model_steps"`
			ProviderRequestsObserved int                    `json:"provider_requests_observed"`
			ReportedCost             struct{ State string } `json:"reported_cost_usd"`
			EstimatedCost            struct{ State string } `json:"estimated_api_cost_usd"`
		} `json:"result"`
		IgnoredRoot string                          `json:"ignored_artifact_root"`
		Artifacts   []struct{ Path, SHA256 string } `json:"artifacts"`
		Redaction   struct {
			Result                   string `json:"result"`
			CredentialValuesRetained bool   `json:"credential_values_retained"`
			RawNamespaceRetained     bool   `json:"raw_cache_namespace_retained"`
		} `json:"redaction"`
		ClaimLimits []string `json:"claim_limits"`
	}
	if err := json.Unmarshal(b, &evidence); err != nil {
		t.Fatalf("decode tracked evidence: %v", err)
	}
	if evidence.SchemaVersion != 2 || evidence.Bounds.Invocations.Actual != 2 || evidence.Bounds.Invocations.Maximum != 2 || evidence.Bounds.Retries.Actual != 0 || evidence.Bounds.Retries.Maximum != 0 || evidence.Bounds.WallTime.Actual != 0 || evidence.Bounds.WallTime.Maximum != 120 || evidence.Bounds.WallTime.MeasurementResolution != 1 {
		t.Fatalf("tracked bounds are incomplete: %#v", evidence.Bounds)
	}
	if len(evidence.Invocations) != 2 {
		t.Fatalf("invocations = %d, want 2", len(evidence.Invocations))
	}
	wantFailures := []string{"EACCES", "EROFS"}
	for i, invocation := range evidence.Invocations {
		if invocation.Ordinal != i+1 || invocation.ExitStatus != 1 || invocation.ElapsedSeconds != 0 || invocation.ModelSteps != 0 || invocation.ProviderRequestsObserved != 0 || invocation.FailureClass != "container_startup_unavailable" {
			t.Errorf("invocation %d is incomplete: %#v", i+1, invocation)
		}
		for _, fragment := range []string{"timeout --signal=TERM 55s docker run", "auth.json:ro", "openai/gpt-5.6-luna", "--variant high", "ktx-cache-evidence-"} {
			if !strings.Contains(invocation.Command, fragment) {
				t.Errorf("invocation %d command lacks %q", i+1, fragment)
			}
		}
		if len(invocation.SanitizedStderrExcerpt) == 0 || !strings.Contains(invocation.SanitizedStderrExcerpt[0], wantFailures[i]) {
			t.Errorf("invocation %d sanitized failure excerpt = %#v", i+1, invocation.SanitizedStderrExcerpt)
		}
	}
	if evidence.Result.SuccessfulInvocations != 0 || evidence.Result.ModelSteps != 0 || evidence.Result.ProviderRequestsObserved != 0 || evidence.Result.ReportedCost.State != "unavailable" || evidence.Result.EstimatedCost.State != "unavailable" {
		t.Errorf("tracked result overclaims unavailable evidence: %#v", evidence.Result)
	}
	if len(evidence.Artifacts) != 11 || evidence.Redaction.Result != "pass" || evidence.Redaction.CredentialValuesRetained || evidence.Redaction.RawNamespaceRetained || len(evidence.ClaimLimits) == 0 {
		t.Errorf("artifact/redaction/claim evidence is incomplete")
	}
	seen := map[string]bool{}
	for _, artifact := range evidence.Artifacts {
		decoded, err := hex.DecodeString(artifact.SHA256)
		if err != nil || len(decoded) != sha256.Size || artifact.Path == "" || seen[artifact.Path] {
			t.Errorf("invalid artifact entry: %#v", artifact)
		}
		seen[artifact.Path] = true
	}

	root := filepath.Join("..", "..", filepath.FromSlash(evidence.IgnoredRoot))
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	for _, artifact := range evidence.Artifacts {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(artifact.Path)))
		if err != nil {
			t.Fatalf("read retained artifact %s: %v", artifact.Path, err)
		}
		got := sha256.Sum256(content)
		if hex.EncodeToString(got[:]) != artifact.SHA256 {
			t.Errorf("retained artifact %s hash mismatch", artifact.Path)
		}
		for i, stderrPath := range []string{"raw/invocation-1.stderr", "raw/invocation-2.stderr"} {
			if artifact.Path == stderrPath && !strings.Contains(string(content), evidence.Invocations[i].SanitizedStderrExcerpt[0]) {
				t.Errorf("tracked invocation %d excerpt is absent from retained stderr", i+1)
			}
		}
	}
	secretPattern := regexp.MustCompile(`(?i)Authorization:|Bearer\s+[A-Za-z0-9._-]{8,}|"(?:access|refresh)"\s*:\s*"|sk-[A-Za-z0-9]`)
	for _, subdir := range []string{"raw", "normalized"} {
		err := filepath.WalkDir(filepath.Join(root, subdir), func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if secretPattern.Match(content) {
				t.Errorf("sensitive credential pattern found in %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("validated %d tracked artifact hashes and redaction scope against %s", len(evidence.Artifacts), evidence.IgnoredRoot)
}
