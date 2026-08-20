package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

const (
	liveValidationSchema        = "ktx.stats.live-validation/v1"
	liveValidationMatrixSHA256  = "b13a859487c0fbd329174fdda3e67c212761aa61152bf278208f5d1a8257a2d9"
	liveValidationReceiptSHA256 = "5bf1e31399276e4368a5dd5674bd5535a076eec1a3eaff9c156dcd6d9f3c21e3"
)

type liveValidationGate struct {
	State       string   `json:"state"`
	Reasons     []string `json:"reasons"`
	Population  int      `json:"population"`
	Numerator   int      `json:"numerator"`
	Denominator int      `json:"denominator"`
	CoveragePct float64  `json:"coverage_pct"`
	Authority   string   `json:"authority"`
}

type liveValidationArtifact struct {
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Schema   string `json:"schema"`
	Accepted bool   `json:"accepted"`
}
type liveValidationRate struct {
	Numerator   int     `json:"numerator"`
	Denominator int     `json:"denominator"`
	Percent     float64 `json:"percent"`
	Authority   string  `json:"authority"`
}
type liveValidationReport struct {
	Schema string `json:"schema"`
	Status string `json:"status"`
	Window struct {
		From    string `json:"from"`
		To      string `json:"to"`
		Seconds int64  `json:"seconds"`
	} `json:"window"`
	Collection struct {
		ID        string `json:"id"`
		Ingress   string `json:"ingress"`
		Candidate string `json:"candidate"`
	} `json:"collection"`
	Artifacts struct {
		Matrix  liveValidationArtifact `json:"matrix"`
		Receipt liveValidationArtifact `json:"receipt"`
	} `json:"artifacts"`
	RequiredFacts struct {
		Population  int      `json:"population"`
		Numerator   int      `json:"numerator"`
		Denominator int      `json:"denominator"`
		Cases       int      `json:"cases"`
		State       string   `json:"state"`
		Reasons     []string `json:"reasons"`
	} `json:"required_facts"`
	Delivery struct {
		Gate                   liveValidationGate `json:"gate"`
		Calls                  int                `json:"calls"`
		CoveredCalls           int                `json:"covered_calls"`
		RawTokens              int                `json:"raw_estimated_tokens"`
		DeliveredTokens        int                `json:"delivered_estimated_tokens"`
		RatioPct               float64            `json:"delivered_to_raw_pct"`
		RawTokensPerCall       float64            `json:"raw_tokens_per_call"`
		DeliveredTokensPerCall float64            `json:"delivered_tokens_per_call"`
	} `json:"delivery"`
	Disposition struct {
		Gate        liveValidationGate `json:"gate"`
		Counts      map[string]int     `json:"counts"`
		Percentages map[string]float64 `json:"percentages"`
	} `json:"disposition"`
	Outcomes liveValidationGate `json:"outcomes"`
	Recovery struct {
		Gate                  liveValidationGate `json:"gate"`
		EligibleCues          int                `json:"eligible_lossy_deliveries"`
		IdentityLinkedCues    int                `json:"identity_linked_cues"`
		AuthoritativeAttempts int                `json:"authoritative_attempts"`
		DistinctFollowedCues  int                `json:"distinct_followed_cues"`
		FailedAttempts        int                `json:"failed_attempts"`
		NonExactSuccesses     int                `json:"non_exact_successes"`
		IdentityCoverage      liveValidationRate `json:"identity_coverage"`
		CueFollow             liveValidationRate `json:"cue_follow"`
		ByteExact             liveValidationRate `json:"byte_exact"`
	} `json:"recovery"`
	ToolCallInflation struct {
		Gate                   liveValidationGate `json:"gate"`
		AuthoritativeToolCalls int                `json:"authoritative_tool_calls"`
		StartedRuns            int                `json:"started_runs"`
		ClosedRuns             int                `json:"closed_runs"`
		InterruptedRuns        int                `json:"interrupted_runs"`
		CallsPerClosedRun      float64            `json:"calls_per_closed_run"`
	} `json:"tool_call_inflation"`
	Latency struct {
		Gate     liveValidationGate `json:"gate"`
		Measured int                `json:"measured"`
		P50Ms    float64            `json:"p50_ms"`
		P95Ms    float64            `json:"p95_ms"`
	} `json:"latency"`
	Continuity struct {
		Gate             liveValidationGate    `json:"gate"`
		RequestedSeconds int64                 `json:"requested_seconds"`
		CoveredSeconds   int64                 `json:"covered_seconds"`
		Gaps             []dbpkg.ContinuityGap `json:"gaps"`
	} `json:"continuity"`
}

