package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
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

func TestStatsSummaryLogic(t *testing.T) {
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
		if got.SchemaVersion != 2 || got.GeneratedAt != now.Format(time.RFC3339Nano) {
			t.Errorf("metadata = %+v", got)
		}
		if got.Window.Seconds != 1800 || got.Window.From != now.Add(-30*time.Minute).Format(time.RFC3339Nano) || got.Window.To != got.GeneratedAt {
			t.Errorf("window = %+v", got.Window)
		}
		if got.Calls.TotalWithOutput != 0 || got.Calls.WithCompressedArtifact != 0 {
			t.Errorf("calls = %+v", got.Calls)
		}
		if got.Tokens.RawEstimatedPerSecond != 0 || got.Tokens.DeliveredEstimatedPerSecond != 0 || got.Tokens.ReductionPercent != nil {
			t.Errorf("tokens = %+v", got.Tokens)
		}
		if got.CacheReuse.ContributingModelSteps != 0 || got.CacheReuse.UncachedInput.State != "missing" || got.CacheReuse.CachedInputShare.State != "missing" {
			t.Errorf("cache_reuse = %+v", got.CacheReuse)
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

	t.Run("aggregates complete runtime cache telemetry with an exact share", func(t *testing.T) {
		db := openMemoryDB(t)
		sid := seedSession(t, db)
		seedStatsTelemetry(t, db, sid, 1, now.Add(-2*time.Minute), measuredStatsToken(20), measuredStatsToken(80), measuredStatsToken(0))
		seedStatsTelemetry(t, db, sid, 2, now.Add(-time.Minute), measuredStatsToken(30), measuredStatsToken(70), measuredStatsToken(0))

		var buf bytes.Buffer
		if _, err := statsSummaryLogic(nil, FormatJSON, 30*time.Minute, now, db, &buf); err != nil {
			t.Fatalf("statsSummaryLogic: %v", err)
		}
		var got statsSummary
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		cache := got.CacheReuse
		if cache.ContractVersion != reuse.RequiredContractVersion || cache.Runtime != reuse.RequiredRuntime || cache.RuntimeVersion != reuse.RequiredRuntimeVersion || cache.ContributingModelSteps != 2 {
			t.Fatalf("cache metadata = %+v", cache)
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
		share := got.CacheReuse.CachedInputShare
		if share.State != "not_applicable" || share.Value != nil || share.Reason == nil || *share.Reason != "logical_input_not_positive" {
			t.Errorf("cached_input_share = %+v", share)
		}
	})

	t.Run("JSON schema has only the version 2 contract keys", func(t *testing.T) {
		db := openMemoryDB(t)
		var buf bytes.Buffer
		if _, err := statsSummaryLogic(nil, FormatJSON, 30*time.Minute, now, db, &buf); err != nil {
			t.Fatalf("statsSummaryLogic: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		assertStatsJSONKeys(t, got, "schema_version", "generated_at", "window", "calls", "tokens", "cache_reuse")
		assertStatsJSONKeys(t, got["window"].(map[string]any), "seconds", "from", "to")
		assertStatsJSONKeys(t, got["calls"].(map[string]any), "total_with_output", "with_compressed_artifact")
		assertStatsJSONKeys(t, got["tokens"].(map[string]any), "raw_estimated", "delivered_estimated", "raw_estimated_per_second", "delivered_estimated_per_second", "reduction_percent")
		cache := got["cache_reuse"].(map[string]any)
		assertStatsJSONKeys(t, cache, "contract_version", "runtime", "runtime_version", "contributing_model_steps", "uncached_input", "cache_read", "cache_write", "logical_input", "cached_input_share", "formula", "unit")
		for _, field := range []string{"uncached_input", "cache_read", "cache_write", "logical_input"} {
			assertStatsJSONKeys(t, cache[field].(map[string]any), "state", "value")
		}
		assertStatsJSONKeys(t, cache["cached_input_share"].(map[string]any), "state", "value", "ratio", "reason")
	})

	t.Run("text and summary help follow command conventions", func(t *testing.T) {
		db := openMemoryDB(t)
		var text bytes.Buffer
		if _, err := statsSummaryLogic(nil, FormatText, 30*time.Minute, now, db, &text); err != nil {
			t.Fatalf("text: %v", err)
		}
		if !strings.Contains(text.String(), "WINDOW") || !strings.Contains(text.String(), "RAW_TOK") || !strings.Contains(text.String(), "CACHE_READ") || !strings.Contains(text.String(), "CACHED_INPUT_PCT") {
			t.Errorf("text output missing fields: %q", text.String())
		}
		var help bytes.Buffer
		if _, err := statsSummaryLogic([]string{"--help"}, FormatJSON, 30*time.Minute, now, db, &help); err != nil {
			t.Fatalf("help: %v", err)
		}
		if !strings.Contains(help.String(), "ktx stats summary") {
			t.Errorf("summary help missing command: %q", help.String())
		}
		if !strings.Contains(help.String(), "schema_version 2") || !strings.Contains(help.String(), "cached-input share") || !strings.Contains(help.String(), "not a provider hit rate") {
			t.Errorf("summary help missing cache contract: %q", help.String())
		}
	})
}

func TestParseStatsArgsSummaryWindow(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    time.Duration
		wantErr string
	}{
		{name: "default", args: []string{"summary"}, want: 30 * time.Minute},
		{name: "separate value", args: []string{"summary", "--window", "45s"}, want: 45 * time.Second},
		{name: "equals value", args: []string{"summary", "--window=1h30m"}, want: 90 * time.Minute},
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
			if !reflect.DeepEqual(rest, []string{"summary"}) {
				t.Errorf("rest = %v, want [summary]", rest)
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
		}); err != nil {
			t.Fatalf("PersistToolCall[%d]: %v", i, err)
		}
		ids = append(ids, id)
	}
	return ids
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
		Runtime:        reuse.RequiredRuntime,
		RuntimeVersion: reuse.RequiredRuntimeVersion,
		Source:         telemetry.SourceEventHook,
	}
	inserted, err := db.PersistSessionTelemetry(sessionID, capturedAt.Format(time.RFC3339Nano), observation)
	if err != nil || !inserted {
		t.Fatalf("PersistSessionTelemetry(%d) = %t, %v", ordinal, inserted, err)
	}
}

func measuredStatsToken(value int64) telemetry.IntegerMeasurement {
	return telemetry.IntegerMeasurement{State: telemetry.MeasurementMeasured, Value: &value}
}
