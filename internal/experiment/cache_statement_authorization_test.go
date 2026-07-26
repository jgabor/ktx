package experiment

import (
	"math"
	"math/big"
	"reflect"
	"strings"
	"testing"
)

func authorizationEvidence(t *testing.T, attempts []Attempt) (map[string]any, map[string]any, map[string]any) {
	t.Helper()
	summary := BuildSummary(comparisonManifest(), attempts)
	return summary["direct_cached_input_share"].(map[string]any), summary["estimated_consecutive_step_reuse"].(map[string]any), summary["cached_input_share_comparison"].(map[string]any)
}

func statementAuthorization(t *testing.T, attempts []Attempt) map[string]any {
	t.Helper()
	summary := BuildSummary(comparisonManifest(), attempts)
	authorization, ok := summary["statement_authorization"].(map[string]any)
	if !ok {
		t.Fatalf("statement authorization missing: %#v", summary)
	}
	return authorization
}

func authorizationOutcome(t *testing.T, authorization map[string]any, name string) map[string]any {
	t.Helper()
	outcomes, ok := authorization["outcomes"].(map[string]any)
	if !ok {
		t.Fatalf("authorization outcomes missing: %#v", authorization)
	}
	outcome, ok := outcomes[name].(map[string]any)
	if !ok {
		t.Fatalf("authorization outcome %q missing: %#v", name, outcomes)
	}
	return outcome
}

func assertAuthorized(t *testing.T, outcome map[string]any, want bool, reasons ...string) {
	t.Helper()
	gotReasons, _ := outcome["reasons"].([]string)
	if outcome["authorized"] != want || len(gotReasons) != len(reasons) || (len(reasons) > 0 && !reflect.DeepEqual(gotReasons, reasons)) {
		t.Fatalf("authorization = %#v, want authorized=%v reasons=%v", outcome, want, reasons)
	}
	statement, exists := outcome["statement"]
	if !exists || (want && statement == nil) || (!want && statement != nil) {
		t.Fatalf("statement presence does not match authorization: %#v", outcome)
	}
	if outcome["gates"] == nil || outcome["evidence"] == nil || outcome["scope"] == nil {
		t.Fatalf("authorization metadata incomplete: %#v", outcome)
	}
}

func TestCacheStatementAuthorizationOutcomeMatrixAndBoundaries(t *testing.T) {
	tests := []struct {
		name          string
		attempts      func() []Attempt
		higher        bool
		higherReasons []string
		lower         bool
		lowerReasons  []string
		inconclusive  bool
		inconReasons  []string
	}{
		{name: "four pairs remain descriptive", attempts: func() []Attempt { return comparisonPairs(4, 20, 30) }, higherReasons: []string{"fewer_than_five_complete_pairs"}, lowerReasons: []string{"fewer_than_five_complete_pairs"}, inconReasons: []string{"fewer_than_five_complete_pairs"}},
		{name: "five pairs higher with quality", attempts: func() []Attempt { return comparisonPairs(5, 20, 30) }, higher: true, lowerReasons: []string{"candidate_not_lower"}, inconReasons: []string{"two_sided_95_percent_interval_excludes_zero"}},
		{name: "higher with lower score", attempts: func() []Attempt {
			a := comparisonPairs(5, 20, 30)
			for i := 1; i < len(a); i += 2 {
				a[i].Score.Score = .5
			}
			return a
		}, higherReasons: []string{"quality_not_preserved"}, lowerReasons: []string{"candidate_not_lower"}, inconReasons: []string{"two_sided_95_percent_interval_excludes_zero"}},
		{name: "higher with lower full pass rate", attempts: func() []Attempt {
			a := comparisonPairs(5, 20, 30)
			a[1].FullPass = false
			return a
		}, higherReasons: []string{"quality_not_preserved"}, lowerReasons: []string{"candidate_not_lower"}, inconReasons: []string{"two_sided_95_percent_interval_excludes_zero"}},
		{name: "lower with quality", attempts: func() []Attempt { return comparisonPairs(5, 30, 20) }, higherReasons: []string{"candidate_not_higher"}, lower: true, inconReasons: []string{"two_sided_95_percent_interval_excludes_zero"}},
		{name: "lower without quality", attempts: func() []Attempt {
			a := comparisonPairs(5, 30, 20)
			for i := 1; i < len(a); i += 2 {
				a[i].Score.Score = .5
				a[i].FullPass = false
			}
			return a
		}, higherReasons: []string{"candidate_not_higher", "quality_not_preserved"}, lower: true, inconReasons: []string{"two_sided_95_percent_interval_excludes_zero"}},
		{name: "interval contains zero", attempts: inconclusiveComparisonAttempts, higherReasons: []string{"two_sided_95_percent_interval_contains_zero"}, lowerReasons: []string{"two_sided_95_percent_interval_contains_zero"}, inconclusive: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			authorization := statementAuthorization(t, tc.attempts())
			assertAuthorized(t, authorizationOutcome(t, authorization, "direct_available"), true)
			assertAuthorized(t, authorizationOutcome(t, authorization, "comparative_descriptive"), true)
			assertAuthorized(t, authorizationOutcome(t, authorization, "candidate_higher_success"), tc.higher, tc.higherReasons...)
			assertAuthorized(t, authorizationOutcome(t, authorization, "candidate_lower"), tc.lower, tc.lowerReasons...)
			assertAuthorized(t, authorizationOutcome(t, authorization, "inconclusive"), tc.inconclusive, tc.inconReasons...)
		})
	}
}

