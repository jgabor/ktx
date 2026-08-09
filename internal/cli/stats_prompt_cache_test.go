package cli

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/cache/reuse"
	dbpkg "github.com/jgabor/ktx/internal/db"
)

func TestStatsSummaryPromptCacheTokenHitRate(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	provider, model := "openai", "gpt-5"
	row := func(sessionID string, capturedAt time.Time, provider, model *string, input, read, write dbpkg.IntegerMeasurement) dbpkg.StatsSummaryCacheRow {
		return dbpkg.StatsSummaryCacheRow{
			SessionID: sessionID, CapturedAt: capturedAt,
			Runtime: reuse.RequiredRuntime, RuntimeVersion: reuse.RequiredRuntimeVersion,
			Provider: provider, Model: model,
			UncachedInput: input, CacheRead: read, CacheWrite: write,
		}
	}

	t.Run("publishes a token-weighted rate from one identified population", func(t *testing.T) {
		rate := aggregateStatsSummaryPromptCacheTokenHitRate([]dbpkg.StatsSummaryCacheRow{
			row("one", now.Add(-time.Minute), &provider, &model, measuredStatsSummaryToken(20), measuredStatsSummaryToken(30), measuredStatsSummaryToken(10)),
			row("two", now, &provider, &model, measuredStatsSummaryToken(10), measuredStatsSummaryToken(30), measuredStatsSummaryToken(0)),
		}, 0)

		if rate.State != statsSummaryMeasured || rate.Value == nil || math.Abs(*rate.Value-0.6) > 1e-15 || rate.Ratio == nil || *rate.Ratio != "3/5" {
			t.Fatalf("rate = %+v, want measured 3/5", rate)
		}
		if rate.Kind != "token_weighted" || rate.Unit != "token_fraction" || rate.Formula != "cache_read / (uncached_input + cache_read + cache_write)" || rate.Source != "runtime_emitted_model_step_telemetry" || rate.Runtime != reuse.RequiredRuntime || rate.RuntimeVersion != reuse.RequiredRuntimeVersion || rate.Provider == nil || *rate.Provider != provider || rate.Model == nil || *rate.Model != model {
			t.Errorf("rate provenance = %+v", rate)
		}
		for name, want := range map[string]string{"uncached_input": "30", "cache_read": "60", "cache_write": "10"} {
			var got statsSummaryIntegerMeasurement
			switch name {
			case "uncached_input":
				got = rate.RawBuckets.UncachedInput
			case "cache_read":
				got = rate.RawBuckets.CacheRead
			default:
				got = rate.RawBuckets.CacheWrite
			}
			if got.State != statsSummaryMeasured || got.Value == nil || got.Value.String() != want {
				t.Errorf("%s = %+v, want measured %s", name, got, want)
			}
		}
		if rate.LogicalInput.State != statsSummaryMeasured || rate.LogicalInput.Value == nil || rate.LogicalInput.Value.String() != "100" {
			t.Errorf("logical_input = %+v, want measured 100", rate.LogicalInput)
		}
		if rate.TemporalScope.Scope != statsSummaryObservedSubsetScope || rate.TemporalScope.RequestedWindowCoverage != (statsSummaryRequestedWindowCoverage{State: statsSummaryRequestedWindowCoverageUnavailable, Reason: statsSummaryCollectorLifecycleEvidenceUnavailable, Scope: statsSummaryObservedSubsetScope}) || rate.TemporalScope.ContributingSessions != 2 || rate.TemporalScope.ContributingModelSteps != 2 {
			t.Errorf("temporal scope = %+v", rate.TemporalScope)
		}
	})

	for _, test := range []struct {
		name               string
		rows               []dbpkg.StatsSummaryCacheRow
		unsupported        int
		wantReason         string
		wantRawBucketState string
		wantMeasuredZero   bool
	}{
		{
			name: "zero numerator remains measured",
			rows: []dbpkg.StatsSummaryCacheRow{
				row("one", now, &provider, &model, measuredStatsSummaryToken(10), measuredStatsSummaryToken(0), measuredStatsSummaryToken(0)),
			},
			wantMeasuredZero: true,
		},
		{
			name: "zero denominator is unavailable",
			rows: []dbpkg.StatsSummaryCacheRow{
				row("one", now, &provider, &model, measuredStatsSummaryToken(0), measuredStatsSummaryToken(0), measuredStatsSummaryToken(0)),
			},
			wantReason:         statsSummaryLogicalInputNotPositive,
			wantRawBucketState: statsSummaryMeasured,
		},
		{
			name: "missing required bucket is unavailable",
			rows: []dbpkg.StatsSummaryCacheRow{
				row("one", now, &provider, &model, dbpkg.IntegerMeasurement{State: dbpkg.MeasurementUnknown}, measuredStatsSummaryToken(4), measuredStatsSummaryToken(0)),
			},
			wantReason:         statsSummaryRequiredTokenBucketMissing,
			wantRawBucketState: statsSummaryMissing,
		},
		{
			name: "malformed required bucket is unavailable",
			rows: []dbpkg.StatsSummaryCacheRow{
				row("one", now, &provider, &model, measuredStatsSummaryToken(4), dbpkg.IntegerMeasurement{State: dbpkg.MeasurementMalformed}, measuredStatsSummaryToken(0)),
			},
			wantReason:         statsSummaryRequiredTokenBucketMalformed,
			wantRawBucketState: statsSummaryMalformed,
		},
		{
			name: "negative stored bucket is malformed",
			rows: []dbpkg.StatsSummaryCacheRow{
				row("one", now, &provider, &model, measuredStatsSummaryToken(4), measuredStatsSummaryToken(-1), measuredStatsSummaryToken(0)),
			},
			wantReason:         statsSummaryRequiredTokenBucketMalformed,
			wantRawBucketState: statsSummaryMalformed,
		},
		{
			name: "missing runtime emitted identity is unavailable",
			rows: []dbpkg.StatsSummaryCacheRow{
				row("one", now, nil, &model, measuredStatsSummaryToken(4), measuredStatsSummaryToken(1), measuredStatsSummaryToken(0)),
			},
			wantReason:         statsSummaryRuntimeIdentityMissing,
			wantRawBucketState: statsSummaryUnavailable,
		},
		{
			name: "mixed runtime emitted identity is unavailable",
			rows: []dbpkg.StatsSummaryCacheRow{
				row("one", now.Add(-time.Minute), &provider, &model, measuredStatsSummaryToken(4), measuredStatsSummaryToken(1), measuredStatsSummaryToken(0)),
				row("two", now, statsSummaryString("anthropic"), statsSummaryString("claude"), measuredStatsSummaryToken(4), measuredStatsSummaryToken(1), measuredStatsSummaryToken(0)),
			},
			wantReason:         statsSummaryRuntimeIdentityMixed,
			wantRawBucketState: statsSummaryUnavailable,
		},
		{
			name:               "no contributing model step is unavailable",
			wantReason:         statsSummaryNoContributingModelSteps,
			wantRawBucketState: statsSummaryUnavailable,
		},
		{
			name:               "unsupported runtime or version is unavailable",
			unsupported:        1,
			wantReason:         statsSummaryUnsupportedRuntimeOrVersion,
			wantRawBucketState: statsSummaryUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			rate := aggregateStatsSummaryPromptCacheTokenHitRate(test.rows, test.unsupported)
			if test.wantMeasuredZero {
				if rate.State != statsSummaryMeasured || rate.Value == nil || *rate.Value != 0 || rate.Ratio == nil || *rate.Ratio != "0" || rate.Reason != nil || rate.RawBuckets.CacheRead.State != statsSummaryMeasured || rate.RawBuckets.CacheRead.Value == nil || rate.RawBuckets.CacheRead.Value.Sign() != 0 {
					t.Fatalf("rate = %+v, want measured zero", rate)
				}
				return
			}
			if rate.State != statsSummaryUnavailable || rate.Value != nil || rate.Ratio != nil || rate.Reason == nil || *rate.Reason != test.wantReason {
				t.Fatalf("rate = %+v, want unavailable %q", rate, test.wantReason)
			}
			if rate.RawBuckets.UncachedInput.State != test.wantRawBucketState && rate.RawBuckets.CacheRead.State != test.wantRawBucketState && rate.RawBuckets.CacheWrite.State != test.wantRawBucketState {
				t.Errorf("raw buckets = %+v, want a %q bucket", rate.RawBuckets, test.wantRawBucketState)
			}
		})
	}
}

func TestStatsSummaryPromptCacheTokenHitRateDetectsUnsupportedRuntime(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	db := openMemoryDB(t)
	sessionID := seedSession(t, db)
	provider, model := "openai", "gpt-5"
	seedStatsTelemetryWithContractAndIdentity(t, db, sessionID, 1, now.Add(-time.Minute).Format(time.RFC3339Nano), "other-runtime", "9.9.9", &provider, &model, measuredStatsToken(10), measuredStatsToken(5), measuredStatsToken(0))

	var output bytes.Buffer
	if _, err := statsSummaryLogic(nil, FormatJSON, 30*time.Minute, now, db, &output); err != nil {
		t.Fatal(err)
	}
	var summary statsSummary
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	rate := summary.PromptCacheTokenHitRate
	if rate.State != statsSummaryUnavailable || rate.Reason == nil || *rate.Reason != statsSummaryUnsupportedRuntimeOrVersion || rate.TemporalScope.ContributingModelSteps != 0 {
		t.Fatalf("rate = %+v", rate)
	}
}

func measuredStatsSummaryToken(value int64) dbpkg.IntegerMeasurement {
	return dbpkg.IntegerMeasurement{State: dbpkg.MeasurementMeasured, Value: &value}
}
