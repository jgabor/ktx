package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jgabor/ktx/internal/cache/reuse"
	dbpkg "github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/telemetry"
)

// TestStatsLogic exercises the aggregation, filtering,
// sorting, and rendering of `ktx stats`.
func TestStatsLogic(t *testing.T) {
	t.Run("groups by tool and rank by delivered tokens descending", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		seedCalls(t, db, sid, []callSeed{
			{tool: "bash", command: "ls -la", output: strings.Repeat("x", 400) + "\n"},
			{tool: "bash", command: "ls -la", output: strings.Repeat("y", 400) + "\n"},
			{tool: "bash", command: "cat big.txt", output: strings.Repeat("z", 800) + "\n"},
			{tool: "bash", command: "cat big.txt", output: strings.Repeat("w", 800) + "\n"},
		})

		var buf bytes.Buffer
		_, err := statsLogic(nil, FormatJSON, statsDefaultLimit, db, &buf)
		if err != nil {
			t.Fatalf("statsLogic: %v", err)
		}

		var groups []statsGroup
		if err := json.Unmarshal(buf.Bytes(), &groups); err != nil {
			t.Fatalf("json unmarshal: %v\noutput: %s", err, buf.String())
		}
		if len(groups) != 2 {
			t.Fatalf("got %d groups, want 2", len(groups))
		}

		// cat big.txt has 800-byte outputs, ls -la has 400-byte.
		if groups[0].Command != "cat" {
			t.Errorf("first group command=%q, want %q (higher delivered tokens)", groups[0].Command, "cat")
		}
		if groups[0].TotalDeliveredTokens <= groups[1].TotalDeliveredTokens {
			t.Errorf("groups not sorted desc: first=%d, second=%d",
				groups[0].TotalDeliveredTokens, groups[1].TotalDeliveredTokens)
		}
		if groups[0].Tool != "bash" {
			t.Errorf("first group tool=%q, want %q", groups[0].Tool, "bash")
		}
		if groups[0].Calls != 2 {
			t.Errorf("first group calls=%d, want 2", groups[0].Calls)
		}
	})

	t.Run("uncompressed calls have delivered equal to raw", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		seedCalls(t, db, sid, []callSeed{
			{tool: "bash", command: "ls", output: strings.Repeat("a", 400) + "\n"},
			{tool: "bash", command: "ls", output: strings.Repeat("b", 400) + "\n"},
		})

		var buf bytes.Buffer
		_, err := statsLogic(nil, FormatJSON, statsDefaultLimit, db, &buf)
		if err != nil {
			t.Fatalf("statsLogic: %v", err)
		}

		var groups []statsGroup
		if err := json.Unmarshal(buf.Bytes(), &groups); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		if len(groups) != 1 {
			t.Fatalf("got %d groups, want 1", len(groups))
		}
		if groups[0].TotalRawTokens != groups[0].TotalDeliveredTokens {
			t.Errorf("raw=%d != delivered=%d (should be equal for uncompressed)",
				groups[0].TotalRawTokens, groups[0].TotalDeliveredTokens)
		}
	})

	t.Run("compressed calls have delivered less than raw", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		ids := seedCalls(t, db, sid, []callSeed{
			{tool: "bash", command: "cat big", output: strings.Repeat("x", 800) + "\n"},
			{tool: "bash", command: "cat big", output: strings.Repeat("y", 800) + "\n"},
		})
		seedCompressedRow(t, db, ids[0], strings.Repeat("s", 200)+"\n", "2026-07-12T00:00:01Z")
		seedCompressedRow(t, db, ids[1], strings.Repeat("s", 200)+"\n", "2026-07-12T00:00:01Z")

		var buf bytes.Buffer
		_, err := statsLogic(nil, FormatJSON, statsDefaultLimit, db, &buf)
		if err != nil {
			t.Fatalf("statsLogic: %v", err)
		}

		var groups []statsGroup
		if err := json.Unmarshal(buf.Bytes(), &groups); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		if len(groups) != 1 {
			t.Fatalf("got %d groups, want 1", len(groups))
		}
		if groups[0].TotalDeliveredTokens >= groups[0].TotalRawTokens {
			t.Errorf("delivered=%d should be < raw=%d (compressed)",
				groups[0].TotalDeliveredTokens, groups[0].TotalRawTokens)
		}
	})

	t.Run("groups with fewer than 2 calls excluded", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		seedCalls(t, db, sid, []callSeed{
			{tool: "bash", command: "ls", output: strings.Repeat("a", 400) + "\n"},
			{tool: "read", command: "cat", output: strings.Repeat("b", 400) + "\n"},
			{tool: "read", command: "cat", output: strings.Repeat("c", 400) + "\n"},
		})

		var buf bytes.Buffer
		_, err := statsLogic(nil, FormatJSON, statsDefaultLimit, db, &buf)
		if err != nil {
			t.Fatalf("statsLogic: %v", err)
		}

		var groups []statsGroup
		if err := json.Unmarshal(buf.Bytes(), &groups); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		for _, g := range groups {
			if g.Calls < 2 {
				t.Errorf("group with calls=%d should have been excluded", g.Calls)
			}
		}
		if len(groups) != 1 {
			t.Fatalf("got %d groups, want 1 (only the 2-call group)", len(groups))
		}
	})

	t.Run("groups with fewer than 50 delivered tokens excluded", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		seedCalls(t, db, sid, []callSeed{
			{tool: "bash", command: "ls", output: "short\n"}, // 6 bytes → ~1 token
			{tool: "bash", command: "ls", output: "short\n"}, // 6 bytes → ~1 token
			{tool: "bash", command: "cat", output: strings.Repeat("z", 400) + "\n"},
			{tool: "bash", command: "cat", output: strings.Repeat("z", 400) + "\n"},
		})

		var buf bytes.Buffer
		_, err := statsLogic(nil, FormatJSON, statsDefaultLimit, db, &buf)
		if err != nil {
			t.Fatalf("statsLogic: %v", err)
		}

		var groups []statsGroup
		if err := json.Unmarshal(buf.Bytes(), &groups); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		for _, g := range groups {
			if g.TotalDeliveredTokens < 50 {
				t.Errorf("group with delivered=%d should have been excluded", g.TotalDeliveredTokens)
			}
		}
		if len(groups) != 1 {
			t.Fatalf("got %d groups, want 1 (only the 50+ token group)", len(groups))
		}
	})

	t.Run("--limit caps the number of groups", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		seedCalls(t, db, sid, []callSeed{
			{tool: "bash", command: "ls", output: strings.Repeat("a", 400) + "\n"},
			{tool: "bash", command: "ls", output: strings.Repeat("b", 400) + "\n"},
			{tool: "read", command: "cat", output: strings.Repeat("c", 400) + "\n"},
			{tool: "read", command: "cat", output: strings.Repeat("d", 400) + "\n"},
		})

		var buf bytes.Buffer
		_, err := statsLogic(nil, FormatJSON, 1, db, &buf)
		if err != nil {
			t.Fatalf("statsLogic: %v", err)
		}

		var groups []statsGroup
		if err := json.Unmarshal(buf.Bytes(), &groups); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		if len(groups) != 1 {
			t.Fatalf("got %d groups with --limit 1, want 1", len(groups))
		}
	})

	t.Run("text output is a table", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		seedCalls(t, db, sid, []callSeed{
			{tool: "bash", command: "ls", output: strings.Repeat("a", 400) + "\n"},
			{tool: "bash", command: "ls", output: strings.Repeat("b", 400) + "\n"},
		})

		var buf bytes.Buffer
		_, err := statsLogic(nil, FormatText, statsDefaultLimit, db, &buf)
		if err != nil {
			t.Fatalf("statsLogic: %v", err)
		}

		out := buf.String()
		if !strings.Contains(out, "TOOL") {
			t.Errorf("text output missing header: %q", out)
		}
		if !strings.Contains(out, "bash") {
			t.Errorf("text output missing tool name: %q", out)
		}
		if !strings.Contains(out, "ls") {
			t.Errorf("text output missing command: %q", out)
		}
	})

	t.Run("empty database returns empty output", func(t *testing.T) {
		db := openMemoryDB(t)

		var buf bytes.Buffer
		_, err := statsLogic(nil, FormatJSON, statsDefaultLimit, db, &buf)
		if err != nil {
			t.Fatalf("statsLogic: %v", err)
		}

		var groups []statsGroup
		if err := json.Unmarshal(buf.Bytes(), &groups); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		if len(groups) != 0 {
			t.Fatalf("got %d groups, want 0", len(groups))
		}

		var textBuf bytes.Buffer
		_, err = statsLogic(nil, FormatText, statsDefaultLimit, db, &textBuf)
		if err != nil {
			t.Fatalf("statsLogic text: %v", err)
		}
		if !strings.Contains(textBuf.String(), "no stats") {
			t.Errorf("text output should say 'no stats': %q", textBuf.String())
		}
	})

	t.Run("invalid --limit returns usage error", func(t *testing.T) {
		db := openMemoryDB(t)
		var buf bytes.Buffer
		_, err := statsLogic(nil, FormatJSON, 0, db, &buf)
		if err == nil {
			t.Error("expected usage error for --limit 0")
		}
	})

	t.Run("--help renders help text", func(t *testing.T) {
		db := openMemoryDB(t)
		var buf bytes.Buffer
		_, err := statsLogic([]string{"--help"}, FormatJSON, statsDefaultLimit, db, &buf)
		if err != nil {
			t.Fatalf("statsLogic: %v", err)
		}
		if !strings.Contains(buf.String(), "ktx stats") {
			t.Errorf("help output missing command name: %q", buf.String())
		}
	})
}

