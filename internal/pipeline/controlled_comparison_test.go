package pipeline_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/cli"
	"github.com/jgabor/ktx/internal/db"
)

const (
	comparisonWarmups = 100
	comparisonSamples = 1000
	latencyGateNS     = int64(2 * time.Millisecond)
)

type comparisonArm struct {
	OutputBytes  int              `json:"output_bytes"`
	OutputSHA256 string           `json:"output_sha256"`
	CueBytes     int              `json:"cue_bytes"`
	CueSHA256    *string          `json:"cue_sha256"`
	Ran          []string         `json:"ran"`
	Latency      latencyAggregate `json:"latency_ns"`
}

type latencyAggregate struct {
	Count int   `json:"count"`
	Sum   int64 `json:"sum"`
	Min   int64 `json:"min"`
	Max   int64 `json:"max"`
	Mean  int64 `json:"mean"`
}

type comparisonCase struct {
	ID                  string        `json:"id"`
	InputBytes          int           `json:"input_bytes"`
	InputSHA256         string        `json:"input_sha256"`
	Eligible            bool          `json:"eligible"`
	Baseline            comparisonArm `json:"baseline"`
	Candidate           comparisonArm `json:"candidate"`
	GrossBytesSaved     int           `json:"gross_bytes_saved"`
	NetBytesSaved       int           `json:"net_bytes_saved"`
	Safety              string        `json:"safety"`
	RecoveryExercised   bool          `json:"recovery_exercised"`
	PersistedSourceID   *string       `json:"persisted_source_id"`
	FooterSourceID      *string       `json:"footer_source_id"`
	RecoverySHA256      *string       `json:"recovery_sha256"`
	RecoveryByteExact   bool          `json:"recovery_byte_exact"`
	AdditionalToolCalls int           `json:"additional_tool_calls"`
}

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (a *latencyAggregate) add(ns int64) {
	if a.Count == 0 || ns < a.Min {
		a.Min = ns
	}
	if ns > a.Max {
		a.Max = ns
	}
	a.Count++
	a.Sum += ns
	a.Mean = a.Sum / int64(a.Count)
}

