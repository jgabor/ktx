package experiment

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/jgabor/ktx/internal/benchcontract"
)

type ReadDescriptionMetadata struct {
	Version        string        `json:"version"`
	Scorer         string        `json:"scorer,omitempty"`
	Source         string        `json:"source"`
	Boundary       string        `json:"boundary"`
	PostHookSchema string        `json:"post_hook_schema"`
	CacheControl   string        `json:"cache_control"`
	Original       InputMetadata `json:"original"`
	Candidate      InputMetadata `json:"candidate"`
}

func readDescriptionInputs() []InputMetadata {
	return []InputMetadata{
		{ID: "original", Kind: "read-description", Format: "txt", Path: "inputs/read-description/original.txt", SHA256: benchcontract.ReadDescriptionOriginalSHA256},
		{ID: "candidate", Kind: "read-description", Format: "txt", Path: "inputs/read-description/candidate.txt", SHA256: benchcontract.ReadDescriptionCandidateSHA256},
	}
}

func validateReadDescriptionOptions(o CompareOptions) error {
	if o.ReadDescriptionTreatment == "" {
		if o.Task.FollowupPrompt != "" || o.Task.ScoringKind == "read_preservation" {
			return errors.New("followup_prompt and read_preservation require the read-description treatment")
		}
		return nil
	}
	if o.ReadDescriptionTreatment != benchcontract.ReadDescriptionTreatment {
		return fmt.Errorf("unsupported read-description treatment; use %s", benchcontract.ReadDescriptionTreatment)
	}
	if o.Model != "openai/gpt-6-luna" || o.Variant != "high" || o.RuntimeConfig == "" || o.PricingSnapshot == "" || o.CacheControl != "observed" || o.Warmup != 0 || o.WarmupSet || o.methodologyVersion != exactMatchMethodologyVersion || o.TargetValid != 1 || o.MaxAttempts != 2 || o.Task.WorkspaceMode != "corpus" || o.Task.Skill != "" {
		return errors.New("read-description treatment requires exact-match, corpus-only task, openai/gpt-6-luna/high, runtime-config, pinned pricing, observed cache control, target-valid 1, max-attempts 2 and no warmup")
	}
	if o.Task.ScoringKind == "read_preservation" {
		if err := validateReadPreservationTask(o.Task); err != nil {
			return err
		}
	}
	var baseline []byte
	for _, path := range []string{o.BaselineConfig, o.CandidateConfig} {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := readDescriptionPipeline(data); err != nil {
			return err
		}
		if baseline != nil && !bytes.Equal(baseline, data) {
			return errors.New("read-description arms require byte-identical passthrough configurations")
		}
		baseline = data
	}
	for _, input := range readDescriptionInputs() {
		data, err := os.ReadFile(filepath.Join(o.RepoRoot, "docs", "reports", "normalization", "normalization-read-"+input.ID+".txt"))
		if err != nil || sha256Bytes(data) != input.SHA256 {
			return fmt.Errorf("read-description %s fixture missing or changed", input.ID)
		}
	}
	return nil
}

func readDescriptionPipeline(data []byte) error {
	var config struct {
		Pipeline struct {
			Default struct {
				Stages []string `toml:"stages"`
			} `toml:"default"`
		} `toml:"pipeline"`
	}
	metadata, err := toml.Decode(string(data), &config)
	if err != nil || len(metadata.Undecoded()) != 0 || !metadata.IsDefined("pipeline", "default", "stages") || len(config.Pipeline.Default.Stages) != 0 {
		return errors.New("read-description treatment requires only an empty pipeline.default.stages")
	}
	return nil
}

