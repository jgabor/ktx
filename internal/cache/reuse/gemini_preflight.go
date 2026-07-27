package reuse

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type GeminiProposal struct {
	SchemaVersion       int          `json:"schema_version"`
	Paths               []GeminiPath `json:"paths"`
	Limits              Limits       `json:"limits"`
	ArtifactDestination string       `json:"artifact_destination"`
	RedactionPolicy     []string     `json:"redaction_policy"`
}

type GeminiPath struct {
	Name                          string   `json:"name"`
	Source                        string   `json:"source"`
	Version                       string   `json:"version"`
	References                    []string `json:"references"`
	Transport                     string   `json:"transport"`
	Provider                      string   `json:"provider"`
	Model                         string   `json:"model"`
	AccessMode                    string   `json:"access_mode"`
	CredentialSource              string   `json:"credential_source"`
	CredentialType                string   `json:"credential_type"`
	RuntimeBinary                 string   `json:"runtime_binary,omitempty"`
	RuntimeVersion                string   `json:"runtime_version,omitempty"`
	RequiredNativeRequests        int      `json:"required_native_requests"`
	RequiredAttempts              int      `json:"required_attempts"`
	MaxInputTokens                int64    `json:"max_input_tokens"`
	MaxOutputTokens               int64    `json:"max_output_tokens"`
	InputUSDPerMillion            float64  `json:"input_usd_per_million"`
	OutputUSDPerMillion           float64  `json:"output_usd_per_million"`
	StorageUSDPerMillionTokenHour float64  `json:"storage_usd_per_million_token_hour"`
	MaxStorageHours               float64  `json:"max_storage_hours"`
}

func LoadGeminiProposal(path string) (GeminiProposal, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return GeminiProposal{}, err
	}
	var proposal GeminiProposal
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&proposal); err != nil {
		return GeminiProposal{}, fmt.Errorf("decode Gemini proposal: %w", err)
	}
	return proposal, nil
}

func GeminiPreflight(proposal GeminiProposal, env Environment) (Report, error) {
	if err := validateGeminiProposal(proposal); err != nil {
		return Report{}, err
	}
	report := Report{SchemaVersion: SchemaVersion, Ready: true, NetworkActivity: "none", BillableRequests: 0, Limits: proposal.Limits}
	spend := geminiWorstCaseSpend(proposal.Paths)
	ceiling := geminiCeilingCondition(proposal, spend)
	for i, path := range proposal.Paths {
		entry := CapabilityEntry{
			Name: path.Name, Source: path.Source, Version: path.Version, References: append([]string(nil), path.References...),
			Provider: path.Provider, Model: path.Model, NativeRequestMaximum: path.RequiredNativeRequests,
			AttemptMaximum: path.RequiredAttempts, SpendMaximumUSD: spend[i], WallTimeMaximum: proposal.Limits.MaxWallTime,
			ArtifactDestination: proposal.ArtifactDestination, RedactionPolicy: append([]string(nil), proposal.RedactionPolicy...),
		}
		credential := env.Credential(path.CredentialSource, path.CredentialType)
		entry.Access = AccessState{
			Credential: credential.State, CredentialSource: path.CredentialSource,
			CredentialType: credential.Type, ModelAccess: "unverified_without_provider_request",
		}
		switch {
		case ceiling != "":
			entry.Access.State = "unavailable"
			entry.Access.UnavailableCondition = ceiling
			report.Ready = false
		case credential.State != "present":
			entry.Access.State = "unavailable"
			entry.Access.UnavailableCondition = fmt.Sprintf("required %s credential from %s is %s", path.CredentialType, path.CredentialSource, credential.State)
			report.Ready = false
		case path.RuntimeBinary != "":
			version, err := env.RuntimeVersion(path.RuntimeBinary)
			if err != nil {
				entry.Access.State = "unavailable"
				entry.Access.Runtime = "unavailable"
				entry.Access.UnavailableCondition = path.RuntimeBinary + " executable is unavailable"
				report.Ready = false
			} else if version != path.RuntimeVersion {
				entry.Access.State = "unavailable"
				entry.Access.Runtime = version
				entry.Access.UnavailableCondition = fmt.Sprintf("%s version is %s; required %s", path.RuntimeBinary, version, path.RuntimeVersion)
				report.Ready = false
			} else {
				entry.Access.State = "credential_present"
				entry.Access.Runtime = version
			}
		default:
			entry.Access.State = "credential_present"
		}
		report.Capabilities = append(report.Capabilities, entry)
	}
	return report, nil
}