type liveMatrix struct {
	Schema string `json:"schema"`
	Cases  []struct {
		ID       string `json:"id"`
		Expected struct {
			RequiredFacts []struct {
				Fact  string `json:"fact"`
				Exact int    `json:"exact_occurrences"`
			} `json:"required_facts"`
		} `json:"expected"`
	} `json:"cases"`
}
type liveReceipt struct {
	Schema       string `json:"schema"`
	Intervention struct {
		Stage string `json:"stage"`
	} `json:"intervention"`
	Cases []struct {
		ID, Safety  string
		InputSHA256 string `json:"input_sha256"`
	} `json:"cases"`
	Disposition string `json:"disposition"`
}

func statsLiveValidationLogic(args []string, format string, db *dbpkg.DB, stdout io.Writer) error {
	fs := flag.NewFlagSet("stats live-validation", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var fromText, toText, collection, ingress, candidate, matrixPath, receiptPath string
	fs.StringVar(&fromText, "from", "", "UTC inclusive bound")
	fs.StringVar(&toText, "to", "", "UTC exclusive bound")
	fs.StringVar(&collection, "collection", "", "collection ID")
	fs.StringVar(&ingress, "ingress", "", "supported ingress")
	fs.StringVar(&candidate, "candidate", "", "candidate stage identity")
	fs.StringVar(&matrixPath, "matrix", "tools/bench/evidence/targeted-residual-evidence-matrix.json", "matrix path")
	fs.StringVar(&receiptPath, "receipt", "tools/bench/evidence/targeted-residual-controlled-comparison.json", "receipt path")
	if err := fs.Parse(args); err != nil {
		return usageError("ktx stats live-validation: %v", err)
	}
	if fs.NArg() != 0 {
		return usageError("ktx stats live-validation: unexpected argument %q", fs.Arg(0))
	}
	from, err := time.Parse(time.RFC3339, fromText)
	if err != nil || from.Location() != time.UTC {
		return usageError("ktx stats live-validation: --from must be RFC3339 UTC")
	}
	to, err := time.Parse(time.RFC3339, toText)
	if err != nil || to.Location() != time.UTC || !from.Before(to) {
		return usageError("ktx stats live-validation: --to must be later RFC3339 UTC")
	}
	if !dbpkg.IDPattern.MatchString(collection) || ingress != "run" || candidate != "keep-ends:73:1" {
		return usageError("ktx stats live-validation: require a valid collection, --ingress=run, and --candidate=keep-ends:73:1")
	}
	matrix, matrixArtifact, err := loadLiveMatrix(matrixPath)
	if err != nil {
		return usageError("ktx stats live-validation: matrix: %v", err)
	}
	receipt, receiptArtifact, err := loadLiveReceipt(receiptPath)
	if err != nil {
		return usageError("ktx stats live-validation: receipt: %v", err)
	}
	data, err := db.LiveValidationDataFor(collection, ingress, from, to)
	if err != nil {
		return internalError("ktx stats live-validation: %v", err)
	}
	r := buildLiveValidationReport(from, to, collection, ingress, candidate, matrix, receipt, data)
	r.Artifacts.Matrix = matrixArtifact
	r.Artifacts.Receipt = receiptArtifact
	if !matrixArtifact.Accepted || !receiptArtifact.Accepted {
		r.RequiredFacts.State = "blocked"
		r.RequiredFacts.Reasons = append(r.RequiredFacts.Reasons, "controlled artifact schema or receipt acceptance mismatch")
		r.Status = "blocked"
	}
	continuity, err := db.EvaluateContinuity(collection, from, to)
	if err != nil {
		return internalError("ktx stats live-validation: continuity: %v", err)
	}
	applyLiveContinuity(&r, continuity)
	if format == FormatText {
		renderLiveValidationText(stdout, r)
	} else {
		_ = json.NewEncoder(stdout).Encode(r)
	}
	if r.Status == "blocked" {
		return exitWith(ExitConflict)
	}
	return nil
}

func loadLiveMatrix(path string) (liveMatrix, liveValidationArtifact, error) {
	var v liveMatrix
	a, b, err := loadLiveArtifact(path)
	if err == nil {
		err = json.Unmarshal(b, &v)
	}
	a.Schema = v.Schema
	a.Accepted = err == nil && a.SHA256 == liveValidationMatrixSHA256 && v.Schema == "ktx.targeted-residual-evidence-matrix.v2" && len(v.Cases) == 7
	return v, a, err
}

func loadLiveReceipt(path string) (liveReceipt, liveValidationArtifact, error) {
	var v liveReceipt
	a, b, err := loadLiveArtifact(path)
	if err == nil {
		err = json.Unmarshal(b, &v)
	}
	a.Schema = v.Schema
	a.Accepted = err == nil && a.SHA256 == liveValidationReceiptSHA256 && v.Schema == "ktx.targeted-residual-controlled-comparison.v1" && v.Disposition == "accept_experiment_only"
	return v, a, err
}

func loadLiveArtifact(path string) (liveValidationArtifact, []byte, error) {
	b, err := os.ReadFile(path)
	a := liveValidationArtifact{Path: filepath.Clean(path)}
	if err != nil {
		return a, nil, err
	}
	sum := sha256.Sum256(b)
	a.SHA256 = hex.EncodeToString(sum[:])
	return a, b, nil
}

func gate(pop, num, den int, authority string, reasons ...string) liveValidationGate {
	g := liveValidationGate{State: "qualified", Population: pop, Numerator: num, Denominator: den, Authority: authority, Reasons: reasons}
	if den > 0 {
		g.CoveragePct = math.Round(float64(num)*10000/float64(den)) / 100
	}
	if len(reasons) > 0 {
		g.State = "blocked"
	}
	return g
}

func addReason(reasons []string, condition bool, text string) []string {
	if condition {
		return append(reasons, text)
	}
	return reasons
}

func buildLiveValidationReport(from, to time.Time, collection, ingress, candidate string, m liveMatrix, receipt liveReceipt, data dbpkg.LiveValidationData) liveValidationReport {
	var r liveValidationReport
	r.Schema = liveValidationSchema
	r.Status = "qualified"
	r.Window.From = from.Format(time.RFC3339)
	r.Window.To = to.Format(time.RFC3339)
	r.Window.Seconds = int64(to.Sub(from).Seconds())
	r.Collection.ID = collection
	r.Collection.Ingress = ingress
	r.Collection.Candidate = candidate
	caseSafety := map[string]string{}
	duplicateReceiptCase := false
	for _, c := range receipt.Cases {
		if _, exists := caseSafety[c.ID]; exists {
			duplicateReceiptCase = true
		}
		caseSafety[c.ID] = c.Safety
	}
	facts := 0
	preserved := 0
	factReasons := []string{}
	for _, c := range m.Cases {
		for range c.Expected.RequiredFacts {
			facts++
			if caseSafety[c.ID] == "pass" {
				preserved++
			}
		}
	}
	factReasons = addReason(factReasons, len(m.Cases) != 7 || len(receipt.Cases) != 7, "controlled case linkage mismatch")
	factReasons = addReason(factReasons, duplicateReceiptCase || len(caseSafety) != len(m.Cases), "controlled case identities do not link one-to-one")
	factReasons = addReason(factReasons, receipt.Intervention.Stage != candidate || receipt.Disposition != "accept_experiment_only", "controlled receipt not accepted for candidate")
	factReasons = addReason(factReasons, facts != 11 || preserved != 11, "required facts below 11/11")
	r.RequiredFacts.Population = facts
	r.RequiredFacts.Numerator = preserved
	r.RequiredFacts.Denominator = 11
	r.RequiredFacts.Cases = len(m.Cases)
	r.RequiredFacts.State = "qualified"
	r.RequiredFacts.Reasons = factReasons
	if len(factReasons) > 0 {
		r.RequiredFacts.State = "blocked"
	}
	n := len(data.Sources)
	deliveryCovered, dispositionCovered := 0, 0
	raw, delivered := 0, 0
	disp := map[string]int{}
	latencies := []float64{}
	for _, s := range data.Sources {
		if s.DeliveryMeasured {
			raw += estimatedTokens(s.RawBytes)
			delivered += estimatedTokens(s.DeliveredBytes)
			deliveryCovered++
		}
		if s.Disposition != "" {
			dispositionCovered++
			disp[s.Disposition]++
		}
		if s.LatencyState == dbpkg.LatencyStateMeasured && s.LatencyAuthority == dbpkg.LatencyAuthorityRuntime && s.LatencyMs != nil {
			latencies = append(latencies, float64(*s.LatencyMs))
		}
	}
	deliveryReasons := []string{}
	deliveryReasons = addReason(deliveryReasons, n < 100, "population below 100")
	deliveryReasons = addReason(deliveryReasons, deliveryCovered < 95 || n == 0 || float64(deliveryCovered)/float64(max(1, n)) < .95, "delivery coverage below 95% or 95 calls")
	deliveryReasons = addReason(deliveryReasons, raw <= 0, "raw token denominator is zero")
	r.Delivery.Gate = gate(n, deliveryCovered, n, "wrapper_runtime_delivery", deliveryReasons...)
	r.Delivery.Calls = n
	r.Delivery.CoveredCalls = deliveryCovered
	r.Delivery.RawTokens = raw
	r.Delivery.DeliveredTokens = delivered
	if raw > 0 {
		r.Delivery.RatioPct = float64(delivered) * 100 / float64(raw)
	}
	if deliveryCovered > 0 {
		r.Delivery.RawTokensPerCall = float64(raw) / float64(deliveryCovered)
		r.Delivery.DeliveredTokensPerCall = float64(delivered) / float64(deliveryCovered)
	}
	r.Disposition.Counts = disp
	r.Disposition.Percentages = map[string]float64{}
	for k, v := range disp {
		if n > 0 {
			r.Disposition.Percentages[k] = float64(v) * 100 / float64(n)
		}
	}
	dispositionReasons := []string{}
	dispositionReasons = addReason(dispositionReasons, n < 100, "population below 100")
	dispositionReasons = addReason(dispositionReasons, dispositionCovered < 95 || n == 0 || float64(dispositionCovered)/float64(max(1, n)) < .95, "disposition coverage below 95% or 95 calls")
	r.Disposition.Gate = gate(n, dispositionCovered, n, "compression_disposition", dispositionReasons...)
	outcomeReasons := []string{}
	outcomeReasons = addReason(outcomeReasons, n < 100, "population below 100")
	outcomeReasons = addReason(outcomeReasons, n < 95 || n == 0, "known outcomes below 95 calls or 95%")
	r.Outcomes = gate(n, n, n, "opencode_exit", outcomeReasons...)
	sort.Float64s(latencies)
	latReasons := []string{}
	latReasons = addReason(latReasons, n < 100, "population below 100")
	latReasons = addReason(latReasons, len(latencies) < 95 || n == 0 || float64(len(latencies))/float64(max(1, n)) < .95, "runtime latency coverage below 95% or 95 calls")
	r.Latency.Gate = gate(n, len(latencies), n, "runtime", latReasons...)
	r.Latency.Measured = len(latencies)
	if len(latencies) > 0 {
		r.Latency.P50Ms = statsPercentile(latencies, .5)
		r.Latency.P95Ms = statsPercentile(latencies, .95)
	}
	cues := map[string]bool{}
	followed := map[string]bool{}
	attempts, failed, nonExact, exactSuccess := 0, 0, 0, 0
	for _, x := range data.Recoveries {
		cues[x.CueSourceID] = true
		if x.AttemptSourceID != "" {
			attempts++
			followed[x.CueSourceID] = true
			if x.ResultState == "failed" {
				failed++
			}
			if x.ResultState == "succeeded" && (x.ByteExact == nil || !*x.ByteExact) {
				nonExact++
			}
			if x.ResultState == "succeeded" && x.ByteExact != nil && *x.ByteExact {
				exactSuccess++
			}
		}
	}
	eligibleLossy := 0
	for _, s := range data.Sources {
		if s.DeliveryMeasured && s.RecoveryCueObserved && s.Disposition == "artifact_persisted" && s.DeliveredBytes < s.RawBytes {
			eligibleLossy++
		}
	}
	recReasons := []string{}
	recReasons = addReason(recReasons, eligibleLossy < 20, "eligible lossy deliveries below 20")
	recReasons = addReason(recReasons, len(cues) < 20, "identity-linked delivered cues below 20")
	recReasons = addReason(recReasons, eligibleLossy == 0 || len(cues) != eligibleLossy, "cue identity coverage below 100%")
	recReasons = addReason(recReasons, attempts < 5, "authoritative attempts below 5")
	recReasons = addReason(recReasons, failed > 0 || nonExact > 0, "failed or non-byte-exact recovery")
	r.Recovery.Gate = gate(eligibleLossy, len(cues), eligibleLossy, "adapter_delivery+opencode_hook", recReasons...)
	r.Recovery.EligibleCues = eligibleLossy
	r.Recovery.IdentityLinkedCues = len(cues)
	r.Recovery.AuthoritativeAttempts = attempts
	r.Recovery.DistinctFollowedCues = len(followed)
	r.Recovery.FailedAttempts = failed
	r.Recovery.NonExactSuccesses = nonExact
	r.Recovery.IdentityCoverage = liveRate(len(cues), eligibleLossy)
	r.Recovery.IdentityCoverage.Authority = "wrapper_observation+adapter_ack"
	r.Recovery.CueFollow = liveRate(len(followed), len(cues))
	r.Recovery.CueFollow.Authority = "opencode_hook_attempt"
	r.Recovery.ByteExact = liveRate(exactSuccess, attempts)
	r.Recovery.ByteExact.Authority = "retained_source_hash+opencode_hook_result"
	started, closed, interrupted := qualifiedRunClosures(data.RunEvents, to)
	runReasons := []string{}
	runReasons = addReason(runReasons, len(started) < 20, "started runs below 20")
	runReasons = addReason(runReasons, len(closed) < 19 || len(started) == 0 || float64(len(closed))/float64(max(1, len(started))) < .95, "run closure below 19 or 95%")
	runReasons = addReason(runReasons, interrupted > 0, "interrupted run")
	r.ToolCallInflation.Gate = gate(len(started), len(closed), len(started), "ingress_process", runReasons...)
	r.ToolCallInflation.AuthoritativeToolCalls = data.AuthoritativeToolCalls
	r.ToolCallInflation.StartedRuns = len(started)
	r.ToolCallInflation.ClosedRuns = len(closed)
	r.ToolCallInflation.InterruptedRuns = interrupted
	if len(closed) > 0 {
		r.ToolCallInflation.CallsPerClosedRun = float64(data.AuthoritativeToolCalls) / float64(len(closed))
	}
	if r.RequiredFacts.State == "blocked" || r.Delivery.Gate.State == "blocked" || r.Disposition.Gate.State == "blocked" || r.Outcomes.State == "blocked" || r.Recovery.Gate.State == "blocked" || r.ToolCallInflation.Gate.State == "blocked" || r.Latency.Gate.State == "blocked" {
		r.Status = "blocked"
	}
	return r
}

func liveRate(numerator, denominator int) liveValidationRate {
	r := liveValidationRate{Numerator: numerator, Denominator: denominator}
	if denominator > 0 {
		r.Percent = math.Round(float64(numerator)*10000/float64(denominator)) / 100
	}
	return r
}

func qualifiedRunClosures(events []dbpkg.LiveValidationRunEvent, to time.Time) (map[string]bool, map[string]bool, int) {
	type runState struct{ started, closed, conflict, interrupted bool }
	states := map[string]*runState{}
	interrupted := 0
	for _, e := range events {
		at, err := time.Parse(time.RFC3339Nano, e.ObservedAt)
		if err != nil {
			continue
		}
		s := states[e.InstanceID]
		if s == nil {
			s = &runState{}
			states[e.InstanceID] = s
		}
		switch e.State {
		case "started":
			if !at.Before(to) || s.started {
				s.conflict = true
			} else {
				s.started = true
			}
		case "interrupted":
			if s.started && !s.closed {
				s.interrupted = true
				interrupted++
			}
		case "stopped":
			if !s.started || s.closed {
				s.conflict = true
			} else if !s.conflict && !s.interrupted {
				s.closed = true
			}
		}
	}
	started, closed := map[string]bool{}, map[string]bool{}
	for id, s := range states {
		if s.started && !s.conflict {
			started[id] = true
			if s.closed && !s.interrupted {
				closed[id] = true
			}
		}
	}
	return started, closed, interrupted
}

func applyLiveContinuity(r *liveValidationReport, c dbpkg.ContinuityResult) {
	requested := r.Window.Seconds
	type span struct{ from, to time.Time }
	var spans []span
	windowFrom, _ := time.Parse(time.RFC3339, r.Window.From)
	windowTo, _ := time.Parse(time.RFC3339, r.Window.To)
	for _, g := range c.Gaps {
		a, e1 := time.Parse(time.RFC3339Nano, g.From)
		b, e2 := time.Parse(time.RFC3339Nano, g.To)
		if e1 == nil && e2 == nil && b.After(a) {
			if a.Before(windowFrom) {
				a = windowFrom
			}
			if b.After(windowTo) {
				b = windowTo
			}
			if b.After(a) {
				spans = append(spans, span{a, b})
			}
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].from.Before(spans[j].from) })
	missing := time.Duration(0)
	for i := 0; i < len(spans); {
		end := spans[i].to
		j := i + 1
		for j < len(spans) && !spans[j].from.After(end) {
			if spans[j].to.After(end) {
				end = spans[j].to
			}
			j++
		}
		missing += end.Sub(spans[i].from)
		i = j
	}
	covered := requested - int64(math.Ceil(missing.Seconds()))
	if covered < 0 {
		covered = 0
	}
	reasons := []string{}
	reasons = addReason(reasons, requested != 259200, "requested duration is not 259200 seconds")
	reasons = addReason(reasons, !c.Continuous || len(c.Gaps) > 0, "continuity gap or unclosed interval")
	reasons = addReason(reasons, covered != requested, "continuity coverage below 100%")
	r.Continuity.Gate = gate(int(requested), int(covered), int(requested), "ingress_process", reasons...)
	r.Continuity.RequestedSeconds = requested
	r.Continuity.CoveredSeconds = covered
	r.Continuity.Gaps = c.Gaps
	if r.Continuity.Gate.State == "blocked" {
		r.Status = "blocked"
	}
}