func captureReadDescription(dir string, o CompareOptions) (*ReadDescriptionMetadata, error) {
	if o.ReadDescriptionTreatment == "" {
		return nil, nil
	}
	if err := validateReadDescriptionOptions(o); err != nil {
		return nil, err
	}
	inputs := readDescriptionInputs()
	for _, input := range inputs {
		data, err := os.ReadFile(filepath.Join(o.RepoRoot, "docs", "reports", "normalization", "normalization-read-"+input.ID+".txt"))
		if err != nil || sha256Bytes(data) != input.SHA256 {
			return nil, errors.New("read-description fixture changed during capture")
		}
		path := filepath.Join(dir, input.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return nil, err
		}
	}
	scorer := ""
	if o.Task.ScoringKind == "read_preservation" {
		scorer = readPreservationScorer
	}
	return &ReadDescriptionMetadata{Version: o.ReadDescriptionTreatment, Scorer: scorer, Source: benchcontract.ReadDescriptionSource, Boundary: "tool.definition", PostHookSchema: "unknown", CacheControl: "observed", Original: inputs[0], Candidate: inputs[1]}, nil
}

func (a *archive) validateReadDescription(m Manifest) error {
	var retained []InputMetadata
	for _, input := range m.Inputs {
		if input.Kind == "read-description" {
			retained = append(retained, input)
		}
	}
	if m.ReadDescription == nil {
		if len(retained) > 0 || m.Task.FollowupPrompt != "" || m.Task.ScoringKind == "read_preservation" {
			return errors.New("unclaimed read-description inputs or follow-up")
		}
		return nil
	}
	want := readDescriptionInputs()
	r := m.ReadDescription
	if r.Version != benchcontract.ReadDescriptionTreatment || r.Source != benchcontract.ReadDescriptionSource || r.Boundary != "tool.definition" || r.PostHookSchema != "unknown" || r.CacheControl != "observed" || r.Original != want[0] || r.Candidate != want[1] || !reflect.DeepEqual(retained, want) {
		return errors.New("invalid read-description source, transform or input identity")
	}
	if m.Identities["opencode"] != "1.18.5" || m.Model != "openai/gpt-6-luna" || m.Variant == nil || *m.Variant != "high" || m.RuntimeConfig == nil || m.Pricing == nil || m.Task.WorkspaceMode != "corpus" || m.Task.Skill != "" {
		return errors.New("invalid read-description execution scope")
	}
	if m.Task.ScoringKind == "read_preservation" {
		if r.Scorer != readPreservationScorer {
			return errors.New("invalid read-preservation scorer identity")
		}
		if err := validateReadPreservationTask(m.Task); err != nil {
			return err
		}
	} else if r.Scorer != "" {
		return errors.New("unclaimed read-preservation scorer")
	}
	if !validSHA256(m.Identities["ktx_binary_sha256"]) || !validSHA256(m.Identities["generated_plugin_sha256"]) {
		return errors.New("missing read-description binary/plugin attestation")
	}
	for _, input := range want {
		data, err := a.readFile(input.Path)
		if err != nil {
			return err
		}
		if sha256Bytes(data) != input.SHA256 {
			return errors.New("read-description fixture identity mismatch")
		}
	}
	var baseline []byte
	for _, side := range Sides {
		data, err := a.readFile(m.Pipelines[side].Path)
		if err != nil {
			return err
		}
		if err := readDescriptionPipeline(data); err != nil {
			return err
		}
		if baseline != nil && !bytes.Equal(baseline, data) {
			return errors.New("read-description pipeline differs between arms")
		}
		baseline = data
	}
	return nil
}

type readDefinitionObservation struct {
	Version             string `json:"version"`
	Boundary            string `json:"boundary"`
	Runtime             string `json:"runtime"`
	Owner               string `json:"owner"`
	Interval            string `json:"interval"`
	Epoch               string `json:"epoch"`
	Ordinal             int    `json:"ordinal"`
	Side                string `json:"side"`
	Tool                string `json:"tool"`
	BeforeSHA256        string `json:"before_sha256"`
	AfterSHA256         string `json:"after_sha256"`
	ParametersSHA256    string `json:"parameters_sha256"`
	JSONSchemaSHA256    string `json:"json_schema_sha256"`
	ParametersUnchanged bool   `json:"parameters_unchanged"`
	JSONSchemaUnchanged bool   `json:"json_schema_unchanged"`
	Before              string `json:"before"`
	After               string `json:"after"`
	ParametersAST       string `json:"parameters_ast"`
}

