package cacheevidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/experiment"
)

func TestAnthropicParityPreservesNativeAndOpenCodeSources(t *testing.T) {
	native := fixture(t, "anthropic-messages-cache-write-sanitized.json")
	normalized := fixture(t, "opencode-anthropic-cache-step-sanitized.jsonl")
	nativeBefore, normalizedBefore := string(native), string(normalized)
	evidence, err := BuildAnthropicParityEvidence(native, normalized, "Messages API 2023-06-01; docs retrieved 2026-07-24", "1.18.4 (49c69c5)")
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Native.Raw != nativeBefore || evidence.Normalized.Raw != normalizedBefore || evidence.Native.Source == evidence.Normalized.Source {
		t.Fatal("parity evidence did not preserve independent sources")
	}
	n, o := evidence.Native.Observations[0], evidence.Normalized.Observations[0]
	assertMeasured(t, "native uncached input", n.UncachedInput, 24)
	assertMeasured(t, "native cache creation", n.CacheWrite, 1024)
	assertMeasured(t, "native measured-zero cache read", n.CacheRead, 0)
	assertMeasured(t, "OpenCode uncached input", o.UncachedInput, 24)
	assertMeasured(t, "OpenCode cache write", o.CacheWrite, 1024)
	assertMeasured(t, "OpenCode measured-zero cache read", o.CacheRead, 0)
	if n.Identity.RequestID == nil || n.Identity.ResponseID == nil || n.Control.Mode == nil || *n.Control.Mode != "ephemeral; ttl=1h" || n.Retry.MaxAttempts == nil {
		t.Fatalf("native identity/control/retry = %#v", n)
	}
	if o.Control.Availability == nil || o.Retry.Availability == nil {
		t.Fatalf("OpenCode unavailable fields = %#v", o)
	}
}

func TestAnthropicUnsupportedFixturePreservesAbsentCacheFields(t *testing.T) {
	retained, err := NormalizeAnthropic(fixture(t, "anthropic-messages-cache-unavailable-sanitized.json"), "Messages API 2023-06-01; docs retrieved 2026-07-24")
	if err != nil {
		t.Fatal(err)
	}
	event := retained.Observations[0]
	assertMeasured(t, "unsupported uncached input", event.UncachedInput, 100)
	if event.CacheRead.State != experiment.MeasurementUnknown || event.CacheRead.Value != nil || event.CacheWrite.State != experiment.MeasurementUnknown || event.CacheWrite.Value != nil {
		t.Fatalf("absent cache fields became values: read=%#v write=%#v", event.CacheRead, event.CacheWrite)
	}
	if event.Control.Eligibility == nil || !strings.Contains(*event.Control.Eligibility, "below_1024") {
		t.Fatalf("eligibility = %#v", event.Control.Eligibility)
	}
}

func TestAnthropicNormalizationPreservesMalformedMeasurements(t *testing.T) {
	raw := strings.Replace(string(fixture(t, "anthropic-messages-cache-write-sanitized.json")), `"cache_creation_input_tokens": 1024`, `"cache_creation_input_tokens": 1024.5`, 1)
	retained, err := NormalizeAnthropic([]byte(raw), "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if got := retained.Observations[0].CacheWrite; got.State != experiment.MeasurementMalformed || got.Value != nil {
		t.Fatalf("cache creation = %#v", got)
	}
}

func TestAnthropicPreflightAcceptedAndUnavailable(t *testing.T) {
	proposal := validAnthropicProposal()
	available := fakeEnvironment{
		credentials: map[string]CredentialMetadata{
			"environment:ANTHROPIC_API_KEY": {State: "present", Type: "api"},
			"opencode:Anthropic":            {State: "present", Type: "api"},
		},
		versions: map[string]string{"opencode": "1.18.4"},
	}
	report, err := Preflight(proposal, available)
	if err != nil || !report.Ready || report.BillableRequests != 0 || report.NetworkActivity != "none" {
		t.Fatalf("available preflight = %#v, %v", report, err)
	}
	report, err = Preflight(proposal, fakeEnvironment{versions: map[string]string{"opencode": "1.18.4"}})
	if err != nil || report.Ready || report.BillableRequests != 0 {
		t.Fatalf("unavailable preflight = %#v, %v", report, err)
	}
	for _, capability := range report.Capabilities {
		if capability.Access.State != "unavailable" || capability.Access.UnavailableCondition == "" {
			t.Errorf("unavailable capability = %#v", capability)
		}
	}
}