func TestCacheStatementAuthorizationUnavailableDirectAndAbsentIdentity(t *testing.T) {
	attempts := comparisonPairs(1, 20, 30)
	for i := range attempts {
		attempts[i].ModelSteps[0].CacheRead = UnknownInteger()
	}
	authorization := statementAuthorization(t, attempts)
	assertAuthorized(t, authorizationOutcome(t, authorization, "direct_available"), false, "direct_evidence_incomplete")

	empty := buildCacheStatementAuthorization(map[string]any{}, map[string]any{}, map[string]any{})
	identity := empty["identity_provenance"].(map[string]any)
	if identity["configured_context_classification"] != "absent" || identity["source_identity_classification"] != "absent" || identity["configured_identity_backfills_source_events"] != false {
		t.Fatalf("absent identity classification = %#v", identity)
	}
}

func inconclusiveComparisonAttempts() []Attempt {
	attempts := comparisonPairs(5, 50, 50)
	reads := []int{90, 10, 90, 10, 50}
	for i := range reads {
		attempts[i*2+1].ModelSteps[0] = comparisonAttempt("x", Candidate, i+1, 1, reads[i], 100, 1, true).ModelSteps[0]
	}
	return attempts
}

func TestCacheStatementAuthorizationFoundationalBlockersAndPrecedence(t *testing.T) {
	wantPrecedence := []string{"unsupported_runtime_accounting", "direct_evidence_incomplete", "evidence_projection_inconsistent", "cycle_pair_incomplete", "protocol_noncompliant", "identity_provenance_unstable", "runtime_emitted_identity_conflict", "fewer_than_five_complete_pairs", "two_sided_95_percent_interval_unavailable", "two_sided_95_percent_interval_contains_zero", "candidate_not_higher", "candidate_not_lower", "two_sided_95_percent_interval_excludes_zero", "quality_not_preserved", "no_eligible_consecutive_step_pairs"}
	if !reflect.DeepEqual(statementAuthorizationReasonPrecedence, wantPrecedence) {
		t.Fatalf("statement reason precedence = %v", statementAuthorizationReasonPrecedence)
	}
	tests := []struct {
		name   string
		mutate func(*Manifest, []Attempt)
		want   []string
	}{
		{name: "unsupported", mutate: func(m *Manifest, _ []Attempt) { m.Identities[directCacheRuntime] = "1.18.3" }, want: []string{"unsupported_runtime_accounting"}},
		{name: "incomplete", mutate: func(_ *Manifest, a []Attempt) { a[1].ModelSteps[0].CacheRead = UnknownInteger() }, want: []string{"direct_evidence_incomplete"}},
		{name: "protocol", mutate: func(_ *Manifest, a []Attempt) { a[1].CacheControl.Source = "other" }, want: []string{"protocol_noncompliant"}},
		{name: "identity conflict", mutate: func(_ *Manifest, a []Attempt) { other := "other"; a[1].ModelSteps[0].Identity.Provider = &other }, want: []string{"runtime_emitted_identity_conflict"}},
		{name: "precedence", mutate: func(_ *Manifest, a []Attempt) {
			a[1].ModelSteps[0].CacheRead = UnknownInteger()
			a[1].CacheControl.Source = "other"
			other := "other"
			a[1].ModelSteps[0].Identity.Provider = &other
		}, want: []string{"direct_evidence_incomplete", "protocol_noncompliant", "runtime_emitted_identity_conflict"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manifest := comparisonManifest()
			attempts := comparisonPairs(5, 20, 30)
			tc.mutate(&manifest, attempts)
			var authorization map[string]any
			if supportsDirectCachedInputShare(manifest) {
				authorization = BuildSummary(manifest, attempts)["statement_authorization"].(map[string]any)
			} else {
				direct := buildDirectCachedInputShare(manifest, attempts)
				secondary := buildSecondaryCacheEstimate(attempts)
				comparison := buildCachedInputShareComparison(manifest, attempts)
				authorization = buildCacheStatementAuthorization(direct, secondary, comparison)
			}
			for _, name := range []string{"candidate_higher_success", "candidate_lower", "inconclusive"} {
				outcome := authorizationOutcome(t, authorization, name)
				got := outcome["reasons"].([]string)
				if len(got) < len(tc.want) || !reflect.DeepEqual(got[:len(tc.want)], tc.want) {
					t.Fatalf("%s reasons = %v, want prefix %v", name, got, tc.want)
				}
				if outcome["statement"] != nil {
					t.Fatalf("blocked directional statement = %#v", outcome)
				}
			}
		})
	}
}

