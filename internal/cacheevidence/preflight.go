// Package cacheevidence implements the bounded, offline gate for OpenAI and
// OpenCode cache evidence. It never performs provider or model requests.
package cacheevidence

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const SchemaVersion = 1

type Proposal struct {
	SchemaVersion       int      `json:"schema_version"`
	Paths               []Path   `json:"paths"`
	Limits              Limits   `json:"limits"`
	ArtifactDestination string   `json:"artifact_destination"`
	RedactionPolicy     []string `json:"redaction_policy"`
}

type Path struct {
	Name                   string  `json:"name"`
	Source                 string  `json:"source"`
	Version                string  `json:"version"`
	Citation               string  `json:"citation"`
	Transport              string  `json:"transport"`
	Provider               string  `json:"provider"`
	Model                  string  `json:"model"`
	Variant                string  `json:"variant,omitempty"`
	CredentialEnvironment  string  `json:"credential_environment"`
	RuntimeBinary          string  `json:"runtime_binary,omitempty"`
	RequiredNativeRequests int     `json:"required_native_requests"`
	RequiredAttempts       int     `json:"required_attempts"`
	MaxInputTokens         int64   `json:"max_input_tokens"`
	MaxOutputTokens        int64   `json:"max_output_tokens"`
	InputUSDPerMillion     float64 `json:"input_usd_per_million"`
	CacheWriteMultiplier   float64 `json:"cache_write_multiplier"`
	OutputUSDPerMillion    float64 `json:"output_usd_per_million"`
}

type Limits struct {
	MaxNativeRequests int     `json:"max_native_requests"`
	MaxAttempts       int     `json:"max_attempts"`
	MaxSpendUSD       float64 `json:"max_spend_usd"`
	MaxWallTime       string  `json:"max_wall_time"`
}

type AccessState struct {
	State                string `json:"state"`
	Credential           string `json:"credential"`
	Runtime              string `json:"runtime,omitempty"`
	ModelAccess          string `json:"model_access"`
	UnavailableCondition string `json:"unavailable_condition,omitempty"`
}

type CapabilityEntry struct {
	Name                 string      `json:"name"`
	Source               string      `json:"source"`
	Version              string      `json:"version"`
	Provider             string      `json:"provider"`
	Model                string      `json:"model"`
	Variant              string      `json:"variant,omitempty"`
	Access               AccessState `json:"access"`
	NativeRequestMaximum int         `json:"native_request_maximum"`
	AttemptMaximum       int         `json:"attempt_maximum"`
	SpendMaximumUSD      float64     `json:"spend_maximum_usd"`
	WallTimeMaximum      string      `json:"wall_time_maximum"`
	ArtifactDestination  string      `json:"artifact_destination"`
	RedactionPolicy      []string    `json:"redaction_policy"`
}

type Report struct {
	SchemaVersion    int               `json:"schema_version"`
	Ready            bool              `json:"ready"`
	NetworkActivity  string            `json:"network_activity"`
	BillableRequests int               `json:"billable_requests"`
	Limits           Limits            `json:"limits"`
	Capabilities     []CapabilityEntry `json:"capabilities"`
}

type Environment interface {
	CredentialPresent(string) bool
	RuntimeVersion(string) (string, error)
}

type LocalEnvironment struct{}

func (LocalEnvironment) CredentialPresent(name string) bool { return os.Getenv(name) != "" }

func (LocalEnvironment) RuntimeVersion(binary string) (string, error) {
	out, err := exec.Command(binary, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func LoadProposal(path string) (Proposal, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Proposal{}, err
	}
	var proposal Proposal
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&proposal); err != nil {
		return Proposal{}, fmt.Errorf("decode proposal: %w", err)
	}
	return proposal, nil
}

func Preflight(proposal Proposal, env Environment) (Report, error) {
	if err := validateProposal(proposal); err != nil {
		return Report{}, err
	}
	report := Report{SchemaVersion: SchemaVersion, Ready: true, NetworkActivity: "none", BillableRequests: 0, Limits: proposal.Limits}
	spendByPath := worstCaseSpend(proposal.Paths)
	ceilingConditions := unenforceableConditions(proposal)
	for i, path := range proposal.Paths {
		entry := CapabilityEntry{
			Name: path.Name, Source: path.Source, Version: path.Version,
			Provider: path.Provider, Model: path.Model, Variant: path.Variant,
			NativeRequestMaximum: path.RequiredNativeRequests,
			AttemptMaximum:       path.RequiredAttempts,
			SpendMaximumUSD:      spendByPath[i], WallTimeMaximum: proposal.Limits.MaxWallTime,
			ArtifactDestination: proposal.ArtifactDestination,
			RedactionPolicy:     append([]string(nil), proposal.RedactionPolicy...),
		}
		entry.Access.Credential = availability(env.CredentialPresent(path.CredentialEnvironment))
		entry.Access.ModelAccess = "unverified_without_provider_request"
		if ceilingConditions[i] != "" {
			entry.Access.State = "unavailable"
			entry.Access.UnavailableCondition = ceilingConditions[i]
			report.Ready = false
		} else if entry.Access.Credential == "absent" {
			entry.Access.State = "unavailable"
			entry.Access.UnavailableCondition = path.CredentialEnvironment + " is not set"
			report.Ready = false
		} else if path.RuntimeBinary != "" {
			version, err := env.RuntimeVersion(path.RuntimeBinary)
			if err != nil {
				entry.Access.State = "unavailable"
				entry.Access.Runtime = "unavailable"
				entry.Access.UnavailableCondition = path.RuntimeBinary + " executable is unavailable"
				report.Ready = false
			} else if version != path.Version {
				entry.Access.State = "unavailable"
				entry.Access.Runtime = version
				entry.Access.UnavailableCondition = fmt.Sprintf("%s version is %s; required %s", path.RuntimeBinary, version, path.Version)
				report.Ready = false
			} else {
				entry.Access.State = "credential_present"
				entry.Access.Runtime = version
			}
		} else {
			entry.Access.State = "credential_present"
		}
		report.Capabilities = append(report.Capabilities, entry)
	}
	return report, nil
}