func TestTrackedAnthropicEvidenceIsOfflineAndFixtureHashesMatch(t *testing.T) {
	root := filepath.Join("..", "..")
	capabilityRaw, err := os.ReadFile(filepath.Join(root, "tools", "bench", "evidence", "anthropic-capability.json"))
	if err != nil {
		t.Fatal(err)
	}
	var capability struct {
		APIFamily              string `json:"api_family"`
		APIVersion             string `json:"api_version"`
		AuthoritativeSemantics map[string]struct {
			Detail     string   `json:"detail"`
			References []string `json:"references"`
		} `json:"authoritative_semantics"`
		RuntimeNormalization struct {
			Fields []struct {
				Operation string `json:"operation"`
				Rounding  string `json:"rounding"`
			} `json:"fields"`
		} `json:"runtime_normalization"`
		EvidenceState map[string]any `json:"evidence_state"`
	}
	if err := json.Unmarshal(capabilityRaw, &capability); err != nil {
		t.Fatal(err)
	}
	if capability.APIFamily == "" || capability.APIVersion == "" {
		t.Fatalf("unqualified capability = %#v", capability)
	}
	for _, name := range []string{"cache_control", "cache_creation", "cache_read", "eligibility", "ttl", "identity", "retry"} {
		entry, ok := capability.AuthoritativeSemantics[name]
		if !ok || entry.Detail == "" || len(entry.References) == 0 {
			t.Errorf("capability semantics %q = %#v", name, entry)
		}
	}
	operations := map[string]bool{}
	for _, field := range capability.RuntimeNormalization.Fields {
		operations[field.Operation] = true
		if strings.Contains(field.Operation, "renamed") && field.Rounding == "" {
			t.Errorf("renamed field lacks rounding treatment: %#v", field)
		}
	}
	if !operations["renamed"] || !operations["dropped/unavailable"] || capability.EvidenceState["direct_live"] == nil || capability.EvidenceState["opencode_live"] == nil {
		t.Fatalf("incomplete normalization/access evidence: operations=%#v state=%#v", operations, capability.EvidenceState)
	}

	preflightRaw, err := os.ReadFile(filepath.Join(root, "tools", "bench", "evidence", "anthropic-opencode-preflight.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report Report
	if err := json.Unmarshal(preflightRaw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Ready || report.NetworkActivity != "none" || report.BillableRequests != 0 || len(report.Capabilities) != 2 {
		t.Fatalf("tracked preflight = %#v", report)
	}
	for _, capability := range report.Capabilities {
		if capability.Access.State != "unavailable" || capability.Access.UnavailableCondition == "" || capability.NativeRequestMaximum != 2 || capability.AttemptMaximum != 2 || capability.SpendMaximumUSD != 0.017024 {
			t.Errorf("tracked capability = %#v", capability)
		}
	}

	wants := map[string]string{
		"anthropic-messages-cache-write-sanitized.json":       "99162156a13be904f37d975165017daf4fb0d9cfaee5421ff24ad56d3c39a383",
		"anthropic-messages-cache-unavailable-sanitized.json": "544ea4eca874b0793a178e0b0bd5b8cb1c80e03a486a2468804dea00a489477c",
		"opencode-anthropic-cache-step-sanitized.jsonl":       "4de09e22c7d515831967c518464181c4cf632c099f5e1f70026555110ddc91b7",
	}
	for name, want := range wants {
		raw := fixture(t, name)
		sum := sha256.Sum256(raw)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s SHA-256 = %s, want %s", name, got, want)
		}
	}
}

func validAnthropicProposal() Proposal {
	paths := []Path{
		{Name: "direct_anthropic", Source: "Anthropic Messages API", Version: "2023-06-01 / 2026-07-24 docs", References: []string{"https://platform.claude.com/docs/en/api/versioning"}, Transport: "direct_anthropic", Provider: "anthropic", Model: "claude-sonnet-5", AccessMode: "documented_direct", CredentialSource: "environment:ANTHROPIC_API_KEY", CredentialType: "api", RequiredNativeRequests: 2, RequiredAttempts: 2, MaxInputTokens: 2048, MaxOutputTokens: 32, InputUSDPerMillion: 2, CacheWriteMultiplier: 2, OutputUSDPerMillion: 10},
		{Name: "opencode_anthropic", Source: "OpenCode Anthropic runtime", Version: "1.18.4", References: []string{"https://github.com/anomalyco/opencode/tree/49c69c5ed3ccf706b61b3febb43c8aaff7f8325e"}, Transport: "opencode_anthropic", Provider: "anthropic", Model: "claude-sonnet-5", AccessMode: "runtime", CredentialSource: "opencode:Anthropic", CredentialType: "api", RuntimeBinary: "opencode", RequiredNativeRequests: 2, RequiredAttempts: 2, MaxInputTokens: 2048, MaxOutputTokens: 32, InputUSDPerMillion: 2, CacheWriteMultiplier: 2, OutputUSDPerMillion: 10},
	}
	return Proposal{SchemaVersion: SchemaVersion, Paths: paths, Limits: Limits{MaxNativeRequests: 4, MaxAttempts: 4, MaxSpendUSD: 0.035, MaxWallTime: "2m"}, ArtifactDestination: ".bench-data/cache-evidence/anthropic-opencode-sonnet-5-20260724", RedactionPolicy: []string{"omit credentials", "synthetic prompts only"}}
}