func TestStatsWindowLogic(t *testing.T) {
	now := time.Date(2026, 7, 12, 0, 30, 0, 0, time.UTC)

	t.Run("empty window has zero rates and unavailable reduction", func(t *testing.T) {
		db := openMemoryDB(t)
		var buf bytes.Buffer
		if _, err := statsSummaryLogic(nil, FormatJSON, 30*time.Minute, now, db, &buf); err != nil {
			t.Fatalf("statsSummaryLogic: %v", err)
		}

		var got statsSummary
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("json unmarshal: %v\noutput: %s", err, buf.String())
		}
		if got.SchemaVersion != 6 || got.GeneratedAt != now.Format(time.RFC3339Nano) {
			t.Errorf("metadata = %+v", got)
		}
		if got.Window.Seconds != 1800 || got.Window.From != now.Add(-30*time.Minute).Format(time.RFC3339Nano) || got.Window.To != got.GeneratedAt {
			t.Errorf("window = %+v", got.Window)
		}
		if got.Calls.TotalWithOutput != 0 || got.Calls.WithCompressedArtifact != 0 {
			t.Errorf("calls = %+v", got.Calls)
		}
		if got.Outcomes != (statsOutcomeTotals{}) {
			t.Errorf("outcomes = %+v", got.Outcomes)
		}
		if got.Tokens.RawEstimatedPerSecond != 0 || got.Tokens.DeliveredEstimatedPerSecond != 0 || got.Tokens.ReductionPercent != nil {
			t.Errorf("tokens = %+v", got.Tokens)
		}
		cache := got.CacheReuse
		if cache.EvidenceScope != "provider_prompt_prefix_cache_tokens" || cache.RequestedWindowCoverage != (statsSummaryRequestedWindowCoverage{
			State: "unavailable", Reason: "collector_lifecycle_evidence_unavailable", Scope: "observed_subset",
		}) {
			t.Errorf("cache coverage = %+v", cache)
		}
		if cache.ContributingSessions != 0 || cache.ContributingModelSteps != 0 || cache.FirstContributingCapture != nil || cache.LastContributingCapture != nil || cache.ObservedSpanSeconds != nil || cache.UncachedInput.State != "missing" || cache.CachedInputShare.State != "missing" {
			t.Errorf("cache_reuse = %+v", got.CacheReuse)
		}
	})

	t.Run("outcomes reconcile between summary and breakdown", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		success, failure := false, true
		seedCalls(t, db, sid, []callSeed{
			{tool: "read", output: "success", isError: &success},
			{tool: "read", output: "failure", isError: &failure},
			{tool: "read", output: "unknown"},
			{tool: "read", output: "also unknown"},
		})

		var summaryJSON, breakdownJSON bytes.Buffer
		if _, err := statsSummaryLogic(nil, FormatJSON, 30*time.Minute, now, db, &summaryJSON); err != nil {
			t.Fatal(err)
		}
		if _, err := statsBreakdownLogic(nil, FormatJSON, 30*time.Minute, now, db, &breakdownJSON); err != nil {
			t.Fatal(err)
		}
		var summary statsSummary
		var breakdown statsBreakdown
		if err := json.Unmarshal(summaryJSON.Bytes(), &summary); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(breakdownJSON.Bytes(), &breakdown); err != nil {
			t.Fatal(err)
		}
		want := statsOutcomeTotals{SuccessfulCalls: 1, ErrorCalls: 1, UnknownCalls: 2, KnownCalls: 2, TotalCalls: 4}
		errorRate, coverage := 50.0, 50.0
		want.ErrorRatePctOfKnown = &errorRate
		want.OutcomeCoveragePct = &coverage
		if !reflect.DeepEqual(summary.Outcomes, want) || !reflect.DeepEqual(breakdown.Outcomes, want) {
			t.Fatalf("summary outcomes=%+v breakdown outcomes=%+v want=%+v", summary.Outcomes, breakdown.Outcomes, want)
		}
		if summary.Outcomes.KnownCalls != summary.Outcomes.SuccessfulCalls+summary.Outcomes.ErrorCalls || summary.Outcomes.TotalCalls != summary.Outcomes.KnownCalls+summary.Outcomes.UnknownCalls || summary.Outcomes.TotalCalls != summary.Calls.TotalWithOutput {
			t.Errorf("outcomes do not reconcile: outcomes=%+v calls=%+v", summary.Outcomes, summary.Calls)
		}
	})

	t.Run("malformed SQLite outcome storage remains available and unknown", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ktx.db")
		db, err := dbpkg.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		sid := seedSession(t, db)
		success, failure := false, true
		ids := seedCalls(t, db, sid, []callSeed{
			{tool: "bash", output: "success", isError: &success},
			{tool: "bash", output: "failure", isError: &failure},
			{tool: "bash", output: "integer"},
			{tool: "bash", output: "real"},
			{tool: "bash", output: "text"},
			{tool: "bash", output: "blob"},
			{tool: "bash", output: "null"},
			{tool: "bash", output: "exit-only"},
		})
		raw, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { raw.Close() })
		updates := []struct {
			id    string
			value any
		}{
			{id: ids[2], value: int64(7)},
			{id: ids[3], value: float64(0.5)},
			{id: ids[4], value: "malformed"},
			{id: ids[5], value: []byte{0}},
			{id: ids[6], value: nil},
		}
		for _, update := range updates {
			if _, err := raw.Exec(`UPDATE tool_calls SET is_error = ? WHERE id = ?`, update.value, update.id); err != nil {
				t.Fatalf("update %s: %v", update.id, err)
			}
		}
		if _, err := raw.Exec(`UPDATE tool_calls SET exit_code = 9 WHERE id = ?`, ids[7]); err != nil {
			t.Fatal(err)
		}

		var summaryJSON, breakdownJSON, summaryText, breakdownText bytes.Buffer
		if _, err := statsSummaryLogic(nil, FormatJSON, 30*time.Minute, now, db, &summaryJSON); err != nil {
			t.Fatal(err)
		}
		if _, err := statsBreakdownLogic(nil, FormatJSON, 30*time.Minute, now, db, &breakdownJSON); err != nil {
			t.Fatal(err)
		}
		if _, err := statsSummaryLogic(nil, FormatText, 30*time.Minute, now, db, &summaryText); err != nil {
			t.Fatal(err)
		}
		if _, err := statsBreakdownLogic(nil, FormatText, 30*time.Minute, now, db, &breakdownText); err != nil {
			t.Fatal(err)
		}
		var summary statsSummary
		var breakdown statsBreakdown
		if err := json.Unmarshal(summaryJSON.Bytes(), &summary); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(breakdownJSON.Bytes(), &breakdown); err != nil {
			t.Fatal(err)
		}
		errorRate, coverage := 50.0, 25.0
		want := statsOutcomeTotals{SuccessfulCalls: 1, ErrorCalls: 1, UnknownCalls: 6, KnownCalls: 2, TotalCalls: 8, ErrorRatePctOfKnown: &errorRate, OutcomeCoveragePct: &coverage}
		if !reflect.DeepEqual(summary.Outcomes, want) || !reflect.DeepEqual(breakdown.Outcomes, want) {
			t.Fatalf("summary outcomes=%+v breakdown outcomes=%+v want=%+v", summary.Outcomes, breakdown.Outcomes, want)
		}
		if summary.Calls != breakdown.Calls || !reflect.DeepEqual(summary.Tokens, breakdown.Tokens) || summary.Calls.TotalWithOutput != 8 {
			t.Errorf("summary calls/tokens=%+v/%+v breakdown=%+v/%+v", summary.Calls, summary.Tokens, breakdown.Calls, breakdown.Tokens)
		}
		assertOutcomeTextColumns(t, "summary", summaryText.String(), "50.00", "25.00")
		assertOutcomeTextColumns(t, "breakdown", breakdownText.String(), "50.00", "25.00")
		encoded, err := json.Marshal(breakdown.DeliveryBreakdown)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "outcome") || strings.Contains(string(encoded), "error_calls") {
			t.Errorf("delivery groups contain outcomes: %s", encoded)
		}
	})

	t.Run("outcome edge states remain distinct in JSON and text", func(t *testing.T) {
		tests := []struct {
			name             string
			rows             []dbpkg.StatsSummaryRow
			wantErrorRate    *float64
			wantCoverage     *float64
			wantErrorText    string
			wantCoverageText string
		}{
			{name: "empty", wantErrorText: "-", wantCoverageText: "-"},
			{name: "all unknown", rows: []dbpkg.StatsSummaryRow{{RawChars: 1, DeliveredChars: 1}}, wantCoverage: float64Pointer(0), wantErrorText: "-", wantCoverageText: "0.00"},
			{name: "all known zero error", rows: []dbpkg.StatsSummaryRow{{RawChars: 1, DeliveredChars: 1, IsError: boolPointer(false)}}, wantErrorRate: float64Pointer(0), wantCoverage: float64Pointer(100), wantErrorText: "0.00", wantCoverageText: "100.00"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				window, calls, outcomes, tokens := aggregateStatsWindow(tt.rows, 30*time.Minute, now)
				if !reflect.DeepEqual(outcomes.ErrorRatePctOfKnown, tt.wantErrorRate) || !reflect.DeepEqual(outcomes.OutcomeCoveragePct, tt.wantCoverage) {
					t.Fatalf("outcomes = %+v", outcomes)
				}
				summary := statsSummary{Window: window, Calls: calls, Outcomes: outcomes, Tokens: tokens}
				breakdown := statsBreakdown{Window: window, Calls: calls, Outcomes: outcomes, Tokens: tokens, DeliveryBreakdown: aggregateStatsBreakdown(tt.rows)}
				for name, value := range map[string]any{"summary": summary, "breakdown": breakdown} {
					encoded, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					var rendered map[string]json.RawMessage
					if err := json.Unmarshal(encoded, &rendered); err != nil {
						t.Fatal(err)
					}
					var renderedOutcomes statsOutcomeTotals
					if err := json.Unmarshal(rendered["outcomes"], &renderedOutcomes); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(renderedOutcomes, outcomes) {
						t.Errorf("%s JSON outcomes = %+v, want %+v", name, renderedOutcomes, outcomes)
					}
				}
				for name, render := range map[string]func(*bytes.Buffer){
					"summary":   func(buf *bytes.Buffer) { printStatsSummaryText(buf, summary) },
					"breakdown": func(buf *bytes.Buffer) { printStatsBreakdownText(buf, breakdown) },
				} {
					var buf bytes.Buffer
					render(&buf)
					assertOutcomeTextColumns(t, name, buf.String(), tt.wantErrorText, tt.wantCoverageText)
				}
			})
		}
	})

	t.Run("uses per-call character rounding and retained artifact coverage", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		ids := seedCalls(t, db, sid, []callSeed{
			{tool: "read", output: "界界界界界"}, // 5 characters => 2 tokens, not 5 UTF-8 tokens.
			{tool: "bash", output: "12345"}, // 5 characters => 2 tokens.
		})
		seedCompressedRow(t, db, ids[1], "x", now.Add(-time.Minute).Format(time.RFC3339Nano))

		var buf bytes.Buffer
		if _, err := statsSummaryLogic(nil, FormatJSON, 30*time.Minute, now, db, &buf); err != nil {
			t.Fatalf("statsSummaryLogic: %v", err)
		}
		var got statsSummary
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		if got.Calls.TotalWithOutput != 2 || got.Calls.WithCompressedArtifact != 1 {
			t.Errorf("calls = %+v, want total 2 compressed 1", got.Calls)
		}
		if got.Tokens.RawEstimated != 4 || got.Tokens.DeliveredEstimated != 3 {
			t.Errorf("token totals = %+v, want raw 4 delivered 3", got.Tokens)
		}
		if math.Abs(got.Tokens.RawEstimatedPerSecond-4.0/1800) > 1e-12 || math.Abs(got.Tokens.DeliveredEstimatedPerSecond-3.0/1800) > 1e-12 {
			t.Errorf("rates = %+v", got.Tokens)
		}
		if got.Tokens.ReductionPercent == nil || *got.Tokens.ReductionPercent != 25 {
			t.Errorf("reduction_percent = %v, want 25", got.Tokens.ReductionPercent)
		}
	})

	t.Run("groups delivery evidence independently from first-artifact totals", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		ids := seedCalls(t, db, sid, []callSeed{
			{tool: "read", output: "12345"},
			{tool: "bash", command: "git --no-pager status", output: strings.Repeat("x", 1025)},
			{tool: "grep", command: "rg needle", output: "1234"},
			{tool: "bash", command: "git diff", output: "123456789"},
		})
		seedCompressedRow(t, db, ids[3], "x", now.Add(-time.Minute).Format(time.RFC3339Nano))
		for _, disposition := range []dbpkg.CompressionDisposition{
			{SourceID: ids[1], Profile: "default", Route: "default_fallback", Disposition: dbpkg.CompressionDispositionIntentionalPassthrough, ObservedAt: now.Add(-time.Minute).Format(time.RFC3339Nano)},
			{SourceID: ids[2], Profile: "search", Route: "matched", Disposition: dbpkg.CompressionDispositionAntiWorseningRejected, ObservedAt: now.Add(-time.Minute).Format(time.RFC3339Nano)},
			{SourceID: ids[3], Profile: "git-diff", Route: "matched", Disposition: dbpkg.CompressionDispositionArtifactPersisted, ArtifactID: "c" + ids[3][1:], ObservedAt: now.Add(-time.Minute).Format(time.RFC3339Nano)},
		} {
			if err := db.RecordCompressionDisposition(&disposition); err != nil {
				t.Fatal(err)
			}
		}

		var buf bytes.Buffer
		if _, err := statsBreakdownLogic(nil, FormatJSON, 30*time.Minute, now, db, &buf); err != nil {
			t.Fatalf("statsBreakdownLogic: %v", err)
		}
		var got statsBreakdown
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Calls.TotalWithOutput != 4 || got.Calls.WithCompressedArtifact != 1 || got.Tokens.RawEstimated != 263 || got.Tokens.DeliveredEstimated != 261 {
			t.Fatalf("headline totals = calls %+v tokens %+v", got.Calls, got.Tokens)
		}
		if got.DeliveryBreakdown.DistinctGroups != 4 || got.DeliveryBreakdown.OmittedDistinctGroups != 0 || len(got.DeliveryBreakdown.Groups) != 4 {
			t.Fatalf("delivery_breakdown = %+v", got.DeliveryBreakdown)
		}
		groups := map[string]statsBreakdownGroup{}
		for _, group := range got.DeliveryBreakdown.Groups {
			groups[group.Disposition] = group
		}
		unobserved := groups[dbpkg.CompressionDispositionUnobserved]
		if unobserved.Command != nil || unobserved.Subcommand != nil || unobserved.Profile != nil || unobserved.Route != nil || unobserved.RawOutputSizeBand != "1-1k" || unobserved.Calls != 1 {
			t.Errorf("unobserved group = %+v", unobserved)
		}
		passthrough := groups[dbpkg.CompressionDispositionIntentionalPassthrough]
		if passthrough.Command == nil || *passthrough.Command != "git" || passthrough.Subcommand == nil || *passthrough.Subcommand != "status" || passthrough.Profile == nil || *passthrough.Profile != "default" || passthrough.Route == nil || *passthrough.Route != "default_fallback" || passthrough.RawOutputSizeBand != "1k-10k" || passthrough.DeliveredEstimated != 257 {
			t.Errorf("passthrough group = %+v", passthrough)
		}
		artifact := groups[dbpkg.CompressionDispositionArtifactPersisted]
		if artifact.WithCompressedArtifact != 1 || artifact.RawEstimated != 3 || artifact.DeliveredEstimated != 1 {
			t.Errorf("artifact group = %+v", artifact)
		}
		assertDeliveryReconciliation(t, got)
	})

	t.Run("does not expose raw command arguments", func(t *testing.T) {
		rows := []dbpkg.StatsSummaryRow{
			{Tool: "bash", Input: `{"command":"echo super-secret-value"}`, RawChars: 10, DeliveredChars: 10, Disposition: dbpkg.CompressionDispositionUnobserved},
			{Tool: "bash", Input: `{"command":"cat /home/alice/private.txt"}`, RawChars: 10, DeliveredChars: 10, Disposition: dbpkg.CompressionDispositionUnobserved},
			{Tool: "bash", Input: `{"command":"TOKEN=super-secret-value curl example.com"}`, RawChars: 10, DeliveredChars: 10, Disposition: dbpkg.CompressionDispositionUnobserved},
			{Tool: "bash", Input: `{"command":"git -C /home/alice/private-repo status"}`, RawChars: 10, DeliveredChars: 10, Disposition: dbpkg.CompressionDispositionIntentionalPassthrough},
			{Tool: "grep", Input: `{"command":"rg private-needle"}`, RawChars: 10, DeliveredChars: 10, Disposition: dbpkg.CompressionDispositionAntiWorseningRejected},
		}

		got := aggregateStatsBreakdown(rows)
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{"super-secret-value", "/home/alice", "private-needle"} {
			if bytes.Contains(encoded, []byte(private)) {
				t.Errorf("breakdown exposes %q: %s", private, encoded)
			}
		}

		var foundGitStatus bool
		for _, group := range got.Groups {
			if group.Command != nil && *group.Command == "git" && group.Subcommand != nil && *group.Subcommand == "status" {
				foundGitStatus = true
			}
		}
		if !foundGitStatus {
			t.Errorf("breakdown did not retain safe git/status dimensions: %+v", got.Groups)
		}
	})

	t.Run("caps detailed groups and reconciles omitted totals", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		for i := 0; i < 102; i++ {
			output := strings.Repeat("x", i+1)
			if err := db.PersistToolCall(&dbpkg.ToolCall{
				ID: fmt.Sprintf("g%015d", i), SessionID: sid, Tool: fmt.Sprintf("tool-%03d", i),
				Input: `{"command":"cmd"}`, Output: output, Timestamp: "2026-07-12T00:00:01Z",
			}); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 2; i++ {
			sourceID := fmt.Sprintf("g%015d", i)
			seedCompressedRow(t, db, sourceID, "", now.Add(-time.Minute).Format(time.RFC3339Nano))
			if err := db.RecordCompressionDisposition(&dbpkg.CompressionDisposition{
				SourceID: sourceID, Profile: "small", Route: "matched",
				Disposition: dbpkg.CompressionDispositionArtifactPersisted, ArtifactID: "c" + sourceID[1:],
				ObservedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
			}); err != nil {
				t.Fatal(err)
			}
		}

		var buf bytes.Buffer
		if _, err := statsBreakdownLogic(nil, FormatJSON, 30*time.Minute, now, db, &buf); err != nil {
			t.Fatalf("statsBreakdownLogic: %v", err)
		}
		var got statsBreakdown
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.DeliveryBreakdown.DetailedGroupLimit != 100 || got.DeliveryBreakdown.DistinctGroups != 102 || got.DeliveryBreakdown.OmittedDistinctGroups != 2 || len(got.DeliveryBreakdown.Groups) != 100 || got.DeliveryBreakdown.Other.Calls != 2 || got.DeliveryBreakdown.Other.WithCompressedArtifact != 2 || got.DeliveryBreakdown.Other.RawEstimated != 2 || got.DeliveryBreakdown.Other.DeliveredEstimated != 0 {
			t.Fatalf("bounded breakdown = %+v", got.DeliveryBreakdown)
		}
		if first := got.DeliveryBreakdown.Groups[0]; first.Tool != "tool-100" || first.DeliveredEstimated != 26 {
			t.Errorf("first ranked group = %+v, want deterministic tool-100 tie winner with 26 delivered tokens", first)
		}
		assertDeliveryReconciliation(t, got)
	})

	t.Run("aggregates complete runtime cache telemetry with an exact share", func(t *testing.T) {
		db := openMemoryDB(t)
		firstSessionID := seedSession(t, db)
		lastSessionID := "bbbbbbbbbbbbbbbb"
		if err := db.InsertSession(&dbpkg.Session{ID: lastSessionID, Agent: "test-agent"}); err != nil {
			t.Fatal(err)
		}
		firstCapture := now.Add(-30 * time.Minute)
		firstCaptureOffset := firstCapture.In(time.FixedZone("UTC+02", 2*60*60))
		seedStatsTelemetry(t, db, firstSessionID, 1, firstCaptureOffset, measuredStatsToken(20), measuredStatsToken(80), measuredStatsToken(0))
		seedStatsTelemetry(t, db, lastSessionID, 2, now, measuredStatsToken(30), measuredStatsToken(70), measuredStatsToken(0))

		var buf bytes.Buffer
		if _, err := statsSummaryLogic(nil, FormatJSON, 30*time.Minute, now, db, &buf); err != nil {
			t.Fatalf("statsSummaryLogic: %v", err)
		}
		var got statsSummary
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		cache := got.CacheReuse
		if cache.ContractVersion != reuse.RequiredContractVersion || cache.Runtime != reuse.RequiredRuntime || cache.RuntimeVersion != reuse.RequiredRuntimeVersion || cache.EvidenceScope != "provider_prompt_prefix_cache_tokens" || cache.ContributingSessions != 2 || cache.ContributingModelSteps != 2 {
			t.Fatalf("cache metadata = %+v", cache)
		}
		if cache.RequestedWindowCoverage != (statsSummaryRequestedWindowCoverage{
			State: "unavailable", Reason: "collector_lifecycle_evidence_unavailable", Scope: "observed_subset",
		}) {
			t.Errorf("requested_window_coverage = %+v", cache.RequestedWindowCoverage)
		}
		if cache.FirstContributingCapture == nil || *cache.FirstContributingCapture != firstCapture.Format(time.RFC3339Nano) || cache.LastContributingCapture == nil || *cache.LastContributingCapture != now.Format(time.RFC3339Nano) || cache.ObservedSpanSeconds == nil || *cache.ObservedSpanSeconds != (30*time.Minute).Seconds() {
			t.Errorf("capture metadata = first %v last %v span %v", cache.FirstContributingCapture, cache.LastContributingCapture, cache.ObservedSpanSeconds)
		}
		for name, measurement := range map[string]statsSummaryIntegerMeasurement{
			"uncached_input": cache.UncachedInput,
			"cache_read":     cache.CacheRead,
			"cache_write":    cache.CacheWrite,
			"logical_input":  cache.LogicalInput,
		} {
			if measurement.State != "measured" || measurement.Value == nil {
				t.Fatalf("%s = %+v, want measured", name, measurement)
			}
		}
		if cache.UncachedInput.Value.String() != "50" || cache.CacheRead.Value.String() != "150" || cache.CacheWrite.Value.String() != "0" || cache.LogicalInput.Value.String() != "200" {
			t.Errorf("cache totals = input %v read %v write %v logical %v", cache.UncachedInput.Value, cache.CacheRead.Value, cache.CacheWrite.Value, cache.LogicalInput.Value)
		}
		if cache.CachedInputShare.State != "measured" || cache.CachedInputShare.Value == nil || math.Abs(*cache.CachedInputShare.Value-0.75) > 1e-12 || cache.CachedInputShare.Ratio == nil || *cache.CachedInputShare.Ratio != "3/4" {
			t.Errorf("cached_input_share = %+v, want measured 3/4", cache.CachedInputShare)
		}
	})

	t.Run("excludes invalid, out-of-window, and unsupported telemetry from window metadata", func(t *testing.T) {
		db := openMemoryDB(t)
		acceptedSessionID := seedSession(t, db)
		excludedSessionID := "bbbbbbbbbbbbbbbb"
		if err := db.InsertSession(&dbpkg.Session{ID: excludedSessionID, Agent: "test-agent"}); err != nil {
			t.Fatal(err)
		}
		capture := now.Add(-time.Minute)
		seedStatsTelemetry(t, db, acceptedSessionID, 1, capture, measuredStatsToken(10), measuredStatsToken(20), measuredStatsToken(0))
		seedStatsTelemetryWithContract(t, db, excludedSessionID, 2, "not-a-timestamp", reuse.RequiredRuntime, reuse.RequiredRuntimeVersion, measuredStatsToken(100), measuredStatsToken(100), measuredStatsToken(100))
		seedStatsTelemetry(t, db, excludedSessionID, 3, now.Add(-31*time.Minute), measuredStatsToken(100), measuredStatsToken(100), measuredStatsToken(100))
		seedStatsTelemetry(t, db, excludedSessionID, 4, now.Add(time.Nanosecond), measuredStatsToken(100), measuredStatsToken(100), measuredStatsToken(100))
		seedStatsTelemetryWithContract(t, db, excludedSessionID, 5, capture.Format(time.RFC3339Nano), "other-runtime", reuse.RequiredRuntimeVersion, measuredStatsToken(100), measuredStatsToken(100), measuredStatsToken(100))
		seedStatsTelemetryWithContract(t, db, excludedSessionID, 6, capture.Format(time.RFC3339Nano), reuse.RequiredRuntime, "1.18.4", measuredStatsToken(100), measuredStatsToken(100), measuredStatsToken(100))

		var buf bytes.Buffer
		if _, err := statsSummaryLogic(nil, FormatJSON, 30*time.Minute, now, db, &buf); err != nil {
			t.Fatalf("statsSummaryLogic: %v", err)
		}
		var got statsSummary
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		cache := got.CacheReuse
		if cache.ContributingSessions != 1 || cache.ContributingModelSteps != 1 || cache.FirstContributingCapture == nil || *cache.FirstContributingCapture != capture.Format(time.RFC3339Nano) || cache.LastContributingCapture == nil || *cache.LastContributingCapture != capture.Format(time.RFC3339Nano) || cache.ObservedSpanSeconds == nil || *cache.ObservedSpanSeconds != 0 {
			t.Errorf("capture metadata = %+v", cache)
		}
		if cache.UncachedInput.State != "measured" || cache.UncachedInput.Value == nil || cache.UncachedInput.Value.String() != "10" || cache.CacheRead.State != "measured" || cache.CacheRead.Value == nil || cache.CacheRead.Value.String() != "20" {
			t.Errorf("cache totals = %+v", cache)
		}
	})

	t.Run("does not reconstruct incomplete cache totals", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		seedStatsTelemetry(t, db, sid, 1, now.Add(-2*time.Minute), measuredStatsToken(5), measuredStatsToken(7), measuredStatsToken(11))
		seedStatsTelemetry(t, db, sid, 2, now.Add(-time.Minute),
			telemetry.IntegerMeasurement{State: telemetry.MeasurementUnknown}, measuredStatsToken(13), telemetry.IntegerMeasurement{State: telemetry.MeasurementMalformed})

		var buf bytes.Buffer
		if _, err := statsSummaryLogic(nil, FormatJSON, 30*time.Minute, now, db, &buf); err != nil {
			t.Fatalf("statsSummaryLogic: %v", err)
		}
		var got statsSummary
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		cache := got.CacheReuse
		if cache.ContributingSessions != 1 || cache.ContributingModelSteps != 2 || cache.FirstContributingCapture == nil || *cache.FirstContributingCapture != now.Add(-2*time.Minute).Format(time.RFC3339Nano) || cache.LastContributingCapture == nil || *cache.LastContributingCapture != now.Add(-time.Minute).Format(time.RFC3339Nano) || cache.ObservedSpanSeconds == nil || *cache.ObservedSpanSeconds != time.Minute.Seconds() {
			t.Errorf("capture metadata = %+v", cache)
		}
		if cache.UncachedInput.State != "missing" || cache.UncachedInput.Value != nil {
			t.Errorf("uncached_input = %+v, want missing", cache.UncachedInput)
		}
		if cache.CacheRead.State != "measured" || cache.CacheRead.Value == nil || cache.CacheRead.Value.String() != "20" {
			t.Errorf("cache_read = %+v, want measured 20", cache.CacheRead)
		}
		if cache.CacheWrite.State != "malformed" || cache.CacheWrite.Value != nil || cache.LogicalInput.State != "malformed" || cache.CachedInputShare.State != "malformed" {
			t.Errorf("cache states = write %+v logical %+v share %+v", cache.CacheWrite, cache.LogicalInput, cache.CachedInputShare)
		}
	})

	t.Run("zero logical input makes the share not applicable", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		seedStatsTelemetry(t, db, sid, 1, now.Add(-time.Minute), measuredStatsToken(0), measuredStatsToken(0), measuredStatsToken(0))

		var buf bytes.Buffer
		if _, err := statsSummaryLogic(nil, FormatJSON, 30*time.Minute, now, db, &buf); err != nil {
			t.Fatalf("statsSummaryLogic: %v", err)
		}
		var got statsSummary
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		cache := got.CacheReuse
		if cache.ContributingSessions != 1 || cache.ContributingModelSteps != 1 || cache.FirstContributingCapture == nil || cache.LastContributingCapture == nil || *cache.FirstContributingCapture != now.Add(-time.Minute).Format(time.RFC3339Nano) || *cache.LastContributingCapture != now.Add(-time.Minute).Format(time.RFC3339Nano) || cache.ObservedSpanSeconds == nil || *cache.ObservedSpanSeconds != 0 {
			t.Errorf("zero-span capture metadata = %+v", cache)
		}
		share := cache.CachedInputShare
		if share.State != "not_applicable" || share.Value != nil || share.Reason == nil || *share.Reason != "logical_input_not_positive" {
			t.Errorf("cached_input_share = %+v", share)
		}
	})

	t.Run("summary JSON schema has only the fixed version 6 contract keys", func(t *testing.T) {
		db := openMemoryDB(t)
		var buf bytes.Buffer
		if _, err := statsSummaryLogic(nil, FormatJSON, 30*time.Minute, now, db, &buf); err != nil {
			t.Fatalf("statsSummaryLogic: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		if got["schema_version"] != float64(6) {
			t.Errorf("schema_version = %v, want 6", got["schema_version"])
		}
		assertStatsJSONKeys(t, got, "schema_version", "generated_at", "window", "calls", "outcomes", "tokens", "cache_reuse")
		assertStatsJSONKeys(t, got["window"].(map[string]any), "seconds", "from", "to")
		assertStatsJSONKeys(t, got["calls"].(map[string]any), "total_with_output", "with_compressed_artifact")
		assertStatsJSONKeys(t, got["outcomes"].(map[string]any), "successful_calls", "error_calls", "unknown_calls", "known_calls", "total_calls", "error_rate_pct_of_known", "outcome_coverage_pct")
		assertStatsJSONKeys(t, got["tokens"].(map[string]any), "raw_estimated", "delivered_estimated", "raw_estimated_per_second", "delivered_estimated_per_second", "reduction_percent")
		cache := got["cache_reuse"].(map[string]any)
		assertStatsJSONKeys(t, cache,
			"contract_version", "runtime", "runtime_version", "evidence_scope", "requested_window_coverage",
			"first_contributing_capture", "last_contributing_capture", "observed_span_seconds", "contributing_sessions", "contributing_model_steps",
			"uncached_input", "cache_read", "cache_write", "logical_input", "cached_input_share", "formula", "unit")
		assertStatsJSONKeys(t, cache["requested_window_coverage"].(map[string]any), "state", "reason", "scope")
		for _, field := range []string{"uncached_input", "cache_read", "cache_write", "logical_input"} {
			assertStatsJSONKeys(t, cache[field].(map[string]any), "state", "value")
		}
		assertStatsJSONKeys(t, cache["cached_input_share"].(map[string]any), "state", "value", "ratio", "reason")
	})

	t.Run("breakdown JSON schema contains the detailed version 2 contract", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		seedCalls(t, db, sid, []callSeed{{tool: "read", output: "result"}})
		var buf bytes.Buffer
		if _, err := statsBreakdownLogic(nil, FormatJSON, 30*time.Minute, now, db, &buf); err != nil {
			t.Fatalf("statsBreakdownLogic: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		if got["schema_version"] != float64(2) {
			t.Errorf("schema_version = %v, want 2", got["schema_version"])
		}
		assertStatsJSONKeys(t, got, "schema_version", "generated_at", "window", "calls", "outcomes", "tokens", "delivery_breakdown")
		assertStatsJSONKeys(t, got["outcomes"].(map[string]any), "successful_calls", "error_calls", "unknown_calls", "known_calls", "total_calls", "error_rate_pct_of_known", "outcome_coverage_pct")
		delivery := got["delivery_breakdown"].(map[string]any)
		assertStatsJSONKeys(t, delivery, "detailed_group_limit", "distinct_groups", "omitted_distinct_groups", "groups", "other")
		assertStatsJSONKeys(t, delivery["other"].(map[string]any), "calls", "with_compressed_artifact", "raw_estimated", "delivered_estimated")
		groups := delivery["groups"].([]any)
		if len(groups) != 1 {
			t.Fatalf("groups = %v, want one group", groups)
		}
		assertStatsJSONKeys(t, groups[0].(map[string]any),
			"tool", "command", "subcommand", "profile", "route", "disposition", "raw_output_size_band",
			"calls", "with_compressed_artifact", "raw_estimated", "delivered_estimated")
	})

	t.Run("text and help keep summary separate from breakdown", func(t *testing.T) {
		db := openMemoryDB(t)
		var summaryText bytes.Buffer
		if _, err := statsSummaryLogic(nil, FormatText, 30*time.Minute, now, db, &summaryText); err != nil {
			t.Fatalf("text: %v", err)
		}
		if !strings.Contains(summaryText.String(), "WINDOW") || !strings.Contains(summaryText.String(), "RAW_TOK") || !strings.Contains(summaryText.String(), "PROVIDER_PROMPT_PREFIX_CACHE_EVIDENCE") || !strings.Contains(summaryText.String(), "REQUESTED_WINDOW_COVERAGE") || !strings.Contains(summaryText.String(), "OBSERVED_SPAN_SECONDS") || !strings.Contains(summaryText.String(), "CONTRIBUTING_KTX_SESSIONS") || !strings.Contains(summaryText.String(), "PROMPT_PREFIX_CACHE_READ") || !strings.Contains(summaryText.String(), "PROMPT_PREFIX_CACHED_INPUT_PCT") || strings.Contains(summaryText.String(), "DISPOSITION") {
			t.Errorf("summary text fields = %q", summaryText.String())
		}
		if strings.Count(strings.TrimSpace(summaryText.String()), "\n") != 1 {
			t.Errorf("summary text should contain one header and one row: %q", summaryText.String())
		}
		var summaryHelp bytes.Buffer
		if _, err := statsSummaryLogic([]string{"--help"}, FormatJSON, 30*time.Minute, now, db, &summaryHelp); err != nil {
			t.Fatalf("help: %v", err)
		}
		if !strings.Contains(summaryHelp.String(), "ktx stats summary") || !strings.Contains(summaryHelp.String(), "schema_version 6") || !strings.Contains(summaryHelp.String(), "fixed-shape") || !strings.Contains(summaryHelp.String(), "provider prompt/prefix-cache") || !strings.Contains(summaryHelp.String(), "collector_lifecycle_evidence_unavailable") || !strings.Contains(summaryHelp.String(), "not a provider hit rate") {
			t.Errorf("summary help = %q", summaryHelp.String())
		}

		var breakdownText bytes.Buffer
		if _, err := statsBreakdownLogic(nil, FormatText, 30*time.Minute, now, db, &breakdownText); err != nil {
			t.Fatalf("breakdown text: %v", err)
		}
		if !strings.Contains(breakdownText.String(), "DISPOSITION") || strings.Contains(breakdownText.String(), "CACHE_READ") {
			t.Errorf("breakdown text fields = %q", breakdownText.String())
		}
		var breakdownHelp bytes.Buffer
		if _, err := statsBreakdownLogic([]string{"--help"}, FormatJSON, 30*time.Minute, now, db, &breakdownHelp); err != nil {
			t.Fatalf("breakdown help: %v", err)
		}
		if !strings.Contains(breakdownHelp.String(), "ktx stats breakdown") || !strings.Contains(breakdownHelp.String(), "schema_version 2") || !strings.Contains(breakdownHelp.String(), "up to 100") {
			t.Errorf("breakdown help = %q", breakdownHelp.String())
		}
	})
}

