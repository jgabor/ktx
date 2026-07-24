package cacheevidence

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeminiPreflightReportsAvailableAndUnavailableWithoutCalls(t *testing.T) {
	proposal, err := LoadGeminiProposal(filepath.Join("..", "..", "tools", "bench", "evidence", "gemini-opencode-proposal.json"))
	if err != nil {
		t.Fatal(err)
	}
	available := fakeEnvironment{
		credentials: map[string]CredentialMetadata{
			"environment:GEMINI_API_KEY": {State: "present", Type: "api"},
			"opencode:Google":            {State: "present", Type: "oauth"},
		},
		versions: map[string]string{"opencode": "1.18.4"},
	}
	report, err := GeminiPreflight(proposal, available)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Ready || report.NetworkActivity != "none" || report.BillableRequests != 0 || len(report.Capabilities) != 2 {
		t.Fatalf("available report = %#v", report)
	}
	if math.Abs(report.Capabilities[0].SpendMaximumUSD-0.0160448) > 1e-12 || math.Abs(report.Capabilities[1].SpendMaximumUSD-0.0052352) > 1e-12 {
		t.Fatalf("spend maxima = %#v", report.Capabilities)
	}

	unavailable := available
	unavailable.credentials = map[string]CredentialMetadata{
		"environment:GEMINI_API_KEY": {State: "absent", Type: "api"},
		"opencode:Google":            {State: "present", Type: "oauth"},
	}
	report, err = GeminiPreflight(proposal, unavailable)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || report.NetworkActivity != "none" || report.BillableRequests != 0 {
		t.Fatalf("unavailable report = %#v", report)
	}
	if got := report.Capabilities[0].Access.UnavailableCondition; !strings.Contains(got, "GEMINI_API_KEY") || !strings.Contains(got, "absent") {
		t.Fatalf("direct unavailable condition = %q", got)
	}
	if report.Capabilities[1].Access.State != "credential_present" || report.Capabilities[1].Access.ModelAccess != "unverified_without_provider_request" {
		t.Fatalf("runtime access = %#v", report.Capabilities[1].Access)
	}
}

func TestGeminiPreflightRejectsUnenforceableOrWrongProviderPlans(t *testing.T) {
	proposal, err := LoadGeminiProposal(filepath.Join("..", "..", "tools", "bench", "evidence", "gemini-opencode-proposal.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		mutate func(*GeminiProposal)
		want   string
	}{
		{"wrong model", func(p *GeminiProposal) { p.Paths[0].Model = "gemini-3.1-pro-preview" }, "requires Gemini Developer API"},
		{"retries enabled", func(p *GeminiProposal) { p.Paths[0].RequiredAttempts++ }, "disable retries"},
		{"request ceiling", func(p *GeminiProposal) { p.Limits.MaxNativeRequests = 4 }, "cannot cover 5"},
		{"spend ceiling", func(p *GeminiProposal) { p.Limits.MaxSpendUSD = 0.01 }, "below worst-case spend"},
		{"runtime mismatch", func(p *GeminiProposal) {}, "required 1.18.4"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			candidate := proposal
			candidate.Paths = append([]GeminiPath(nil), proposal.Paths...)
			tt.mutate(&candidate)
			env := fakeEnvironment{
				credentials: map[string]CredentialMetadata{
					"environment:GEMINI_API_KEY": {State: "present", Type: "api"},
					"opencode:Google":            {State: "present", Type: "oauth"},
				},
				versions: map[string]string{"opencode": "1.18.3"},
			}
			report, err := GeminiPreflight(candidate, env)
			if tt.name == "wrong model" || tt.name == "retries enabled" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("error = %v, want %q", err, tt.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, capability := range report.Capabilities {
				found = found || strings.Contains(capability.Access.UnavailableCondition, tt.want)
			}
			if report.Ready || !found || report.BillableRequests != 0 {
				t.Fatalf("report = %#v, want unavailable %q", report, tt.want)
			}
		})
	}
}

func TestLoadGeminiProposalRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proposal.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"unexpected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGeminiProposal(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("LoadGeminiProposal() error = %v", err)
	}
}

func fixtureFromEvidence(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "tools", "bench", "evidence", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
