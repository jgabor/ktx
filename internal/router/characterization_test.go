package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/config"
	"github.com/jgabor/ktx/internal/db"
)

type routingCharacterization struct {
	SchemaVersion int    `json:"schema_version"`
	Status        string `json:"status"`
	Manifest      struct {
		References []characterizationReference `json:"references"`
		Retained   struct {
			Calls []struct {
				ID               string   `json:"id"`
				CaseID           string   `json:"case_id"`
				EvidenceClass    string   `json:"evidence_class"`
				Runtime          *string  `json:"runtime"`
				ContentSHA256    string   `json:"content_sha256"`
				Signals          []string `json:"signals"`
				CandidateLayer   []string `json:"candidate_layers"`
				CandidateProfile []string `json:"candidate_profiles"`
				MappingOutcome   string   `json:"mapping_outcome"`
			} `json:"calls"`
		} `json:"retained_call_snapshot"`
		Fixtures         []characterizationReference `json:"fixtures"`
		CandidateSignals []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"candidate_signals"`
		Mappings []struct {
			Layer   string `json:"layer"`
			Profile string `json:"profile"`
			Status  string `json:"status"`
		} `json:"candidate_layer_profile_mappings"`
		AmbiguityCases []string `json:"ambiguity_cases"`
		FallbackCases  []string `json:"fallback_cases"`
	} `json:"manifest"`
	Findings struct {
		FixtureCases []fixtureCharacterization `json:"fixture_cases"`
		RoutingCases []routingCase             `json:"routing_cases"`
		Residual     struct {
			EvidenceClass    string `json:"evidence_class"`
			NotRuntimeTokens bool   `json:"not_runtime_tokens"`
		} `json:"residual_spend_estimates"`
		RuntimeSpend struct {
			EvidenceClass string    `json:"evidence_class"`
			Ordinals      []int     `json:"source_event_ordinals"`
			Cost          []float64 `json:"cost_usd"`
		} `json:"runtime_reported_spend"`
		HistoricalSpend struct {
			EvidenceClass string `json:"evidence_class"`
		} `json:"historical_fixture_spend"`
		Followups []struct {
			ID       string `json:"id"`
			Required bool   `json:"required"`
			Status   string `json:"status"`
		} `json:"product_contract_followups"`
	} `json:"findings"`
	FindingsRaw json.RawMessage `json:"-"`
	ClaimLimits []string        `json:"claim_limits"`
	Determinism struct {
		FindingsSHA256 string `json:"findings_sha256"`
	} `json:"determinism"`
}

type characterizationReference struct {
	ID            string `json:"id"`
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	EvidenceClass string `json:"evidence_class"`
}

type fixtureCharacterization struct {
	ID                 string   `json:"id"`
	Bytes              int      `json:"bytes"`
	Lines              int      `json:"lines"`
	JSONDocument       bool     `json:"json_document"`
	DiffMarkers        bool     `json:"diff_markers"`
	LogMarkers         bool     `json:"log_markers"`
	InstructionMarkers bool     `json:"instruction_markers"`
	NDJSONRecords      int      `json:"ndjson_records"`
	CandidateLayers    []string `json:"candidate_layers"`
	CandidateProfiles  []string `json:"candidate_profiles"`
	MappingBasis       string   `json:"mapping_basis"`
	MappingOutcome     string   `json:"mapping_outcome"`
}

type routingCase struct {
	ID       string `json:"id"`
	ToolCall struct {
		Tool    string `json:"tool"`
		Input   string `json:"input"`
		IsError *bool  `json:"is_error"`
	} `json:"tool_call"`
	Routes          []routingCaseRule `json:"routes"`
	DefaultProfile  string            `json:"default_profile"`
	ExpectedProfile string            `json:"expected_profile"`
	Outcome         string            `json:"outcome"`
}

type routingCaseRule struct {
	Tool          string `json:"tool"`
	CommandPrefix string `json:"command_prefix"`
	Subcommand    string `json:"subcommand"`
	ExitStatus    string `json:"exit_status"`
	Profile       string `json:"profile"`
}

func TestRoutingCandidateCharacterization(t *testing.T) {
	root := filepath.Join("..", "..")
	path := filepath.Join(root, "tools", "bench", "evidence", "routing-candidate-characterization.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Findings json.RawMessage `json:"findings"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var c routingCharacterization
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	c.FindingsRaw = envelope.Findings
	if c.SchemaVersion != 1 || c.Status != "routing_candidate_evidence_characterized" {
		t.Fatalf("invalid characterization identity: version=%d status=%q", c.SchemaVersion, c.Status)
	}

	validateCharacterizationReferences(t, root, append(c.Manifest.References, c.Manifest.Fixtures...))
	validateRetainedCallManifest(t, c)
	validateFixtureCases(t, root, c)
	validateRoutingCases(t, c)
	validateCharacterizationBoundaries(t, c)

	var compact bytes.Buffer
	if err := json.Compact(&compact, c.FindingsRaw); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(compact.Bytes())
	gotDigest := hex.EncodeToString(digest[:])
	if gotDigest != c.Determinism.FindingsSHA256 {
		t.Fatalf("findings digest = %s, want %s", gotDigest, c.Determinism.FindingsSHA256)
	}
	t.Logf("findings_sha256=%s", gotDigest)
}

func validateCharacterizationReferences(t *testing.T, root string, references []characterizationReference) {
	t.Helper()
	seen := make(map[string]bool)
	for _, reference := range references {
		if reference.Path == "" || reference.SHA256 == "" || reference.EvidenceClass == "" {
			t.Errorf("incomplete reference: %#v", reference)
			continue
		}
		if seen[reference.Path] {
			continue
		}
		seen[reference.Path] = true
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(reference.Path)))
		if err != nil {
			t.Errorf("read %s: %v", reference.Path, err)
			continue
		}
		if reference.EvidenceClass == "documented_contract" {
			// The retained digest identifies the contract reviewed by the
			// characterization. Current wording has its own contract checks.
			continue
		}
		digest := sha256.Sum256(raw)
		if got := hex.EncodeToString(digest[:]); got != reference.SHA256 {
			t.Errorf("%s sha256 = %s, want %s", reference.Path, got, reference.SHA256)
		}
	}
}

