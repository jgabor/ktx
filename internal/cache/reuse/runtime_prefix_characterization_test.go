package reuse

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
)

type prefixCharacterization struct {
	SchemaVersion int    `json:"schema_version"`
	Status        string `json:"status"`
	Source        struct {
		Runtime             string `json:"runtime"`
		RuntimeVersion      string `json:"runtime_version"`
		DirectProviderCalls int    `json:"direct_provider_calls"`
	} `json:"source"`
	References []struct{ Path, SHA256 string } `json:"references"`
	Observed   struct {
		Session struct {
			Availability     string `json:"availability"`
			Source           string `json:"source_reference"`
			SameOrdinals     []int  `json:"same_session_ordinals"`
			SameSHA256       string `json:"same_session_sha256"`
			DistinctOrdinals []int  `json:"distinct_session_ordinals"`
			DistinctSHA256   string `json:"distinct_session_sha256"`
		} `json:"runtime_session_relationship"`
		Prompt struct {
			Availability string `json:"availability"`
			Source       string `json:"source_reference"`
			Groups       []struct {
				Ordinals []int  `json:"ordinals"`
				SHA256   string `json:"sha256"`
			} `json:"groups"`
		} `json:"submitted_prompt_identity"`
		Order struct {
			Availability     string `json:"availability"`
			CacheOrdinals    []int  `json:"cache_control_event_ordinals"`
			SessionStepOrder []int  `json:"same_session_model_step_ordinals"`
			TemporalLaunch   []int  `json:"temporal_launch_order"`
			CompletionOrder  string `json:"completion_order"`
		} `json:"execution_order"`
		Measurements struct {
			Availability string   `json:"availability"`
			Sources      []string `json:"source_references"`
			Ordinals     []int    `json:"cache_control_model_step_ordinals"`
		} `json:"token_cache_measurements"`
	} `json:"observed"`
	Unavailable map[string]string `json:"unavailable"`
	Privacy     map[string]bool   `json:"privacy"`
}

func TestRuntimePrefixCharacterization(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	path := filepath.Join(root, "tools", "bench", "evidence", "opencode-runtime-prefix-characterization.json")
	var characterization prefixCharacterization
	readJSON(t, path, &characterization)
	if characterization.SchemaVersion != 1 || characterization.Status != "runtime_visible_prefix_characterized" || characterization.Source.Runtime != "opencode" || characterization.Source.RuntimeVersion != "1.18.4" || characterization.Source.DirectProviderCalls != 0 {
		t.Fatalf("invalid characterization source or status: %#v", characterization)
	}

	wantReferences := map[string]bool{
		"tools/bench/evidence/opencode-runtime-cache-controls.json":                   false,
		"tools/bench/evidence/opencode-runtime-cache-controls/source-events.jsonl":    false,
		"tools/bench/evidence/opencode-runtime-temporal-controls.json":                false,
		"tools/bench/evidence/opencode-runtime-temporal-controls/source-events.jsonl": false,
	}
	for _, reference := range characterization.References {
		if _, ok := wantReferences[reference.Path]; !ok {
			t.Errorf("unexpected evidence reference %q", reference.Path)
			continue
		}
		checkHash(t, filepath.Join(root, filepath.FromSlash(reference.Path)), reference.SHA256)
		wantReferences[reference.Path] = true
	}
	for path, seen := range wantReferences {
		if !seen {
			t.Errorf("missing evidence reference %q", path)
		}
	}

	cacheEvents := readSourceExcerpts(t, filepath.Join(root, filepath.FromSlash(characterization.Observed.Session.Source)))
	validateCharacterizedSessions(t, characterization, cacheEvents)
	validateCharacterizedPrompts(t, characterization, cacheEvents)
	validateCharacterizedOrderAndMeasurements(t, root, characterization, cacheEvents)
	validateCharacterizedBoundaries(t, path, characterization)
}

func validateCharacterizedSessions(t *testing.T, characterization prefixCharacterization, events []sourceExcerpt) {
	t.Helper()
	const eventsPath = "tools/bench/evidence/opencode-runtime-cache-controls/source-events.jsonl"
	if characterization.Observed.Session.Availability != "observed" || characterization.Observed.Session.Source != eventsPath || !reflect.DeepEqual(characterization.Observed.Session.SameOrdinals, []int{2, 3, 6}) || !reflect.DeepEqual(characterization.Observed.Session.DistinctOrdinals, []int{4}) {
		t.Fatalf("session relationship = %#v", characterization.Observed.Session)
	}
	for _, ordinal := range characterization.Observed.Session.SameOrdinals {
		if events[ordinal-1].SessionSHA256 == nil || *events[ordinal-1].SessionSHA256 != characterization.Observed.Session.SameSHA256 {
			t.Errorf("session hash mismatch at ordinal %d", ordinal)
		}
	}
	for _, ordinal := range characterization.Observed.Session.DistinctOrdinals {
		if events[ordinal-1].SessionSHA256 == nil || *events[ordinal-1].SessionSHA256 != characterization.Observed.Session.DistinctSHA256 || characterization.Observed.Session.DistinctSHA256 == characterization.Observed.Session.SameSHA256 {
			t.Errorf("distinct session hash mismatch at ordinal %d", ordinal)
		}
	}
}

