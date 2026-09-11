package pipeline_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgabor/ktx/internal/cli"
	"github.com/jgabor/ktx/internal/pipeline"
	"github.com/jgabor/ktx/internal/pipeline/stages"
)

type matrixPlacement struct {
	Fact    string `json:"fact"`
	Offsets []int  `json:"offsets"`
}

func runMatrixCandidate(c matrixCase, original []byte, stage string) pipeline.Result {
	if c.Tool != "read" || c.Outcome != "success" {
		return pipeline.Result{Out: bytes.Clone(original)}
	}
	reg := pipeline.NewRegistry()
	stages.RegisterBuiltins(reg)
	return (&pipeline.Runner{Registry: reg, Pipeline: []string{stage}}).Run(original)
}

func TestBoundedCandidateEvaluation(t *testing.T) {
	var matrix struct {
		Cases []matrixCase `json:"cases"`
	}
	path := filepath.Join("..", "..", "tools", "bench", "evidence", "targeted-residual-evidence-matrix.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &matrix); err != nil {
		t.Fatal(err)
	}

	for _, candidate := range []struct {
		stage        string
		wantFailures map[string][]string
		wantGross    int
		wantCue      int
	}{
		{stage: "head:10", wantFailures: map[string][]string{
			"eligible-head":   {"required fact occurrence count: REPEATED_FACT"},
			"eligible-middle": {"required fact occurrence count: MIDDLE_REQUIRED", "required fact occurrence count: REPEATED_FACT"},
			"eligible-tail":   {"required fact occurrence count: TAIL_REQUIRED", "required fact occurrence count: REPEATED_FACT"},
		}, wantGross: 13440, wantCue: 315},
		{stage: "json-compact", wantFailures: map[string][]string{}, wantGross: 0, wantCue: 0},
		{stage: "keep-ends:73:1", wantFailures: map[string][]string{}, wantGross: 1745, wantCue: 211},
	} {
		t.Run(candidate.stage, func(t *testing.T) {
			grossSavings := 0
			cueBytes := 0
			for _, c := range matrix.Cases {
				original := generateMatrixCase(t, c)
				retainedOriginal := bytes.Clone(original)
				result := runMatrixCandidate(c, original, candidate.stage)
				if !c.Expected.Eligible {
					if !bytes.Equal(result.Out, original) || len(result.Ran) != 0 {
						t.Fatalf("case %q violated byte-exact passthrough", c.ID)
					}
				} else {
					grossSavings += len(original) - len(result.Out)
				}
				if c.Expected.Eligible && len(result.Out) < len(original) {
					cueBytes += len(cli.RecoveryFooter("evaluationsrcxxx", len(original), len(result.Out)))
				}
				var failures []string
				for _, required := range c.Expected.RequiredFacts {
					if bytes.Count(result.Out, []byte(required.Fact)) != required.ExactOccurrences {
						failures = append(failures, "required fact occurrence count: "+required.Fact)
					}
				}
				if c.Expected.Errors.RequiredText != nil && bytes.Count(result.Out, []byte(*c.Expected.Errors.RequiredText)) != c.Expected.Errors.ExactOccurrences {
					failures = append(failures, "required error occurrence count")
				}
				if strings.Join(failures, "|") != strings.Join(candidate.wantFailures[c.ID], "|") {
					t.Fatalf("case %q failures = %v, want %v", c.ID, failures, candidate.wantFailures[c.ID])
				}
				if !bytes.Equal(retainedOriginal, original) {
					t.Fatalf("case %q recovery original changed", c.ID)
				}
			}
			if grossSavings != candidate.wantGross || cueBytes != candidate.wantCue {
				t.Fatalf("gross/cue/net bytes = %d/%d/%d, want %d/%d/%d", grossSavings, cueBytes, grossSavings-cueBytes, candidate.wantGross, candidate.wantCue, candidate.wantGross-candidate.wantCue)
			}
		})
	}
}

type matrixFact struct {
	Fact             string `json:"fact"`
	ExactOccurrences int    `json:"exact_occurrences"`
}

