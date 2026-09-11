package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/cli/testutil"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

func TestLiveValidationGateCanQualifyAndCountsDistinctCues(t *testing.T) {
	matrix, _, err := loadLiveMatrix("../../tools/bench/evidence/targeted-residual-evidence-matrix.json")
	if err != nil {
		t.Fatal(err)
	}
	receipt, _, err := loadLiveReceipt("../../tools/bench/evidence/targeted-residual-controlled-comparison.json")
	if err != nil {
		t.Fatal(err)
	}
	data := dbpkg.LiveValidationData{AuthoritativeToolCalls: 100}
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	lat := int64(10)
	for i := 0; i < 100; i++ {
		data.Sources = append(data.Sources, dbpkg.LiveValidationSource{ID: "source", Disposition: "artifact_persisted", ArtifactID: "artifact", RawBytes: 400, DeliveredBytes: 200, DeliveryMeasured: true, RecoveryCueObserved: i < 20, LatencyMs: &lat, LatencyState: dbpkg.LatencyStateMeasured, LatencyAuthority: dbpkg.LatencyAuthorityRuntime})
	}
	for i := 0; i < 20; i++ {
		cue := string(rune('a'+i)) + "cue"
		exact := true
		data.Recoveries = append(data.Recoveries, dbpkg.LiveValidationRecovery{CueSourceID: cue, AttemptSourceID: "attempt", ResultState: "succeeded", ByteExact: &exact})
		if i == 0 {
			data.Recoveries = append(data.Recoveries, dbpkg.LiveValidationRecovery{CueSourceID: cue, AttemptSourceID: "retry", ResultState: "succeeded", ByteExact: &exact})
		}
		instance := string(rune('a'+i)) + "run"
		data.RunEvents = append(data.RunEvents, dbpkg.LiveValidationRunEvent{InstanceID: instance, State: "started", ObservedAt: from.Add(time.Minute).Format(time.RFC3339Nano)}, dbpkg.LiveValidationRunEvent{InstanceID: instance, State: "stopped", ObservedAt: from.Add(2 * time.Minute).Format(time.RFC3339Nano)})
	}
	r := buildLiveValidationReport(from, from.Add(72*time.Hour), "collectionabcdef", "run", "keep-ends:73:1", matrix, receipt, data)
	applyLiveContinuity(&r, dbpkg.ContinuityResult{Continuous: true})
	if r.Status != "qualified" || r.Recovery.DistinctFollowedCues != 20 || r.Recovery.AuthoritativeAttempts != 21 || r.Recovery.ByteExact.Numerator != 21 || r.Recovery.ByteExact.Denominator != 21 || r.Recovery.ByteExact.Percent != 100 || r.Recovery.ByteExact.Authority == "" {
		t.Fatalf("report status=%s recovery=%+v", r.Status, r.Recovery)
	}
}

func TestLiveValidationByteExactRateIncludesFailedAndNonExactAttempts(t *testing.T) {
	matrix, _, _ := loadLiveMatrix("../../tools/bench/evidence/targeted-residual-evidence-matrix.json")
	receipt, _, _ := loadLiveReceipt("../../tools/bench/evidence/targeted-residual-controlled-comparison.json")
	data := dbpkg.LiveValidationData{}
	lat := int64(1)
	for i := 0; i < 100; i++ {
		data.Sources = append(data.Sources, dbpkg.LiveValidationSource{Disposition: "artifact_persisted", RawBytes: 100, DeliveredBytes: 50, DeliveryMeasured: true, RecoveryCueObserved: i < 20, LatencyMs: &lat, LatencyState: dbpkg.LatencyStateMeasured, LatencyAuthority: dbpkg.LatencyAuthorityRuntime})
	}
	exact, notExact := true, false
	for i := 0; i < 20; i++ {
		cue := fmt.Sprintf("cue%d", i)
		data.Recoveries = append(data.Recoveries, dbpkg.LiveValidationRecovery{CueSourceID: cue, AttemptSourceID: "attempt", ResultState: "succeeded", ByteExact: &exact})
	}
	data.Recoveries = append(data.Recoveries, dbpkg.LiveValidationRecovery{CueSourceID: "cue0", AttemptSourceID: "failed", ResultState: "failed"}, dbpkg.LiveValidationRecovery{CueSourceID: "cue1", AttemptSourceID: "nonexact", ResultState: "succeeded", ByteExact: &notExact})
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	r := buildLiveValidationReport(from, from.Add(time.Hour), "collectionabcdef", "run", "keep-ends:73:1", matrix, receipt, data)
	if r.Recovery.ByteExact.Numerator != 20 || r.Recovery.ByteExact.Denominator != 22 || r.Recovery.Gate.State != "blocked" {
		t.Fatalf("recovery=%+v", r.Recovery)
	}
}

