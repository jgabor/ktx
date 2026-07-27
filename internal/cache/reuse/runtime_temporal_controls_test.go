package reuse

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type temporalEvidence struct {
	SchemaVersion int    `json:"schema_version"`
	Status        string `json:"status"`
	Source        struct {
		Runtime             string `json:"runtime"`
		RuntimeVersion      string `json:"runtime_version"`
		Model               string `json:"model"`
		DirectProviderCalls int    `json:"direct_provider_calls"`
	} `json:"source"`
	PreflightBounds  temporalAccounting `json:"preflight_bounds"`
	ActualAccounting temporalAccounting `json:"actual_live_accounting"`
	Sequential       struct {
		Classification string `json:"execution_classification"`
		LiveCallNeeded bool   `json:"live_call_needed"`
		EvidencePath   string `json:"evidence_path"`
		EvidenceHash   string `json:"evidence_sha256"`
		EventsPath     string `json:"source_events_path"`
		EventsHash     string `json:"source_events_sha256"`
		Order          []int  `json:"execution_order"`
		Steps          []int  `json:"runtime_visible_model_steps"`
		TotalSteps     int    `json:"total_runtime_visible_model_steps"`
	} `json:"sequential"`
	Concurrent struct {
		Classification  string `json:"execution_classification"`
		RequestedMode   string `json:"requested_mode"`
		LaunchOrder     []int  `json:"launch_order"`
		CollectionOrder []int  `json:"collection_order"`
		Steps           []int  `json:"runtime_visible_model_steps"`
		TotalSteps      int    `json:"total_runtime_visible_model_steps"`
	} `json:"concurrent"`
	TTL struct {
		Classification    string `json:"classification"`
		RuntimeField      string `json:"runtime_field"`
		ComparisonCeiling int    `json:"comparison_ceiling_seconds"`
		IntentionalWait   int    `json:"intentional_wait_seconds"`
		Source            string `json:"source"`
	} `json:"ttl"`
	Vocabulary           map[string]string               `json:"classification_vocabulary"`
	ClassificationCounts map[string]int                  `json:"classification_counts"`
	UnavailableFields    []string                        `json:"unavailable_fields"`
	TrackedArtifacts     []struct{ Path, SHA256 string } `json:"tracked_artifacts"`
	Redaction            struct {
		Credentials bool `json:"credential_values_retained"`
		Prompts     bool `json:"raw_prompt_content_retained_in_evidence"`
		Namespaces  bool `json:"session_or_cache_namespaces_retained"`
		MessageIDs  bool `json:"message_or_part_ids_retained"`
		RawTracked  bool `json:"raw_artifacts_tracked"`
	} `json:"redaction"`
}

type temporalAccounting struct {
	Invocations int `json:"opencode_invocations"`
	Attempts    int `json:"invocation_attempts"`
	Steps       int `json:"successful_model_steps"`
	Retries     int `json:"intentional_retries"`
	Spend       int `json:"spend_usd"`
	Wall        int `json:"wall_time_seconds"`
}

type temporalSourceEvent struct {
	InvocationOrdinal int     `json:"invocation_ordinal"`
	LaunchOrdinal     int     `json:"launch_ordinal"`
	SourceOrdinal     int     `json:"source_event_ordinal"`
	ModelStepOrdinal  int     `json:"model_step_ordinal"`
	EventType         string  `json:"event_type"`
	ExitStatus        int     `json:"exit_status"`
	Elapsed           int     `json:"elapsed_seconds"`
	RuntimeVersion    string  `json:"runtime_version"`
	Model             string  `json:"model"`
	UncachedInput     int64   `json:"uncached_input"`
	CacheRead         int64   `json:"cache_read"`
	CacheWrite        int64   `json:"cache_write"`
	Output            int64   `json:"output"`
	Reasoning         int64   `json:"reasoning"`
	Cost              float64 `json:"cost_usd"`
	Classification    string  `json:"classification"`
}