type matrixCase struct {
	ID         string   `json:"id"`
	Categories []string `json:"categories"`
	Tool       string   `json:"tool"`
	Outcome    string   `json:"outcome"`
	Generator  struct {
		Algorithm    string            `json:"algorithm"`
		Bytes        int               `json:"bytes"`
		FillByte     string            `json:"fill_byte"`
		NewlineEvery int               `json:"newline_every"`
		Placements   []matrixPlacement `json:"placements"`
		JSON         *struct {
			RequiredFact  string `json:"required_fact"`
			RepeatedFact  string `json:"repeated_fact"`
			RepeatedCount int    `json:"repeated_count"`
			PaddingByte   string `json:"padding_byte"`
		} `json:"json"`
	} `json:"generator"`
	Expected struct {
		RequiredFacts []matrixFact `json:"required_facts"`
		Errors        struct {
			IsError          *bool   `json:"is_error"`
			RequiredText     *string `json:"required_text"`
			ExactOccurrences int     `json:"exact_occurrences"`
		} `json:"errors"`
		Recovery    string `json:"recovery"`
		Eligible    bool   `json:"eligible"`
		Passthrough bool   `json:"passthrough"`
	} `json:"expected"`
}

func generateMatrixCase(t *testing.T, c matrixCase) []byte {
	t.Helper()
	switch c.Generator.Algorithm {
	case "fixed_lines_v1":
		if len(c.Generator.FillByte) != 1 || c.Generator.NewlineEvery < 1 {
			t.Fatalf("case %q has invalid fixed-lines parameters", c.ID)
		}
		out := bytes.Repeat([]byte(c.Generator.FillByte), c.Generator.Bytes)
		for i := c.Generator.NewlineEvery - 1; i < len(out); i += c.Generator.NewlineEvery {
			out[i] = '\n'
		}
		for _, p := range c.Generator.Placements {
			for _, offset := range p.Offsets {
				if offset < 0 || offset+len(p.Fact) > len(out) {
					t.Fatalf("case %q placement %q at %d is out of bounds", c.ID, p.Fact, offset)
				}
				copy(out[offset:], p.Fact)
			}
		}
		return out
	case "json_object_v1":
		p := c.Generator.JSON
		if p == nil || len(p.PaddingByte) != 1 || p.RepeatedCount < 1 {
			t.Fatalf("case %q has invalid JSON parameters", c.ID)
		}
		v := struct {
			Required string   `json:"required"`
			Items    []string `json:"items"`
			Padding  string   `json:"padding"`
		}{Required: p.RequiredFact, Items: make([]string, p.RepeatedCount)}
		for i := range v.Items {
			v.Items[i] = p.RepeatedFact
		}
		base, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		padding := c.Generator.Bytes - len(base)
		if padding < 0 {
			t.Fatalf("case %q JSON exceeds target length", c.ID)
		}
		v.Padding = strings.Repeat(p.PaddingByte, padding)
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return out
	default:
		t.Fatalf("case %q has unknown generator %q", c.ID, c.Generator.Algorithm)
		return nil
	}
}

