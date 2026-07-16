// Package usage parses recorded OpenCode step_finish usage evidence
// from a JSONL stream and aggregates it under one shared
// interpretation: a per-field SUM across step_finish events, an
// absent-versus-zero distinction (an explicit JSON null or a missing
// field is absent, not zero), and a completeness gate that exposes an
// aggregate only when every step_finish event reported the field.
//
// Only usage evidence is interpreted here — token, cost, cache, and
// reasoning totals and their per-field event counts. Answer scoring
// and excerpts, tool-call counting, stream-complete detection, and
// model or agent metadata are intentionally left to the callers that
// need them, so this package forces no unrelated concerns on its
// consumers.
//
// Integer fields (tokens input/output, cache read/write, reasoning)
// are parsed as exact JSON integers via json.Number — never as
// float64, which loses precision above 2^53. A value that is
// fractional, out of int64 range, or would lose precision when
// narrowed to float64 (the experiment path's output type) is rejected
// from completeness for that event. Exact sums are accumulated without
// int64 overflow and are exposed only while they remain within the same
// float64 boundary. Cost is a float64 field and accepts any JSON number.
//
// Only OpenCode JSONL is supported; no runtime-format dispatch is
// performed.
package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"math/big"
)

// MaxRecordSize is the maximum number of content bytes (excluding the
// trailing newline) of a single JSONL record accepted by the parser.
// A record whose content is at most MaxRecordSize bytes is read in
// full; a longer record is skipped. Both consuming paths use this
// limit so a record that is larger than 4 MiB but no larger than
// 64 MiB is accepted consistently, and an exactly-64-MiB record is
// accepted at the boundary.
const MaxRecordSize = 64 * 1024 * 1024 // 64 MiB

// maxExactInt is the largest integer whose float64 conversion is
// exact. Integer evidence at or below this magnitude round-trips
// losslessly through the experiment path's *float64 output type and
// the CLI path's *int64 output type, so the two paths report
// equivalent values for every accepted field. A value above this
// threshold is rejected from completeness rather than reported as a
// precision-losing approximation.
const maxExactInt = 1 << 53

// Evidence is the aggregate step_finish usage evidence for one
// recorded stream.
//
// Each numeric pointer is non-nil — including a pointer to zero — only
// when every step_finish event reported that field; it is nil when the
// field was absent, null, fractional, out of range, or
// precision-losing on any event, when the aggregate overflows int64 or
// crosses the exact float64 integer boundary, or when the stream carried
// no step_finish events at all. An explicitly reported zero is a valid
// measurement and is preserved as a non-nil pointer to 0, never conflated
// with an absent value.
//
// Integer fields (TokensIn, TokensOut, CacheRead, CacheWrite,
// Reasoning) are *int64 — exact JSON integers within [-2^53, 2^53].
// Cost is *float64 — any JSON number. The experiment path narrows
// the *int64 fields to *float64 at its boundary (exact for every
// accepted value); the CLI path passes them through as *int64.
type Evidence struct {
	// HasData reports whether at least one step_finish event was
	// parsed. Callers use it to fall through to command-specific
	// fallback paths when the stream carries no step-level evidence.
	HasData bool

	// Steps is the number of step_finish events observed.
	Steps int

	TokensIn   *int64
	TokensOut  *int64
	Cost       *float64
	CacheRead  *int64
	CacheWrite *int64
	Reasoning  *int64

	// Event counters report how many step_finish events carried each
	// field. A counter equal to Steps marks complete per-event evidence;
	// the pointer can still be nil when the exact aggregate is outside the
	// shared int64/float64 range. A smaller counter (including zero) marks
	// the aggregate as nil by the completeness gate.
	TokensInEvents   int
	TokensOutEvents  int
	CostEvents       int
	CacheReadEvents  int
	CacheWriteEvents int
	ReasoningEvents  int
}

// Parse reads a JSONL stream from r and returns its aggregate
// step_finish usage evidence. Malformed lines and non-step_finish
// events are ignored. A single JSONL record may contain up to
// MaxRecordSize content bytes; the trailing newline is handled
// separately so an exactly-MaxRecordSize record is accepted.
//
// Parse is the convenience entry point for callers that need only
// usage evidence (no other event interpretation). Callers that must
// observe other event kinds in the same scan — answer text,
// tool_use counts, stream-complete markers, model metadata — drive an
// Aggregator inline via AddLine so they scan the stream exactly once.
func Parse(r io.Reader) Evidence {
	a := &Aggregator{}
	readDelimitedLines(r, MaxRecordSize, func(line []byte) bool {
		a.AddLine(line)
		return false
	})
	return a.Result()
}