func TestOpenCodeRuntimeTemporalControlEvidence(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	base := filepath.Join(root, "tools", "bench", "evidence")
	validateTemporalPreflight(t, filepath.Join(base, "opencode-runtime-temporal-controls-preflight.json"))
	var evidence temporalEvidence
	readJSON(t, filepath.Join(base, "opencode-runtime-temporal-controls.json"), &evidence)

	if evidence.SchemaVersion != 1 || evidence.Status != "runtime_temporal_controls_complete" || evidence.Source.Runtime != "opencode" || evidence.Source.RuntimeVersion != "1.18.4" || evidence.Source.Model != "opencode/big-pickle" || evidence.Source.DirectProviderCalls != 0 {
		t.Fatalf("invalid temporal evidence source or status: %#v", evidence)
	}
	if evidence.PreflightBounds != (temporalAccounting{Invocations: 2, Attempts: 2, Steps: 2, Wall: 60}) {
		t.Fatalf("preflight bounds = %#v", evidence.PreflightBounds)
	}
	if evidence.ActualAccounting != (temporalAccounting{Invocations: 2, Attempts: 2, Steps: 2, Wall: 25}) {
		t.Fatalf("actual accounting = %#v", evidence.ActualAccounting)
	}

	if evidence.Sequential.Classification != "observed" || evidence.Sequential.LiveCallNeeded || fmt.Sprint(evidence.Sequential.Order) != "[2 3]" || fmt.Sprint(evidence.Sequential.Steps) != "[1 1]" || evidence.Sequential.TotalSteps != 2 {
		t.Fatalf("sequential evidence = %#v", evidence.Sequential)
	}
	checkHash(t, filepath.Join(root, filepath.FromSlash(evidence.Sequential.EvidencePath)), evidence.Sequential.EvidenceHash)
	checkHash(t, filepath.Join(root, filepath.FromSlash(evidence.Sequential.EventsPath)), evidence.Sequential.EventsHash)
	if evidence.Concurrent.Classification != "observed" || evidence.Concurrent.RequestedMode != "concurrent" || fmt.Sprint(evidence.Concurrent.LaunchOrder) != "[1 2]" || fmt.Sprint(evidence.Concurrent.CollectionOrder) != "[1 2]" || fmt.Sprint(evidence.Concurrent.Steps) != "[1 1]" || evidence.Concurrent.TotalSteps != 2 {
		t.Fatalf("concurrent evidence = %#v", evidence.Concurrent)
	}

	eventsPath := filepath.Join(base, "opencode-runtime-temporal-controls", "source-events.jsonl")
	events := readTemporalSourceEvents(t, eventsPath)
	if len(events) != 2 {
		t.Fatalf("source event count = %d", len(events))
	}
	for i, event := range events {
		if event.InvocationOrdinal != i+1 || event.LaunchOrdinal != i+1 || event.SourceOrdinal != 3 || event.ModelStepOrdinal != 1 || event.EventType != "step_finish" || event.ExitStatus != 0 || event.Elapsed <= 0 || event.RuntimeVersion != "1.18.4" || event.Model != "opencode/big-pickle" || event.UncachedInput != 4699 || event.CacheRead != 0 || event.CacheWrite != 0 || event.Output != 7 || event.Reasoning <= 0 || event.Cost != 0 || event.Classification != "observed" {
			t.Errorf("source event %d = %#v", i+1, event)
		}
	}

	validateTemporalAccounting(t, filepath.Join(base, "opencode-runtime-temporal-controls", "accounting.json"), events)
	validateTemporalClassifications(t, evidence)
	for _, artifact := range evidence.TrackedArtifacts {
		checkHash(t, filepath.Join(root, filepath.FromSlash(artifact.Path)), artifact.SHA256)
	}
	if evidence.Redaction.Credentials || evidence.Redaction.Prompts || evidence.Redaction.Namespaces || evidence.Redaction.MessageIDs || evidence.Redaction.RawTracked {
		t.Fatalf("redaction declaration = %#v", evidence.Redaction)
	}
	validateTemporalRedaction(t, filepath.Join(base, "opencode-runtime-temporal-controls.json"), eventsPath, filepath.Join(base, "opencode-runtime-temporal-controls", "accounting.json"))
}

func validateTemporalPreflight(t *testing.T, path string) {
	t.Helper()
	var preflight struct {
		Status          string `json:"status"`
		NetworkActivity string `json:"network_activity"`
		ModelCalls      int    `json:"model_calls"`
		Source          struct {
			Runtime             string `json:"runtime"`
			RuntimeVersion      string `json:"runtime_version"`
			Model               string `json:"model"`
			DirectProviderCalls bool   `json:"direct_provider_calls"`
		} `json:"source"`
		Bounds map[string]struct {
			Actual  int `json:"actual_before_live"`
			Maximum int `json:"maximum"`
		} `json:"bounds"`
		TTL struct {
			Classification string `json:"classification"`
			Wait           int    `json:"intentional_wait_seconds"`
		} `json:"ttl"`
	}
	readJSON(t, path, &preflight)
	if preflight.Status != "ready" || preflight.NetworkActivity != "none" || preflight.ModelCalls != 0 || preflight.Source.Runtime != "opencode" || preflight.Source.RuntimeVersion != "1.18.4" || preflight.Source.Model != "opencode/big-pickle" || preflight.Source.DirectProviderCalls || preflight.TTL.Classification != "unavailable" || preflight.TTL.Wait != 0 {
		t.Fatalf("invalid temporal preflight: %#v", preflight)
	}
	wantMax := map[string]int{"opencode_invocations": 2, "invocation_attempts": 2, "successful_model_steps": 2, "intentional_retries": 0, "spend_usd": 0, "wall_time_seconds": 60}
	for name, maximum := range wantMax {
		bound, ok := preflight.Bounds[name]
		if !ok || bound.Actual != 0 || bound.Maximum != maximum {
			t.Errorf("preflight bound %q = %#v", name, bound)
		}
	}
}