func TestCacheStatementAuthorizationIdentityClassification(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func([]Attempt)
		wantSource string
		wantStable bool
	}{
		{name: "configured only", mutate: func(a []Attempt) {
			for i := range a {
				a[i].ModelSteps[0].Identity.Provider = nil
				a[i].ModelSteps[0].Identity.Model = nil
				a[i].ModelSteps[0].Identity.Runtime = nil
			}
		}, wantSource: "configured_only", wantStable: true},
		{name: "partial", mutate: func(a []Attempt) {
			for i := range a {
				a[i].ModelSteps[0].Identity.Runtime = nil
			}
		}, wantSource: "partial", wantStable: true},
		{name: "present", mutate: func([]Attempt) {}, wantSource: "present", wantStable: true},
		{name: "conflicting", mutate: func(a []Attempt) { other := "other"; a[1].ModelSteps[0].Identity.Model = &other }, wantSource: "present", wantStable: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attempts := comparisonPairs(5, 20, 30)
			tc.mutate(attempts)
			authorization := statementAuthorization(t, attempts)
			identity := authorization["identity_provenance"].(map[string]any)
			if identity["source_identity_classification"] != tc.wantSource || identity["stable_nonconflicting"] != tc.wantStable || identity["configured_identity_backfills_source_events"] != false {
				t.Fatalf("identity classification = %#v", identity)
			}
			if tc.wantSource == "configured_only" && authorizationOutcome(t, authorization, "candidate_higher_success")["authorized"] != true {
				t.Fatalf("configured-only identity blocked otherwise qualified outcome: %#v", authorization)
			}
		})
	}
}

