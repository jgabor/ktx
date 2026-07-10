// Package parser converts agent-runtime event bytes into ktx tool_call
// records. It is the single source of truth for the ktx tool_call shape:
// both the wrapper and hook paths
// use ParseEvent.
//
// The package is pure: no I/O, no database, no global state. Tests
// exercise it directly with byte slices.
package parser

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	dbpkg "github.com/jgabor/ktx/internal/db"
)

// ParseEvent returns the tool_call encoded in line, or (nil, false) when
// line is not a tool event (text, step lifecycle, etc.). The caller
// forwards the result to db.PersistToolCall when ok=true; passthrough
// otherwise.
//
// line is a single JSON object terminated by a newline. The function
// tolerates trailing whitespace and a missing trailing newline.
//
// The returned *db.ToolCall is fully populated except for SessionID,
// which the caller fills in (the parser does not know which session
// the event belongs to). ID is a fresh 16-char nanoid, ContentHash
// is the SHA-256 of Output, and Timestamp is normalised to
// RFC3339Nano. The cost pointer fields (TokensIn, TokensOut, Cost,
// LatencyMs, Model, Provider) round-trip a nil source field to a nil
// pointer (SQL NULL / JSON null) and a non-nil source value to a
// non-nil pointer (SQL value / JSON value), preserving the
// absent-vs-zero distinction at the db boundary.
func ParseEvent(line []byte) (*dbpkg.ToolCall, bool) {
	ev, ok := parseToolEvent(line)
	if !ok {
		return nil, false
	}
	tc, err := buildToolCall(ev)
	if err != nil {
		// NewID can only fail on gonanoid's internal entropy source
		// (effectively impossible). Surface as a non-tool event so the
		// caller's persist loop continues without recording — better
		// than crashing the run.
		return nil, false
	}
	return tc, true
}

// buildToolCall materialises a parsed event into the db's ToolCall
// shape. ID is always generated fresh — 16-char lowercase alpha
// nanoids are mandated, and runtime-supplied IDs (e.g., OpenCode's
// callID) do not necessarily match. Cross-referencing runtime IDs
// against ktx records is a future enhancement.
//
// content_hash is derived from output. timestamp is normalised to
// RFC3339Nano so downstream readers do not need to disambiguate
// milliseconds vs seconds vs ISO strings.
//
// The cost fields (TokensIn, TokensOut, Cost, LatencyMs, Model, Provider)
// are pointer-copied from the parsed event: a nil pointer round-trips
// to SQL NULL / JSON null (field absent in source), a non-nil pointer
// to zero round-trips to SQL 0 / JSON 0 (source emitted zero). The
// convention matches the dbpkg.ToolCall shape and preserves
// the reconstructability requirement from the cost-mechanics decision:
// 'absent' is not conflated with 'zero'.
func buildToolCall(ev *parsedEvent) (*dbpkg.ToolCall, error) {
	id, err := dbpkg.NewID()
	if err != nil {
		return nil, fmt.Errorf("generate id: %w", err)
	}
	ts := ev.Timestamp
	if ts == "" {
		ts = time.Now().UTC().Format(time.RFC3339Nano)
	} else {
		ts = normaliseTimestamp(ts)
	}
	return &dbpkg.ToolCall{
		ID:          id,
		Tool:        ev.Tool,
		Input:       ev.Input,
		Output:      ev.Output,
		ContentHash: dbpkg.ContentHash(ev.Output),
		Timestamp:   ts,
		EventType:   "tool_call",
		TokensIn:    ev.TokensIn,
		TokensOut:   ev.TokensOut,
		Cost:        ev.Cost,
		LatencyMs:   ev.LatencyMs,
		Model:       ev.Model,
		Provider:    ev.Provider,
		ExitCode:    ev.ExitCode,
		IsError:     ev.IsError,
	}, nil
}

