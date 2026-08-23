// Package reuse implements bounded offline gates and source retention
// for provider and runtime cache evidence. It never performs provider or model
// requests.
package reuse

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	Name                   string   `json:"name"`
	Source                 string   `json:"source"`
	Version                string   `json:"version"`
	References             []string `json:"references"`
	Transport              string   `json:"transport"`
	Provider               string   `json:"provider"`
	Model                  string   `json:"model"`
	Variant                string   `json:"variant,omitempty"`
	AccessMode             string   `json:"access_mode"`
	CredentialSource       string   `json:"credential_source"`
	CredentialType         string   `json:"credential_type"`
	UnavailableCondition   string   `json:"unavailable_condition,omitempty"`
	RuntimeBinary          string   `json:"runtime_binary,omitempty"`
	RequiredNativeRequests int      `json:"required_native_requests"`
	RequiredAttempts       int      `json:"required_attempts"`
	MaxInputTokens         int64    `json:"max_input_tokens"`
	MaxOutputTokens        int64    `json:"max_output_tokens"`
	InputUSDPerMillion     float64  `json:"input_usd_per_million"`
	CacheWriteMultiplier   float64  `json:"cache_write_multiplier"`
	OutputUSDPerMillion    float64  `json:"output_usd_per_million"`
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
	CredentialSource     string `json:"credential_source"`
	CredentialType       string `json:"credential_type"`
	Runtime              string `json:"runtime,omitempty"`
	ModelAccess          string `json:"model_access"`
	UnavailableCondition string `json:"unavailable_condition,omitempty"`
}

type CapabilityEntry struct {
	Name                 string      `json:"name"`
	Source               string      `json:"source"`
	Version              string      `json:"version"`
	References           []string    `json:"references"`
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
	Credential(string, string) CredentialMetadata
	RuntimeVersion(string) (string, error)
}

type CredentialMetadata struct {
	State string
	Type  string
}

type LocalEnvironment struct{}

func (LocalEnvironment) Credential(source, expectedType string) CredentialMetadata {
	if name, ok := strings.CutPrefix(source, "environment:"); ok {
		return CredentialMetadata{State: availability(os.Getenv(name) != ""), Type: expectedType}
	}
	provider, ok := strings.CutPrefix(source, "opencode:")
	if !ok {
		return CredentialMetadata{State: "unavailable", Type: expectedType}
	}
	out, err := exec.Command("opencode", "auth", "list").Output()
	if err != nil {
		return CredentialMetadata{State: "unavailable", Type: expectedType}
	}
	return credentialFromOpenCodeAuthList(out, provider, expectedType)
}