func TestKeepEndsControlledComparison(t *testing.T) {
	var matrix struct {
		Cases []matrixCase `json:"cases"`
	}
	b, err := os.ReadFile("../../tools/bench/evidence/targeted-residual-evidence-matrix.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &matrix); err != nil {
		t.Fatal(err)
	}

	receipt := struct {
		Schema         string           `json:"schema"`
		Intervention   map[string]any   `json:"intervention"`
		LatencyPolicy  map[string]any   `json:"latency_policy"`
		ToolCallEffect map[string]any   `json:"tool_call_effect"`
		Cases          []comparisonCase `json:"cases"`
		Totals         map[string]any   `json:"totals"`
		Disposition    string           `json:"disposition"`
	}{
		Schema:         "ktx.targeted-residual-controlled-comparison.v1",
		Intervention:   map[string]any{"stage": "keep-ends:73:1", "scope": "test-only registry/Runner invocation", "shipped_configuration_changed": false},
		LatencyPolicy:  map[string]any{"warmups_per_case": comparisonWarmups, "paired_samples_per_case": comparisonSamples, "order": "alternating baseline-first/candidate-first", "clock": "time.Since monotonic nanoseconds", "summary": "count/sum/min/max/mean; mean is integer sum/count", "candidate_mean_gate_ns": latencyGateNS},
		ToolCallEffect: map[string]any{"definition": "pipeline execution introduces no agent/runtime tool calls; each lossy result exercises one local retained-original retrieval", "additional_agent_runtime_calls": 0},
	}

	gross, cues, recoveryExercises := 0, 0, 0
	accepted := true
	recoveryDB, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer recoveryDB.Close()
	const sessionID = "comparisonxxxxxx"
	if err := recoveryDB.InsertSession(&db.Session{ID: sessionID, Agent: "test", StartedAt: "2026-08-19T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range matrix.Cases {
		original := generateMatrixCase(t, c)
		baseline := runMatrixCandidate(matrixCase{Tool: "bash"}, original, "keep-ends:73:1")
		candidate := runMatrixCandidate(c, original, "keep-ends:73:1")
		var cue []byte
		if c.Expected.Eligible && len(candidate.Out) < len(original) {
			sourceID := map[string]string{"eligible-middle": "comparemiddlexxx", "eligible-tail": "comparetailxxxxx"}[c.ID]
			if err := recoveryDB.PersistToolCall(&db.ToolCall{ID: sourceID, SessionID: sessionID, Tool: c.Tool, Output: string(original), ContentHash: digest(original), Timestamp: "2026-08-19T00:00:01Z"}); err != nil {
				t.Fatal(err)
			}
			cue = []byte(cli.RecoveryFooter(sourceID, len(original), len(candidate.Out)))
		}

		row := comparisonCase{ID: c.ID, InputBytes: len(original), InputSHA256: digest(original), Eligible: c.Expected.Eligible}
		row.Baseline = comparisonArm{OutputBytes: len(baseline.Out), OutputSHA256: digest(baseline.Out), Ran: baseline.Ran}
		row.Candidate = comparisonArm{OutputBytes: len(candidate.Out), OutputSHA256: digest(candidate.Out), CueBytes: len(cue), Ran: candidate.Ran}
		if len(cue) > 0 {
			h := digest(cue)
			row.Candidate.CueSHA256 = &h
		}
		row.GrossBytesSaved = len(original) - len(candidate.Out)
		row.NetBytesSaved = row.GrossBytesSaved - len(cue)
		gross += row.GrossBytesSaved
		cues += len(cue)

		row.Safety = "pass"
		if !bytes.Equal(baseline.Out, original) || (!c.Expected.Eligible && (!bytes.Equal(candidate.Out, original) || len(candidate.Ran) != 0)) {
			row.Safety, accepted = "fail_passthrough", false
		}
		for _, fact := range c.Expected.RequiredFacts {
			if bytes.Count(candidate.Out, []byte(fact.Fact)) != fact.ExactOccurrences {
				row.Safety, accepted = "fail_required_fact_count", false
			}
		}
		if c.Expected.Errors.RequiredText != nil && bytes.Count(candidate.Out, []byte(*c.Expected.Errors.RequiredText)) != c.Expected.Errors.ExactOccurrences {
			row.Safety, accepted = "fail_error_count", false
		}
		if len(cue) > 0 {
			const prefix, suffix = "`ktx read ", "`"
			start := strings.Index(string(cue), prefix)
			if start < 0 {
				t.Fatalf("case %q recovery footer has no read command: %q", c.ID, cue)
			}
			end := strings.Index(string(cue)[start+len(prefix):], suffix)
			if end < 0 {
				t.Fatalf("case %q recovery footer has no read command terminator: %q", c.ID, cue)
			}
			footerSourceID := string(cue)[start+len(prefix) : start+len(prefix)+end]
			persistedSourceID := map[string]string{"eligible-middle": "comparemiddlexxx", "eligible-tail": "comparetailxxxxx"}[c.ID]
			row.PersistedSourceID, row.FooterSourceID = &persistedSourceID, &footerSourceID
			if footerSourceID != persistedSourceID {
				t.Fatalf("case %q footer source ID = %q, persisted %q", c.ID, footerSourceID, persistedSourceID)
			}
			if err := recoveryDB.InsertCompressed(&db.Compressed{ID: "artifact" + footerSourceID[8:], SourceID: footerSourceID, Output: string(candidate.Out), ContentHash: digest(candidate.Out), Pipeline: `{"profile":"experiment","route":"paired","stages":["keep-ends:73:1"]}`, Timestamp: "2026-08-19T00:00:02Z"}); err != nil {
				t.Fatal(err)
			}
			recovered, err := recoveryDB.GetSourceOutput(footerSourceID)
			if err != nil {
				t.Fatal(err)
			}
			h := digest([]byte(recovered))
			row.RecoveryExercised, row.RecoverySHA256, row.RecoveryByteExact = true, &h, bytes.Equal([]byte(recovered), original)
			recoveryExercises++
			if !row.RecoveryByteExact || len(candidate.Out)+len(cue) >= len(original) {
				row.Safety, accepted = "fail_recovery_or_net_gate", false
			}
		}

		for i := 0; i < comparisonWarmups+comparisonSamples; i++ {
			measure := func(eligible bool) int64 {
				start := time.Now()
				if eligible {
					_ = runMatrixCandidate(c, original, "keep-ends:73:1")
				} else {
					_ = runMatrixCandidate(matrixCase{Tool: "bash"}, original, "keep-ends:73:1")
				}
				return time.Since(start).Nanoseconds()
			}
			var baseNS, candidateNS int64
			if i%2 == 0 {
				baseNS, candidateNS = measure(false), measure(true)
			} else {
				candidateNS, baseNS = measure(true), measure(false)
			}
			if i >= comparisonWarmups {
				row.Baseline.Latency.add(baseNS)
				row.Candidate.Latency.add(candidateNS)
			}
		}
		if row.Candidate.Latency.Mean > latencyGateNS {
			row.Safety = "fail_latency_gate"
			accepted = false
		}
		receipt.Cases = append(receipt.Cases, row)
	}
	net := gross - cues
	if gross != 1745 || cues != 211 || net != 1534 || net <= 0 {
		accepted = false
	}
	receipt.Totals = map[string]any{"gross_bytes_saved": gross, "recovery_cue_bytes": cues, "net_bytes_saved": net, "recovery_exercises": recoveryExercises, "additional_agent_runtime_tool_calls": 0}
	if accepted {
		receipt.Disposition = "accept_experiment_only"
	} else {
		receipt.Disposition = "reject_retain_unchanged"
	}

	if path := os.Getenv("KTX_COMPARISON_RECEIPT"); path != "" {
		out, err := json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, '\n')
		if err := os.WriteFile(path, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if !accepted {
		t.Fatal("controlled comparison hard gate failed")
	}
}