func validateProposal(p Proposal) error {
	if p.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d", p.SchemaVersion)
	}
	if len(p.Paths) != 2 {
		return fmt.Errorf("proposal requires exactly the direct OpenAI and routine OpenCode paths")
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
		if strings.TrimSpace(path.Source) == "" || strings.TrimSpace(path.Version) == "" || strings.TrimSpace(path.Citation) == "" {
			return fmt.Errorf("each path requires exact source, version, and citation")
		}
		key := path.Provider + "/" + path.Model + "/" + path.Variant
		switch path.Transport {
		case "direct_openai":
			if key != "openai/gpt-5.6-luna/high" {
				return fmt.Errorf("priced OpenAI evidence requires openai/gpt-5.6-luna with variant high")
			}
			seen[path.Transport] = true
		case "opencode":
			if key != "opencode/big-pickle/" {
				return fmt.Errorf("routine OpenCode evidence requires opencode/big-pickle without a variant")
			}
			seen[path.Transport] = true
		default:
			return fmt.Errorf("unsupported transport %q", path.Transport)
		}
	}
	if !seen["direct_openai"] || !seen["opencode"] {
		return fmt.Errorf("proposal requires one direct OpenAI path and one OpenCode path")
	}
	return nil
}

func unenforceableConditions(p Proposal) []string {
	conditions := make([]string, len(p.Paths))
	requests, attempts, spend := 0, 0, 0.0
	for i, path := range p.Paths {
		switch {
		case path.RequiredNativeRequests <= 0:
			conditions[i] = path.Name + " required native request count is not positive"
		case path.RequiredAttempts != path.RequiredNativeRequests:
			conditions[i] = path.Name + " does not disable retries with one attempt per native request"
		case path.MaxInputTokens <= 0 || path.MaxOutputTokens <= 0:
			conditions[i] = path.Name + " token ceiling is not positive"
		case path.InputUSDPerMillion < 0 || path.CacheWriteMultiplier < 1 || path.OutputUSDPerMillion < 0:
			conditions[i] = path.Name + " pricing bound is invalid"
		}
		requests += path.RequiredNativeRequests
		attempts += path.RequiredAttempts
	}
	for _, value := range worstCaseSpend(p.Paths) {
		spend += value
	}
	global := ""
	switch {
	case p.Limits.MaxNativeRequests <= 0 || requests > p.Limits.MaxNativeRequests:
		global = fmt.Sprintf("declared native request ceiling %d cannot cover %d required requests", p.Limits.MaxNativeRequests, requests)
	case p.Limits.MaxAttempts <= 0 || attempts > p.Limits.MaxAttempts:
		global = fmt.Sprintf("declared attempt ceiling %d cannot cover %d required attempts", p.Limits.MaxAttempts, attempts)
	case p.Limits.MaxSpendUSD < 0 || spend > p.Limits.MaxSpendUSD+1e-12:
		global = fmt.Sprintf("declared spend ceiling %s is below worst-case spend %s", money(p.Limits.MaxSpendUSD), money(spend))
	default:
		duration, err := time.ParseDuration(p.Limits.MaxWallTime)
		if err != nil || duration <= 0 {
			global = fmt.Sprintf("declared maximum wall time %q is not a positive duration", p.Limits.MaxWallTime)
		}
	}
	if global != "" {
		for i := range conditions {
			if conditions[i] == "" {
				conditions[i] = global
			}
		}
	}
	return conditions
}

func worstCaseSpend(paths []Path) []float64 {
	values := make([]float64, len(paths))
	for i, path := range paths {
		inputRate := path.InputUSDPerMillion * math.Max(1, path.CacheWriteMultiplier)
		perRequest := (float64(path.MaxInputTokens)*inputRate + float64(path.MaxOutputTokens)*path.OutputUSDPerMillion) / 1_000_000
		values[i] = perRequest * float64(path.RequiredNativeRequests)
	}
	return values
}

func availability(ok bool) string {
	if ok {
		return "present"
	}
	return "absent"
}

func money(value float64) string { return strconv.FormatFloat(value, 'f', 6, 64) }
