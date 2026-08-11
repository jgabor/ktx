package main

import (
	"strings"
	"testing"
)

func TestCompareUsesExplicitMappingAndCandidateOnlyTarget(t *testing.T) {
	runs := testRuns([]float64{100, 101, 99, 100, 100}, []float64{104, 105, 103, 104, 105}, true)
	report, err := compare(runs, append([]string(nil), requiredOrder...), 1, passingExternalGates())
	if err != nil {
		t.Fatal(err)
	}
	if report.MeasurementVerdict != "retain" || report.Decision != "retain" || report.RerunAllowed {
		t.Fatalf("verdict = measurement %q decision %q rerun %t", report.MeasurementVerdict, report.Decision, report.RerunAllowed)
	}
	if report.SchemaVersion != 2 || report.PolicyVersion != "read-lease-v2" {
		t.Fatalf("schema/policy = %d/%q", report.SchemaVersion, report.PolicyVersion)
	}
	if len(report.Metrics) != 15 {
		t.Fatalf("metric count = %d, want 15", len(report.Metrics))
	}
	metric := report.Metrics[0]
	if metric.Selector != "component.focused_reads[workload.name=ktx-read-output-1024].distribution.latency_nanoseconds_per_operation.median" || metric.BaselineCenter != 100 || metric.Status != "pass" {
		t.Fatalf("first metric = %#v", metric)
	}
	if targetGate := report.HardGates[4]; targetGate.Name != "blocked_reader_target" || targetGate.Status != "pass" {
		t.Fatalf("target gate = %#v", targetGate)
	}
}

