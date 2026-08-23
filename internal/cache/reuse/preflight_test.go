package reuse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeEnvironment struct {
	credentials map[string]CredentialMetadata
	versions    map[string]string
}

func (f fakeEnvironment) Credential(source, expectedType string) CredentialMetadata {
	if value, ok := f.credentials[source]; ok {
		return value
	}
	return CredentialMetadata{State: "absent", Type: expectedType}
}

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
				Name: "priced_openai", Source: "OpenAI Responses API", Version: "2026-07-24 docs", References: []string{"https://example.test/openai"},
				Transport: "direct_openai", Provider: "openai", Model: "gpt-5.6-luna", Variant: "high", AccessMode: "documented_direct", CredentialSource: "environment:OPENAI_API_KEY", CredentialType: "api",
				RequiredNativeRequests: 2, RequiredAttempts: 2, MaxInputTokens: 8192, MaxOutputTokens: 64,
				InputUSDPerMillion: 1, CacheWriteMultiplier: 1.25, OutputUSDPerMillion: 6,
			},
			{
				Name: "routine_opencode", Source: "OpenCode", Version: "1.18.5", References: []string{"https://example.test/opencode"},
				Transport: "opencode", Provider: "openai", Model: "gpt-5.6-luna", Variant: "high", AccessMode: "runtime", CredentialSource: "environment:OPENCODE_API_KEY", CredentialType: "api", RuntimeBinary: "opencode",
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
		credentials: map[string]CredentialMetadata{
			"environment:OPENAI_API_KEY":   {State: "present", Type: "api"},
			"environment:OPENCODE_API_KEY": {State: "present", Type: "api"},
		},
		versions: map[string]string{"opencode": "1.18.5"},
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
		if entry.Source == "" || entry.Version == "" || len(entry.References) == 0 || entry.NativeRequestMaximum != 2 || entry.AttemptMaximum != 2 || entry.WallTimeMaximum != "2m" || entry.ArtifactDestination == "" || len(entry.RedactionPolicy) == 0 {
			t.Errorf("incomplete preflight entry: %#v", entry)
		}
	}

	for _, tt := range []struct {
		name      string
		versions  map[string]string
		condition string
	}{
		{name: "runtime version mismatch", versions: map[string]string{"opencode": "1.18.4"}, condition: "opencode version is 1.18.4; required 1.18.5"},
		{name: "runtime executable unavailable", versions: nil, condition: "opencode executable is unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			unavailable := fakeEnvironment{
				credentials: map[string]CredentialMetadata{"environment:OPENCODE_API_KEY": {State: "present", Type: "api"}},
				versions:    tt.versions,
			}
			report, err := Preflight(proposal, unavailable)
			if err != nil {
				t.Fatalf("unavailable Preflight() error = %v", err)
			}
			if report.Ready || report.BillableRequests != 0 {
				t.Fatalf("unavailable report = %#v", report)
			}
			if got := report.Capabilities[0].Access.UnavailableCondition; got != "required api credential from environment:OPENAI_API_KEY is absent" {
				t.Errorf("OpenAI unavailable condition = %q", got)
			}
			if got := report.Capabilities[1].Access.UnavailableCondition; got != tt.condition {
				t.Errorf("OpenCode unavailable condition = %q, want %q", got, tt.condition)
			}
		})
	}
}