func TestStatsCommandDispatchesWindowViews(t *testing.T) {
	for _, tt := range []struct {
		name        string
		wantField   string
		forbidField string
	}{
		{name: "summary", wantField: "cache_reuse", forbidField: "delivery_breakdown"},
		{name: "breakdown", wantField: "delivery_breakdown", forbidField: "cache_reuse"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := openMemoryDB(t)
			var stdout bytes.Buffer
			_, err := statsCommandFunc(&DispatchContext{
				Args:       []string{tt.name},
				ParsedArgs: statsParsedArgs{Limit: statsDefaultLimit, Window: statsDefaultWindow},
				Format:     FormatJSON,
				Stdout:     &stdout,
				DB:         db,
			})
			if err != nil {
				t.Fatalf("statsCommandFunc: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("json unmarshal: %v", err)
			}
			if _, ok := got[tt.wantField]; !ok {
				t.Errorf("output missing %q: %v", tt.wantField, got)
			}
			if _, ok := got[tt.forbidField]; ok {
				t.Errorf("output unexpectedly contains %q: %v", tt.forbidField, got)
			}
		})
	}
}

func TestStatsBreakdownEndToEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ktx.db")
	db, err := dbpkg.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KTX_DB", path)

	t.Run("window and format reach the breakdown", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := runWith([]string{"stats", "breakdown", "--window", "45m", "--format", "json"}, &stdout, &stderr); code != ExitOK {
			t.Fatalf("exit = %d; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		var got map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		if got["schema_version"] != float64(statsBreakdownSchemaVersion) || got["window"].(map[string]any)["seconds"] != float64(45*60) {
			t.Errorf("breakdown output = %v", got)
		}
	})

	t.Run("help is nested", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := runWith([]string{"stats", "breakdown", "--help"}, &stdout, &stderr); code != ExitOK {
			t.Fatalf("exit = %d; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "ktx stats breakdown") || stderr.Len() != 0 {
			t.Errorf("stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	})

	t.Run("limit is rejected", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := runWith([]string{"stats", "breakdown", "--limit", "1"}, &stdout, &stderr); code != ExitUsage {
			t.Fatalf("exit = %d; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		if stdout.Len() != 0 || !strings.Contains(stderr.String(), "--limit is only valid for grouped stats") {
			t.Errorf("stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	})
}

func assertDeliveryReconciliation(t *testing.T, summary statsBreakdown) {
	t.Helper()
	calls := summary.DeliveryBreakdown.Other.Calls
	artifacts := summary.DeliveryBreakdown.Other.WithCompressedArtifact
	raw := summary.DeliveryBreakdown.Other.RawEstimated
	delivered := summary.DeliveryBreakdown.Other.DeliveredEstimated
	for _, group := range summary.DeliveryBreakdown.Groups {
		calls += group.Calls
		artifacts += group.WithCompressedArtifact
		raw += group.RawEstimated
		delivered += group.DeliveredEstimated
	}
	if calls != summary.Calls.TotalWithOutput || artifacts != summary.Calls.WithCompressedArtifact || raw != summary.Tokens.RawEstimated || delivered != summary.Tokens.DeliveredEstimated {
		t.Errorf("delivery totals calls=%d artifacts=%d raw=%d delivered=%d do not reconcile with calls=%+v tokens=%+v", calls, artifacts, raw, delivered, summary.Calls, summary.Tokens)
	}
}

func TestParseStatsArgsWindow(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantArg string
		want    time.Duration
		wantErr string
	}{
		{name: "summary default", args: []string{"summary"}, wantArg: "summary", want: 30 * time.Minute},
		{name: "summary separate value", args: []string{"summary", "--window", "45s"}, wantArg: "summary", want: 45 * time.Second},
		{name: "breakdown equals value", args: []string{"breakdown", "--window=1h30m"}, wantArg: "breakdown", want: 90 * time.Minute},
		{name: "missing", args: []string{"summary", "--window"}, wantErr: "requires a duration"},
		{name: "zero", args: []string{"summary", "--window", "0s"}, wantErr: "must be positive"},
		{name: "negative", args: []string{"summary", "--window=-1m"}, wantErr: "must be positive"},
		{name: "invalid", args: []string{"summary", "--window", "soon"}, wantErr: "invalid --window"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rest, parsedAny, err := parseStatsArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseStatsArgs: %v", err)
			}
			if !reflect.DeepEqual(rest, []string{tt.wantArg}) {
				t.Errorf("rest = %v, want [%s]", rest, tt.wantArg)
			}
			parsed := parsedAny.(statsParsedArgs)
			if parsed.Window != tt.want {
				t.Errorf("window = %v, want %v", parsed.Window, tt.want)
			}
		})
	}
}

func assertStatsJSONKeys(t *testing.T, got map[string]any, want ...string) {
	t.Helper()
	gotKeys := make([]string, 0, len(got))
	for key := range got {
		gotKeys = append(gotKeys, key)
	}
	sort.Strings(gotKeys)
	sort.Strings(want)
	if !reflect.DeepEqual(gotKeys, want) {
		t.Errorf("JSON keys = %v, want %v", gotKeys, want)
	}
}

// openMemoryDB is a helper that opens an in-memory database.
func openMemoryDB(t *testing.T) *dbpkg.DB {
	t.Helper()
	d, err := dbpkg.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// callSeed is a minimal tool_call fixture for the stats tests.
type callSeed struct {
	tool    string
	command string
	output  string
	isError *bool
}

// seedSession inserts a session and returns its ID.
func seedSession(t *testing.T, db *dbpkg.DB) string {
	t.Helper()
	sid := "aaaaaaaaaaaaaaaa"
	if err := db.InsertSession(&dbpkg.Session{
		ID:         sid,
		Agent:      "test-agent",
		StartedAt:  "2026-07-12T00:00:00Z",
		LastSeenAt: "2026-07-12T00:00:00Z",
	}); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	return sid
}

// seedCalls inserts tool_call rows and returns their IDs.
func seedCalls(t *testing.T, db *dbpkg.DB, sessionID string, calls []callSeed) []string {
	t.Helper()
	var ids []string
	for i, c := range calls {
		id := string(rune('a'+i)) + string(rune('b'+i)) + "xxxxxxxxxxxxxx"
		if len(id) != 16 {
			id = strings.Repeat("a", 16)
		}
		if err := db.PersistToolCall(&dbpkg.ToolCall{
			ID:        id,
			SessionID: sessionID,
			Tool:      c.tool,
			Input:     `{"command": "` + c.command + `"}`,
			Output:    c.output,
			Timestamp: "2026-07-12T00:00:01Z",
			IsError:   c.isError,
		}); err != nil {
			t.Fatalf("PersistToolCall[%d]: %v", i, err)
		}
		ids = append(ids, id)
	}
	return ids
}

func boolPointer(value bool) *bool { return &value }

func float64Pointer(value float64) *float64 { return &value }

func assertOutcomeTextColumns(t *testing.T, name, output, wantErrorRate, wantCoverage string) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		t.Fatalf("%s text = %q", name, output)
	}
	headings := strings.Fields(lines[0])
	values := strings.Fields(lines[1])
	if len(values) < 10 {
		t.Fatalf("%s columns headings=%v values=%v", name, headings, values)
	}
	columns := make(map[string]string, len(headings))
	for i := range values {
		columns[headings[i]] = values[i]
	}
	if columns["ERROR_PCT_KNOWN"] != wantErrorRate || columns["OUTCOME_COVERAGE_PCT"] != wantCoverage {
		t.Errorf("%s outcome text columns = %v", name, columns)
	}
}

// seedCompressedRow inserts a compressed row for a source tool_call.
func seedCompressedRow(t *testing.T, db *dbpkg.DB, sourceID, output, timestamp string) {
	t.Helper()
	cid := "c" + sourceID[1:]
	if err := db.InsertCompressed(&dbpkg.Compressed{
		ID:          cid,
		SourceID:    sourceID,
		Output:      output,
		ContentHash: dbpkg.ContentHash(output),
		Pipeline:    "[]",
		Timestamp:   timestamp,
	}); err != nil {
		t.Fatalf("InsertCompressed: %v", err)
	}
}

func seedStatsTelemetry(t *testing.T, db *dbpkg.DB, sessionID string, ordinal uint64, capturedAt time.Time, input, read, write telemetry.IntegerMeasurement) {
	t.Helper()
	seedStatsTelemetryWithContract(t, db, sessionID, ordinal, capturedAt.Format(time.RFC3339Nano), reuse.RequiredRuntime, reuse.RequiredRuntimeVersion, input, read, write)
}

func seedStatsTelemetryWithContract(t *testing.T, db *dbpkg.DB, sessionID string, ordinal uint64, capturedAt, runtime, runtimeVersion string, input, read, write telemetry.IntegerMeasurement) {
	t.Helper()
	name := fmt.Sprintf("%d", ordinal)
	observation := &telemetry.Observation{
		Key:     telemetry.Key{SessionID: "source_" + name, MessageID: "message_" + name, PartID: "part_" + name},
		Ordinal: ordinal,
		Reason:  "stop",
		Cost:    telemetry.CostMeasurement{State: telemetry.MeasurementUnknown},
		Tokens: telemetry.Tokens{
			Input:      input,
			Output:     telemetry.IntegerMeasurement{State: telemetry.MeasurementUnknown},
			Reasoning:  telemetry.IntegerMeasurement{State: telemetry.MeasurementUnknown},
			CacheRead:  read,
			CacheWrite: write,
		},
		Runtime:        runtime,
		RuntimeVersion: runtimeVersion,
		Source:         telemetry.SourceEventHook,
	}
	inserted, err := db.PersistSessionTelemetry(sessionID, capturedAt, observation)
	if err != nil || !inserted {
		t.Fatalf("PersistSessionTelemetry(%d) = %t, %v", ordinal, inserted, err)
	}
}

func measuredStatsToken(value int64) telemetry.IntegerMeasurement {
	return telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &value}
}