func TestCacheStatementAuthorizationSecondaryAvailabilityAndForbiddenWording(t *testing.T) {
	zero := statementAuthorization(t, comparisonPairs(1, 20, 30))
	assertAuthorized(t, authorizationOutcome(t, zero, "secondary_available"), false, "no_eligible_consecutive_step_pairs")

	attempts := comparisonPairs(1, 20, 30)
	for i := range attempts {
		attempts[i].ModelSteps = append(attempts[i].ModelSteps, directStep(2, MeasuredInteger(60), MeasuredInteger(40), MeasuredInteger(0)))
		attempts[i].ModelSteps[1].Identity = attempts[i].ModelSteps[0].Identity
		attempts[i].sequence = coveredAttemptSequence(2)
	}
	authorization := statementAuthorization(t, attempts)
	secondary := authorizationOutcome(t, authorization, "secondary_available")
	assertAuthorized(t, secondary, true)
	text := secondary["statement"].(string)
	for _, required := range []string{"estimated", "pi_consecutive_step_reusable_basis", "eligible", "ineligible", "descriptive", "non-ranking"} {
		if !strings.Contains(text, required) {
			t.Errorf("secondary statement %q omits %q", text, required)
		}
	}

	for name, raw := range authorization["outcomes"].(map[string]any) {
		statement, _ := raw.(map[string]any)["statement"].(string)
		lower := strings.ToLower(statement)
		for _, forbidden := range []string{"request", "hit", "miss", "fill", "eviction", "eligibility", "prefix", "isolation", "ttl", "missed-cost", "causal", "significant", "cache hit rate"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("%s authorized statement contains forbidden wording %q: %q", name, forbidden, statement)
			}
		}
	}
}

func TestCacheStatementAuthorizationUsesNarrowDirectionalWording(t *testing.T) {
	higherAuthorization := statementAuthorization(t, comparisonPairs(5, 20, 30))
	higher := authorizationOutcome(t, higherAuthorization, "candidate_higher_success")["statement"].(string)
	if !strings.Contains(higher, "candidate had higher OpenCode-reported cached-input share under the retained protocol") || strings.Contains(strings.ToLower(higher), "success") || strings.Contains(strings.ToLower(higher), "improvement") {
		t.Fatalf("higher wording exceeded scope: %q", higher)
	}
	lowerAuthorization := statementAuthorization(t, comparisonPairs(5, 30, 20))
	lower := authorizationOutcome(t, lowerAuthorization, "candidate_lower")["statement"].(string)
	if !strings.Contains(lower, "candidate had lower OpenCode-reported cached-input share under the retained protocol") || strings.Contains(strings.ToLower(lower), "failure") {
		t.Fatalf("lower wording exceeded scope: %q", lower)
	}
	descriptive := authorizationOutcome(t, higherAuthorization, "comparative_descriptive")["statement"].(string)
	if !strings.Contains(descriptive, "Across 5 complete pairs") || !strings.Contains(descriptive, "values were 1/10, 1/10, 1/10, 1/10, 1/10") {
		t.Fatalf("comparative descriptive wording omitted exact pair evidence: %q", descriptive)
	}
}

func TestCacheStatementAuthorizationDeterministicRenderingAndDescriptiveIndependence(t *testing.T) {
	attempts := comparisonPairs(5, 20, 30)
	for i := 1; i < len(attempts); i += 2 {
		attempts[i].Score.Score = .5
	}
	first := BuildSummary(comparisonManifest(), attempts)
	second := BuildSummary(comparisonManifest(), append([]Attempt(nil), attempts...))
	if !reflect.DeepEqual(first["statement_authorization"], second["statement_authorization"]) {
		t.Fatal("statement authorization is not deterministic")
	}
	authorization := first["statement_authorization"].(map[string]any)
	assertAuthorized(t, authorizationOutcome(t, authorization, "comparative_descriptive"), true)
	assertAuthorized(t, authorizationOutcome(t, authorization, "candidate_higher_success"), false, "quality_not_preserved")
	text := RenderSummary(first)
	if !strings.Contains(text, "statement_authorization outcome=comparative_descriptive authorized=true") ||
		!strings.Contains(text, "statement_authorization outcome=candidate_higher_success authorized=false") ||
		!strings.Contains(text, "statement=null") || !strings.Contains(text, "source_identity_classification=present") {
		t.Fatalf("authorization rendering incomplete:\n%s", text)
	}
}