func validateCharacterizedPrompts(t *testing.T, characterization prefixCharacterization, events []sourceExcerpt) {
	t.Helper()
	if characterization.Observed.Prompt.Availability != "observed" || characterization.Observed.Prompt.Source != characterization.Observed.Session.Source || len(characterization.Observed.Prompt.Groups) != 2 {
		t.Fatalf("prompt identity = %#v", characterization.Observed.Prompt)
	}
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, group := range characterization.Observed.Prompt.Groups {
		if !hex64.MatchString(group.SHA256) {
			t.Errorf("invalid prompt SHA-256 %q", group.SHA256)
		}
		for _, ordinal := range group.Ordinals {
			if events[ordinal-1].PromptSHA256 != group.SHA256 {
				t.Errorf("prompt hash mismatch at ordinal %d", ordinal)
			}
		}
	}
}

func validateCharacterizedOrderAndMeasurements(t *testing.T, root string, characterization prefixCharacterization, cacheEvents []sourceExcerpt) {
	t.Helper()
	order := characterization.Observed.Order
	if order.Availability != "observed" || !reflect.DeepEqual(order.CacheOrdinals, []int{1, 2, 3, 4, 5, 6}) || !reflect.DeepEqual(order.SessionStepOrder, []int{2, 3, 6}) || !reflect.DeepEqual(order.TemporalLaunch, []int{1, 2}) || order.CompletionOrder != "unavailable" {
		t.Fatalf("execution order = %#v", order)
	}
	for i, event := range cacheEvents {
		if event.Ordinal != order.CacheOrdinals[i] {
			t.Errorf("cache event order mismatch at index %d", i)
		}
	}
	temporalEvents := readTemporalSourceEvents(t, filepath.Join(root, "tools", "bench", "evidence", "opencode-runtime-temporal-controls", "source-events.jsonl"))
	for i, event := range temporalEvents {
		if event.LaunchOrdinal != order.TemporalLaunch[i] {
			t.Errorf("temporal launch order mismatch at index %d", i)
		}
	}
	measurements := characterization.Observed.Measurements
	wantSources := []string{"tools/bench/evidence/opencode-runtime-cache-controls/source-events.jsonl", "tools/bench/evidence/opencode-runtime-temporal-controls/source-events.jsonl"}
	if measurements.Availability != "observed" || !reflect.DeepEqual(measurements.Ordinals, []int{2, 3, 6}) || !reflect.DeepEqual(measurements.Sources, wantSources) {
		t.Fatalf("measurement references = %#v", measurements)
	}
	for _, ordinal := range measurements.Ordinals {
		event := cacheEvents[ordinal-1]
		if event.UncachedInput == nil || event.CacheRead == nil || event.CacheWrite == nil || event.Output == nil || event.Cost == nil {
			t.Errorf("missing retained measurement at ordinal %d", ordinal)
		}
	}
}

func validateCharacterizedBoundaries(t *testing.T, path string, characterization prefixCharacterization) {
	t.Helper()
	wantUnavailable := []string{"provider_visible_prefix", "breakpoint", "tool_schema_segmentation", "ttl", "provider_request_identity", "provider_request_count", "isolation"}
	if len(characterization.Unavailable) != len(wantUnavailable) {
		t.Fatalf("unavailable labels = %#v", characterization.Unavailable)
	}
	for _, field := range wantUnavailable {
		if characterization.Unavailable[field] != "unavailable" {
			t.Errorf("%s availability = %q", field, characterization.Unavailable[field])
		}
	}
	wantPrivacy := []string{"prompt_content_retained", "credentials_retained", "raw_session_or_cache_namespaces_retained", "message_or_part_ids_retained", "additional_source_excerpts_retained"}
	if len(characterization.Privacy) != len(wantPrivacy) {
		t.Fatalf("privacy labels = %#v", characterization.Privacy)
	}
	for _, field := range wantPrivacy {
		retained, ok := characterization.Privacy[field]
		if !ok {
			t.Errorf("missing privacy label %s", field)
			continue
		}
		if retained {
			t.Errorf("privacy field %s unexpectedly retained", field)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{[]byte(".bench-data"), []byte("sessionID"), []byte("messageID"), []byte("ses_"), []byte("msg_"), []byte("prt_"), []byte("OPENCODE_API_KEY"), []byte("synthetic-cache-control-token-")} {
		if bytes.Contains(raw, forbidden) {
			t.Errorf("characterization retains forbidden content %q", forbidden)
		}
	}
}
