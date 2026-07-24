package cacheevidence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeEnvironment struct {
	credentials map[string]bool
	versions    map[string]string
}

func (f fakeEnvironment) CredentialPresent(name string) bool { return f.credentials[name] }
func (f fakeEnvironment) RuntimeVersion(name string) (string, error) {
	version, ok := f.versions[name]
	if !ok {
		return "", os.ErrNotExist
	}
	return version, nil
}

func validProposal() Proposal {
	return Proposal{
		SchemaVersion: SchemaVersion,
		Paths: []Path{
			{
				Name: "priced_openai", Source: "OpenAI Responses API", Version: "2026-07-24 docs", Citation: "https://example.test/openai",
				Transport: "direct_openai", Provider: "openai", Model: "gpt-5.6-luna", Variant: "high", CredentialEnvironment: "OPENAI_API_KEY",
				RequiredNativeRequests: 2, RequiredAttempts: 2, MaxInputTokens: 8192, MaxOutputTokens: 64,
				InputUSDPerMillion: 1, CacheWriteMultiplier: 1.25, OutputUSDPerMillion: 6,
			},
			{
				Name: "routine_opencode", Source: "OpenCode", Version: "1.18.4", Citation: "https://example.test/opencode",
				Transport: "opencode", Provider: "opencode", Model: "big-pickle", CredentialEnvironment: "OPENCODE_API_KEY", RuntimeBinary: "opencode",
				RequiredNativeRequests: 2, RequiredAttempts: 2, MaxInputTokens: 8192, MaxOutputTokens: 64,
				CacheWriteMultiplier: 1,
			},
		},
		Limits:              Limits{MaxNativeRequests: 4, MaxAttempts: 4, MaxSpendUSD: 0.025, MaxWallTime: "2m"},
		ArtifactDestination: ".bench-data/cache-evidence/test",
		RedactionPolicy:     []string{"omit credentials", "hash cache keys"},
	}
}

func TestPreflightAcceptedAndUnavailableCases(t *testing.T) {
	proposal := validProposal()
	available := fakeEnvironment{
		credentials: map[string]bool{"OPENAI_API_KEY": true, "OPENCODE_API_KEY": true},
		versions:    map[string]string{"opencode": "1.18.4"},
	}
	report, err := Preflight(proposal, available)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if !report.Ready || report.NetworkActivity != "none" || report.BillableRequests != 0 {
		t.Fatalf("accepted report = %#v", report)
	}
	if len(report.Capabilities) != 2 || report.Capabilities[0].SpendMaximumUSD != 0.021248 {
		t.Fatalf("capabilities = %#v", report.Capabilities)
	}
	for _, entry := range report.Capabilities {
		if entry.Source == "" || entry.Version == "" || entry.NativeRequestMaximum != 2 || entry.AttemptMaximum != 2 || entry.WallTimeMaximum != "2m" || entry.ArtifactDestination == "" || len(entry.RedactionPolicy) == 0 {
			t.Errorf("incomplete preflight entry: %#v", entry)
		}
	}

	unavailable := fakeEnvironment{
		credentials: map[string]bool{"OPENCODE_API_KEY": true},
		versions:    map[string]string{"opencode": "1.18.3"},
	}
	report, err = Preflight(proposal, unavailable)
	if err != nil {
		t.Fatalf("unavailable Preflight() error = %v", err)
	}
	if report.Ready || report.BillableRequests != 0 {
		t.Fatalf("unavailable report = %#v", report)
	}
	if got := report.Capabilities[0].Access.UnavailableCondition; got != "OPENAI_API_KEY is not set" {
		t.Errorf("OpenAI unavailable condition = %q", got)
	}
	if got := report.Capabilities[1].Access.UnavailableCondition; got != "opencode version is 1.18.3; required 1.18.4" {
		t.Errorf("OpenCode unavailable condition = %q", got)
	}
}

func TestPreflightRetainsUnenforceableCeilings(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*Proposal)
		want   string
	}{
		{"spend ceiling", func(p *Proposal) { p.Limits.MaxSpendUSD = 0.01 }, "below worst-case spend"},
		{"request ceiling", func(p *Proposal) { p.Limits.MaxNativeRequests = 3 }, "cannot cover 4 required requests"},
		{"retry", func(p *Proposal) { p.Paths[0].RequiredAttempts = 3 }, "does not disable retries"},
		{"wall time", func(p *Proposal) { p.Limits.MaxWallTime = "0s" }, "not a positive duration"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			proposal := validProposal()
			tt.mutate(&proposal)
			report, err := Preflight(proposal, fakeEnvironment{credentials: map[string]bool{"OPENAI_API_KEY": true, "OPENCODE_API_KEY": true}, versions: map[string]string{"opencode": "1.18.4"}})
			if err != nil {
				t.Fatalf("Preflight() error = %v", err)
			}
			if report.Ready || report.BillableRequests != 0 {
				t.Fatalf("report = %#v", report)
			}
			found := false
			for _, entry := range report.Capabilities {
				found = found || strings.Contains(entry.Access.UnavailableCondition, tt.want)
			}
			if !found {
				t.Fatalf("capabilities = %#v, want condition containing %q", report.Capabilities, tt.want)
			}
		})
	}
}

func TestPreflightRejectsWrongPathOrRetentionPolicy(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Proposal)
		want   string
	}{
		{"priced model", func(p *Proposal) { p.Paths[0].Model = "gpt-5.6" }, "requires openai/gpt-5.6-luna"},
		{"routine model", func(p *Proposal) { p.Paths[1].Model = "gpt-5.6-luna" }, "requires opencode/big-pickle"},
		{"copilot", func(p *Proposal) { p.Paths[1].Provider = "github-copilot" }, "requires opencode/big-pickle"},
		{"artifact", func(p *Proposal) { p.ArtifactDestination = "evidence/output" }, "below .bench-data/cache-evidence"},
		{"redaction", func(p *Proposal) { p.RedactionPolicy = nil }, "redaction policy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proposal := validProposal()
			tt.mutate(&proposal)
			_, err := Preflight(proposal, fakeEnvironment{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Preflight() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestLoadProposalRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proposal.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"unexpected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProposal(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("LoadProposal() error = %v", err)
	}
}
