// Package canon implements the strict input-equality comparison used by the
// KTX tool-call evidence identity layer.
//
// Ordinary JSON comparison treats differently formatted but semantically
// equal text as distinct (whitespace and member-order changes conflict).
// That blocks equivalent tool-call deliveries from recognized trusted
// ingresses from converging to one source row. The contract here removes
// ONLY insignificant formatting differences — object member order and
// insignificant whitespace — while leaving the values themselves exact.
//
// The comparison rule (from the recorded identity decision) is:
//
//   - Member names are compared after JSON unescaping. A duplicate decoded
//     name at any object depth fails normalization.
//   - Objects are serialized with members ordered by their decoded names
//     using RFC 8785 UTF-16 code-unit ordering; insignificant whitespace
//     outside strings is removed; strings and member names use RFC 8785
//     string serialization.
//   - Every number token's original lexeme is preserved exactly, so 1,
//     1.0, 1e0, and 1E+0 are all distinct.
//   - Array element order is preserved.
//   - Booleans, null, and string scalar values keep their exact values.
//   - Inputs that are invalid JSON, contain a lone surrogate / invalid
//     Unicode scalar, or carry a duplicate member name cannot be normalized:
//     they compare by their exact original UTF-8 bytes instead.
//
// The package is pure: no internal dependencies, no third-party modules.
package canon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"unicode/utf16"
	"unicode/utf8"
)

// Equal reports whether two inputs are the same tool-call input evidence
// under the strict comparison rule.
//
// When both sides canonicalize successfully, the canonical forms are
// compared byte-for-byte. If either side fails normalization (invalid JSON,
// invalid Unicode scalar, or a duplicate decoded member name), the inputs
// compare by their exact original UTF-8 bytes. Two failed-normalization
// inputs are therefore equal only when they were byte-identical to begin
// with — they do not accidentally converge because neither could be
// canonicalized.
func Equal(a, b []byte) bool {
	ca, oka := Canonicalize(a)
	cb, okb := Canonicalize(b)
	if oka && okb {
		return bytes.Equal(ca, cb)
	}
	return bytes.Equal(a, b)
}

