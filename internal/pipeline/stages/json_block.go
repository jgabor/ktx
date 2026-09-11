package stages

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

// JSONBlock compacts complete JSON objects and arrays on their own lines in
// mixed text. It preserves every byte outside each JSON value, including its
// indentation and trailing newline. JSON tokens are never re-marshaled.
//
// Invalid UTF-8 is unchanged. A balanced but invalid or inline-suffixed block
// is skipped as a unit. An unclosed block leaves the remaining input unchanged,
// so nested candidate starts cannot cause repeated scans of the same suffix.
func JSONBlock(input []byte) ([]byte, error) {
	if !utf8.Valid(input) {
		return input, nil
	}
	var out bytes.Buffer
	retained := 0
	for pos := 0; pos < len(input); {
		start := pos
		for start < len(input) && (input[start] == ' ' || input[start] == '\t') {
			start++
		}
		if start == len(input) || (input[start] != '{' && input[start] != '[') {
			pos = jsonBlockNextLine(input, start)
			continue
		}
		end, closed := jsonBlockEnd(input, start)
		if !closed {
			break
		}
		tail := end
		for tail < len(input) && (input[tail] == ' ' || input[tail] == '\t' || input[tail] == '\r') {
			tail++
		}
		if tail == len(input) || input[tail] == '\n' {
			var compact bytes.Buffer
			if json.Compact(&compact, input[start:end]) == nil && compact.Len() < end-start {
				out.Write(input[retained:start])
				out.Write(compact.Bytes())
				retained = end
			}
		}
		pos = jsonBlockNextLine(input, tail)
	}
	if retained == 0 {
		return input, nil
	}
	out.Write(input[retained:])
	return out.Bytes(), nil
}

func jsonBlockNextLine(input []byte, start int) int {
	if offset := bytes.IndexByte(input[start:], '\n'); offset >= 0 {
		return start + offset + 1
	}
	return len(input)
}

// jsonBlockEnd only locates the candidate boundary. json.Compact validates the
// complete JSON grammar, matching delimiter types, escapes, and nesting limit.
func jsonBlockEnd(input []byte, start int) (int, bool) {
	depth := 0
	inString, escaped := false, false
	for i := start; i < len(input); i++ {
		b := input[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case b == '\\':
				escaped = true
			case b == '"':
				inString = false
			}
			continue
		}
		switch b {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return len(input), false
}