func renderLiveValidationText(w io.Writer, r liveValidationReport) {
	fmt.Fprintf(w, "status: %s\nschema: %s\nwindow: %s to %s (%ds)\ncollection: %s\ncandidate: %s\nrequired_facts: %d/%d (%s)\ndelivery: %s\nrecovery: %s\nrecovery_identity: %d/%d (%.2f%%)\ncue_follow: %d/%d (%.2f%%)\nbyte_exact_recovery: %d/%d (%.2f%%; %s)\ncontinuity: %s\n", r.Status, r.Schema, r.Window.From, r.Window.To, r.Window.Seconds, r.Collection.ID, r.Collection.Candidate, r.RequiredFacts.Numerator, r.RequiredFacts.Denominator, r.RequiredFacts.State, r.Delivery.Gate.State, r.Recovery.Gate.State, r.Recovery.IdentityCoverage.Numerator, r.Recovery.IdentityCoverage.Denominator, r.Recovery.IdentityCoverage.Percent, r.Recovery.CueFollow.Numerator, r.Recovery.CueFollow.Denominator, r.Recovery.CueFollow.Percent, r.Recovery.ByteExact.Numerator, r.Recovery.ByteExact.Denominator, r.Recovery.ByteExact.Percent, r.Recovery.ByteExact.Authority, r.Continuity.Gate.State)
}