func TestCacheStatementAuthorizationRejectsContradictoryComparisonProjections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   []string
	}{
		{name: "direction conflicts with interval", mutate: func(comparison map[string]any) {
			stats := comparison["direct"].(map[string]any)["paired_difference"].(map[string]any)
			stats["interval"] = map[string]any{"lower": -.1, "upper": .1}
		}, want: []string{"evidence_projection_inconsistent"}},
		{name: "aggregate quality conflicts with pairs", mutate: func(comparison map[string]any) {
			for _, raw := range comparison["pairs"].([]any) {
				candidate := raw.(map[string]any)["candidate"].(map[string]any)
				candidate["quality"] = map[string]any{"fact_score": .5, "full_pass": false}
			}
		}, want: []string{"evidence_projection_inconsistent"}},
		{name: "declared pair count conflicts with rows", mutate: func(comparison map[string]any) {
			comparison["complete_pair_count"] = 4
		}, want: []string{"evidence_projection_inconsistent"}},
		{name: "malformed pair ratio", mutate: func(comparison map[string]any) {
			pair := comparison["pairs"].([]any)[0].(map[string]any)
			pair["candidate"].(map[string]any)["direct_share"].(map[string]any)["ratio"] = "not-a-ratio"
		}, want: []string{"evidence_projection_inconsistent"}},
		{name: "non-finite statistic", mutate: func(comparison map[string]any) {
			stats := comparison["direct"].(map[string]any)["paired_difference"].(map[string]any)
			stats["sample_standard_deviation"] = math.NaN()
		}, want: []string{"evidence_projection_inconsistent"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			direct, secondary, comparison := authorizationEvidence(t, comparisonPairs(5, 20, 30))
			tc.mutate(comparison)
			authorization := buildCacheStatementAuthorization(direct, secondary, comparison)
			for _, outcomeName := range []string{"comparative_descriptive", "candidate_higher_success", "candidate_lower", "inconclusive"} {
				outcome := authorizationOutcome(t, authorization, outcomeName)
				if outcome["authorized"] != false || outcome["statement"] != nil {
					t.Fatalf("%s trusted contradictory projection: %#v", outcomeName, outcome)
				}
				got := outcome["reasons"].([]string)
				if len(got) < len(tc.want) || !reflect.DeepEqual(got[:len(tc.want)], tc.want) {
					t.Fatalf("%s reasons = %v, want prefix %v", outcomeName, got, tc.want)
				}
			}
			if tc.name == "aggregate quality conflicts with pairs" {
				higher := authorizationOutcome(t, authorization, "candidate_higher_success")
				if !containsString(higher["reasons"].([]string), "quality_not_preserved") {
					t.Fatalf("pair-derived quality loss did not block higher outcome: %#v", higher)
				}
			}
		})
	}
}

func TestCacheStatementAuthorizationRejectsUnreportedPairIdentityConflict(t *testing.T) {
	for _, field := range []string{"provider", "model", "runtime"} {
		t.Run(field, func(t *testing.T) {
			direct, secondary, comparison := authorizationEvidence(t, comparisonPairs(5, 20, 30))
			pair := comparison["pairs"].([]any)[0].(map[string]any)
			identity := pair["candidate"].(map[string]any)["identity_provenance"].(map[string]any)
			identity["source_events"].([]any)[0].(map[string]any)[field] = "other"
			comparison["blocking_reasons"] = []string{}
			authorization := buildCacheStatementAuthorization(direct, secondary, comparison)
			for _, name := range []string{"comparative_descriptive", "candidate_higher_success", "candidate_lower", "inconclusive"} {
				outcome := authorizationOutcome(t, authorization, name)
				if outcome["authorized"] != false || !containsString(outcome["reasons"].([]string), "runtime_emitted_identity_conflict") {
					t.Fatalf("%s accepted identity conflict: %#v", name, outcome)
				}
			}
			comparisonIdentity := authorization["identity_provenance"].(map[string]any)["comparison_scope"].(map[string]any)
			if comparisonIdentity["stable_nonconflicting"] != false {
				t.Fatalf("comparison identity conflict hidden: %#v", comparisonIdentity)
			}
		})
	}
}

