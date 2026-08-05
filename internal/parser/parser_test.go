// Pure-function coverage of parser.ParseEvent. Every fixture is a byte
// slice — no database I/O, no subprocess, no fs. The tests are the
// regression net for the OpenCode tool_use shape, passthrough, and
// table-driven coverage of the recognised and rejected shapes.
package parser

import (
	"encoding/json"
	"strings"
	"testing"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// TestParseEvent is the table-driven regression net for the parser.
// Each case is a single JSON line as the wrapper sees it; the test
// asserts the populated *db.ToolCall shape (for tool events) or
// (nil, false) (for non-tool events). No database is opened.
//
// Tool-name/Output/Provider checks use struct equality for the simple
// cases and targeted pointer checks for the cost fields so a regression
// in absent-vs-zero round-trip is observable.
func TestParseEvent(t *testing.T) {
	t.Run("OpenCode tool_use with full cost envelope", func(t *testing.T) {
		// OpenCode tool_use shape — top.type == "tool_use",
		// part.type == "tool", part.state.input/output. This is the
		// canonical acceptance fixture shape and the wrapper's
		// primary recognition path.
		//
		// Note on timestamp: the parser's `_ = json.Unmarshal(top["timestamp"],
		// &ev.Timestamp)` only extracts string-typed timestamp values; a
		// JSON number is silently dropped and the parser falls back to
		// time.Now(). The acceptance fixture uses a number timestamp
		// (1781874170009), so the asserted Timestamp here is a string
		// form (preserved behaviour; see parser.go's "no production
		// behaviour change" contract). The Surprises entry records
		// this as a pre-existing limitation.
		const payload = `{"type":"tool_use","timestamp":"2026-06-20T11:42:50.009Z","sessionID":"ses_12005f840ffe3UdMoswMamfi93","part":{"type":"tool","tool":"bash","callID":"call_00_wdxisTIz0cbblljji98N2053","state":{"status":"completed","input":{"command":"echo hello world","description":"Print hello world"},"output":"hello world\n","metadata":{"output":"hello world\n","exit":0,"description":"Print hello world","truncated":false},"title":"Print hello world","time":{"start":1781874170005,"end":1781874170008}},"id":"prt_edffa17ab001KmnhiK8RBDlyPg","sessionID":"ses_12005f840ffe3UdMoswMamfi93","messageID":"msg_edffa0b8d00137shbHhTbaqUw2"}}`

		tc, ok := ParseEvent([]byte(payload))
		if !ok {
			t.Fatalf("ParseEvent returned (nil, false); want populated tool_call")
		}
		if tc.Tool != "bash" {
			t.Errorf("Tool = %q, want %q", tc.Tool, "bash")
		}
		if tc.EventType != "tool_call" {
			t.Errorf("EventType = %q, want %q", tc.EventType, "tool_call")
		}
		// OpenCode latency: 1781874170008 - 1781874170005 = 3ms.
		if tc.LatencyMs == nil || *tc.LatencyMs != 3 {
			t.Errorf("LatencyMs = %v, want pointer to 3", tc.LatencyMs)
		}
		// OpenCode provider default (runtime-known single exception):
		// the parser fills provider="opencode" because the wrapper
		// knows its agent. No "provider" field in the source event.
		if tc.Provider == nil || *tc.Provider != "opencode" {
			t.Errorf("Provider = %v, want pointer to %q", tc.Provider, "opencode")
		}
		// TokensIn/TokensOut/Cost/Model are NOT emitted on OpenCode
		// tool_use events; the parser leaves the pointers nil.
		if tc.TokensIn != nil {
			t.Errorf("TokensIn = %v, want nil (OpenCode emits on step_finish)", *tc.TokensIn)
		}
		if tc.TokensOut != nil {
			t.Errorf("TokensOut = %v, want nil", *tc.TokensOut)
		}
		if tc.Cost != nil {
			t.Errorf("Cost = %v, want nil", *tc.Cost)
		}
		if tc.Model != nil {
			t.Errorf("Model = %v, want nil", *tc.Model)
		}
		// Input should be the JSON object re-encoded (it was a JSON
		// object in part.state.input, not a string).
		if !strings.Contains(tc.Input, "echo hello world") {
			t.Errorf("Input = %q, want it to contain %q", tc.Input, "echo hello world")
		}
		if tc.Output != "hello world\n" {
			t.Errorf("Output = %q, want %q", tc.Output, "hello world\n")
		}
		// SessionID is empty — the parser does not know which session
		// the event belongs to; the wrapper sets it.
		if tc.SessionID != "" {
			t.Errorf("SessionID = %q, want empty (caller sets)", tc.SessionID)
		}
		// ID is a fresh nanoid: 16 lowercase alpha chars.
		if len(tc.ID) != 16 {
			t.Errorf("ID length = %d, want 16", len(tc.ID))
		}
		for _, r := range tc.ID {
			if r < 'a' || r > 'z' {
				t.Errorf("ID = %q contains non-lowercase-alpha char %q", tc.ID, r)
				break
			}
		}
		// String-typed timestamp passes through normaliseTimestamp
		// unchanged (already RFC3339Nano).
		wantTS := "2026-06-20T11:42:50.009Z"
		if tc.Timestamp != wantTS {
			t.Errorf("Timestamp = %q, want %q (string-typed ISO-8601 passthrough)", tc.Timestamp, wantTS)
		}
		// ContentHash is SHA-256 of Output.
		if tc.ContentHash != dbpkg.ContentHash(tc.Output) {
			t.Errorf("ContentHash = %q, want SHA-256 of Output", tc.ContentHash)
		}
	})

	t.Run("OpenCode tool_use with cost fields in state.metadata (defensive path)", func(t *testing.T) {
		// A future OpenCode shape (or a custom adapter) that places
		// the cost fields inside state.metadata. The parser's defensive
		// reads surface them so they round-trip into the persisted
		// tool_call row.
		const payload = `{"type":"tool_use","timestamp":1781874170009,"part":{"type":"tool","tool":"bash","callID":"call_001","state":{"input":{"command":"echo"},"output":"hello\n","metadata":{"tokens_in":150,"tokens_out":25,"cost":0.001234,"model":"claude-sonnet-4-20250514","provider":"anthropic","exit":0}}}}`

		tc, ok := ParseEvent([]byte(payload))
		if !ok {
			t.Fatalf("ParseEvent returned (nil, false); want populated tool_call")
		}
		if tc.Tool != "bash" {
			t.Errorf("Tool = %q, want %q", tc.Tool, "bash")
		}
		if tc.TokensIn == nil || *tc.TokensIn != 150 {
			t.Errorf("TokensIn = %v, want pointer to 150", tc.TokensIn)
		}
		if tc.TokensOut == nil || *tc.TokensOut != 25 {
			t.Errorf("TokensOut = %v, want pointer to 25", tc.TokensOut)
		}
		if tc.Cost == nil || *tc.Cost != 0.001234 {
			t.Errorf("Cost = %v, want pointer to 0.001234", tc.Cost)
		}
		// Provider from metadata wins over the runtime default.
		if tc.Provider == nil || *tc.Provider != "anthropic" {
			t.Errorf("Provider = %v, want pointer to %q (metadata wins over default)", tc.Provider, "anthropic")
		}
		if tc.Model == nil || *tc.Model != "claude-sonnet-4-20250514" {
			t.Errorf("Model = %v, want pointer to claude-sonnet-4-20250514", tc.Model)
		}
	})

	t.Run("OpenCode tool_use with state.time missing end (latency nil)", func(t *testing.T) {
		// A partial state.time (only start, no end) must NOT produce a
		// latency_ms value. The parser returns nil rather than guessing.
		const payload = `{"type":"tool_use","part":{"type":"tool","tool":"bash","state":{"input":{},"output":"x","time":{"start":100}}}}`
		tc, ok := ParseEvent([]byte(payload))
		if !ok {
			t.Fatalf("ParseEvent returned (nil, false); want populated tool_call")
		}
		if tc.LatencyMs != nil {
			t.Errorf("LatencyMs = %v, want nil (missing end)", tc.LatencyMs)
		}
	})

	t.Run("OpenCode tool_use with part.type != tool (skipped)", func(t *testing.T) {
		// top.type == "tool_use" but part.type == "step-start" or
		// similar — NOT a tool call. The parser rejects it.
		const payload = `{"type":"tool_use","part":{"type":"step-start","sessionID":"ses_x"}}`
		tc, ok := ParseEvent([]byte(payload))
		if ok {
			t.Errorf("ParseEvent returned ok=true; want false (part.type != tool)")
		}
		if tc != nil {
			t.Errorf("ParseEvent returned tc=%+v; want nil", tc)
		}
	})

	t.Run("OpenCode tool_use with empty tool name (skipped)", func(t *testing.T) {
		// part.type == "tool" but no tool name in part.tool. Rejected
		// so the wrapper does not record a nameless tool_call.
		const payload = `{"type":"tool_use","part":{"type":"tool","state":{"input":{},"output":"x"}}}`
		tc, ok := ParseEvent([]byte(payload))
		if ok {
			t.Errorf("ParseEvent returned ok=true; want false (empty tool name)")
		}
		if tc != nil {
			t.Errorf("ParseEvent returned tc=%+v; want nil", tc)
		}
	})

	t.Run("flat shape with id/tool/name/input/args/output/result/timestamp/time", func(t *testing.T) {
		// Fallback flat shape. Top-level id/tool/name and the
		// input/args/output/result aliases must all round-trip. The
		// flat-fallback path is the safety net for runtimes that emit
		// the cost fields flat (no part.state nesting).
		const payload = `{"id":"call_flat_001","tool":"read","name":"read","input":"README.md","args":null,"output":"# ktx\n","result":null,"timestamp":"2026-06-18T12:00:00Z","time":1700000000,"tokens_in":200,"tokens_out":50,"cost":0.005,"model":"claude-sonnet-4-20250514","provider":"anthropic"}`

		tc, ok := ParseEvent([]byte(payload))
		if !ok {
			t.Fatalf("ParseEvent returned (nil, false); want populated tool_call")
		}
		// tool wins over name when both are present (the parser reads
		// top["tool"] first and falls back to top["name"] if tool is
		// empty). Here both are populated; tool wins.
		if tc.Tool != "read" {
			t.Errorf("Tool = %q, want %q", tc.Tool, "read")
		}
		// input wins over args (first non-empty alias). args is null,
		// which is empty after Unmarshal (RawMessage for null is `null`,
		// which is 4 bytes — non-empty). Hmm, check this carefully.
		// Actually json.RawMessage("null") is len > 0, so
		// firstNonEmptyRaw returns it. But null is not a string, so
		// jsonValueToString falls through to the marshal path. The
		// parser reads input first (it's a string), so input wins.
		if tc.Input != "README.md" {
			t.Errorf("Input = %q, want %q (input wins over args)", tc.Input, "README.md")
		}
		if tc.Output != "# ktx\n" {
			t.Errorf("Output = %q, want %q", tc.Output, "# ktx\n")
		}
		// Timestamp: ISO-8601 string passes through unchanged.
		if tc.Timestamp != "2026-06-18T12:00:00Z" {
			t.Errorf("Timestamp = %q, want %q (ISO string passthrough)", tc.Timestamp, "2026-06-18T12:00:00Z")
		}
		if tc.TokensIn == nil || *tc.TokensIn != 200 {
			t.Errorf("TokensIn = %v, want pointer to 200", tc.TokensIn)
		}
		if tc.TokensOut == nil || *tc.TokensOut != 50 {
			t.Errorf("TokensOut = %v, want pointer to 50", tc.TokensOut)
		}
		if tc.Cost == nil || *tc.Cost != 0.005 {
			t.Errorf("Cost = %v, want pointer to 0.005", tc.Cost)
		}
		// Provider is explicit on the flat shape — no OpenCode default
		// applies (flat-fallback runtimes get exactly what they emit).
		if tc.Provider == nil || *tc.Provider != "anthropic" {
			t.Errorf("Provider = %v, want pointer to %q", tc.Provider, "anthropic")
		}
	})

	t.Run("flat shape with name alias only (no tool field)", func(t *testing.T) {
		// A runtime that uses "name" instead of "tool". The flat
		// fallback must accept the name alias.
		const payload = `{"name":"bash","input":{"command":"ls"},"output":"file1\nfile2\n"}`
		tc, ok := ParseEvent([]byte(payload))
		if !ok {
			t.Fatalf("ParseEvent returned (nil, false); want populated tool_call")
		}
		if tc.Tool != "bash" {
			t.Errorf("Tool = %q, want %q", tc.Tool, "bash")
		}
		if !strings.Contains(tc.Input, "ls") {
			t.Errorf("Input = %q, want it to contain %q", tc.Input, "ls")
		}
		if tc.Output != "file1\nfile2\n" {
			t.Errorf("Output = %q, want %q", tc.Output, "file1\nfile2\n")
		}
	})

	t.Run("flat shape with string-encoded Unix seconds timestamp", func(t *testing.T) {
		// The parser's `_ = json.Unmarshal(top["timestamp"], &ev.Timestamp)`
		// only handles string-typed values, so the timestamp here is a
		// string-encoded Unix seconds number. normaliseTimestamp then
		// converts it to RFC3339Nano. This is the round-trip path
		// used by runtimes that emit "timestamp":"1700000000" (rare
		// but possible). A bare-JSON-number timestamp is silently
		// dropped to time.Now() — see Surprises for the pre-existing
		// limitation.
		const payload = `{"tool":"bash","input":{},"output":"x","timestamp":"1700000000"}`
		tc, ok := ParseEvent([]byte(payload))
		if !ok {
			t.Fatalf("ParseEvent returned (nil, false); want populated tool_call")
		}
		wantTS := "2023-11-14T22:13:20Z"
		if tc.Timestamp != wantTS {
			t.Errorf("Timestamp = %q, want %q (string Unix seconds normalised)", tc.Timestamp, wantTS)
		}
	})

	t.Run("flat shape with string-encoded Unix milliseconds timestamp", func(t *testing.T) {
		// String-encoded Unix ms (>= 1e10 -> ms branch in
		// normaliseTimestamp).
		const payload = `{"tool":"bash","input":{},"output":"x","timestamp":"1700000000000"}`
		tc, ok := ParseEvent([]byte(payload))
		if !ok {
			t.Fatalf("ParseEvent returned (nil, false); want populated tool_call")
		}
		wantTS := "2023-11-14T22:13:20Z"
		if tc.Timestamp != wantTS {
			t.Errorf("Timestamp = %q, want %q (string Unix ms normalised)", tc.Timestamp, wantTS)
		}
	})

	t.Run("text message passthrough (non-tool event)", func(t *testing.T) {
		// Text events are not tool events. Passthrough.
		const payload = `{"type":"text","timestamp":1781874172216,"sessionID":"ses_12005f840ffe3UdMoswMamfi93","part":{"id":"prt_edffa1ebb001zdageT5jSV6mI8","sessionID":"ses_12005f840ffe3UdMoswMamfi93","type":"text","text":"hello world","time":{"start":1781874171579,"end":1781874171642}}}`
		assertPassthrough(t, []byte(payload))
	})

	t.Run("step_finish passthrough (non-tool event)", func(t *testing.T) {
		// step_finish events are step-aggregates (tokens, cost),
		// not per-tool events. The wrapper persists them as session
		// rollups via CloseSession, not as tool_calls.
		const payload = `{"type":"step_finish","timestamp":1781874170047,"sessionID":"ses_12005f840ffe3UdMoswMamfi93","part":{"id":"prt_edffa18b8001bnh8K0YHAXB3Gs","reason":"tool-calls","sessionID":"ses_12005f840ffe3UdMoswMamfi93","type":"step-finish","tokens":{"total":15333,"input":15256,"output":63},"cost":0}}`
		assertPassthrough(t, []byte(payload))
	})

	t.Run("step_start passthrough (non-tool event)", func(t *testing.T) {
		// step_start events mark the start of a step. No tool
		// data. Passthrough.
		const payload = `{"type":"step_start","timestamp":1781874168571,"sessionID":"ses_12005f840ffe3UdMoswMamfi93","part":{"id":"prt_edffa12f9001taHmXc9OUVjrbB","sessionID":"ses_12005f840ffe3UdMoswMamfi93","type":"step-start"}}`
		assertPassthrough(t, []byte(payload))
	})

	t.Run("unknown explicit type passthrough (non-tool event)", func(t *testing.T) {
		// An explicit type the parser does not recognise is rejected
		// even if it has a tool field. The parser refuses to guess.
		const payload = `{"type":"some_future_runtime_event","tool":"bash","input":{},"output":"x"}`
		assertPassthrough(t, []byte(payload))
	})

	t.Run("empty line passthrough", func(t *testing.T) {
		// A blank line is not a tool event. Passthrough.
		assertPassthrough(t, []byte(""))
	})

	t.Run("malformed JSON passthrough", func(t *testing.T) {
		// Not valid JSON. Passthrough (the wrapper writes the line as-is
		// to stdout and skips persistence).
		assertPassthrough(t, []byte(`{"type":"tool_use","part":`))
	})

	t.Run("OpenCode tool_use without part (rejected)", func(t *testing.T) {
		// top.type == "tool_use" but no "part" field. The OpenCode
		// branch can't extract a tool name without part; reject.
		const payload = `{"type":"tool_use","timestamp":1781874170009}`
		assertPassthrough(t, []byte(payload))
	})
}

// assertPassthrough asserts ParseEvent returns (nil, false) for a line
// that the parser must skip (passthrough signal — the wrapper forwards
// the line to stdout without persisting).
func assertPassthrough(t *testing.T, line []byte) {
	t.Helper()
	tc, ok := ParseEvent(line)
	if ok {
		t.Errorf("ParseEvent returned ok=true (tc=%+v); want false (passthrough)", tc)
	}
	if tc != nil {
		t.Errorf("ParseEvent returned tc=%+v; want nil (passthrough)", tc)
	}
}

// TestParseEvent_AbsentVsZero verifies the cost pointer discipline at the
// parser boundary. A field that is absent in the source event must
// produce a nil pointer in the returned *db.ToolCall (round-trips to
// SQL NULL / JSON null), and a field that is present with a zero value
// must produce a non-nil pointer to zero (round-trips to SQL 0 /
// JSON 0). The convention matches the db side and preserves the
// reconstructability requirement from the cost-mechanics decision.
func TestParseEvent_AbsentVsZero(t *testing.T) {
	t.Run("OpenCode tool_use: absent cost fields stay nil", func(t *testing.T) {
		// Real OpenCode 1.17.8 tool_use events have no cost fields
		// (tokens/cost/model are on step_finish). All cost pointers
		// must be nil.
		const payload = `{"type":"tool_use","part":{"type":"tool","tool":"bash","state":{"input":{},"output":"x"}}}`
		tc, ok := ParseEvent([]byte(payload))
		if !ok {
			t.Fatalf("ParseEvent returned (nil, false); want populated tool_call")
		}
		if tc.TokensIn != nil {
			t.Errorf("TokensIn = %v, want nil", *tc.TokensIn)
		}
		if tc.TokensOut != nil {
			t.Errorf("TokensOut = %v, want nil", *tc.TokensOut)
		}
		if tc.Cost != nil {
			t.Errorf("Cost = %v, want nil", *tc.Cost)
		}
		if tc.LatencyMs != nil {
			t.Errorf("LatencyMs = %v, want nil (no state.time in source)", *tc.LatencyMs)
		}
		if tc.Model != nil {
			t.Errorf("Model = %v, want nil", *tc.Model)
		}
		// Provider is the OpenCode default — non-nil.
		if tc.Provider == nil || *tc.Provider != "opencode" {
			t.Errorf("Provider = %v, want pointer to %q (OpenCode default)", tc.Provider, "opencode")
		}
	})

	t.Run("flat shape: zero-valued cost fields stay non-nil pointers", func(t *testing.T) {
		// tokens_in=0, tokens_out=0, cost=0 on a flat event. The
		// parser must produce non-nil pointers to 0 (NOT nil), so the
		// caller can distinguish "field present, value zero" from
		// "field absent". This is the round-trip contract the db
		// layer relies on.
		const payload = `{"tool":"bash","input":{},"output":"x","tokens_in":0,"tokens_out":0,"cost":0}`
		tc, ok := ParseEvent([]byte(payload))
		if !ok {
			t.Fatalf("ParseEvent returned (nil, false); want populated tool_call")
		}
		if tc.TokensIn == nil {
			t.Errorf("TokensIn = nil; want pointer to 0 (zero is a real value, not absent)")
		} else if *tc.TokensIn != 0 {
			t.Errorf("TokensIn = %d; want 0", *tc.TokensIn)
		}
		if tc.TokensOut == nil {
			t.Errorf("TokensOut = nil; want pointer to 0")
		} else if *tc.TokensOut != 0 {
			t.Errorf("TokensOut = %d; want 0", *tc.TokensOut)
		}
		if tc.Cost == nil {
			t.Errorf("Cost = nil; want pointer to 0")
		} else if *tc.Cost != 0 {
			t.Errorf("Cost = %f; want 0", *tc.Cost)
		}
	})
}

// TestParseEvent_ContentHash is a regression check that the parser
// populates ContentHash from Output — every persisted row
// must carry the SHA-256 of its Output. The db layer uses this hash
// to detect duplicate writes; if the parser skips it, the db layer's
// duplicate detection breaks silently.
func TestParseEvent_ContentHash(t *testing.T) {
	const payload = `{"type":"tool_use","part":{"type":"tool","tool":"bash","state":{"input":{},"output":"hello\n"}}}`
	tc, ok := ParseEvent([]byte(payload))
	if !ok {
		t.Fatalf("ParseEvent returned (nil, false); want populated tool_call")
	}
	want := dbpkg.ContentHash("hello\n")
	if tc.ContentHash != want {
		t.Errorf("ContentHash = %q, want %q", tc.ContentHash, want)
	}
}

// TestParseEvent_TimestampNormalisation exercises the seconds/ms
// disambiguation boundary and the string-passthrough path. A regression
// here would corrupt the persisted timestamp column and break the
// session-tool-call temporal ordering (ktx get, ktx list sorts).
//
// The parser's `_ = json.Unmarshal(top["timestamp"], &ev.Timestamp)`
// only handles string-typed values; a bare JSON number is silently
// dropped to time.Now(). These tests use string-typed timestamps so
// normaliseTimestamp runs.
func TestParseEvent_TimestampNormalisation(t *testing.T) {
	t.Run("string-encoded Unix seconds is normalised", func(t *testing.T) {
		const payload = `{"tool":"bash","input":{},"output":"x","timestamp":"1700000000"}`
		tc, ok := ParseEvent([]byte(payload))
		if !ok {
			t.Fatalf("ParseEvent returned (nil, false)")
		}
		// 1700000000 seconds = 2023-11-14T22:13:20Z.
		if tc.Timestamp != "2023-11-14T22:13:20Z" {
			t.Errorf("Timestamp = %q, want %q", tc.Timestamp, "2023-11-14T22:13:20Z")
		}
	})
	t.Run("string-encoded Unix milliseconds is normalised", func(t *testing.T) {
		const payload = `{"tool":"bash","input":{},"output":"x","timestamp":"1700000000123"}`
		tc, ok := ParseEvent([]byte(payload))
		if !ok {
			t.Fatalf("ParseEvent returned (nil, false)")
		}
		// 1700000000123 ms = 2023-11-14T22:13:20.123Z.
		if tc.Timestamp != "2023-11-14T22:13:20.123Z" {
			t.Errorf("Timestamp = %q, want %q", tc.Timestamp, "2023-11-14T22:13:20.123Z")
		}
	})
	t.Run("ISO-8601 string passes through unchanged", func(t *testing.T) {
		const payload = `{"tool":"bash","input":{},"output":"x","timestamp":"2026-06-18T12:00:00.000Z"}`
		tc, ok := ParseEvent([]byte(payload))
		if !ok {
			t.Fatalf("ParseEvent returned (nil, false)")
		}
		if tc.Timestamp != "2026-06-18T12:00:00.000Z" {
			t.Errorf("Timestamp = %q, want unchanged ISO-8601", tc.Timestamp)
		}
	})
	t.Run("missing timestamp falls back to current time (RFC3339Nano)", func(t *testing.T) {
		// No timestamp field. The parser must still produce a valid
		// RFC3339Nano string (buildToolCall falls back to time.Now()
		// when the parsed event's Timestamp is empty). We assert
		// non-empty and parseable rather than a specific value to
		// avoid clock-skew flakes.
		const payload = `{"tool":"bash","input":{},"output":"x"}`
		tc, ok := ParseEvent([]byte(payload))
		if !ok {
			t.Fatalf("ParseEvent returned (nil, false)")
		}
		if tc.Timestamp == "" {
			t.Fatalf("Timestamp = empty; want RFC3339Nano fallback")
		}
		var raw json.RawMessage
		if err := json.Unmarshal([]byte(`"`+tc.Timestamp+`"`), &raw); err != nil {
			t.Errorf("Timestamp %q is not a valid JSON string: %v", tc.Timestamp, err)
		}
	})
}

// TestParseEvent_FailureSignal covers the OpenCode metadata.exit
// shape and the flat-fallback defensive reads + derivation.
func TestParseEvent_FailureSignal(t *testing.T) {
	openCodeCases := []struct {
		name       string
		payload    string
		wantExit   *int64
		wantError  *bool
		wantOutput string
	}{
		{
			name:     "terminal error overrides zero exit and retains runtime error",
			payload:  `{"type":"tool_use","part":{"type":"tool","tool":"bash","state":{"status":"error","input":{},"output":"","error":"command failed","metadata":{"exit":0}}}}`,
			wantExit: ptrInt64(0), wantError: ptrBool(true), wantOutput: "command failed",
		},
		{
			name:     "terminal error retains nonzero exit",
			payload:  `{"type":"tool_use","part":{"type":"tool","tool":"bash","state":{"status":"error","input":{},"error":"exit status 7","metadata":{"exit":7}}}}`,
			wantExit: ptrInt64(7), wantError: ptrBool(true), wantOutput: "exit status 7",
		},
		{
			name:     "completed nonzero shell exit is failure",
			payload:  `{"type":"tool_use","part":{"type":"tool","tool":"bash","state":{"status":"completed","input":{},"output":"failed\n","metadata":{"exit":7}}}}`,
			wantExit: ptrInt64(7), wantError: ptrBool(true), wantOutput: "failed\n",
		},
		{
			name:      "completed without exit is success",
			payload:   `{"type":"tool_use","part":{"type":"tool","tool":"bash","state":{"status":"completed","input":{},"output":"ok\n"}}}`,
			wantError: ptrBool(false), wantOutput: "ok\n",
		},
		{
			name:     "shell exit without terminal status decides outcome",
			payload:  `{"type":"tool_use","part":{"type":"tool","tool":"bash","state":{"input":{},"output":"ok\n","metadata":{"exit":0}}}}`,
			wantExit: ptrInt64(0), wantError: ptrBool(false), wantOutput: "ok\n",
		},
		{
			name:       "malformed shell exit without terminal status is unknown",
			payload:    `{"type":"tool_use","part":{"type":"tool","tool":"bash","state":{"input":{},"output":"unknown\n","metadata":{"exit":"7"}}}}`,
			wantOutput: "unknown\n",
		},
		{
			name:       "arbitrary tool exit without terminal status is unknown",
			payload:    `{"type":"tool_use","part":{"type":"tool","tool":"read","state":{"input":{},"output":"contents","metadata":{"exit":1}}}}`,
			wantOutput: "contents",
		},
	}
	for _, testCase := range openCodeCases {
		t.Run(testCase.name, func(t *testing.T) {
			tc, ok := ParseEvent([]byte(testCase.payload))
			if !ok {
				t.Fatalf("ParseEvent returned (nil, false)")
			}
			assertInt64Ptr(t, "ExitCode", tc.ExitCode, testCase.wantExit)
			assertBoolPtr(t, "IsError", tc.IsError, testCase.wantError)
			if tc.Output != testCase.wantOutput {
				t.Errorf("Output = %q, want %q", tc.Output, testCase.wantOutput)
			}
		})
	}

	t.Run("flat exit_code=2 → ExitCode=&2 IsError=&true", func(t *testing.T) {
		const payload = `{"tool":"bash","input":"x","output":"y","exit_code":2}`
		tc, ok := ParseEvent([]byte(payload))
		if !ok {
			t.Fatalf("ParseEvent returned (nil, false)")
		}
		if tc.ExitCode == nil || *tc.ExitCode != 2 {
			t.Errorf("ExitCode = %v, want pointer to 2", tc.ExitCode)
		}
		if tc.IsError == nil || *tc.IsError != true {
			t.Errorf("IsError = %v, want pointer to true", tc.IsError)
		}
	})

	t.Run("flat is_error=true no exit_code → IsError=&true ExitCode=nil", func(t *testing.T) {
		const payload = `{"tool":"bash","input":"x","output":"y","is_error":true}`
		tc, ok := ParseEvent([]byte(payload))
		if !ok {
			t.Fatalf("ParseEvent returned (nil, false)")
		}
		if tc.IsError == nil || *tc.IsError != true {
			t.Errorf("IsError = %v, want pointer to true", tc.IsError)
		}
		if tc.ExitCode != nil {
			t.Errorf("ExitCode = %v, want nil", tc.ExitCode)
		}
	})

	t.Run("flat neither field → both nil", func(t *testing.T) {
		const payload = `{"tool":"bash","input":"x","output":"y"}`
		tc, ok := ParseEvent([]byte(payload))
		if !ok {
			t.Fatalf("ParseEvent returned (nil, false)")
		}
		if tc.ExitCode != nil || tc.IsError != nil {
			t.Errorf("ExitCode=%v IsError=%v, want nil nil", tc.ExitCode, tc.IsError)
		}
	})
}

func ptrInt64(value int64) *int64 { return &value }
func ptrBool(value bool) *bool    { return &value }

func assertInt64Ptr(t *testing.T, name string, got, want *int64) {
	t.Helper()
	if (got == nil) != (want == nil) || got != nil && *got != *want {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func assertBoolPtr(t *testing.T, name string, got, want *bool) {
	t.Helper()
	if (got == nil) != (want == nil) || got != nil && *got != *want {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}