// Canonicalize returns the strict canonical form of data.
//
// The ok result is false when data is not normalizable: invalid JSON, a
// duplicate decoded member name at any depth, a string or member name
// containing a lone surrogate or an otherwise invalid Unicode scalar, or
// trailing non-whitespace data after the single top-level value. Callers
// that need equality should use Equal, which falls back to exact-byte
// comparison on failure; Canonicalize is exported so tests and the
// evidence layer can inspect the canonical form directly.
func Canonicalize(data []byte) (canonical []byte, ok bool) {
	// The encoding/json decoder silently replaces a lone-surrogate \uXXXX
	// escape (e.g. \uD800) with U+FFFD, which would conflate it with a
	// genuine \uFFFD. Scanning the raw bytes before decoding detects lone
	// surrogates at any depth so Canonicalize fails and Equal falls back
	// to exact-byte comparison.
	if containsLoneSurrogateEscape(data) {
		return nil, false
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	var out bytes.Buffer
	if !canonicalValue(dec, &out) {
		return nil, false
	}
	// A single top-level value is required. Trailing whitespace is
	// allowed (the decoder skips it); any further token is trailing
	// data and rejects canonicalization.
	if dec.More() {
		return nil, false
	}
	if _, err := dec.Token(); err != nil && !isEOF(err) {
		return nil, false
	}
	if !validScalars(out.String()) {
		return nil, false
	}
	return out.Bytes(), true
}

// canonicalValue reads exactly one JSON value from dec and appends its
// canonical form to out. It returns false on any tokenization or
// structural failure, leaving dec partially advanced.
func canonicalValue(dec *json.Decoder, out *bytes.Buffer) bool {
	tok, err := dec.Token()
	if err != nil {
		return false
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case json.Delim('{'):
			return canonicalObject(dec, out)
		case json.Delim('['):
			return canonicalArray(dec, out)
		default:
			// A closing delim appears where a value was expected.
			return false
		}
	case json.Number:
		// The original number lexeme is preserved exactly: 1, 1.0,
		// 1e0, and 1E+0 are intentionally distinct.
		out.WriteString(string(v))
		return true
	case string:
		enc, ok := encodeString(v)
		if !ok {
			return false
		}
		out.Write(enc)
		return true
	case bool:
		if v {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
		return true
	case nil:
		out.WriteString("null")
		return true
	default:
		// encoding/json does not emit other token types, but this
		// guards against a future variant.
		return false
	}
}

// canonicalMember is one object member in canonical intermediate form: the
// decoded name (for duplicate detection), its UTF-16 code units (for
// ordering), and the canonical renderings of the name and value bytes.
type canonicalMember struct {
	keyUnits []uint16
	keyEnc   []byte
	value    []byte
}

// canonicalObject reads an object body from dec (the opening '{' has
// already been consumed) and appends the canonical object form to out.
// A duplicate decoded name at any depth fails normalization.
func canonicalObject(dec *json.Decoder, out *bytes.Buffer) bool {
	members := make([]canonicalMember, 0, 8)
	seen := make(map[string]struct{})

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return false
		}
		key, ok := keyTok.(string)
		if !ok {
			// A non-string token appeared where a member name was
			// expected; malformed object.
			return false
		}
		if _, dup := seen[key]; dup {
			// A duplicate decoded name at any depth fails
			// normalization. The caller falls back to exact bytes.
			return false
		}
		seen[key] = struct{}{}

		keyEnc, ok := encodeString(key)
		if !ok {
			return false
		}
		var val bytes.Buffer
		if !canonicalValue(dec, &val) {
			return false
		}
		members = append(members, canonicalMember{
			keyUnits: utf16.Encode([]rune(key)),
			keyEnc:   keyEnc,
			value:    val.Bytes(),
		})
	}
	// Consume the closing '}'.
	if _, err := dec.Token(); err != nil {
		return false
	}

	sort.SliceStable(members, func(i, j int) bool {
		return compareUTF16(members[i].keyUnits, members[j].keyUnits) < 0
	})

	out.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			out.WriteByte(',')
		}
		out.Write(m.keyEnc)
		out.WriteByte(':')
		out.Write(m.value)
	}
	out.WriteByte('}')
	return true
}

// canonicalArray reads an array body from dec (the opening '[' has
// already been consumed) and appends the canonical array form to out.
// Array element order is preserved: ['a','b'] and ['b','a'] differ.
func canonicalArray(dec *json.Decoder, out *bytes.Buffer) bool {
	elems := make([][]byte, 0, 8)
	for dec.More() {
		var e bytes.Buffer
		if !canonicalValue(dec, &e) {
			return false
		}
		elems = append(elems, e.Bytes())
	}
	// Consume the closing ']'.
	if _, err := dec.Token(); err != nil {
		return false
	}

	out.WriteByte('[')
	for i, e := range elems {
		if i > 0 {
			out.WriteByte(',')
		}
		out.Write(e)
	}
	out.WriteByte(']')
	return true
}

// compareUTF16 compares two UTF-16 code-unit sequences lexicographically,
// matching RFC 8785 member-name ordering. Supplementary-plane characters
// are represented as surrogate pairs, so this ordering can differ from a
// plain code-point ordering for astral characters.
func compareUTF16(a, b []uint16) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}