func TestCompareSequenceOneNoiseAllowsOneRerun(t *testing.T) {
	runs := testRuns([]float64{90, 100, 110, 120, 130}, []float64{100, 100, 100, 100, 100}, true)
	first, err := compare(runs, append([]string(nil), requiredOrder...), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.MeasurementVerdict != "inconclusive" || !first.RerunAllowed || first.Decision != "inconclusive" {
		t.Fatalf("first sequence = %#v", first)
	}
	if first.Metrics[0].AllowedMaximum == nil {
		t.Fatalf("noisy lower metric omitted its allowed maximum: %#v", first.Metrics[0])
	}
}

func TestCompareSequenceTwoNoiseWithinAllowanceRetainsWithoutAuthorization(t *testing.T) {
	runs := testRuns([]float64{90, 100, 110, 120, 130}, []float64{100, 100, 100, 100, 100}, true)
	second, err := compare(runs, append([]string(nil), requiredOrder...), 2, passingExternalGates())
	if err != nil {
		t.Fatal(err)
	}
	if second.MeasurementVerdict != "retain" || second.RerunAllowed || second.Decision != "retain" {
		t.Fatalf("second sequence = %#v", second)
	}
	noisy := 0
	for _, metric := range second.Metrics {
		if metric.Status == "pass_non_authorizing_noisy" {
			noisy++
		} else if metric.Status != "pass" {
			t.Fatalf("metric %q status = %q", metric.Name, metric.Status)
		}
		if metric.Direction == "lower" && metric.AllowedMaximum == nil {
			t.Fatalf("metric %q omitted allowed maximum", metric.Name)
		}
		if metric.Direction == "higher" && metric.AllowedMinimum == nil {
			t.Fatalf("metric %q omitted allowed minimum", metric.Name)
		}
	}
	if noisy != 7 {
		t.Fatalf("non-authorizing noisy timing metrics = %d, want 7", noisy)
	}
}

func TestCompareSequenceTwoNoiseBeyondAllowanceRejects(t *testing.T) {
	runs := testRuns([]float64{90, 100, 110, 120, 130}, []float64{140, 140, 140, 140, 140}, true)
	report, err := compare(runs, append([]string(nil), requiredOrder...), 2, passingExternalGates())
	if err != nil {
		t.Fatal(err)
	}
	if report.MeasurementVerdict != "reject" || report.Decision != "reject" || report.RerunAllowed {
		t.Fatalf("report = measurement %q decision %q rerun %t", report.MeasurementVerdict, report.Decision, report.RerunAllowed)
	}
	if report.Metrics[0].Status != "regression" || report.Metrics[0].AllowedMaximum == nil {
		t.Fatalf("noisy regression metric = %#v", report.Metrics[0])
	}
}

func TestCompareStableRegressionRejects(t *testing.T) {
	runs := testRuns([]float64{100, 100, 100, 100, 100}, []float64{106, 106, 106, 106, 106}, true)
	report, err := compare(runs, append([]string(nil), requiredOrder...), 2, passingExternalGates())
	if err != nil {
		t.Fatal(err)
	}
	if report.Metrics[0].Status != "regression" || report.Decision != "reject" || report.RerunAllowed {
		t.Fatalf("stable regression report = %#v", report)
	}
}

func TestCompareFailedExternalGateOverridesInconclusiveMeasurement(t *testing.T) {
	runs := testRuns([]float64{90, 100, 110, 120, 130}, []float64{100, 100, 100, 100, 100}, true)
	gates := passingExternalGates()
	gates["mage_check"] = false
	report, err := compare(runs, append([]string(nil), requiredOrder...), 1, gates)
	if err != nil {
		t.Fatal(err)
	}
	if report.MeasurementVerdict != "inconclusive" || report.Decision != "reject" || report.RerunAllowed {
		t.Fatalf("report = measurement %q decision %q rerun %t", report.MeasurementVerdict, report.Decision, report.RerunAllowed)
	}
}

func TestCompareRejectsBlockedCandidateTarget(t *testing.T) {
	runs := testRuns([]float64{100, 100, 100, 100, 100}, []float64{100, 100, 100, 100, 100}, false)
	report, err := compare(runs, append([]string(nil), requiredOrder...), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.MeasurementVerdict != "reject" || report.Decision != "reject" {
		t.Fatalf("report = %#v", report)
	}
}

func TestMetricAllowances(t *testing.T) {
	lower := metricSpec{name: "latency", direction: "lower", timing: true, practicalAllowance: 0.05, noiseLimit: 0.05}
	if result := evaluateMetric(lower, []float64{100, 100, 100, 100, 100}, []float64{106, 106, 106, 106, 106}, 2); result.Status != "regression" {
		t.Fatalf("latency allowance = %#v", result)
	}
	allocation := metricSpec{name: "allocation", direction: "lower", practicalAllowance: 0.10, noiseLimit: 0.05}
	if result := evaluateMetric(allocation, []float64{100, 100, 100, 100, 100}, []float64{110, 110, 110, 110, 110}, 2); result.Status != "pass" {
		t.Fatalf("allocation allowance = %#v", result)
	}
	if result := evaluateMetric(allocation, []float64{90, 100, 110, 120, 130}, []float64{100, 100, 100, 100, 100}, 2); result.Status != "pass" {
		t.Fatalf("noisy allocation gate = %#v", result)
	}
	p99 := metricSpec{name: "p99", direction: "lower", timing: true, practicalAllowance: 0.10, noiseLimit: 0.10}
	if result := evaluateMetric(p99, []float64{100, 100, 100, 100, 100}, []float64{110, 110, 110, 110, 110}, 2); result.Status != "pass" {
		t.Fatalf("p99 allowance = %#v", result)
	}
	throughput := metricSpec{name: "throughput", direction: "higher", timing: true, practicalAllowance: 0.05, noiseLimit: 0.05}
	if result := evaluateMetric(throughput, []float64{100, 100, 100, 100, 100}, []float64{95, 95, 95, 95, 95}, 2); result.Status != "pass" {
		t.Fatalf("throughput allowance = %#v", result)
	}
	if result := evaluateMetric(throughput, []float64{100, 100, 100, 100, 100}, []float64{94, 94, 94, 94, 94}, 2); result.Status != "regression" {
		t.Fatalf("throughput regression = %#v", result)
	}
}

func TestReadRunsRejectsWrongCounterbalancedOrder(t *testing.T) {
	inputs := make([]string, len(requiredOrder))
	for index := range inputs {
		inputs[index] = "B=/tmp/evidence"
	}
	inputs[0] = "C=/tmp/evidence"
	if _, _, err := readRuns(inputs); err == nil || !strings.Contains(err.Error(), "want B") {
		t.Fatalf("order error = %v", err)
	}
}

func testRuns(baseline, candidate []float64, candidateTarget bool) []observedRun {
	runs := make([]observedRun, 0, len(requiredOrder))
	baselineIndex, candidateIndex := 0, 0
	for index, arm := range requiredOrder {
		value := 0.0
		if arm == "B" {
			value = baseline[baselineIndex]
			baselineIndex++
		} else {
			value = candidate[candidateIndex]
			candidateIndex++
		}
		focused := make(map[string]focusedReadEvidence, 4)
		for workload, size := range map[string]int{
			"ktx-read-output-1024":    1024,
			"ktx-read-output-65536":   65536,
			"ktx-read-output-1048576": 1048576,
			"ktx-read-output-2097152": 2097152,
		} {
			focused[workload] = focusedReadEvidence{
				SchemaVersion: focusedReadSchemaVersion,
				Name:          "ktx-read",
				Workload: workloadIdentity{
					Name: workload, Kind: "ktx-read-output", Bytes: size, GeneratorSeed: 1, SHA256: strings.Repeat("a", 64),
				},
				Distribution: readDistribution{
					LatencyNanosecondsPerOperation: numericDistribution{Median: value},
					AllocatedBytesPerOperation:     numericDistribution{Median: value},
					AllocationsPerOperation:        numericDistribution{Median: value},
				},
			}
		}
		targetPass := arm == "C" && candidateTarget
		runs = append(runs, observedRun{
			Position: index + 1, Arm: arm, EvidenceDirectory: "/tmp/evidence", FocusedReads: focused,
			BinarySHA256: map[string]string{"B": strings.Repeat("a", 64), "C": strings.Repeat("b", 64)}[arm],
			HookC1P50:    value, HookC32P99: value, HookC32Throughput: value,
			ExactReconciled: true, SupportedFailures: true, BlockedTargetPass: targetPass,
		})
	}
	return runs
}

func passingExternalGates() map[string]bool {
	gates := make(map[string]bool, len(requiredExternalGates))
	for _, name := range requiredExternalGates {
		gates[name] = true
	}
	return gates
}