func validateGeminiProposal(p GeminiProposal) error {
	if p.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d", p.SchemaVersion)
	}
	if len(p.Paths) != 2 {
		return fmt.Errorf("gemini proposal requires exactly direct Gemini and OpenCode Google paths")
	}
	clean := filepath.Clean(p.ArtifactDestination)
	if clean == "." || filepath.IsAbs(clean) || !strings.HasPrefix(filepath.ToSlash(clean), ".bench-data/cache-evidence/") {
		return fmt.Errorf("artifact destination must be below .bench-data/cache-evidence")
	}
	if len(p.RedactionPolicy) == 0 {
		return fmt.Errorf("redaction policy is required")
	}
	seen := map[string]bool{}
	for _, path := range p.Paths {
		if strings.TrimSpace(path.Source) == "" || strings.TrimSpace(path.Version) == "" || len(path.References) == 0 {
			return fmt.Errorf("each Gemini path requires exact source, version, and references")
		}
		if path.RequiredNativeRequests <= 0 || path.RequiredAttempts != path.RequiredNativeRequests {
			return fmt.Errorf("%s must disable retries with one attempt per native request", path.Name)
		}
		if path.MaxInputTokens <= 0 || path.MaxOutputTokens <= 0 || path.InputUSDPerMillion < 0 || path.OutputUSDPerMillion < 0 || path.StorageUSDPerMillionTokenHour < 0 || path.MaxStorageHours < 0 {
			return fmt.Errorf("%s has an invalid token or pricing bound", path.Name)
		}
		key := path.Provider + "/" + path.Model
		switch path.Transport {
		case "direct_gemini_v1beta":
			if key != "google/gemini-2.5-flash" || path.AccessMode != "documented_direct" || path.CredentialSource != "environment:GEMINI_API_KEY" || path.CredentialType != "api" {
				return fmt.Errorf("direct Gemini evidence requires Gemini Developer API v1beta gemini-2.5-flash with GEMINI_API_KEY")
			}
		case "opencode_google":
			if key != "google/gemini-2.5-flash" || path.AccessMode != "runtime" || path.RuntimeBinary != "opencode" || path.RuntimeVersion != "1.18.4" || path.CredentialSource != "opencode:Google" {
				return fmt.Errorf("OpenCode Gemini evidence requires the pinned Google gemini-2.5-flash runtime path")
			}
		default:
			return fmt.Errorf("unsupported Gemini transport %q", path.Transport)
		}
		seen[path.Transport] = true
	}
	if !seen["direct_gemini_v1beta"] || !seen["opencode_google"] {
		return fmt.Errorf("gemini proposal requires direct and OpenCode paths")
	}
	return nil
}

func geminiWorstCaseSpend(paths []GeminiPath) []float64 {
	spend := make([]float64, len(paths))
	for i, path := range paths {
		requests := float64(path.RequiredNativeRequests) * (float64(path.MaxInputTokens)*path.InputUSDPerMillion + float64(path.MaxOutputTokens)*path.OutputUSDPerMillion) / 1_000_000
		storage := float64(path.MaxInputTokens) * path.StorageUSDPerMillionTokenHour * path.MaxStorageHours / 1_000_000
		spend[i] = requests + storage
	}
	return spend
}

func geminiCeilingCondition(p GeminiProposal, spend []float64) string {
	requests, attempts, total := 0, 0, 0.0
	for i, path := range p.Paths {
		requests += path.RequiredNativeRequests
		attempts += path.RequiredAttempts
		total += spend[i]
	}
	switch {
	case p.Limits.MaxNativeRequests <= 0 || requests > p.Limits.MaxNativeRequests:
		return fmt.Sprintf("declared native request ceiling %d cannot cover %d required requests", p.Limits.MaxNativeRequests, requests)
	case p.Limits.MaxAttempts <= 0 || attempts > p.Limits.MaxAttempts:
		return fmt.Sprintf("declared attempt ceiling %d cannot cover %d required attempts", p.Limits.MaxAttempts, attempts)
	case p.Limits.MaxSpendUSD < 0 || total > p.Limits.MaxSpendUSD+1e-12 || math.IsNaN(total) || math.IsInf(total, 0):
		return fmt.Sprintf("declared spend ceiling %s is below worst-case spend %s", money(p.Limits.MaxSpendUSD), money(total))
	default:
		duration, err := time.ParseDuration(p.Limits.MaxWallTime)
		if err != nil || duration <= 0 {
			return fmt.Sprintf("declared maximum wall time %q is not a positive duration", p.Limits.MaxWallTime)
		}
	}
	return ""
}