func readTemporalSourceEvents(t *testing.T, path string) []temporalSourceEvent {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var events []temporalSourceEvent
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event temporalSourceEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func validateTemporalAccounting(t *testing.T, path string, events []temporalSourceEvent) {
	t.Helper()
	var accounting struct {
		Bounds struct {
			Invocations struct{ Actual, Maximum int } `json:"opencode_invocations"`
			Attempts    struct{ Actual, Maximum int } `json:"invocation_attempts"`
			Steps       struct{ Actual, Maximum int } `json:"successful_model_steps"`
			Retries     struct{ Actual, Maximum int } `json:"intentional_retries"`
			Spend       struct{ Actual, Maximum int } `json:"spend_usd"`
			Wall        struct{ Actual, Maximum int } `json:"wall_time_seconds"`
		} `json:"bounds"`
		Execution struct {
			Mode, CompletionOrder, Timestamps, Ownership string
			LaunchOrder, CollectionOrder                 []int
		} `json:"-"`
		Invocations []struct {
			Ordinal, ExitStatus, Elapsed, Steps int
			Result                              string
		} `json:"-"`
		NativeCount, RetryRelationships string
	}
	readJSON(t, path, &accounting)
	if accounting.Bounds.Invocations.Actual != 2 || accounting.Bounds.Invocations.Maximum != 2 || accounting.Bounds.Attempts.Actual != 2 || accounting.Bounds.Attempts.Maximum != 2 || accounting.Bounds.Steps.Actual != len(events) || accounting.Bounds.Steps.Maximum != 2 || accounting.Bounds.Retries.Actual != 0 || accounting.Bounds.Retries.Maximum != 0 || accounting.Bounds.Spend.Actual != 0 || accounting.Bounds.Spend.Maximum != 0 || accounting.Bounds.Wall.Actual != 25 || accounting.Bounds.Wall.Maximum != 60 {
		t.Fatalf("tracked accounting bounds = %#v", accounting.Bounds)
	}
	var raw map[string]any
	readJSON(t, path, &raw)
	execution := raw["execution"].(map[string]any)
	if execution["requested_mode"] != "concurrent" || execution["completion_order"] != "unavailable" || execution["authoritative_timestamps"] != "unavailable" || execution["concurrency_ownership"] != "unavailable" || raw["native_provider_request_count"] != "unavailable" || raw["retry_relationships"] != "unavailable" {
		t.Fatalf("unavailable accounting fields were upgraded: %#v", raw)
	}
}

func validateTemporalClassifications(t *testing.T, evidence temporalEvidence) {
	t.Helper()
	for _, class := range []string{"observed", "unavailable", "failed-before-model-step", "unknown"} {
		if evidence.Vocabulary[class] == "" {
			t.Errorf("classification %q is undefined", class)
		}
	}
	wantUnavailable := []string{"provider_request_identity", "provider_request_count", "retry_relationships", "authoritative_timestamps", "concurrency_ownership", "separation"}
	if strings.Join(evidence.UnavailableFields, ",") != strings.Join(wantUnavailable, ",") || evidence.ClassificationCounts["observed"] != 2 || evidence.ClassificationCounts["unavailable"] != len(wantUnavailable) || evidence.ClassificationCounts["failed-before-model-step"] != 0 || evidence.ClassificationCounts["unknown"] != 0 {
		t.Errorf("classifications are not reproducible: fields=%v counts=%v", evidence.UnavailableFields, evidence.ClassificationCounts)
	}
	if evidence.TTL.Classification != "unavailable" || evidence.TTL.RuntimeField != "unavailable" || evidence.TTL.ComparisonCeiling != 60 || evidence.TTL.IntentionalWait != 0 || !strings.Contains(evidence.TTL.Source, "49c69c5ed3ccf706b61b3febb43c8aaff7f8325e") {
		t.Errorf("TTL boundary = %#v", evidence.TTL)
	}
}

func validateTemporalRedaction(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range [][]byte{[]byte("sessionID"), []byte("messageID"), []byte("\"part\""), []byte("ses_"), []byte("msg_"), []byte("prt_"), []byte("prompt_sha256"), []byte("TEMPORAL-A"), []byte("TEMPORAL-B"), []byte("synthetic-temporal-control-token-")} {
			if bytes.Contains(raw, forbidden) {
				t.Errorf("%s retains forbidden raw value %q", path, forbidden)
			}
		}
	}
}