// parsedEvent is the result of a successful parseToolEvent.
//
// The cost fields (TokensIn, TokensOut, Cost, LatencyMs, Model, Provider)
// use pointer types so the absent-vs-zero distinction survives into
// buildToolCall: nil = "field absent in the source event" (round-trips
// to SQL NULL and JSON null); a non-nil pointer to 0 = "source emitted
// the field with value zero" (round-trips to SQL 0 and JSON 0).
//
// This mirrors the persistence-side convention in dbpkg.ToolCall:
// the parse layer hands buildToolCall pointers that the struct
// boundary copies straight onto the db shape. A missing cost field
// in the source event is NOT a parse failure — the wrapper records
// the tool call with NULL cost columns and moves on.
type parsedEvent struct {
	ID        string
	Tool      string
	Input     string
	Output    string
	Timestamp string

	// Cost-publication fields. nil = absent in the source event.
	// Populated by parseToolEvent when the event carries the field;
	// buildToolCall copies the pointer through to the persisted row.
	TokensIn  *int64
	TokensOut *int64
	Cost      *float64
	LatencyMs *int64
	Model     *string
	Provider  *string

	// Failure-signal fields. nil = absent in the source event.
	ExitCode *int64
	IsError  *bool
}

// parseToolEvent returns (event, true) if line looks like a tool call,
// (nil, false) otherwise. Malformed JSON or non-tool events return
// (nil, false) so the caller can passthrough unchanged.
//
// Two schemas are recognised:
//
//  1. OpenCode: top.type == "tool_use" and part.type == "tool". The
//     tool name, input, and output live in part.state. Cost extraction
//     from this shape:
//     - latency_ms: computed from part.state.time.end - .start when
//     both are present (OpenCode always emits both on a completed
//     state). If only one is present, latency_ms stays nil — a
//     missing field is not a parse failure.
//     - provider: defaulted to "opencode" (runtime-known single
//     exception; the wrapper knows its agent). Documented as the
//     one place "don't guess" yields to "wrapper knows".
//     - tokens_in / tokens_out / cost / model: the OpenCode runtime
//     does NOT emit these on tool_use events; they live on the
//     separate step_finish event (a step-aggregate, not per-tool).
//     The parser defensively reads part.state.metadata and the
//     top-level event for these fields so a future OpenCode shape
//     or a custom adapter that does emit them would round-trip.
//     Real OpenCode 1.17.8 events leave these nil.
//
//  2. Flat fallback: a top-level object with tool/name and (input|args)
//     and (output|result) fields. Optional id/timestamp/time aliases.
//     Cost fields are read defensively from the top level so a runtime
//     that emits them flat (no nesting) would also round-trip.
//
// Unknown event types ("text", "step_start", "step_finish", etc.) are
// treated as non-tool events and skipped without error.
func parseToolEvent(line []byte) (*parsedEvent, bool) {
	if len(line) == 0 {
		return nil, false
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(line, &top); err != nil {
		return nil, false
	}
	if len(top) == 0 {
		return nil, false
	}

	var eventType string
	_ = json.Unmarshal(top["type"], &eventType)

	// OpenCode schema: tool_use events nest the tool details under
	// "part". Only events with part.type == "tool" are tool calls.
	if eventType == "tool_use" {
		var part map[string]json.RawMessage
		if err := json.Unmarshal(top["part"], &part); err != nil {
			return nil, false
		}
		var partType string
		_ = json.Unmarshal(part["type"], &partType)
		if partType != "tool" {
			return nil, false
		}
		ev := &parsedEvent{}
		_ = json.Unmarshal(part["tool"], &ev.Tool)
		_ = json.Unmarshal(part["callID"], &ev.ID)
		if ev.ID == "" {
			_ = json.Unmarshal(part["id"], &ev.ID)
		}
		var state map[string]json.RawMessage
		if err := json.Unmarshal(part["state"], &state); err == nil {
			ev.Input = jsonValueToString(state["input"])
			ev.Output = jsonValueToString(state["output"])
			ev.LatencyMs = latencyFromState(state["time"])
			// Defensive cost reads inside state.metadata. OpenCode's
			// bash/edit/etc. tools populate state.metadata with tool-
			// specific output (output, exit, description, truncated)
			// but not with tokens/cost/model — those live on
			// step_finish events. The reads below would surface them
			// if a future runtime or custom adapter adds them.
			var md map[string]json.RawMessage
			if err := json.Unmarshal(state["metadata"], &md); err == nil {
				ev.TokensIn = int64PtrFromRaw(md["tokens_in"], md["tokensIn"], md["input_tokens"])
				ev.TokensOut = int64PtrFromRaw(md["tokens_out"], md["tokensOut"], md["output_tokens"])
				ev.Cost = float64PtrFromRaw(md["cost"], md["cost_usd"])
				ev.Model = stringPtrFromRaw(md["model"], md["model_id"])
				ev.Provider = stringPtrFromRaw(md["provider"], md["provider_id"])
				ev.ExitCode = int64PtrFromRaw(md["exit"])
				ev.IsError = isErrorFromExitCode(ev.ExitCode)
			}
		}
		_ = json.Unmarshal(top["timestamp"], &ev.Timestamp)
		// Runtime-known default for the OpenCode adapter: the wrapper
		// knows its agent. If the event later carries a provider field
		// (none today), the defensive read above wins.
		if ev.Provider == nil {
			s := "opencode"
			ev.Provider = &s
		}
		if ev.Tool == "" {
			return nil, false
		}
		return ev, true
	}

	// Flat fallback. Treat any JSON object with a tool/name field as a
	// tool event; recognise optional id/input/args/output/result/time
	// aliases. Known non-tool event types ("text", "step_start",
	// "step_finish") are rejected so non-tool noise does not get
	// recorded.
	switch eventType {
	case "text", "step_start", "step_finish", "":
		// "" allowed: not every runtime emits a type field.
	default:
		// Unknown explicit type — refuse to guess.
		return nil, false
	}

	var tool string
	_ = json.Unmarshal(top["tool"], &tool)
	if tool == "" {
		_ = json.Unmarshal(top["name"], &tool)
	}
	if tool == "" {
		return nil, false
	}

	ev := &parsedEvent{Tool: tool}
	_ = json.Unmarshal(top["id"], &ev.ID)
	ev.Input = jsonValueToString(firstNonEmptyRaw(top["input"], top["args"], top["parameters"]))
	ev.Output = jsonValueToString(firstNonEmptyRaw(top["output"], top["result"], top["content"]))
	_ = json.Unmarshal(top["timestamp"], &ev.Timestamp)
	if ev.Timestamp == "" {
		_ = json.Unmarshal(top["time"], &ev.Timestamp)
	}
	// Defensive cost reads on the flat shape so a runtime that emits
	// the cost fields at the top level (no nesting) round-trips through
	// the wrapper. Real OpenCode events reach the OpenCode branch above
	// and never exercise this path; flat-fallback runtimes that do
	// not emit cost fields leave the pointers nil.
	ev.LatencyMs = latencyFromState(firstNonEmptyRaw(top["time"], top["duration"]))
	ev.TokensIn = int64PtrFromRaw(top["tokens_in"], top["tokensIn"], top["input_tokens"])
	ev.TokensOut = int64PtrFromRaw(top["tokens_out"], top["tokensOut"], top["output_tokens"])
	ev.Cost = float64PtrFromRaw(top["cost"], top["cost_usd"])
	ev.Model = stringPtrFromRaw(top["model"], top["model_id"])
	ev.Provider = stringPtrFromRaw(top["provider"], top["provider_id"])
	ev.ExitCode = int64PtrFromRaw(top["exit_code"], top["exit"], top["code"])
	ev.IsError = boolPtrFromRaw(top["is_error"], top["error"])
	if ev.IsError == nil && ev.ExitCode != nil {
		ev.IsError = isErrorFromExitCode(ev.ExitCode)
	}
	return ev, true
}

// firstNonEmptyRaw returns the first non-empty RawMessage in the list.
func firstNonEmptyRaw(values ...json.RawMessage) json.RawMessage {
	for _, v := range values {
		if len(v) > 0 {
			return v
		}
	}
	return nil
}

// latencyFromState returns the millisecond delta between state.time.end
// and state.time.start when both are present and numeric. A missing
// start or end, a non-numeric value, or an end-before-start returns nil
// — the wrapper records the tool_call with NULL latency_ms rather than
// guess. OpenCode's tool_use event always emits both fields on a
// completed state, so a successful extraction is the common case for
// real OpenCode events; the nil return is reserved for malformed or
// partial inputs.
func latencyFromState(raw json.RawMessage) *int64 {
	if len(raw) == 0 {
		return nil
	}
	var tm struct {
		Start int64 `json:"start"`
		End   int64 `json:"end"`
	}
	if err := json.Unmarshal(raw, &tm); err != nil {
		return nil
	}
	if tm.Start == 0 || tm.End == 0 || tm.End < tm.Start {
		return nil
	}
	delta := tm.End - tm.Start
	return &delta
}

// int64PtrFromRaw returns the first non-empty RawMessage that parses as
// a JSON number, as a pointer to int64. Empty input, malformed JSON,
// or non-numeric content returns nil — the same 'missing field is not
// a parse failure' rule the wrapper applies to top-level cost fields.
func int64PtrFromRaw(values ...json.RawMessage) *int64 {
	for _, v := range values {
		if len(v) == 0 {
			continue
		}
		var n int64
		if err := json.Unmarshal(v, &n); err == nil {
			return &n
		}
		var f float64
		if err := json.Unmarshal(v, &f); err == nil {
			i := int64(f)
			return &i
		}
	}
	return nil
}

// float64PtrFromRaw mirrors int64PtrFromRaw for floating-point fields.
func float64PtrFromRaw(values ...json.RawMessage) *float64 {
	for _, v := range values {
		if len(v) == 0 {
			continue
		}
		var f float64
		if err := json.Unmarshal(v, &f); err == nil {
			return &f
		}
		var n int64
		if err := json.Unmarshal(v, &n); err == nil {
			val := float64(n)
			return &val
		}
	}
	return nil
}

// boolPtrFromRaw returns the first non-empty RawMessage that parses as
// a JSON boolean, as a pointer to bool. Empty input or non-boolean
// content returns nil — the same 'missing field is not a parse
// failure' rule as the other defensive reads.
func boolPtrFromRaw(values ...json.RawMessage) *bool {
	for _, v := range values {
		if len(v) == 0 {
			continue
		}
		var b bool
		if err := json.Unmarshal(v, &b); err == nil {
			return &b
		}
	}
	return nil
}

// isErrorFromExitCode derives is_error from exit_code at parse time
// (OpenCode mapping): present non-zero → &true, present
// zero → &false, absent exit_code → nil.
func isErrorFromExitCode(exitCode *int64) *bool {
	if exitCode == nil {
		return nil
	}
	b := *exitCode != 0
	return &b
}

// stringPtrFromRaw returns the first non-empty RawMessage that parses
// as a non-empty JSON string, as a pointer to string. Empty input,
// malformed JSON, missing values, or empty strings yield nil — the
// 'absent vs empty string' distinction matters at the db boundary
// (an empty string would round-trip as a non-NULL "" column rather
// than a NULL column).
func stringPtrFromRaw(values ...json.RawMessage) *string {
	for _, v := range values {
		if len(v) == 0 {
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err == nil && s != "" {
			return &s
		}
	}
	return nil
}

// jsonValueToString returns a string representation of a JSON value.
// Strings are returned unquoted; other types (objects, arrays, numbers,
// booleans) are re-encoded to JSON text. Empty input yields an empty
// string so callers do not have to guard against nil.
func jsonValueToString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var v any
	if err := json.Unmarshal(raw, &v); err == nil {
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	}
	return string(raw)
}

// normaliseTimestamp converts an integer Unix timestamp (seconds or
// milliseconds) to RFC3339Nano. Strings are returned as-is so the caller
// can supply an ISO-8601 timestamp directly. The seconds-vs-ms
// disambiguation uses magnitude: anything >= 1e10 is milliseconds
// (modern Unix ms is ~1.7e12, modern Unix seconds is ~1.7e9; the
// crossover at year ~2286 in seconds vs ~1970 in milliseconds makes
// this safe for any realistic runtime).
func normaliseTimestamp(ts string) string {
	if ts == "" {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	if n, err := strconv.ParseInt(ts, 10, 64); err == nil {
		if n >= 1e10 {
			return time.UnixMilli(n).UTC().Format(time.RFC3339Nano)
		}
		return time.Unix(n, 0).UTC().Format(time.RFC3339Nano)
	}
	return ts
}