func validateRetainedCallManifest(t *testing.T, c routingCharacterization) {
	t.Helper()
	if len(c.Manifest.Retained.Calls) != 6 {
		t.Fatalf("retained calls = %d, want 6", len(c.Manifest.Retained.Calls))
	}
	seen := make(map[string]bool)
	for _, call := range c.Manifest.Retained.Calls {
		if call.ID == "" || seen[call.ID] || len(call.ContentSHA256) != 64 {
			t.Errorf("invalid retained call provenance: %#v", call)
		}
		seen[call.ID] = true
		if call.EvidenceClass == "runtime_observation" && (call.Runtime == nil || *call.Runtime != "opencode") {
			t.Errorf("runtime observation %s lacks OpenCode provenance", call.ID)
		}
		if call.MappingOutcome == "ambiguous" && len(call.CandidateLayer) < 2 {
			t.Errorf("ambiguous call %s has fewer than two candidate layers", call.ID)
		}
		if len(call.CandidateLayer) != len(call.CandidateProfile) {
			t.Errorf("call %s candidate mapping lengths differ", call.ID)
		}
		t.Logf("retained_call=%s signals=%v mapping=%s", call.ID, call.Signals, call.MappingOutcome)
	}
}

func validateFixtureCases(t *testing.T, root string, c routingCharacterization) {
	t.Helper()
	fixtures := make(map[string]characterizationReference)
	for _, fixture := range c.Manifest.Fixtures {
		fixtures[fixture.ID] = fixture
	}
	if len(c.Findings.FixtureCases) != len(fixtures) {
		t.Fatalf("fixture cases = %d, want %d", len(c.Findings.FixtureCases), len(fixtures))
	}
	for _, want := range c.Findings.FixtureCases {
		reference, ok := fixtures[want.ID]
		if !ok {
			t.Errorf("fixture case %q is not in manifest", want.ID)
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(reference.Path)))
		if err != nil {
			t.Error(err)
			continue
		}
		got := fixtureCharacterization{
			ID:                 want.ID,
			Bytes:              len(raw),
			Lines:              bytes.Count(raw, []byte{'\n'}),
			JSONDocument:       json.Valid(raw),
			DiffMarkers:        bytes.Contains(raw, []byte("diff --git ")) || (bytes.Contains(raw, []byte("\n--- ")) && bytes.Contains(raw, []byte("\n+++ "))),
			LogMarkers:         bytes.Contains(raw, []byte("[debug]")) && bytes.Contains(raw, []byte("[error]")),
			InstructionMarkers: bytes.Contains(raw, []byte("# ")) && (bytes.Contains(raw, []byte("MUST")) || bytes.Contains(raw, []byte("must"))),
			NDJSONRecords:      countNDJSONRecords(raw),
			CandidateLayers:    want.CandidateLayers,
			CandidateProfiles:  want.CandidateProfiles,
			MappingBasis:       want.MappingBasis,
			MappingOutcome:     want.MappingOutcome,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("fixture %s = %#v, want %#v", want.ID, got, want)
		}
		t.Logf("fixture=%s signals=json:%t diff:%t log:%t instructions:%t ndjson:%d", want.ID, got.JSONDocument, got.DiffMarkers, got.LogMarkers, got.InstructionMarkers, got.NDJSONRecords)
	}
}