func TestCacheStatementAuthorizationRejectsWrongDirectRuntimeScope(t *testing.T) {
	direct, secondary, comparison := authorizationEvidence(t, comparisonPairs(5, 20, 30))
	row := direct["attempts"].([]any)[0].(map[string]any)
	runtime := row["identity_provenance"].(map[string]any)["runtime"].(map[string]any)
	runtime["version"] = "other"
	authorization := buildCacheStatementAuthorization(direct, secondary, comparison)
	assertAuthorized(t, authorizationOutcome(t, authorization, "direct_available"), false, "unsupported_runtime_accounting")
	for _, name := range []string{"comparative_descriptive", "candidate_higher_success", "candidate_lower", "inconclusive"} {
		outcome := authorizationOutcome(t, authorization, name)
		if outcome["authorized"] != false || outcome["reasons"].([]string)[0] != "unsupported_runtime_accounting" {
			t.Fatalf("%s ignored direct runtime scope: %#v", name, outcome)
		}
	}
}

func TestCacheStatementAuthorizationRequiresDirectAndSecondaryContracts(t *testing.T) {
	t.Run("wrong direct contract", func(t *testing.T) {
		direct, secondary, comparison := authorizationEvidence(t, comparisonPairs(5, 20, 30))
		direct["contract_version"] = "other"
		authorization := buildCacheStatementAuthorization(direct, secondary, comparison)
		assertAuthorized(t, authorizationOutcome(t, authorization, "direct_available"), false, "unsupported_runtime_accounting")
		for _, name := range []string{"comparative_descriptive", "candidate_higher_success", "candidate_lower", "inconclusive"} {
			outcome := authorizationOutcome(t, authorization, name)
			if outcome["authorized"] != false || outcome["reasons"].([]string)[0] != "unsupported_runtime_accounting" {
				t.Fatalf("%s ignored direct contract: %#v", name, outcome)
			}
		}
	})
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "wrong method", mutate: func(secondary map[string]any) { secondary["method"] = "other" }},
		{name: "ranking effect", mutate: func(secondary map[string]any) { secondary["ranking_effect"] = "rank" }},
		{name: "inconsistent counts", mutate: func(secondary map[string]any) { secondary["eligible_count"] = 99 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts := comparisonPairs(1, 20, 30)
			for i := range attempts {
				attempts[i].ModelSteps = append(attempts[i].ModelSteps, directStep(2, MeasuredInteger(60), MeasuredInteger(40), MeasuredInteger(0)))
				attempts[i].ModelSteps[1].Identity = attempts[i].ModelSteps[0].Identity
				attempts[i].sequence = coveredAttemptSequence(2)
			}
			direct, secondary, comparison := authorizationEvidence(t, attempts)
			tc.mutate(secondary)
			outcome := authorizationOutcome(t, buildCacheStatementAuthorization(direct, secondary, comparison), "secondary_available")
			assertAuthorized(t, outcome, false, "evidence_projection_inconsistent")
		})
	}
}

