package pipeline

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

type jsonShape uint8

const (
	jsonSingle jsonShape = iota + 1
	jsonLines
	jsonWithTrailer
)

type recognizedJSON struct {
	shape    jsonShape
	faithful []byte
}

// recognizeJSON records a compact, semantically identical form of the input.
// It deliberately rejects blank records and adjacent JSON values that could be
// interpreted as either a stream or a text trailer.
func recognizeJSON(input []byte) (recognizedJSON, bool) {
	if len(input) == 0 || !utf8.Valid(input) {
		return recognizedJSON{}, false
	}
	if compact, records, ok := CompactJSON(input); ok {
		if records == 1 {
			return recognizedJSON{shape: jsonSingle, faithful: compact}, true
		}
		return recognizedJSON{shape: jsonLines, faithful: compact}, true
	}

	dec := json.NewDecoder(bytes.NewReader(input))
	var value json.RawMessage
	if dec.Decode(&value) != nil {
		return recognizedJSON{}, false
	}
	offset := dec.InputOffset()
	trailer := input[offset:]
	if len(trimJSONSpace(trailer)) == 0 {
		return recognizedJSON{}, false
	}

	// A second decodable value is an unsupported JSON stream, not a text
	// trailer. NDJSON was handled above where physical record boundaries are
	// unambiguous.
	second := json.NewDecoder(bytes.NewReader(trailer))
	if second.Decode(&value) == nil {
		return recognizedJSON{}, false
	}
	compact, ok := compactJSON(input[:offset])
	if !ok {
		return recognizedJSON{}, false
	}
	faithful := make([]byte, 0, len(compact)+len(trailer))
	faithful = append(faithful, compact...)
	faithful = append(faithful, trailer...)
	return recognizedJSON{shape: jsonWithTrailer, faithful: faithful}, true
}

func (original recognizedJSON) accepts(candidate []byte) bool {
	if len(candidate) == 0 || !utf8.Valid(candidate) {
		return false
	}
	switch original.shape {
	case jsonSingle:
		_, ok := compactJSON(candidate)
		return ok
	case jsonLines:
		if _, ok := compactJSON(candidate); ok {
			return true
		}
		_, records, ok := compactJSONLines(candidate)
		return ok && records > 0
	case jsonWithTrailer:
		dec := json.NewDecoder(bytes.NewReader(candidate))
		var value json.RawMessage
		return dec.Decode(&value) == nil
	default:
		return true
	}
}

func compactJSON(input []byte) ([]byte, bool) {
	var out bytes.Buffer
	if json.Compact(&out, input) != nil {
		return nil, false
	}
	return out.Bytes(), true
}

func compactJSONLines(input []byte) ([]byte, int, bool) {
	lines := bytes.Split(input, []byte{'\n'})
	finalNewline := len(lines) > 1 && len(lines[len(lines)-1]) == 0
	if finalNewline {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil, 0, false
	}

	var out bytes.Buffer
	for i, line := range lines {
		if len(line) == 0 {
			return nil, 0, false
		}
		compact, ok := compactJSON(line)
		if !ok {
			return nil, 0, false
		}
		if i > 0 {
			out.WriteByte('\n')
		}
		out.Write(compact)
	}
	if finalNewline {
		out.WriteByte('\n')
	}
	return out.Bytes(), len(lines), true
}

// CompactJSON compacts one complete JSON value or an NDJSON stream whose
// physical lines are all non-blank complete JSON values. It preserves an
// NDJSON stream's final newline and returns its record count.
func CompactJSON(input []byte) ([]byte, int, bool) {
	if len(input) == 0 || !utf8.Valid(input) {
		return nil, 0, false
	}
	if compact, ok := compactJSON(input); ok {
		return compact, 1, true
	}
	return compactJSONLines(input)
}

func trimJSONSpace(input []byte) []byte {
	return bytes.Trim(input, " \t\r\n")
}