func countNDJSONRecords(raw []byte) int {
	lines := bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'})
	if len(lines) < 2 {
		return 0
	}
	for _, line := range lines {
		if len(line) == 0 || !json.Valid(line) {
			return 0
		}
	}
	return len(lines)
}

func validateRoutingCases(t *testing.T, c routingCharacterization) {
	t.Helper()
	seen := make(map[string]bool)
	for _, call := range c.Manifest.Retained.Calls {
		if call.CaseID != "" {
			seen[call.CaseID] = true
		}
	}
	for _, testCase := range c.Findings.RoutingCases {
		routes := make([]config.RouteRule, len(testCase.Routes))
		for i, route := range testCase.Routes {
			routes[i] = config.RouteRule{Tool: route.Tool, CommandPrefix: route.CommandPrefix, Subcommand: route.Subcommand, ExitStatus: route.ExitStatus, Profile: route.Profile}
		}
		call := &db.ToolCall{Tool: testCase.ToolCall.Tool, Input: testCase.ToolCall.Input, IsError: testCase.ToolCall.IsError}
		if got := Match(call, routes, testCase.DefaultProfile); got != testCase.ExpectedProfile {
			t.Errorf("%s profile = %q, want %q", testCase.ID, got, testCase.ExpectedProfile)
		}
		seen[testCase.ID] = true
		t.Logf("routing_case=%s profile=%s outcome=%s", testCase.ID, testCase.ExpectedProfile, testCase.Outcome)
	}
	for _, id := range append(append([]string{}, c.Manifest.AmbiguityCases...), c.Manifest.FallbackCases...) {
		if !seen[id] {
			t.Errorf("prescribed case %q was not reported", id)
		}
	}
}

func validateCharacterizationBoundaries(t *testing.T, c routingCharacterization) {
	t.Helper()
	if len(c.Manifest.CandidateSignals) != 11 {
		t.Errorf("candidate signals = %d, want 11", len(c.Manifest.CandidateSignals))
	}
	for _, signal := range c.Manifest.CandidateSignals {
		if signal.ID == "" || signal.Status == "" {
			t.Errorf("incomplete candidate signal: %#v", signal)
		}
	}
	if len(c.Manifest.Mappings) != 5 {
		t.Errorf("candidate mappings = %d, want 5", len(c.Manifest.Mappings))
	}
	for _, mapping := range c.Manifest.Mappings {
		if mapping.Status != "unselected_candidate" {
			t.Errorf("mapping %s -> %s status = %q", mapping.Layer, mapping.Profile, mapping.Status)
		}
	}
	if c.Findings.Residual.EvidenceClass != "estimate" || !c.Findings.Residual.NotRuntimeTokens {
		t.Error("character-count token evidence is not explicitly labeled as an estimate")
	}
	if c.Findings.RuntimeSpend.EvidenceClass != "runtime_observation" || !reflect.DeepEqual(c.Findings.RuntimeSpend.Ordinals, []int{2, 3, 6}) || !reflect.DeepEqual(c.Findings.RuntimeSpend.Cost, []float64{0, 0, 0}) {
		t.Errorf("runtime spend evidence is mislabeled or changed: %#v", c.Findings.RuntimeSpend)
	}
	if c.Findings.HistoricalSpend.EvidenceClass != "historical_runtime_fixture_unsupported_for_current_collection" {
		t.Errorf("historical fixture spend class = %q", c.Findings.HistoricalSpend.EvidenceClass)
	}
	if len(c.Findings.Followups) != 1 || c.Findings.Followups[0].ID != "validate-route-exit-status-values" || !c.Findings.Followups[0].Required || c.Findings.Followups[0].Status != "satisfied" {
		t.Errorf("product contract follow-up = %#v", c.Findings.Followups)
	}
	joined := strings.Join(c.ClaimLimits, "\n")
	for _, required := range []string{"No provider-internal cache", "No taxonomy", "unsupported for current collection", "estimates"} {
		if !strings.Contains(joined, required) {
			t.Errorf("claim limits missing %q", required)
		}
	}
}