func TestCacheStatementAuthorizationNormalizesFoundationReasons(t *testing.T) {
	t.Run("empty comparison", func(t *testing.T) {
		direct, secondary, _ := authorizationEvidence(t, comparisonPairs(1, 20, 30))
		authorization := buildCacheStatementAuthorization(direct, secondary, map[string]any{})
		outcome := authorizationOutcome(t, authorization, "comparative_descriptive")
		assertAuthorized(t, outcome, false, "cycle_pair_incomplete")
		if outcome["gates"].(map[string]any)["controlled_comparative_evidence_valid"] != false {
			t.Fatalf("empty comparison marked controlled: %#v", outcome)
		}
	})
	t.Run("temporal exclusion", func(t *testing.T) {
		attempts := comparisonPairs(2, 20, 30)
		attempts[1].sequence = nil
		authorization := statementAuthorization(t, attempts)
		outcome := authorizationOutcome(t, authorization, "comparative_descriptive")
		assertAuthorized(t, outcome, false, "protocol_noncompliant")
		diagnostics := outcome["evidence"].(map[string]any)["observed_blocking_reasons"].([]string)
		if !containsString(diagnostics, "temporal_coverage_unavailable") {
			t.Fatalf("temporal detail not retained: %#v", outcome)
		}
	})
}

func TestCacheStatementAuthorizationRetainsIncompleteCycleIdentityFromDirectRows(t *testing.T) {
	attempt := comparisonPairs(1, 20, 30)[0]
	authorization := statementAuthorization(t, []Attempt{attempt})
	identity := authorization["identity_provenance"].(map[string]any)
	if identity["configured_context_classification"] != "present" || identity["source_identity_classification"] != "present" || identity["attempt_count"] != 1 {
		t.Fatalf("incomplete-cycle identity lost: %#v", identity)
	}
	comparisonScope := identity["comparison_scope"].(map[string]any)
	if comparisonScope["source_identity_classification"] != "absent" || comparisonScope["attempt_count"] != 0 {
		t.Fatalf("comparison identity scope is not separate: %#v", comparisonScope)
	}
	assertAuthorized(t, authorizationOutcome(t, authorization, "comparative_descriptive"), false, "cycle_pair_incomplete")
}