// Required records are bounded public built-in definitions, never assembled
// prompts. The same fingerprint must hold at every invocation and across arms.
func (a *archive) readDescriptionDefinitions(m Manifest, attempt *Attempt) (map[string]string, error) {
	path, claimed := attempt.Artifacts["read_description"]
	if m.ReadDescription == nil {
		if claimed || attempt.ReadDescriptionTreatment != "" {
			return nil, errors.New("unclaimed read-description application")
		}
		return nil, nil
	}
	if attempt.ReadDescriptionTreatment != m.ReadDescription.Version || path != "attempts/"+attempt.AttemptID+"/read-description/hooks.jsonl" || attempt.ContentHashes["read_description"] == "" || attempt.SessionID == nil || attempt.RuntimeEventIntervalID == nil {
		return nil, errors.New("missing read-description application identity")
	}
	file, err := a.open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (16<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 16<<20 || len(data) == 0 || data[len(data)-1] != '\n' || sha256Bytes(data) != attempt.ContentHashes["read_description"] {
		return nil, errors.New("read-description evidence incomplete or changed")
	}
	tools := strings.Fields("invalid question bash read glob grep task webfetch todowrite skill apply_patch")
	definitions := map[string]string{}
	ordinals := map[string]int{}
	counts := map[string]map[string]int{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, 512<<10)
	for scanner.Scan() {
		var r readDefinitionObservation
		if err := decodeStrict(scanner.Bytes(), &r); err != nil {
			return nil, err
		}
		if r.Version != m.ReadDescription.Version || r.Boundary != "tool.definition" || r.Runtime != "1.18.5" || r.Owner != *attempt.SessionID || r.Interval != *attempt.RuntimeEventIntervalID || r.Epoch == "" || r.Ordinal != ordinals[r.Epoch]+1 || r.Side != attempt.Side || !r.ParametersUnchanged || !r.JSONSchemaUnchanged {
			return nil, errors.New("read-description hook provenance mismatch")
		}
		ordinals[r.Epoch]++
		known := false
		for _, tool := range tools {
			known = known || r.Tool == tool
		}
		if !known {
			return nil, errors.New("unexpected read-description tool")
		}
		for _, hash := range []string{r.BeforeSHA256, r.AfterSHA256, r.ParametersSHA256, r.JSONSchemaSHA256} {
			if !validSHA256(hash) {
				return nil, errors.New("invalid tool definition digest")
			}
		}
		if r.Tool == "read" {
			expected := benchcontract.ReadDescriptionOriginalSHA256
			if attempt.Side == Candidate {
				expected = benchcontract.ReadDescriptionCandidateSHA256
			}
			if r.BeforeSHA256 != benchcontract.ReadDescriptionOriginalSHA256 || sha256Bytes([]byte(r.Before)) != r.BeforeSHA256 || r.AfterSHA256 != expected || sha256Bytes([]byte(r.After)) != expected || r.ParametersSHA256 != benchcontract.ReadParametersASTSHA256 || sha256Bytes([]byte(r.ParametersAST)) != r.ParametersSHA256 || r.JSONSchemaSHA256 != sha256Bytes([]byte("null")) || !json.Valid([]byte(r.ParametersAST)) {
				return nil, errors.New("read-description bytes or parameter identity mismatch")
			}
		} else if r.BeforeSHA256 != r.AfterSHA256 || r.Before != "" || r.After != "" || r.ParametersAST != "" {
			return nil, errors.New("non-read definition changed or retained unexpected content")
		}
		identity := r.BeforeSHA256 + r.ParametersSHA256 + r.JSONSchemaSHA256
		if old, exists := definitions[r.Tool]; exists && old != identity {
			return nil, errors.New("tool definition changed within attempt")
		}
		definitions[r.Tool] = identity
		if counts[r.Epoch] == nil {
			counts[r.Epoch] = map[string]int{}
		}
		counts[r.Epoch][r.Tool]++
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	for _, count := range counts {
		for _, tool := range tools {
			if count["read"] == 0 || count[tool] != count["read"] {
				return nil, errors.New("incomplete tool-resolution hook evidence")
			}
		}
	}
	if len(counts) == 0 {
		return nil, errors.New("missing read-description hook invocation")
	}
	attempt.readEpochs = map[string]bool{}
	for epoch := range counts {
		attempt.readEpochs[epoch] = true
	}
	if err := a.validateReadDescriptionTurns(m, *attempt); err != nil {
		return nil, err
	}
	return definitions, nil
}

func validSHA256(hash string) bool {
	return len(hash) == 64 && strings.Trim(hash, "0123456789abcdef") == ""
}

func (a *archive) validateReadDescriptionTurns(m Manifest, attempt Attempt) error {
	turns := 1
	if m.Task.FollowupPrompt != "" {
		turns = 2
	}
	combined := sha256.New()
	session := ""
	for turn := 1; turn <= turns; turn++ {
		key := fmt.Sprintf("turn_%d", turn)
		path := fmt.Sprintf("attempts/%s/read-description/turn-%d.jsonl", attempt.AttemptID, turn)
		if attempt.Artifacts[key] != path || attempt.ContentHashes[key] == "" {
			return errors.New("missing retained turn identity")
		}
		file, err := a.open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		observed, parseErr := RuntimeTurnSession(io.TeeReader(file, io.MultiWriter(combined, hash)))
		err = errors.Join(parseErr, file.Close())
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", hash.Sum(nil)) != attempt.ContentHashes[key] || (session != "" && observed != session) {
			return errors.New("turn content or same-session identity mismatch")
		}
		session = observed
	}
	if turns == 1 && (attempt.Artifacts["turn_2"] != "" || attempt.ContentHashes["turn_2"] != "") {
		return errors.New("unexpected second turn")
	}
	if fmt.Sprintf("%x", combined.Sum(nil)) != attempt.ContentHashes["stream"] {
		return errors.New("retained turns differ from whole-attempt stream")
	}
	path := "attempts/" + attempt.AttemptID + "/launch.json"
	if attempt.Artifacts["launch"] != path || attempt.ContentHashes["launch"] == "" {
		return errors.New("missing read-description launch identity")
	}
	data, err := a.readFile(path)
	if err != nil {
		return err
	}
	var args []string
	if sha256Bytes(data) != attempt.ContentHashes["launch"] || json.Unmarshal(data, &args) != nil {
		return errors.New("read-description launch changed")
	}
	tail := []string{"-m", m.Model, "--variant", *m.Variant}
	if turns == 2 {
		tail = append(tail, "--followup", m.Task.FollowupPrompt)
	}
	tail = append(tail, m.Task.Prompt)
	if len(args) < len(tail)+1 || !reflect.DeepEqual(args[len(args)-len(tail):], tail) || args[len(args)-len(tail)-1] != m.Image["id"] {
		return errors.New("read-description launch model, prompt or image mismatch")
	}
	return nil
}

func validateReadDescriptionComparability(attempts []Attempt) error {
	var first map[string]string
	for _, attempt := range attempts {
		if attempt.readDefinitions == nil {
			continue
		}
		if first != nil && !reflect.DeepEqual(first, attempt.readDefinitions) {
			return errors.New("tool definitions differ between read-description attempts")
		}
		first = attempt.readDefinitions
	}
	return nil
}

func prepareReadDescriptionAttempt(dir, attemptDir, side string, manifest Manifest) ([]string, error) {
	a, err := openArchive(dir)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	if err := a.validateReadDescription(manifest); err != nil {
		return nil, err
	}
	destination := filepath.Join(attemptDir, "read-description")
	if err := os.Mkdir(destination, 0o755); err != nil {
		return nil, err
	}
	for _, input := range readDescriptionInputs() {
		data, err := a.readFile(input.Path)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(destination, input.ID+".txt"), data, 0o644); err != nil {
			return nil, err
		}
	}
	return []string{"-e", benchcontract.ReadDescriptionEnv + "=" + side, "-v", destination + ":" + benchcontract.ReadDescriptionDir + ":rw"}, nil
}