func TestStatsLiveValidationBlockedReportIsReadOnly(t *testing.T) {
	testutil.WithTempXDG(t)
	path := os.Getenv("KTX_DB")
	d, err := dbpkg.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	opened := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if _, err := d.EnsureCollectionInterval("collectionabcdef", []string{"run"}, opened.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runDispatch("stats", []string{"live-validation", "--from", "2026-08-01T00:00:00Z", "--to", "2026-08-01T01:00:00Z", "--collection", "collectionabcdef", "--ingress", "run", "--candidate", "keep-ends:73:1", "--matrix", "../../tools/bench/evidence/targeted-residual-evidence-matrix.json", "--receipt", "../../tools/bench/evidence/targeted-residual-controlled-comparison.json"}, false, &stdout, &stderr)
	if code != ExitConflict {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var report liveValidationReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "blocked" || report.Delivery.Gate.Denominator != 0 || report.RequiredFacts.Numerator != 11 {
		t.Fatalf("report=%+v", report)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !afterInfo.ModTime().Equal(info.ModTime()) {
		t.Fatal("read-only report changed database bytes or mtime")
	}
}

func TestStatsLiveValidationReceiptMismatchBlocksRequiredFacts(t *testing.T) {
	testutil.WithTempXDG(t)
	d, err := dbpkg.Open(os.Getenv("KTX_DB"))
	if err != nil {
		t.Fatal(err)
	}
	opened := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if _, err := d.EnsureCollectionInterval("collectionabcdef", []string{"run"}, opened.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	d.Close()
	receipt, err := os.ReadFile("../../tools/bench/evidence/targeted-residual-controlled-comparison.json")
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(string(receipt), `"safety": "pass"`, `"safety": "fail"`, 1)
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runDispatch("stats", []string{"live-validation", "--from", "2026-08-01T00:00:00Z", "--to", "2026-08-01T01:00:00Z", "--collection", "collectionabcdef", "--ingress", "run", "--candidate", "keep-ends:73:1", "--matrix", "../../tools/bench/evidence/targeted-residual-evidence-matrix.json", "--receipt", path}, false, &stdout, &stderr)
	if code != ExitConflict {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var report liveValidationReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.RequiredFacts.Numerator >= 11 || report.RequiredFacts.State != "blocked" {
		t.Fatalf("required facts=%+v", report.RequiredFacts)
	}
}

func TestStatsHelpListsLiveValidationContract(t *testing.T) {
	var out bytes.Buffer
	printStatsHelp(&out)
	for _, want := range []string{"stats live-validation", "--from UTC", "--collection ID", "--candidate keep-ends:73:1"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help missing %q: %s", want, out.String())
		}
	}
}

func TestStatsLiveValidationHelpIsDedicatedAndDBFree(t *testing.T) {
	testutil.WithTempXDG(t)
	dbPath := os.Getenv("KTX_DB")
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("database exists before help: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := runDispatch("stats", []string{"live-validation", "--help"}, false, &stdout, &stderr)
	if code != ExitOK || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{"ktx stats live-validation —", "Required flags:", "inclusive RFC3339 UTC bound", "exclusive RFC3339 UTC bound", "259200 seconds", "Controlled artifacts:", "--matrix PATH", "--receipt PATH", "exact pinned retained bytes", "qualified status", "blocked report", "byte-exact attempt rates", "  0  help shown", "  3  structured report", "  5  database schema"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help missing %q: %s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "per-tool-call statistics ranked") {
		t.Fatalf("received top-level help: %s", stdout.String())
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("help created database: %v", err)
	}
}

func TestStatsTopHelpDocumentsBlockedExit(t *testing.T) {
	var out bytes.Buffer
	printStatsHelp(&out)
	if !strings.Contains(out.String(), "  3  structured live-validation report is blocked") {
		t.Fatalf("top help missing blocked exit: %s", out.String())
	}
}

func TestLiveValidationDeliveryUsesOnlyCoveredPopulation(t *testing.T) {
	matrix, _, _ := loadLiveMatrix("../../tools/bench/evidence/targeted-residual-evidence-matrix.json")
	receipt, _, _ := loadLiveReceipt("../../tools/bench/evidence/targeted-residual-controlled-comparison.json")
	data := dbpkg.LiveValidationData{Sources: []dbpkg.LiveValidationSource{{RawBytes: 400, DeliveredBytes: 200, DeliveryMeasured: true}, {RawBytes: 40000, DeliveryMeasured: false}}}
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	r := buildLiveValidationReport(from, from.Add(time.Hour), "collectionabcdef", "run", "keep-ends:73:1", matrix, receipt, data)
	if r.Delivery.Calls != 2 || r.Delivery.CoveredCalls != 1 || r.Delivery.RawTokens != 100 || r.Delivery.DeliveredTokens != 50 || r.Delivery.RatioPct != 50 || r.Delivery.RawTokensPerCall != 100 || r.Delivery.DeliveredTokensPerCall != 50 {
		t.Fatalf("delivery=%+v", r.Delivery)
	}
}

func TestQualifiedRunClosuresRequireMatchingStart(t *testing.T) {
	to := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	before := to.Add(-time.Minute).Format(time.RFC3339Nano)
	atEnd := to.Format(time.RFC3339Nano)
	events := []dbpkg.LiveValidationRunEvent{{InstanceID: "valid", State: "started", ObservedAt: before}, {InstanceID: "stop-only", State: "stopped", ObservedAt: before}, {InstanceID: "at-end", State: "started", ObservedAt: atEnd}, {InstanceID: "valid", State: "stopped", ObservedAt: atEnd}, {InstanceID: "interrupted", State: "started", ObservedAt: before}, {InstanceID: "interrupted", State: "interrupted", ObservedAt: before}, {InstanceID: "interrupted", State: "stopped", ObservedAt: atEnd}}
	started, closed, interruptions := qualifiedRunClosures(events, to)
	if len(started) != 2 || !started["valid"] || !started["interrupted"] || len(closed) != 1 || !closed["valid"] || interruptions != 1 {
		t.Fatalf("started=%v closed=%v interruptions=%d", started, closed, interruptions)
	}
}

func TestLiveValidationRejectsArtifactHashMismatch(t *testing.T) {
	for _, fixture := range []struct {
		name, path string
		load       func(string) bool
	}{{"matrix", "../../tools/bench/evidence/targeted-residual-evidence-matrix.json", func(path string) bool { _, a, err := loadLiveMatrix(path); return err == nil && a.Accepted }}, {"receipt", "../../tools/bench/evidence/targeted-residual-controlled-comparison.json", func(path string) bool { _, a, err := loadLiveReceipt(path); return err == nil && a.Accepted }}} {
		t.Run(fixture.name, func(t *testing.T) {
			if !fixture.load(fixture.path) {
				t.Fatal("accepted artifact rejected")
			}
			b, err := os.ReadFile(fixture.path)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), fixture.name+".json")
			if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			if fixture.load(path) {
				t.Fatal("modified artifact hash accepted")
			}
		})
	}
}