func TestCacheStatementAuthorizationRejectsNonAuthoritativePairSets(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any, map[string]any)
	}{
		{name: "duplicate pair rows", mutate: func(_ map[string]any, comparison map[string]any) {
			pair := comparison["pairs"].([]any)[0]
			comparison["pairs"] = []any{pair, pair, pair, pair, pair}
		}},
		{name: "duplicate cycles with different ids", mutate: func(direct, comparison map[string]any) {
			pair := comparison["pairs"].([]any)[1].(map[string]any)
			pair["cycle"] = 1
			for _, side := range []string{Baseline, Candidate} {
				row := pair[side].(map[string]any)
				row["cycle"] = 1
				findDirectAttempt(t, direct, row["attempt_id"].(string))["cycle"] = 1
			}
		}},
		{name: "reused attempt id across cycles", mutate: func(_ map[string]any, comparison map[string]any) {
			pairs := comparison["pairs"].([]any)
			first := pairs[0].(map[string]any)[Baseline].(map[string]any)
			pairs[1].(map[string]any)[Baseline].(map[string]any)["attempt_id"] = first["attempt_id"]
		}},
		{name: "same baseline and candidate id", mutate: func(_ map[string]any, comparison map[string]any) {
			pair := comparison["pairs"].([]any)[0].(map[string]any)
			pair[Candidate].(map[string]any)["attempt_id"] = pair[Baseline].(map[string]any)["attempt_id"]
		}},
		{name: "duplicate direct attempt ids", mutate: func(direct, _ map[string]any) {
			rows := direct["attempts"].([]any)
			direct["attempts"] = append(rows, rows[0])
		}},
		{name: "wrong nested side", mutate: func(_ map[string]any, comparison map[string]any) {
			comparison["pairs"].([]any)[0].(map[string]any)[Candidate].(map[string]any)["side"] = Baseline
		}},
		{name: "wrong direct side", mutate: func(direct, comparison map[string]any) {
			id := comparison["pairs"].([]any)[0].(map[string]any)[Candidate].(map[string]any)["attempt_id"].(string)
			findDirectAttempt(t, direct, id)["side"] = Baseline
		}},
		{name: "wrong nested phase", mutate: func(_ map[string]any, comparison map[string]any) {
			comparison["pairs"].([]any)[0].(map[string]any)[Candidate].(map[string]any)["phase"] = Warmup
		}},
		{name: "wrong direct phase", mutate: func(direct, comparison map[string]any) {
			id := comparison["pairs"].([]any)[0].(map[string]any)[Candidate].(map[string]any)["attempt_id"].(string)
			findDirectAttempt(t, direct, id)["phase"] = Warmup
		}},
		{name: "wrong nested cycle", mutate: func(_ map[string]any, comparison map[string]any) {
			comparison["pairs"].([]any)[0].(map[string]any)[Candidate].(map[string]any)["cycle"] = 2
		}},
		{name: "wrong direct cycle", mutate: func(direct, comparison map[string]any) {
			id := comparison["pairs"].([]any)[0].(map[string]any)[Candidate].(map[string]any)["attempt_id"].(string)
			findDirectAttempt(t, direct, id)["cycle"] = 2
		}},
		{name: "nonpositive outer cycle", mutate: func(_ map[string]any, comparison map[string]any) {
			comparison["pairs"].([]any)[0].(map[string]any)["cycle"] = 0
		}},
		{name: "missing direct row", mutate: func(direct, comparison map[string]any) {
			id := comparison["pairs"].([]any)[0].(map[string]any)[Candidate].(map[string]any)["attempt_id"].(string)
			rows := direct["attempts"].([]any)
			kept := make([]any, 0, len(rows)-1)
			for _, raw := range rows {
				if raw.(map[string]any)["attempt_id"] != id {
					kept = append(kept, raw)
				}
			}
			direct["attempts"] = kept
		}},
		{name: "pair share differs from direct authority", mutate: func(_ map[string]any, comparison map[string]any) {
			pair := comparison["pairs"].([]any)[0].(map[string]any)
			pair[Candidate].(map[string]any)["direct_share"] = map[string]any{"state": "measured", "value": .4, "ratio": "2/5"}
			rederiveComparisonDifferenceProjection(t, comparison)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			direct, secondary, comparison := authorizationEvidence(t, comparisonPairs(5, 20, 30))
			tc.mutate(direct, comparison)
			authorization := buildCacheStatementAuthorization(direct, secondary, comparison)
			for _, name := range []string{"comparative_descriptive", "candidate_higher_success", "candidate_lower", "inconclusive"} {
				outcome := authorizationOutcome(t, authorization, name)
				if outcome["authorized"] != false || outcome["statement"] != nil || !containsString(outcome["reasons"].([]string), "evidence_projection_inconsistent") {
					t.Fatalf("%s accepted a non-authoritative pair set: %#v", name, outcome)
				}
			}
		})
	}
}

func findDirectAttempt(t *testing.T, direct map[string]any, attemptID string) map[string]any {
	t.Helper()
	for _, raw := range direct["attempts"].([]any) {
		row := raw.(map[string]any)
		if row["attempt_id"] == attemptID {
			return row
		}
	}
	t.Fatalf("direct attempt %q not found", attemptID)
	return nil
}

func rederiveComparisonDifferenceProjection(t *testing.T, comparison map[string]any) {
	t.Helper()
	differences := []*big.Rat{}
	for _, raw := range comparison["pairs"].([]any) {
		pair := raw.(map[string]any)
		baseline, ok := new(big.Rat).SetString(pair[Baseline].(map[string]any)["direct_share"].(map[string]any)["ratio"].(string))
		if !ok {
			t.Fatal("baseline test ratio is malformed")
		}
		candidate, ok := new(big.Rat).SetString(pair[Candidate].(map[string]any)["direct_share"].(map[string]any)["ratio"].(string))
		if !ok {
			t.Fatal("candidate test ratio is malformed")
		}
		difference := new(big.Rat).Sub(candidate, baseline)
		pair["candidate_minus_baseline"] = rationalView(difference)
		differences = append(differences, difference)
	}
	comparison["direct"].(map[string]any)["paired_difference"] = pairedDifferenceStatistics(differences)
}