// encodeString serializes a decoded JSON string in RFC 8785 form: the
// opening and closing double quotes, characters U+0020 through U+10FFFF
// emitted as their UTF-8 encoding except for the escaped specials
// (double-quote and backslash), and U+0000 through U+001F emitted with the
// RFC 8785 short escapes (\b, \t, \n, \f, \r) where defined and lowercase
// \uXXXX escapes otherwise. The input must contain only valid Unicode
// scalar values; a lone surrogate or invalid scalar fails normalization.
func encodeString(s string) ([]byte, bool) {
	if !validScalars(s) {
		return nil, false
	}
	var out bytes.Buffer
	out.Grow(len(s) + 2)
	out.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '\\' || r == '"':
			out.WriteByte('\\')
			out.WriteRune(r)
		case r < 0x20:
			switch r {
			case '\b':
				out.WriteString(`\b`)
			case '\t':
				out.WriteString(`\t`)
			case '\n':
				out.WriteString(`\n`)
			case '\f':
				out.WriteString(`\f`)
			case '\r':
				out.WriteString(`\r`)
			default:
				fmt.Fprintf(&out, `\u%04x`, r)
			}
		default:
			out.WriteRune(r)
		}
		i += size
	}
	out.WriteByte('"')
	return out.Bytes(), true
}

// validScalars reports whether s holds only valid Unicode scalar values:
// no surrogates (U+D800..U+DFFF are not scalars) and no invalid UTF-8 byte
// sequences. json.Decoder rejects malformed UTF-8 before producing a string
// token, but it silently substitutes U+FFFD for a lone-surrogate \uXXXX
// escape, so validScalars alone cannot distinguish a genuine U+FFFD from a
// substitution. containsLoneSurrogateEscape scans the raw bytes before
// decoding to reject those; this check is defense-in-depth for anything that
// reaches encodeString through another path.
func validScalars(s string) bool {
	for _, r := range s {
		if r >= 0xD800 && r <= 0xDFFF {
			return false
		}
	}
	return utf8.ValidString(s)
}

func isEOF(err error) bool {
	return err == io.EOF
}

// containsLoneSurrogateEscape reports whether the JSON text contains a
// \uXXXX escape sequence encoding a lone surrogate (U+D800..U+DFFF) that
// is not part of a valid UTF-16 surrogate pair. The encoding/json decoder
// silently substitutes U+FFFD for lone surrogates before a caller can see
// the decoded string, so this scan runs on the raw bytes to catch the
// substitution and reject canonicalization.
//
// A high surrogate (U+D800..U+DBFF) must be followed by a \uXXXX low
// surrogate (U+DC00..U+DFFF); any other continuation is a lone surrogate.
// A low surrogate not immediately preceded by a high surrogate is also
// lone. Raw UTF-8 encoded surrogates (ED A0 80..) are invalid UTF-8 and
// are rejected by the decoder/utf8.ValidString before reaching this point;
// this scan covers only the \u escape path the decoder would otherwise
// silently rewrite.
func containsLoneSurrogateEscape(data []byte) bool {
	for i := 0; i+5 < len(data); {
		if data[i] != '\\' || data[i+1] != 'u' {
			i++
			continue
		}
		r, ok := parseHex4(data[i+2:])
		if !ok {
			i++
			continue
		}
		if r >= 0xDC00 && r <= 0xDFFF {
			// Low surrogate without a preceding high surrogate.
			return true
		}
		if r >= 0xD800 && r <= 0xDBFF {
			// High surrogate: must be followed by \uXXXX low surrogate.
			if i+11 < len(data) && data[i+6] == '\\' && data[i+7] == 'u' {
				if r2, ok2 := parseHex4(data[i+8:]); ok2 && r2 >= 0xDC00 && r2 <= 0xDFFF {
					i += 12 // valid pair; skip both escapes
					continue
				}
			}
			return true
		}
		i += 6
	}
	return false
}

// parseHex4 reads exactly four hexadecimal digits from s and returns the
// decoded rune. It reports ok=false when s is too short or contains a
// non-hex digit.
func parseHex4(s []byte) (rune, bool) {
	if len(s) < 4 {
		return 0, false
	}
	var r rune
	for j := 0; j < 4; j++ {
		var v rune
		switch c := s[j]; {
		case c >= '0' && c <= '9':
			v = rune(c - '0')
		case c >= 'a' && c <= 'f':
			v = rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v = rune(c-'A') + 10
		default:
			return 0, false
		}
		r = r*16 + v
	}
	return r, true
}