// Aggregator accumulates step_finish usage evidence across lines of a
// recorded JSONL stream. It is the single interpretation of token,
// cost, cache, and reasoning aggregation and their completeness
// rules; live benchmark runs and experiments share it.
//
// The zero value is ready to use. Call AddLine for each JSONL line,
// then Result once to obtain the aggregate. Lines fed after Result is
// called are still accumulated but the previously returned Evidence is
// not retroactively updated.
type Aggregator struct {
	steps             int
	tokensIn          big.Int
	tokensOut         big.Int
	cost              float64
	cacheRead         big.Int
	cacheWrite        big.Int
	reasoning         big.Int
	tokensInPresent   int
	tokensOutPresent  int
	costPresent       int
	cacheReadPresent  int
	cacheWritePresent int
	reasoningPresent  int
	hasData           bool
}

// AddLine processes one JSONL line and reports whether it was a
// structurally valid step_finish event. Empty lines, malformed JSON,
// non-step_finish events, and step_finish events whose typed usage
// fields have incompatible JSON types are skipped. Numeric fields may
// still be absent, null, fractional, or outside the shared integer
// range; those events remain structurally valid but the affected field
// fails completeness.
func (a *Aggregator) AddLine(line []byte) bool {
	if len(line) == 0 {
		return false
	}
	var event struct {
		Type string          `json:"type"`
		Part json.RawMessage `json:"part"`
	}
	if json.Unmarshal(line, &event) != nil {
		return false
	}
	if event.Type != "step_finish" {
		return false
	}
	partJSON := event.Part
	if len(partJSON) == 0 {
		// A missing part decoded into the experiment parser's zero-value
		// struct before usage parsing was shared. Preserve that behavior:
		// it is a valid step with absent usage fields.
		partJSON = []byte("{}")
	}
	if !validStepFinishUsage(partJSON) {
		return false
	}
	var part struct {
		Cost   json.RawMessage `json:"cost"`
		Tokens json.RawMessage `json:"tokens"`
	}
	if json.Unmarshal(partJSON, &part) != nil {
		return false
	}
	a.hasData = true
	a.steps++

	var tokens struct {
		Input     json.RawMessage `json:"input"`
		Output    json.RawMessage `json:"output"`
		Reasoning json.RawMessage `json:"reasoning"`
		Cache     struct {
			Read  json.RawMessage `json:"read"`
			Write json.RawMessage `json:"write"`
		} `json:"cache"`
	}
	// An absent or null tokens block leaves every token field absent for
	// this otherwise-valid event. Incompatible JSON types were rejected
	// by the structural gate above.
	_ = json.Unmarshal(part.Tokens, &tokens)
	if v := integerOrNull(tokens.Input); v != nil {
		addInt64(&a.tokensIn, *v)
		a.tokensInPresent++
	}
	if v := integerOrNull(tokens.Output); v != nil {
		addInt64(&a.tokensOut, *v)
		a.tokensOutPresent++
	}
	if v := integerOrNull(tokens.Reasoning); v != nil {
		addInt64(&a.reasoning, *v)
		a.reasoningPresent++
	}
	if v := integerOrNull(tokens.Cache.Read); v != nil {
		addInt64(&a.cacheRead, *v)
		a.cacheReadPresent++
	}
	if v := integerOrNull(tokens.Cache.Write); v != nil {
		addInt64(&a.cacheWrite, *v)
		a.cacheWritePresent++
	}
	if v := numberOrNull(part.Cost); v != nil {
		a.cost += *v
		a.costPresent++
	}
	return true
}

// validStepFinishUsage applies the minimal structural contract inherited
// from the experiment parser: when present, cost and token leaf fields
// must be JSON numbers or null, while tokens and cache must be objects or
// null. Value-level integer rules are applied separately by integerOrNull.
func validStepFinishUsage(part json.RawMessage) bool {
	var typed struct {
		Cost   *float64 `json:"cost"`
		Tokens struct {
			Input     *float64 `json:"input"`
			Output    *float64 `json:"output"`
			Reasoning *float64 `json:"reasoning"`
			Cache     struct {
				Read  *float64 `json:"read"`
				Write *float64 `json:"write"`
			} `json:"cache"`
		} `json:"tokens"`
	}
	return json.Unmarshal(part, &typed) == nil
}

func addInt64(sum *big.Int, value int64) {
	var term big.Int
	term.SetInt64(value)
	sum.Add(sum, &term)
}

// Result finalizes the aggregate, applying the completeness gate. A
// field's pointer is set only when it was present on every step_finish
// event; otherwise the field is omitted (nil) so partial totals are
// never reported as complete evidence.
func (a *Aggregator) Result() Evidence {
	e := Evidence{
		HasData:          a.hasData,
		Steps:            a.steps,
		TokensInEvents:   a.tokensInPresent,
		TokensOutEvents:  a.tokensOutPresent,
		CostEvents:       a.costPresent,
		CacheReadEvents:  a.cacheReadPresent,
		CacheWriteEvents: a.cacheWritePresent,
		ReasoningEvents:  a.reasoningPresent,
	}
	e.TokensIn = exactAggregate(&a.tokensIn, a.tokensInPresent, a.steps)
	e.TokensOut = exactAggregate(&a.tokensOut, a.tokensOutPresent, a.steps)
	if a.steps > 0 && a.costPresent == a.steps {
		v := a.cost
		e.Cost = &v
	}
	e.CacheRead = exactAggregate(&a.cacheRead, a.cacheReadPresent, a.steps)
	e.CacheWrite = exactAggregate(&a.cacheWrite, a.cacheWritePresent, a.steps)
	e.Reasoning = exactAggregate(&a.reasoning, a.reasoningPresent, a.steps)
	return e
}