func TestPreflightRetainsChatGPTOAuthAsUnsupportedForDirectEvidence(t *testing.T) {
	proposal := validProposal()
	condition := "OpenCode ChatGPT OAuth is adapter-scoped and is not a documented direct public Responses API credential"
	proposal.Paths[0].AccessMode = "source_only_unavailable"
	proposal.Paths[0].CredentialSource = "opencode:OpenAI"
	proposal.Paths[0].CredentialType = "oauth"
	proposal.Paths[0].UnavailableCondition = condition
	env := fakeEnvironment{
		credentials: map[string]CredentialMetadata{
			"opencode:OpenAI":              {State: "present", Type: "oauth"},
			"environment:OPENCODE_API_KEY": {State: "present", Type: "api"},
		},
		versions: map[string]string{"opencode": "1.18.5"},
	}
	report, err := Preflight(proposal, env)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	got := report.Capabilities[0]
	if report.Ready || report.BillableRequests != 0 || got.Access.State != "unsupported" || got.Access.Credential != "present" || got.Access.CredentialType != "oauth" || got.Access.ModelAccess != "not_applicable" || got.Access.UnavailableCondition != condition {
		t.Fatalf("ChatGPT OAuth capability = %#v; report ready=%t billable=%d", got, report.Ready, report.BillableRequests)
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
		{"attempt ceiling", func(p *Proposal) { p.Limits.MaxAttempts = 3 }, "cannot cover 4 required attempts"},
		{"retry", func(p *Proposal) { p.Paths[0].RequiredAttempts = 3 }, "does not disable retries"},
		{"non-positive input tokens", func(p *Proposal) { p.Paths[0].MaxInputTokens = 0 }, "token ceiling is not positive"},
		{"non-positive output tokens", func(p *Proposal) { p.Paths[1].MaxOutputTokens = -1 }, "token ceiling is not positive"},
		{"negative input price", func(p *Proposal) { p.Paths[0].InputUSDPerMillion = -1 }, "pricing bound is invalid"},
		{"cache-write multiplier below one", func(p *Proposal) { p.Paths[0].CacheWriteMultiplier = 0.5 }, "pricing bound is invalid"},
		{"negative output price", func(p *Proposal) { p.Paths[0].OutputUSDPerMillion = -1 }, "pricing bound is invalid"},
		{"wall time", func(p *Proposal) { p.Limits.MaxWallTime = "0s" }, "not a positive duration"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			proposal := validProposal()
			tt.mutate(&proposal)
			report, err := Preflight(proposal, fakeEnvironment{credentials: map[string]CredentialMetadata{"environment:OPENAI_API_KEY": {State: "present", Type: "api"}, "environment:OPENCODE_API_KEY": {State: "present", Type: "api"}}, versions: map[string]string{"opencode": "1.18.5"}})
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
		{"routine model", func(p *Proposal) { p.Paths[1].Model = "" }, "requires a provider and model"},
		{"copilot", func(p *Proposal) { p.Paths[1].Provider = "github-copilot" }, "use openai/gpt-5.6-luna with variant high"},
		{"artifact", func(p *Proposal) { p.ArtifactDestination = "evidence/output" }, "below .bench-data/cache-evidence"},
		{"redaction", func(p *Proposal) { p.RedactionPolicy = nil }, "redaction policy"},
		{"reference", func(p *Proposal) { p.Paths[0].References = []string{" "} }, "references must not be blank"},
		{"unsupported reason", func(p *Proposal) { p.Paths[0].AccessMode = "source_only_unavailable" }, "exact unavailable condition"},
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

func TestPreflightRejectsMixedProviderTransports(t *testing.T) {
	proposal := validProposal()
	proposal.Paths = append(proposal.Paths, validAnthropicProposal().Paths[0])
	if _, err := Preflight(proposal, fakeEnvironment{}); err == nil || !strings.Contains(err.Error(), "mixes provider transports") {
		t.Fatalf("Preflight() error = %v", err)
	}
}

func TestCommonProposalValidationAppliesToAllProviders(t *testing.T) {
	openAI := validProposal()
	anthropic := validAnthropicProposal()
	gemini, err := LoadGeminiProposal(filepath.Join("..", "..", "..", "tools", "bench", "evidence", "gemini-opencode-proposal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateProposal(openAI); err != nil {
		t.Fatalf("OpenAI accepted case: %v", err)
	}
	if err := validateAnthropicProposal(anthropic); err != nil {
		t.Fatalf("Anthropic accepted case: %v", err)
	}
	if err := validateGeminiProposal(gemini); err != nil {
		t.Fatalf("Gemini accepted case: %v", err)
	}
	openAI.Paths[0].References[0] = " "
	anthropic.Paths[0].References[0] = " "
	gemini.Paths[0].References[0] = " "
	for name, err := range map[string]error{
		"OpenAI": validateProposal(openAI), "Anthropic": validateAnthropicProposal(anthropic), "Gemini": validateGeminiProposal(gemini),
	} {
		if err == nil || !strings.Contains(err.Error(), "references must not be blank") {
			t.Errorf("%s rejected case error = %v", name, err)
		}
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

func TestOpenCodeCredentialMetadataUsesOnlyProviderAndType(t *testing.T) {
	out := []byte("\x1b[0m●  OpenAI \x1b[90moauth\n●  Other \x1b[90mapi\n")
	if got := credentialFromOpenCodeAuthList(out, "OpenAI", "oauth"); got.State != "present" || got.Type != "oauth" {
		t.Fatalf("credential metadata = %#v", got)
	}
	if got := credentialFromOpenCodeAuthList(out, "OpenAI", "api"); got.State != "absent" || got.Type != "api" {
		t.Fatalf("wrong-type credential metadata = %#v", got)
	}
}
