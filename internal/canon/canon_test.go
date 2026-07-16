// Pure-function coverage of the strict canonical JSON comparison.
//
// Covered behavior:
//
//   - object member reorder + whitespace:   TestCanonical_ObjectOrderAndWhitespace
//   - exact numeric lexemes (1 != 1.0):     TestCanonical_NumericLexemes
//   - duplicate decoded names fail + fallback: TestCanonical_DuplicateKeys
//   - invalid JSON / trailing data fallback: TestCanonical_InvalidJSON
//   - array order preserved:                 TestCanonical_ArrayOrder
//   - strings, escapes, Unicode scalars:     TestCanonical_StringsAndUnicode
//   - lone-surrogate rejection + fallback:   TestCanonical_LoneSurrogate
//   - RFC 8785 short escapes (\b \t \n \f \r): TestCanonical_ShortEscapes
//   - booleans / null / top-level scalars:   TestCanonical_Scalars
//   - UTF-16 member-name ordering (astral):   TestCanonical_UTF16Ordering
//   - canonical form is stable and minimal:  TestCanonical_Form
package canon

import (
	"bytes"
	"testing"
)

func TestCanonical_ObjectOrderAndWhitespace(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"reordered members", `{"a":1,"b":2}`, `{"b":2,"a":1}`, true},
		{"spaces around punctuation", `{"a": 1 , "b" : 2 }`, `{"a":1,"b":2}`, true},
		{"newlines and tabs", "{\n\t\"a\":1\n}", `{"a":1}`, true},
		{"leading and trailing whitespace", `  {"a":1}  `, `{"a":1}`, true},
		{"three members reordered", `{"x":1,"y":2,"z":3}`, `{"z":3,"x":1,"y":2}`, true},
		{"value differs", `{"a":1}`, `{"a":2}`, false},
		{"extra member", `{"a":1}`, `{"a":1,"b":2}`, false},
		{"whitespace inside string is significant", `{"a":"x y"}`, `{"a":"xy"}`, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Equal([]byte(tc.a), []byte(tc.b)); got != tc.want {
				t.Fatalf("Equal(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestCanonical_NumericLexemes(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"same integer", `1`, `1`, true},
		{"integer vs decimal", `1`, `1.0`, false},
		{"integer vs exponent lower", `1`, `1e0`, false},
		{"exponent lower vs upper", `1e0`, `1E0`, false},
		{"exponent sign distinct", `1e+2`, `1e2`, false},
		{"decimal trailing zero distinct", `0.5`, `0.50`, false},
		{"hundred vs 1e2", `100`, `1e2`, false},
		{"same decimal", `0.5`, `0.5`, true},
		{"same exponent", `1e2`, `1e2`, true},
		{"negative lexeme", `-1`, `-1`, true},
		{"neg vs pos one", `-1`, `1`, false},
		{"in object", `{"n":1}`, `{"n":1.0}`, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Equal([]byte(tc.a), []byte(tc.b)); got != tc.want {
				t.Fatalf("Equal(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestCanonical_DuplicateKeys(t *testing.T) {
	// A duplicate decoded name at any depth fails normalization; both
	// Equal sides then compare by exact bytes.
	dup := `{"a":1,"a":2}`
	if _, ok := Canonicalize([]byte(dup)); ok {
		t.Fatalf("Canonicalize(%q) succeeded; want duplicate-key rejection", dup)
	}
	t.Run("byte-identical duplicates compare equal by fallback", func(t *testing.T) {
		t.Parallel()
		if !Equal([]byte(dup), []byte(dup)) {
			t.Fatalf("Equal(dup, dup) = false; want true (exact-byte fallback)")
		}
	})
	t.Run("differently ordered duplicates stay distinct", func(t *testing.T) {
		t.Parallel()
		other := `{"a":2,"a":1}`
		if Equal([]byte(dup), []byte(other)) {
			t.Fatalf("Equal(%q, %q) = true; want false (exact-byte fallback differs)", dup, other)
		}
	})
	t.Run("duplicate after unescaping is detected", func(t *testing.T) {
		t.Parallel()
		// \u0061 decodes to "a"; "a" and "\u0061" name the same member.
		escapedDup := `{"a":1,"\u0061":2}`
		if _, ok := Canonicalize([]byte(escapedDup)); ok {
			t.Fatalf("Canonicalize(%q) succeeded; want decoded duplicate rejection", escapedDup)
		}
	})
	t.Run("duplicate at nesting depth is detected", func(t *testing.T) {
		t.Parallel()
		nested := `{"outer":{"a":1,"a":2}}`
		if _, ok := Canonicalize([]byte(nested)); ok {
			t.Fatalf("Canonicalize(%q) succeeded; want nested duplicate rejection", nested)
		}
	})
}

func TestCanonical_InvalidJSON(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"not json", `not json`},
		{"unclosed object", `{`},
		{"mismatched brace", `}{`},
		{"missing value", `{"a":}`},
		{"trailing comma array", `[1,]`},
		{"truncated literal", `tru`},
		{"trailing data after value", `{"a":1} {"b":2}`},
		{"bare identifier", `undefined`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, ok := Canonicalize([]byte(tc.input)); ok {
				t.Fatalf("Canonicalize(%q) succeeded; want normalization failure", tc.input)
			}
		})
	}
	t.Run("invalid-JSON bytes compare by fallback", func(t *testing.T) {
		t.Parallel()
		if !Equal([]byte(`{`), []byte(`{`)) {
			t.Fatal("Equal('{','{') = false; want true (exact-byte fallback)")
		}
		if Equal([]byte(`{`), []byte(`}`)) {
			t.Fatal("Equal('{','}') = true; want false (bytes differ)")
		}
	})
}

func TestCanonical_ArrayOrder(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"same order", `[1,2,3]`, `[1,2,3]`, true},
		{"whitespace flex", `[1, 2, 3]`, `[1,2,3]`, true},
		{"reversed differs", `[1,2,3]`, `[3,2,1]`, false},
		{"swap differs", `[1,2]`, `[2,1]`, false},
		{"nested same", `[[1,2],[3,4]]`, `[[1,2],[3,4]]`, true},
		{"nested reordered differs", `[[1,2],[3,4]]`, `[[3,4],[1,2]]`, false},
		{"empty arrays equal", `[]`, `[]`, true},
		{"empty vs nonempty", `[]`, `[1]`, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Equal([]byte(tc.a), []byte(tc.b)); got != tc.want {
				t.Fatalf("Equal(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestCanonical_StringsAndUnicode(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"ascii string equals", `{"a":"hello"}`, `{"a":"hello"}`, true},
		{"escaped vs literal quote", `{"a":"\"q\""}`, `{"a":"\"q\""}`, true},
		{"decoded name equivalence", `{"é":1}`, `{"\u00e9":1}`, true},
		{"decoded value equivalence", `{"a":"héllo"}`, `{"a":"héllo"}`, true},
		{"accent vs ascii differs", `{"a":"héllo"}`, `{"a":"hello"}`, false},
		{"unicode object member reorder", `{"a":"1","é":"2"}`, `{"é":"2","a":"1"}`, true},
		{"emoji value equals", `{"a":"🌍"}`, `{"a":"\uD83C\uDF0D"}`, true},
		{"emoji vs different emoji", `{"a":"🌍"}`, `{"a":"🌎"}`, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Equal([]byte(tc.a), []byte(tc.b)); got != tc.want {
				t.Fatalf("Equal(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestCanonical_LoneSurrogate(t *testing.T) {
	t.Run("lone high surrogate rejected", func(t *testing.T) {
		t.Parallel()
		in := []byte(`{"a":"\uD800"}`)
		if _, ok := Canonicalize(in); ok {
			t.Fatalf("Canonicalize(%q) succeeded; want lone-surrogate rejection", in)
		}
	})
	t.Run("lone low surrogate rejected", func(t *testing.T) {
		t.Parallel()
		in := []byte(`"\uDC00"`)
		if _, ok := Canonicalize(in); ok {
			t.Fatalf("Canonicalize(%q) succeeded; want lone-surrogate rejection", in)
		}
	})
	t.Run("lone surrogate at depth rejected", func(t *testing.T) {
		t.Parallel()
		in := []byte(`{"outer":{"a":"x\uD800y","b":2}}`)
		if _, ok := Canonicalize(in); ok {
			t.Fatalf("Canonicalize(%q) succeeded; want lone-surrogate rejection at depth", in)
		}
	})
	t.Run("valid surrogate pair accepted", func(t *testing.T) {
		t.Parallel()
		// \uD83C\uDF0D is the valid surrogate pair for U+1F30D (🌍).
		in := []byte(`{"a":"\uD83C\uDF0D"}`)
		got, ok := Canonicalize(in)
		if !ok {
			t.Fatalf("Canonicalize(%q) failed; want valid pair accepted", in)
		}
		want := []byte(`{"a":"🌍"}`)
		if !bytes.Equal(got, want) {
			t.Fatalf("canonical = %q, want %q", got, want)
		}
	})
	t.Run("lone surrogate not conflated with replacement char", func(t *testing.T) {
		t.Parallel()
		// The decoder substitutes \uD800 with U+FFFD; strict comparison
		// must reject canonicalization so the two compare by exact bytes
		// and stay distinct rather than silently converging.
		if Equal([]byte(`"\uD800"`), []byte(`"\uFFFD"`)) {
			t.Fatal(`Equal("\uD800", "\uFFFD") = true; want false (lone surrogate must not conflate with U+FFFD)`)
		}
	})
	t.Run("identical lone surrogate compares equal by fallback", func(t *testing.T) {
		t.Parallel()
		if !Equal([]byte(`"\uD800"`), []byte(`"\uD800"`)) {
			t.Fatal(`Equal("\uD800", "\uD800") = false; want true (exact-byte fallback)`)
		}
	})
	t.Run("unpaired high then high then low rejected", func(t *testing.T) {
		t.Parallel()
		// The first high surrogate is followed by another high, not a
		// low — a lone surrogate.
		in := []byte(`"\uD800\uD800\uDC00"`)
		if _, ok := Canonicalize(in); ok {
			t.Fatalf("Canonicalize(%q) succeeded; want rejection for unpaired high", in)
		}
	})
}

func TestCanonical_ShortEscapes(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"backslash-b", `"\b"`, `"\b"`},
		{"backslash-t", `"\t"`, `"\t"`},
		{"backslash-n", `"\n"`, `"\n"`},
		{"backslash-f", `"\f"`, `"\f"`},
		{"backslash-r", `"\r"`, `"\r"`},
		{"u0008 normalizes to short escape", `"\u0008"`, `"\b"`},
		{"u0009 normalizes to short escape", `"\u0009"`, `"\t"`},
		{"u000a normalizes to short escape", `"\u000a"`, `"\n"`},
		{"u000c normalizes to short escape", `"\u000c"`, `"\f"`},
		{"u000d normalizes to short escape", `"\u000d"`, `"\r"`},
		{"u0000 stays u-escape", `"\u0000"`, `"\u0000"`},
		{"u0001 stays u-escape", `"\u0001"`, `"\u0001"`},
		{"u0007 stays u-escape", `"\u0007"`, `"\u0007"`},
		{"u001f stays u-escape", `"\u001f"`, `"\u001f"`},
		{"string with tab and text", `"a\tb"`, `"a\tb"`},
		{"all specials in one string", `"\b\t\n\f\r"`, `"\b\t\n\f\r"`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := Canonicalize([]byte(tc.input))
			if !ok {
				t.Fatalf("Canonicalize(%q) failed", tc.input)
			}
			if string(got) != tc.want {
				t.Fatalf("Canonicalize(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
	t.Run("short escape and u-escape are equivalent", func(t *testing.T) {
		t.Parallel()
		if !Equal([]byte(`"\b"`), []byte(`"\u0008"`)) {
			t.Fatal(`Equal("\b", "\u0008") = false; want true`)
		}
		if !Equal([]byte(`"x\ty"`), []byte(`"x\u0009y"`)) {
			t.Fatal(`Equal("x\ty", "x\u0009y") = false; want true`)
		}
	})
}

func TestCanonical_Scalars(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"true equals", `true`, `true`, true},
		{"false equals", `false`, `false`, true},
		{"null equals", `null`, `null`, true},
		{"true vs false", `true`, `false`, false},
		{"true vs one", `true`, `1`, false},
		{"null vs empty string", `null`, `""`, false},
		{"integer top level", `42`, `42`, true},
		{"integer differ", `42`, `43`, false},
		{"string top level", `"hi"`, `"hi"`, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Equal([]byte(tc.a), []byte(tc.b)); got != tc.want {
				t.Fatalf("Equal(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestCanonical_UTF16Ordering(t *testing.T) {
	// By UTF-16 first code unit: z(0x007A) < é(0x00E9) < 😀(0xD83D…)
	// < U+E000(0xE000). By code point it would be é < z < U+E000 < 😀,
	// so a UTF-16 sort diverges from a code-point sort for the astral key.
	in := []byte(`{"\uE000":3,"😀":2,"é":1,"z":0}`)
	got, ok := Canonicalize(in)
	if !ok {
		t.Fatalf("Canonicalize failed")
	}
	// U+E000 is a valid scalar; RFC 8785 emits it as raw UTF-8
	// (not as a \u escape), so the expected form uses an interpreted
	// string so \uE000 resolves to the rune.
	want := []byte("{\"z\":0,\"é\":1,\"😀\":2,\"\uE000\":3}")
	if !bytes.Equal(got, want) {
		t.Fatalf("canonical = %q, want %q", got, want)
	}
	// Reordering the input still converges to the same canonical form.
	reordered := []byte(`{"z":0,"😀":2,"\uE000":3,"é":1}`)
	if !Equal(in, reordered) {
		t.Fatalf("Equal(reordered) = false; want true (converges to canonical form)")
	}
}

func TestCanonical_Form(t *testing.T) {
	// The canonical form is minimal: no insignificant whitespace, objects
	// sorted, numbers and strings emitted in their RFC 8785 form.
	got, ok := Canonicalize([]byte(`{ "b" : 2 , "a" : {"y" : 1 , "x" : 2 } }`))
	if !ok {
		t.Fatalf("Canonicalize failed")
	}
	want := []byte(`{"a":{"x":2,"y":1},"b":2}`)
	if !bytes.Equal(got, want) {
		t.Fatalf("canonical = %q, want %q", got, want)
	}

	// Idempotency: canonicalizing the canonical form is a fixed point.
	again, ok := Canonicalize(got)
	if !ok {
		t.Fatalf("re-canonicalize failed")
	}
	if !bytes.Equal(again, got) {
		t.Fatalf("not a fixed point: %q -> %q", got, again)
	}
}