func credentialFromOpenCodeAuthList(out []byte, provider, expectedType string) CredentialMetadata {
	plain := ansiEscape.ReplaceAllString(string(out), "")
	for _, line := range strings.Split(plain, "\n") {
		fields := strings.Fields(line)
		providerPresent := false
		for _, field := range fields[:max(0, len(fields)-1)] {
			providerPresent = providerPresent || strings.EqualFold(field, provider)
		}
		if len(fields) >= 2 && providerPresent && fields[len(fields)-1] == expectedType {
			return CredentialMetadata{State: "present", Type: expectedType}
		}
	}
	return CredentialMetadata{State: "absent", Type: expectedType}
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

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
	validate, err := proposalValidator(proposal)
	if err != nil {
		return Report{}, err
	}
	if err := validate(proposal); err != nil {
		return Report{}, err
	}
	report := Report{SchemaVersion: SchemaVersion, Ready: true, NetworkActivity: "none", BillableRequests: 0, Limits: proposal.Limits}
	spendByPath := worstCaseSpend(proposal.Paths)
	ceilingConditions := unenforceableConditions(proposal)
	for i, path := range proposal.Paths {
		entry := CapabilityEntry{
			Name: path.Name, Source: path.Source, Version: path.Version,
			References: append([]string(nil), path.References...),
			Provider:   path.Provider, Model: path.Model, Variant: path.Variant,
			NativeRequestMaximum: path.RequiredNativeRequests,
			AttemptMaximum:       path.RequiredAttempts,
			SpendMaximumUSD:      spendByPath[i], WallTimeMaximum: proposal.Limits.MaxWallTime,
			ArtifactDestination: proposal.ArtifactDestination,
			RedactionPolicy:     append([]string(nil), proposal.RedactionPolicy...),
		}
		credential := env.Credential(path.CredentialSource, path.CredentialType)
		entry.Access.Credential = credential.State
		entry.Access.CredentialSource = path.CredentialSource
		entry.Access.CredentialType = credential.Type
		entry.Access.ModelAccess = "unverified_without_provider_request"
		if path.AccessMode == "source_only_unavailable" {
			entry.Access.State = "unsupported"
			entry.Access.ModelAccess = "not_applicable"
			entry.Access.UnavailableCondition = path.UnavailableCondition
			report.Ready = false
		} else if ceilingConditions[i] != "" {
			entry.Access.State = "unavailable"
			entry.Access.UnavailableCondition = ceilingConditions[i]
			report.Ready = false
		} else if entry.Access.Credential != "present" {
			entry.Access.State = "unavailable"
			entry.Access.UnavailableCondition = fmt.Sprintf("required %s credential from %s is %s", path.CredentialType, path.CredentialSource, entry.Access.Credential)
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

func proposalValidator(p Proposal) (func(Proposal) error, error) {
	directProvider := ""
	for _, path := range p.Paths {
		switch path.Transport {
		case "direct_openai":
			if directProvider != "" && directProvider != "openai" {
				return nil, fmt.Errorf("proposal mixes provider transports")
			}
			directProvider = "openai"
		case "direct_anthropic":
			if directProvider != "" && directProvider != "anthropic" {
				return nil, fmt.Errorf("proposal mixes provider transports")
			}
			directProvider = "anthropic"
		}
	}
	switch directProvider {
	case "openai":
		return validateProposal, nil
	case "anthropic":
		return validateAnthropicProposal, nil
	default:
		return nil, fmt.Errorf("proposal requires one supported direct provider transport")
	}
}

type commonProposal struct {
	schemaVersion       int
	pathCount           int
	artifactDestination string
	redactionPolicy     []string
	paths               []commonPath
}

type commonPath struct {
	source, version, credentialSource, credentialType string
	references                                        []string
	requiredNativeRequests, requiredAttempts          int
}

func validateCommonProposal(p commonProposal) error {
	if p.schemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d", p.schemaVersion)
	}
	if p.pathCount != 2 {
		return fmt.Errorf("proposal requires exactly two paths")
	}
	clean := filepath.Clean(p.artifactDestination)
	if clean == "." || filepath.IsAbs(clean) || !strings.HasPrefix(filepath.ToSlash(clean), ".bench-data/cache-evidence/") {
		return fmt.Errorf("artifact destination must be below .bench-data/cache-evidence")
	}
	if len(p.redactionPolicy) == 0 {
		return fmt.Errorf("redaction policy is required")
	}
	for _, path := range p.paths {
		if strings.TrimSpace(path.source) == "" || strings.TrimSpace(path.version) == "" || len(path.references) == 0 {
			return fmt.Errorf("each path requires exact source, version, and references")
		}
		for _, reference := range path.references {
			if strings.TrimSpace(reference) == "" {
				return fmt.Errorf("evidence references must not be blank")
			}
		}
		if strings.TrimSpace(path.credentialSource) == "" || strings.TrimSpace(path.credentialType) == "" {
			return fmt.Errorf("each path requires credential source and type")
		}
		if path.requiredNativeRequests <= 0 {
			return fmt.Errorf("each path requires a positive native request count")
		}
		if path.requiredAttempts != path.requiredNativeRequests {
			return fmt.Errorf("each path must disable retries with one attempt per native request")
		}
	}
	return nil
}

func proposalCommon(p Proposal) commonProposal {
	common := commonProposal{p.SchemaVersion, len(p.Paths), p.ArtifactDestination, p.RedactionPolicy, make([]commonPath, len(p.Paths))}
	for i, path := range p.Paths {
		common.paths[i] = commonPath{path.Source, path.Version, path.CredentialSource, path.CredentialType, path.References, path.RequiredNativeRequests, path.RequiredAttempts}
	}
	return common
}

func validateProposal(p Proposal) error {
	if err := validateCommonProposal(proposalCommon(p)); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, path := range p.Paths {
		key := path.Provider + "/" + path.Model + "/" + path.Variant
		switch path.Transport {
		case "direct_openai":
			if key != "openai/gpt-5.6-luna/high" {
				return fmt.Errorf("priced OpenAI evidence requires openai/gpt-5.6-luna with variant high")
			}
			if path.AccessMode != "documented_direct" && path.AccessMode != "source_only_unavailable" {
				return fmt.Errorf("direct OpenAI path has invalid access mode %q", path.AccessMode)
			}
			if path.AccessMode == "source_only_unavailable" && strings.TrimSpace(path.UnavailableCondition) == "" {
				return fmt.Errorf("source-only direct path requires an exact unavailable condition")
			}
			seen[path.Transport] = true
		case "opencode":
			if strings.TrimSpace(path.Provider) == "" || strings.TrimSpace(path.Model) == "" {
				return fmt.Errorf("OpenCode evidence requires a provider and model")
			}
			if path.Provider == "github-copilot" {
				return fmt.Errorf("github-copilot/* models are not permitted; use openai/gpt-5.6-luna with variant high")
			}
			if path.AccessMode != "runtime" {
				return fmt.Errorf("OpenCode path requires runtime access mode")
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
	global := ceilingCondition(p.Limits, requests, attempts, spend)
	if global != "" {
		for i := range conditions {
			if conditions[i] == "" {
				conditions[i] = global
			}
		}
	}
	return conditions
}

func ceilingCondition(limits Limits, requests, attempts int, spend float64) string {
	switch {
	case limits.MaxNativeRequests <= 0 || requests > limits.MaxNativeRequests:
		return fmt.Sprintf("declared native request ceiling %d cannot cover %d required requests", limits.MaxNativeRequests, requests)
	case limits.MaxAttempts <= 0 || attempts > limits.MaxAttempts:
		return fmt.Sprintf("declared attempt ceiling %d cannot cover %d required attempts", limits.MaxAttempts, attempts)
	case limits.MaxSpendUSD < 0 || spend > limits.MaxSpendUSD+1e-12 || math.IsNaN(spend) || math.IsInf(spend, 0):
		return fmt.Sprintf("declared spend ceiling %s is below worst-case spend %s", money(limits.MaxSpendUSD), money(spend))
	default:
		duration, err := time.ParseDuration(limits.MaxWallTime)
		if err != nil || duration <= 0 {
			return fmt.Sprintf("declared maximum wall time %q is not a positive duration", limits.MaxWallTime)
		}
	}
	return ""
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