// exactAggregate exposes a complete integer total only when its exact
// mathematical sum fits int64 and remains within the shared float64 exact
// integer boundary. big.Int prevents intermediate int64 wraparound, including
// when large positive and negative values later cancel.
func exactAggregate(sum *big.Int, present, steps int) *int64 {
	if steps == 0 || present != steps || !sum.IsInt64() {
		return nil
	}
	v := sum.Int64()
	if v > maxExactInt || v < -maxExactInt {
		return nil
	}
	return &v
}

// integerOrNull parses a JSON number into an exact *int64. Returns nil
// for an absent field, an explicit JSON null, a non-number JSON type
// (string, bool, object, array), a fractional value, a value out of
// int64 range, or a value whose magnitude exceeds 2^53 (which would
// lose precision when narrowed to float64). The caller treats nil as
// "field absent for this event" so an emitted zero is preserved as a
// non-nil pointer to 0 while a null, missing, fractional, or
// precision-losing field stays nil.
//
// json.Number is used to inspect the raw token without floating-point
// conversion. Int64() rejects fractional values and int64 overflow;
// the 2^53 gate rejects values that would lose precision as float64.
func integerOrNull(raw json.RawMessage) *int64 {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err != nil {
		return nil
	}
	n, err := num.Int64()
	if err != nil {
		return nil
	}
	if n > maxExactInt || n < -maxExactInt {
		return nil
	}
	return &n
}

// numberOrNull parses a JSON number field into *float64 for the cost
// field. Returns nil for an absent field, an explicit JSON null, empty
// input, or a non-numeric value — the caller treats nil as "field
// absent for this event" so an emitted zero is preserved as a non-nil
// pointer to 0 while a null or missing field stays nil. Integer and
// floating-point JSON numbers are both accepted.
func numberOrNull(raw json.RawMessage) *float64 {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return &f
	}
	return nil
}

// ReadDelimitedLines reads newline-delimited lines from r. Each line
// (content excluding the trailing newline) may be up to maxLen bytes;
// a longer line is skipped without error. The callback receives each
// valid line; returning true from the callback stops iteration.
//
// This uses bufio.Reader, whose ReadLine handles lines longer than
// the read buffer via isPrefix chunking, so an exactly maxLen record
// followed by a newline is accepted at the exact boundary (a
// bufio.Scanner's maxTokenSize must reserve one byte for the
// delimiter and cannot). Callers that need to observe other event
// kinds in the same pass drive an Aggregator inline through this
// reader so the stream is read exactly once.
func ReadDelimitedLines(r io.Reader, maxLen int, cb func(line []byte) bool) {
	readDelimitedLines(r, maxLen, cb)
}

// readDelimitedLines is the unexported core; see ReadDelimitedLines.
func readDelimitedLines(r io.Reader, maxLen int, cb func([]byte) bool) {
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, ok, stop := readOneLine(br, maxLen)
		if stop {
			return
		}
		if !ok {
			continue
		}
		if cb(line) {
			return
		}
	}
}

// readOneLine reads one newline-terminated line (content without \n).
// At EOF, if a partial line without a trailing newline was accumulated,
// it is returned as a valid line (the common no-final-newline case).
// Oversized lines are consumed and discarded. Returns (line, true,
// false) for a valid line, (nil, false, false) for a skipped oversized
// line, and (nil, false, true) to stop (EOF or read error with no line).
func readOneLine(br *bufio.Reader, maxLen int) (line []byte, ok bool, stop bool) {
	chunk, isPrefix, err := br.ReadLine()
	if err != nil {
		return nil, false, true
	}
	var buf []byte
	buf = append(buf, chunk...)
	for isPrefix {
		if len(buf) > maxLen {
			// Oversized: consume the remainder of this line
			// without accumulating further, then signal skip.
			for isPrefix {
				_, isPrefix, err = br.ReadLine()
				if err != nil {
					return nil, false, true
				}
			}
			return nil, false, false
		}
		chunk, isPrefix, err = br.ReadLine()
		if err != nil {
			// EOF while accumulating a long line with no
			// trailing newline. The buffer holds the complete
			// content; fall through to the size check below.
			break
		}
		buf = append(buf, chunk...)
	}
	if len(buf) > maxLen {
		return nil, false, false
	}
	return buf, true, false
}