func TestTargetedResidualEvidenceMatrix(t *testing.T) {
	var matrix struct {
		Schema            string `json:"schema"`
		OpportunitySignal struct {
			Tool           string  `json:"tool"`
			Command        *string `json:"command"`
			Subcommand     *string `json:"subcommand"`
			Profile        string  `json:"profile"`
			Route          string  `json:"route"`
			Disposition    string  `json:"disposition"`
			OutputSizeBand string  `json:"output_size_band"`
		} `json:"opportunity_signal"`
		RoutingContract struct {
			CandidatePredicates   []string `json:"candidate_predicates"`
			UnsupportedPredicates []string `json:"unsupported_predicates"`
			Precedence            string   `json:"precedence"`
			EligibleWhen          string   `json:"eligible_when"`
			Otherwise             string   `json:"otherwise"`
		} `json:"routing_contract"`
		Provenance      json.RawMessage `json:"provenance"`
		EvaluationRules []string        `json:"evaluation_rules"`
		Cases           []matrixCase    `json:"cases"`
	}

	path := filepath.Join("..", "..", "tools", "bench", "evidence", "targeted-residual-evidence-matrix.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &matrix); err != nil {
		t.Fatal(err)
	}
	if matrix.Schema != "ktx.targeted-residual-evidence-matrix.v2" || len(matrix.EvaluationRules) != 7 || len(matrix.Cases) != 7 {
		t.Fatal("matrix contract is incomplete")
	}
	if matrix.OpportunitySignal.Tool != "read" || matrix.OpportunitySignal.Command != nil || matrix.OpportunitySignal.Subcommand != nil || matrix.OpportunitySignal.Profile != "default" || matrix.OpportunitySignal.Route != "default_fallback" || matrix.OpportunitySignal.Disposition != "anti_worsening_rejected" || matrix.OpportunitySignal.OutputSizeBand != "1k-10k" {
		t.Fatal("opportunity signal changed")
	}
	if strings.Join(matrix.RoutingContract.CandidatePredicates, ",") != "tool=read,exit_status=success" || strings.Join(matrix.RoutingContract.UnsupportedPredicates, ",") != "content,output_size" || matrix.RoutingContract.Precedence != "ordered_first_match" || matrix.RoutingContract.EligibleWhen == "" || matrix.RoutingContract.Otherwise != "byte_exact_passthrough" {
		t.Fatal("routing contract is incomplete")
	}
	if len(matrix.Provenance) != 0 || digest(b) != "e55c0e979239c4dd80c33f504ef80d40b4d2499bcbd44153d96b45f89af438b6" {
		t.Fatal("expected the exact provenance-free matrix")
	}

	categories := map[string]int{}
	ruleResults := map[string]map[bool]int{"tool=read": {}, "exit_status=success": {}}
	for _, c := range matrix.Cases {
		original := generateMatrixCase(t, c)
		if len(original) != c.Generator.Bytes || len(original) < 1024 || len(original) >= 10240 {
			t.Fatalf("case %q generated %d bytes, want %d in band", c.ID, len(original), c.Generator.Bytes)
		}
		if c.Generator.Algorithm == "json_object_v1" && !json.Valid(original) {
			t.Fatalf("case %q generated invalid JSON", c.ID)
		}
		for _, category := range c.Categories {
			categories[category]++
		}
		for _, required := range c.Expected.RequiredFacts {
			if got := bytes.Count(original, []byte(required.Fact)); got != required.ExactOccurrences {
				t.Fatalf("case %q fact %q count = %d, want %d", c.ID, required.Fact, got, required.ExactOccurrences)
			}
		}
		if c.Expected.Errors.RequiredText == nil {
			if c.Expected.Errors.ExactOccurrences != 0 {
				t.Fatalf("case %q has error count without text", c.ID)
			}
		} else if got := bytes.Count(original, []byte(*c.Expected.Errors.RequiredText)); got != c.Expected.Errors.ExactOccurrences {
			t.Fatalf("case %q error count = %d, want %d", c.ID, got, c.Expected.Errors.ExactOccurrences)
		}
		wantEligible := c.Tool == "read" && c.Outcome == "success"
		if c.Expected.Eligible != wantEligible || c.Expected.Passthrough == wantEligible || c.Expected.Recovery != "byte_exact_original" {
			t.Fatalf("case %q has incorrect behavior expectations", c.ID)
		}
		wantError := c.Outcome == "failure"
		if (c.Expected.Errors.IsError == nil) != (c.Outcome == "unknown") || c.Expected.Errors.IsError != nil && *c.Expected.Errors.IsError != wantError {
			t.Fatalf("case %q error state does not match outcome", c.ID)
		}
		retainedOriginal := bytes.Clone(original)
		if !wantEligible {
			delivered, artifactCreated := original, false
			if artifactCreated || !bytes.Equal(delivered, original) {
				t.Fatalf("case %q did not pass through byte exactly", c.ID)
			}
		}
		if !bytes.Equal(retainedOriginal, original) {
			t.Fatalf("case %q recovery was not byte exact", c.ID)
		}
		ruleResults["tool=read"][c.Tool == "read"]++
		ruleResults["exit_status=success"][c.Outcome == "success"]++
	}
	for _, category := range []string{"head", "middle", "tail", "structured", "failure", "eligible", "passthrough"} {
		if categories[category] == 0 {
			t.Errorf("matrix has no %s case", category)
		}
	}
	for rule, outcomes := range ruleResults {
		if outcomes[true] == 0 || outcomes[false] == 0 {
			t.Errorf("eligibility rule %s lacks positive or rejecting coverage", rule)
		}
	}
}
